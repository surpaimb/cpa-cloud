use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use crate::jsonc_document::JsoncDocument;
use crate::live::engine::digest;
use crate::{AppError, ClientConfiguration, SecretString};

const PROVIDER_ID: &str = "cpa-cloud";

#[derive(Debug, Clone)]
pub struct ConfigurationProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
}

impl ConfigurationProfile {
    pub fn for_opencode(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let models = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "openai-chat")
            .map(|item| item.candidates.clone())
            .unwrap_or_default();
        if models.is_empty() {
            return Err(AppError::Config(
                "当前员工 Key 没有 OpenAI Chat 协议模型，不能配置 OpenCode".into(),
            ));
        }
        Ok(Self {
            base_url: base_url.into(),
            employee_key,
            models,
        })
    }
}

#[derive(Clone, Serialize, PartialEq, Eq)]
pub struct FilePlan {
    pub path: PathBuf,
    pub expected_pre_digest: Option<String>,
    pub desired_digest: String,
    #[serde(skip)]
    pub desired_bytes: Vec<u8>,
}

impl std::fmt::Debug for FilePlan {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("FilePlan")
            .field("path", &self.path)
            .field("expected_pre_digest", &self.expected_pre_digest)
            .field("desired_digest", &self.desired_digest)
            .field("desired_bytes", &"[REDACTED]")
            .finish()
    }
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct OpenCodePlan {
    pub config: FilePlan,
    pub ownership: FilePlan,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    provider_id: String,
    provider_digest: String,
}

pub fn plan_opencode(
    config_path: &Path,
    ownership_path: &Path,
    profile: &ConfigurationProfile,
) -> Result<OpenCodePlan, AppError> {
    validate_profile(profile)?;
    let previous = read_optional(config_path)?;
    let source = previous
        .as_deref()
        .map(|bytes| {
            std::str::from_utf8(bytes)
                .map_err(|_| AppError::Config("OpenCode 配置不是 UTF-8 文本".into()))
        })
        .transpose()?
        .unwrap_or("{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n");
    let mut document = JsoncDocument::parse(source)
        .map_err(|err| AppError::Config(format!("无法解析 OpenCode JSONC 配置：{err}")))?;
    let mut desired = document.value().clone();

    let root = desired
        .as_object_mut()
        .ok_or_else(|| AppError::Config("OpenCode 配置根必须是对象".into()))?;
    if root
        .get("providers")
        .and_then(Value::as_object)
        .is_some_and(|providers| providers.contains_key(PROVIDER_ID))
    {
        return Err(AppError::Conflict(
            "OpenCode 已存在原生 providers.cpa-cloud；它会遮蔽 legacy provider。为避免改写未知配置，已停止"
                .into(),
        ));
    }
    if let Some(provider_root) = root.get("provider") {
        if !provider_root.is_object() {
            return Err(AppError::Config(
                "OpenCode 配置中的 provider 必须是对象；未写入任何文件".into(),
            ));
        }
    } else {
        root.insert("provider".into(), json!({}));
    }

    let current_provider = desired
        .get("provider")
        .and_then(Value::as_object)
        .and_then(|providers| providers.get(PROVIDER_ID));
    verify_ownership(ownership_path, current_provider)?;

    let provider = provider_value(profile)?;
    desired["provider"][PROVIDER_ID] = provider.clone();
    document.apply(&desired)?;
    let output = document.validated_source()?.into_bytes();
    let provider_digest = value_digest(&provider)?;
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "opencode".into(),
        provider_id: PROVIDER_ID.into(),
        provider_digest,
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|err| AppError::Config(format!("无法生成所有权记录：{err}")))?;

    Ok(OpenCodePlan {
        config: file_plan(config_path, previous.as_deref(), output),
        ownership: file_plan(
            ownership_path,
            read_optional(ownership_path)?.as_deref(),
            ownership_bytes,
        ),
    })
}

fn provider_value(profile: &ConfigurationProfile) -> Result<Value, AppError> {
    let base_url = normalize_openai_base_url(&profile.base_url)?;
    let mut models = serde_json::Map::new();
    for model in &profile.models {
        models.insert(model.clone(), json!({"name": model}));
    }
    Ok(json!({
        "name": "CPA Cloud",
        "npm": "@ai-sdk/openai-compatible",
        "options": {
            "baseURL": base_url,
            "apiKey": profile.employee_key.expose_secret().trim()
        },
        "models": models
    }))
}

fn verify_ownership(path: &Path, current: Option<&Value>) -> Result<(), AppError> {
    let Some(current) = current else {
        return Ok(());
    };
    let bytes = read_optional(path)?.ok_or_else(|| {
        AppError::Conflict(
            "OpenCode 已存在 cpa-cloud provider，但缺少 CPA 所有权记录；为避免覆盖用户配置，已停止"
                .into(),
        )
    })?;
    let record: OwnershipRecord = serde_json::from_slice(&bytes)
        .map_err(|_| AppError::Conflict("CPA 所有权记录无效；未写入任何文件".into()))?;
    if record.schema_version != 1
        || record.tool != "opencode"
        || record.provider_id != PROVIDER_ID
        || record.provider_digest != value_digest(current)?
    {
        return Err(AppError::Conflict(
            "OpenCode 的 cpa-cloud provider 已在外部修改；未写入任何文件".into(),
        ));
    }
    Ok(())
}

fn validate_profile(profile: &ConfigurationProfile) -> Result<(), AppError> {
    if profile.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config(
            "当前 Key 没有可用于 OpenCode 的模型".into(),
        ));
    }
    if profile.models.iter().any(|model| {
        model.is_empty() || model.len() > 200 || model.chars().any(|ch| ch.is_control())
    }) {
        return Err(AppError::Config("模型标识无效".into()));
    }
    Ok(())
}

fn normalize_openai_base_url(input: &str) -> Result<String, AppError> {
    let mut url = crate::discovery::discovery_endpoint(input)?;
    let path = url
        .path()
        .trim_end_matches("/client-configuration")
        .to_owned();
    url.set_path(&path);
    Ok(url.to_string().trim_end_matches('/').to_string())
}

fn read_optional(path: &Path) -> Result<Option<Vec<u8>>, AppError> {
    match std::fs::read(path) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(err) => Err(AppError::io(path, err)),
    }
}

fn file_plan(path: &Path, previous: Option<&[u8]>, desired_bytes: Vec<u8>) -> FilePlan {
    FilePlan {
        path: path.to_path_buf(),
        expected_pre_digest: digest(previous),
        desired_digest: digest(Some(&desired_bytes)).expect("bytes always have a digest"),
        desired_bytes,
    }
}

fn value_digest(value: &Value) -> Result<String, AppError> {
    let bytes = serde_json::to_vec(value)
        .map_err(|err| AppError::Config(format!("无法计算配置摘要：{err}")))?;
    Ok(hex::encode(Sha256::digest(bytes)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn profile() -> ConfigurationProfile {
        ConfigurationProfile {
            base_url: "https://cpa.example.test".into(),
            employee_key: SecretString::new("employee-secret"),
            models: vec!["alpha".into(), "beta".into()],
        }
    }

    #[test]
    fn plans_jsonc_without_losing_user_comments() {
        let temp = tempfile::tempdir().unwrap();
        let config = temp.path().join("opencode.jsonc");
        let ownership = temp.path().join("opencode.ownership.json");
        std::fs::write(
            &config,
            "{\n  // keep this comment\n  \"theme\": \"system\",\n}\n",
        )
        .unwrap();

        let plan = plan_opencode(&config, &ownership, &profile()).unwrap();
        let text = String::from_utf8(plan.config.desired_bytes).unwrap();
        assert!(text.contains("// keep this comment"));
        assert!(text.contains("\"theme\": \"system\""));
        assert!(text.contains("\"ANTHROPIC_API_KEY\"") == false);
        assert!(text.contains("employee-secret"));
        assert!(!String::from_utf8(plan.ownership.desired_bytes)
            .unwrap()
            .contains("employee-secret"));
    }

    #[test]
    fn refuses_unowned_existing_provider() {
        let temp = tempfile::tempdir().unwrap();
        let config = temp.path().join("opencode.json");
        let ownership = temp.path().join("ownership.json");
        std::fs::write(
            &config,
            r#"{"provider":{"cpa-cloud":{"options":{"apiKey":"someone-else"}}}}"#,
        )
        .unwrap();
        assert!(matches!(
            plan_opencode(&config, &ownership, &profile()),
            Err(AppError::Conflict(_))
        ));
    }

    #[test]
    fn refuses_malformed_jsonc_without_a_write_plan() {
        let temp = tempfile::tempdir().unwrap();
        let config = temp.path().join("opencode.jsonc");
        std::fs::write(&config, "{ broken").unwrap();
        assert!(plan_opencode(&config, &temp.path().join("owner"), &profile()).is_err());
    }

    #[test]
    fn opencode_uses_only_openai_chat_candidates() {
        let configuration = ClientConfiguration {
            schema_version: 1,
            protocols: vec![
                crate::discovery::ProtocolConfiguration {
                    protocol: "openai-responses".into(),
                    candidates: vec!["responses-only".into()],
                    default_model: "responses-only".into(),
                    default_source: "public_id_ascending".into(),
                },
                crate::discovery::ProtocolConfiguration {
                    protocol: "openai-chat".into(),
                    candidates: vec!["chat-model".into()],
                    default_model: "chat-model".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        };
        let profile = ConfigurationProfile::for_opencode(
            "https://cpa.example.test",
            SecretString::new("secret"),
            &configuration,
        )
        .unwrap();
        assert_eq!(profile.models, vec!["chat-model"]);
    }

    #[test]
    fn refuses_native_provider_that_would_shadow_legacy_projection() {
        let temp = tempfile::tempdir().unwrap();
        let config = temp.path().join("opencode.json");
        std::fs::write(
            &config,
            r#"{"providers":{"cpa-cloud":{"settings":{"baseURL":"https://elsewhere.invalid"}}}}"#,
        )
        .unwrap();
        assert!(matches!(
            plan_opencode(&config, &temp.path().join("owner"), &profile()),
            Err(AppError::Conflict(_))
        ));
    }

    #[test]
    fn plan_debug_never_exposes_employee_key() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_opencode(
            &temp.path().join("opencode.json"),
            &temp.path().join("ownership.json"),
            &profile(),
        )
        .unwrap();
        let rendered = format!("{plan:?}");
        assert!(!rendered.contains("employee-secret"));
        assert!(rendered.contains("[REDACTED]"));
    }
}
