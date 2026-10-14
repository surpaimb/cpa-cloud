use serde::{Deserialize, Serialize};
use url::{Host, Url};

use crate::{AppError, SecretString};

const MAX_RESPONSE_BYTES: u64 = 1_048_576;

#[derive(Clone, Deserialize)]
pub struct DiscoveryRequest {
    pub base_url: String,
    pub employee_key: SecretString,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ProtocolConfiguration {
    pub protocol: String,
    pub candidates: Vec<String>,
    #[serde(default)]
    pub default_model: String,
    #[serde(default)]
    pub default_source: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ClientConfiguration {
    pub schema_version: u32,
    pub protocols: Vec<ProtocolConfiguration>,
}

pub async fn discover_configuration(
    request: &DiscoveryRequest,
) -> Result<ClientConfiguration, AppError> {
    let endpoint = discovery_endpoint(&request.base_url)?;
    if request.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }

    let client = reqwest::Client::builder()
        // Employee Keys must never be delegated to a system-configured proxy.
        // Remote CPA endpoints are reached directly; local development remains
        // restricted to numeric loopback addresses below.
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(std::time::Duration::from_secs(8))
        .build()?;
    let mut response = client
        .get(endpoint)
        .bearer_auth(request.employee_key.expose_secret().trim())
        .header(reqwest::header::ACCEPT, "application/json")
        .send()
        .await?
        .error_for_status()?;

    if response
        .content_length()
        .is_some_and(|size| size > MAX_RESPONSE_BYTES)
    {
        return Err(AppError::Config("服务端配置响应过大".into()));
    }
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await? {
        if body.len().saturating_add(chunk.len()) > MAX_RESPONSE_BYTES as usize {
            return Err(AppError::Config("服务端配置响应过大".into()));
        }
        body.extend_from_slice(&chunk);
    }
    let configuration: ClientConfiguration = serde_json::from_slice(&body)
        .map_err(|_| AppError::Config("服务端返回了无法识别的配置".into()))?;
    validate_configuration(&configuration)?;
    Ok(configuration)
}

pub fn discovery_endpoint(input: &str) -> Result<Url, AppError> {
    let trimmed = input.trim();
    let mut url = Url::parse(trimmed).map_err(|_| AppError::InvalidAddress)?;
    if !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(AppError::InvalidAddress);
    }
    let loopback = match url.host() {
        Some(Host::Ipv4(address)) => address.is_loopback(),
        Some(Host::Ipv6(address)) => address.is_loopback(),
        _ => false,
    };
    if url.scheme() != "https" && !(url.scheme() == "http" && loopback) {
        return Err(AppError::InvalidAddress);
    }
    if url.host_str().is_none() {
        return Err(AppError::InvalidAddress);
    }
    let path = url.path().trim_end_matches('/');
    let base = path
        .strip_suffix("/v1")
        .or_else(|| path.strip_suffix("/v1/client-configuration"))
        .unwrap_or(path);
    url.set_path(&format!("{base}/v1/client-configuration"));
    Ok(url)
}

fn validate_configuration(configuration: &ClientConfiguration) -> Result<(), AppError> {
    if configuration.schema_version != 1 {
        return Err(AppError::Config("服务端配置版本暂不受支持".into()));
    }
    const ALLOWED: [&str; 4] = [
        "anthropic-messages",
        "openai-responses",
        "gemini-generate-content",
        "openai-chat",
    ];
    let mut seen = std::collections::HashSet::new();
    for item in &configuration.protocols {
        if !ALLOWED.contains(&item.protocol.as_str()) || !seen.insert(item.protocol.as_str()) {
            return Err(AppError::Config("服务端协议列表无效".into()));
        }
        if item.candidates.iter().any(|model| {
            model.is_empty() || model.len() > 200 || model.chars().any(|ch| ch.is_control())
        }) {
            return Err(AppError::Config("服务端模型标识无效".into()));
        }
        if !item.default_model.is_empty() && !item.candidates.contains(&item.default_model) {
            return Err(AppError::Config("服务端默认模型不在候选列表中".into()));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::process::Command;
    use std::time::Duration;

    #[test]
    fn endpoint_accepts_https_and_loopback_http() {
        assert_eq!(
            discovery_endpoint("https://cpa.example.test/base/")
                .unwrap()
                .as_str(),
            "https://cpa.example.test/base/v1/client-configuration"
        );
        assert!(discovery_endpoint("http://127.0.0.1:8788").is_ok());
        assert!(discovery_endpoint("http://127.23.45.67:8788").is_ok());
        assert!(discovery_endpoint("http://[::1]:8788").is_ok());
        assert!(discovery_endpoint("http://localhost:8788").is_err());
        assert!(discovery_endpoint("http://cpa.example.test").is_err());
    }

    #[test]
    fn endpoint_rejects_secret_bearing_or_ambiguous_urls() {
        for value in [
            "https://user:secret@example.test",
            "https://example.test?key=secret",
            "https://example.test/#fragment",
        ] {
            let error = discovery_endpoint(value).unwrap_err().to_string();
            assert!(!error.contains("secret"));
            assert!(!error.contains("user"));
        }
    }

    #[test]
    fn employee_key_bypasses_system_proxy_for_numeric_loopback() {
        let target = TcpListener::bind("127.0.0.1:0").unwrap();
        let target_address = target.local_addr().unwrap();
        let proxy = TcpListener::bind("127.0.0.1:0").unwrap();
        let proxy_address = proxy.local_addr().unwrap();

        let serve_once = |listener: TcpListener, expected_request_target: &'static str| {
            std::thread::spawn(move || {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(2)))
                    .unwrap();
                let mut request = Vec::new();
                loop {
                    let mut chunk = [0_u8; 1024];
                    let length = stream.read(&mut chunk).unwrap();
                    assert!(length != 0, "request ended before its headers");
                    request.extend_from_slice(&chunk[..length]);
                    if request.windows(4).any(|window| window == b"\r\n\r\n") {
                        break;
                    }
                    assert!(request.len() <= 16 * 1024, "request headers are too large");
                }
                let request = String::from_utf8_lossy(&request);
                assert!(request.starts_with(expected_request_target));
                assert!(request
                    .to_ascii_lowercase()
                    .contains("authorization: bearer synthetic-employee-key"));
                let body = r#"{"schema_version":1,"protocols":[]}"#;
                write!(
                    stream,
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                    body.len(),
                    body
                )
                .unwrap();
            })
        };
        let proxy_thread = serve_once(proxy, "GET http://");
        let target_thread = serve_once(target, "GET /v1/client-configuration HTTP/1.1");

        // Run the environment-sensitive assertion in a child test process so
        // proxy variables cannot race or leak into the crate's parallel tests.
        let proxy_url = format!("http://{proxy_address}");
        let output = Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "discovery::tests::proxy_child_process",
                "--ignored",
                "--nocapture",
            ])
            .env("CPA_TEST_TARGET_URL", format!("http://{target_address}"))
            .env("HTTP_PROXY", &proxy_url)
            .env("http_proxy", &proxy_url)
            .env("HTTPS_PROXY", &proxy_url)
            .env("https_proxy", &proxy_url)
            .env("ALL_PROXY", &proxy_url)
            .env("all_proxy", &proxy_url)
            .env_remove("NO_PROXY")
            .env_remove("no_proxy")
            .output()
            .unwrap();

        if !output.status.success() {
            panic!(
                "isolated proxy test failed: {}",
                String::from_utf8_lossy(&output.stderr)
            );
        }
        proxy_thread.join().unwrap();
        target_thread.join().unwrap();
    }

    #[test]
    #[ignore = "helper executed by employee_key_bypasses_system_proxy_for_numeric_loopback"]
    fn proxy_child_process() {
        let target = std::env::var("CPA_TEST_TARGET_URL").unwrap();
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        runtime.block_on(async move {
            // Positive sentinel: the default client must honor the injected
            // proxy, proving the test environment can observe the synthetic
            // Bearer credential at that boundary.
            let baseline = reqwest::Client::builder()
                .redirect(reqwest::redirect::Policy::none())
                .build()
                .unwrap()
                .get(format!("{target}/v1/client-configuration"))
                .bearer_auth("synthetic-employee-key")
                .send()
                .await
                .unwrap();
            assert!(baseline.status().is_success());

            // Production discovery uses no_proxy(), so the second request must
            // reach the numeric-loopback target directly instead of the proxy.
            let protected = discover_configuration(&DiscoveryRequest {
                base_url: target,
                employee_key: SecretString::new("synthetic-employee-key"),
            })
            .await
            .unwrap();
            assert_eq!(protected.schema_version, 1);
        });
    }
}
