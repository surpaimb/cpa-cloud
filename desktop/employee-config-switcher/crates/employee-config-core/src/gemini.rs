//! Gemini CLI gateway projection for `~/.gemini/settings.json` and `.env`.
//!
//! This module is independently authored from the fixed public Gemini CLI
//! v0.62.0 configuration and authentication contracts recorded in
//! `PROVENANCE.md`. It creates a complete configuration only below an isolated
//! synthetic root. Existing Gemini CLI configuration remains blocked until the
//! replacement transaction gates pass.

use std::path::Path;

use serde::{Deserialize, Serialize};
use serde_json::json;
use sha2::{Digest, Sha256};

use crate::live::engine::{apply_create_only, CreateFilePlan, CreateOnlyReceipt};
use crate::plan::{
    file_plan, normalize_cpa_gateway_url, read_optional, transaction_error,
    validate_transaction_relative, FilePlan,
};
use crate::{AppError, ClientConfiguration, SecretString};

#[derive(Debug, Clone)]
pub struct GeminiProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub default_model: String,
}

impl GeminiProfile {
    pub fn for_gemini(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let Some(protocol) = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "gemini-generate-content")
        else {
            return Err(AppError::Config(
                "当前员工 Key 没有 Gemini GenerateContent 协议模型，不能配置 Gemini CLI".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 Gemini CLI 提供有效的默认 Gemini 模型".into(),
            ));
        }
        Ok(Self {
            base_url: base_url.into(),
            employee_key,
            default_model: protocol.default_model.clone(),
        })
    }
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct GeminiPlan {
    pub settings: FilePlan,
    pub environment: FilePlan,
    pub ownership: FilePlan,
    pub selected_model: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    settings_digest: String,
    environment_digest: String,
}

pub fn plan_new_gemini(
    settings_path: &Path,
    environment_path: &Path,
    ownership_path: &Path,
    profile: &GeminiProfile,
) -> Result<GeminiPlan, AppError> {
    validate_profile(profile)?;
    if read_optional(settings_path)?.is_some()
        || read_optional(environment_path)?.is_some()
        || read_optional(ownership_path)?.is_some()
    {
        return Err(AppError::Conflict(
            "Gemini CLI 已存在 settings.json、.env 或 CPA 所有权文件；create-only 安全阶段不会替换它们"
                .into(),
        ));
    }

    let settings_document = json!({
        "$schema": "https://raw.githubusercontent.com/google-gemini/gemini-cli/v0.62.0/schemas/settings.schema.json",
        "security": {
            "auth": {
                "selectedType": "gateway"
            },
            "environmentVariableRedaction": {
                "enabled": true,
                "blocked": ["GEMINI_API_KEY"]
            }
        },
        "telemetry": {
            "enabled": false,
            "logPrompts": false
        }
    });
    let mut settings_bytes = serde_json::to_vec_pretty(&settings_document)
        .map_err(|error| AppError::Config(format!("无法生成 Gemini CLI settings.json：{error}")))?;
    settings_bytes.push(b'\n');

    let environment_bytes = format!(
        "GEMINI_API_KEY={}\nGEMINI_MODEL={}\nGOOGLE_GEMINI_BASE_URL={}\nGOOGLE_GENAI_API_VERSION=\"v1beta\"\nGEMINI_TELEMETRY_ENABLED=\"false\"\nGEMINI_TELEMETRY_LOG_PROMPTS=\"false\"\n",
        dotenv_quote(profile.employee_key.expose_secret().trim()),
        dotenv_quote(&profile.default_model),
        dotenv_quote(&normalize_cpa_gateway_url(&profile.base_url)?),
    )
    .into_bytes();
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "gemini".into(),
        settings_digest: hex::encode(Sha256::digest(&settings_bytes)),
        environment_digest: hex::encode(Sha256::digest(&environment_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Gemini CLI 所有权记录：{error}")))?;

    Ok(GeminiPlan {
        settings: file_plan(settings_path, None, settings_bytes),
        environment: file_plan(environment_path, None, environment_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model: profile.default_model.clone(),
    })
}

/// Create complete Gemini CLI user configuration only when all three managed
/// files are absent. Other Gemini state, history and trusted-folder files stay
/// untouched.
pub fn apply_new_gemini(
    root: &Path,
    settings_relative: &Path,
    environment_relative: &Path,
    ownership_relative: &Path,
    profile: &GeminiProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(settings_relative, "Gemini CLI")?;
    validate_transaction_relative(environment_relative, "Gemini CLI")?;
    validate_transaction_relative(ownership_relative, "Gemini CLI")?;
    validate_gemini_paths(settings_relative, environment_relative, ownership_relative)?;
    let plan = plan_new_gemini(
        &root.join(settings_relative),
        &root.join(environment_relative),
        &root.join(ownership_relative),
        profile,
    )?;
    apply_create_only(
        root,
        vec![
            CreateFilePlan::new(settings_relative, plan.settings.desired_bytes.to_vec()),
            CreateFilePlan::new(
                environment_relative,
                plan.environment.desired_bytes.to_vec(),
            ),
            CreateFilePlan::new(ownership_relative, plan.ownership.desired_bytes.to_vec()),
        ],
    )
    .map_err(|error| transaction_error("Gemini CLI", error))
}

fn validate_gemini_paths(
    settings_relative: &Path,
    environment_relative: &Path,
    ownership_relative: &Path,
) -> Result<(), AppError> {
    let settings_name = settings_relative
        .file_name()
        .and_then(|value| value.to_str());
    let environment_name = environment_relative
        .file_name()
        .and_then(|value| value.to_str());
    let ownership_name = ownership_relative
        .file_name()
        .and_then(|value| value.to_str());
    let same_parent = settings_relative.parent() == environment_relative.parent()
        && settings_relative.parent() == ownership_relative.parent();
    if settings_name != Some("settings.json")
        || environment_name != Some(".env")
        || ownership_name != Some("cpa-cloud.ownership.json")
        || !same_parent
        || settings_relative == environment_relative
        || settings_relative == ownership_relative
        || environment_relative == ownership_relative
    {
        return Err(AppError::Config(
            "Gemini CLI 安全写入只接受同一配置目录内互不相同的 settings.json、.env 和 cpa-cloud.ownership.json"
                .into(),
        ));
    }
    Ok(())
}

fn validate_profile(profile: &GeminiProfile) -> Result<(), AppError> {
    let key = profile.employee_key.expose_secret().trim();
    if key.is_empty() || key.chars().any(|ch| ch.is_control()) {
        return Err(AppError::Config("员工 Key 不能为空或包含控制字符".into()));
    }
    if profile.default_model.is_empty()
        || profile.default_model.len() > 200
        || profile.default_model.chars().any(|ch| ch.is_control())
    {
        return Err(AppError::Config("Gemini CLI 默认模型无效".into()));
    }
    Ok(())
}

fn dotenv_quote(value: &str) -> String {
    let mut result = String::with_capacity(value.len() + 2);
    result.push('"');
    for character in value.chars() {
        match character {
            '\\' => result.push_str("\\\\"),
            '"' => result.push_str("\\\""),
            '\n' => result.push_str("\\n"),
            '\r' => result.push_str("\\r"),
            '\t' => result.push_str("\\t"),
            other => result.push(other),
        }
    }
    result.push('"');
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    fn configuration() -> ClientConfiguration {
        ClientConfiguration {
            schema_version: 1,
            protocols: vec![
                crate::discovery::ProtocolConfiguration {
                    protocol: "openai-chat".into(),
                    candidates: vec!["chat-only".into()],
                    default_model: "chat-only".into(),
                    default_source: "public_id_ascending".into(),
                },
                crate::discovery::ProtocolConfiguration {
                    protocol: "gemini-generate-content".into(),
                    candidates: vec!["gemini-alpha".into(), "gemini-beta".into()],
                    default_model: "gemini-beta".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> GeminiProfile {
        GeminiProfile::for_gemini(
            "https://cpa.example.test/company/v1",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn plan_selects_gateway_auth_and_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_gemini(
            &temp.path().join("settings.json"),
            &temp.path().join(".env"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "gemini-beta");
        let settings: serde_json::Value =
            serde_json::from_slice(&plan.settings.desired_bytes).unwrap();
        assert_eq!(settings["security"]["auth"]["selectedType"], "gateway");
        assert_eq!(settings["telemetry"]["enabled"], false);
        assert_eq!(settings["telemetry"]["logPrompts"], false);
        let environment = String::from_utf8(plan.environment.desired_bytes.to_vec()).unwrap();
        assert!(environment.contains("GEMINI_API_KEY=\"employee-secret\""));
        assert!(environment.contains("GEMINI_MODEL=\"gemini-beta\""));
        assert!(environment.contains("GOOGLE_GEMINI_BASE_URL=\"https://cpa.example.test/company\""));
        assert!(environment.contains("GOOGLE_GENAI_API_VERSION=\"v1beta\""));

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn dotenv_values_are_quoted_without_line_injection() {
        assert_eq!(dotenv_quote("a=b # c"), "\"a=b # c\"");
        assert_eq!(dotenv_quote("quote\\\""), "\"quote\\\\\\\"\"");
    }

    #[test]
    fn create_only_transaction_leaves_other_gemini_state_untouched() {
        let temp = tempfile::tempdir().unwrap();
        let gemini = temp.path().join(".gemini");
        std::fs::create_dir(&gemini).unwrap();
        let trusted = gemini.join("trustedFolders.json");
        std::fs::write(&trusted, b"trusted-sentinel").unwrap();
        let trusted_identity = crate::live::engine::test_file_identity(&trusted).unwrap();
        let trusted_modified = std::fs::metadata(&trusted).unwrap().modified().unwrap();

        let receipt = apply_new_gemini(
            temp.path(),
            Path::new(".gemini/settings.json"),
            Path::new(".gemini/.env"),
            Path::new(".gemini/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();

        assert_eq!(receipt.created.len(), 3);
        assert_eq!(std::fs::read(&trusted).unwrap(), b"trusted-sentinel");
        assert_eq!(
            crate::live::engine::test_file_identity(&trusted).unwrap(),
            trusted_identity
        );
        assert_eq!(
            std::fs::metadata(&trusted).unwrap().modified().unwrap(),
            trusted_modified
        );
    }

    #[test]
    fn create_only_transaction_refuses_any_existing_managed_file() {
        let temp = tempfile::tempdir().unwrap();
        let gemini = temp.path().join(".gemini");
        std::fs::create_dir(&gemini).unwrap();
        let settings = gemini.join("settings.json");
        let original = b"{\"security\":{}}\n";
        std::fs::write(&settings, original).unwrap();
        let error = apply_new_gemini(
            temp.path(),
            Path::new(".gemini/settings.json"),
            Path::new(".gemini/.env"),
            Path::new(".gemini/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("existing Gemini settings must remain blocked");
        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read(settings).unwrap(), original);
    }

    #[test]
    fn create_only_transaction_refuses_an_ownership_only_collision() {
        let temp = tempfile::tempdir().unwrap();
        let gemini = temp.path().join(".gemini");
        std::fs::create_dir(&gemini).unwrap();
        let ownership = gemini.join("cpa-cloud.ownership.json");
        let original = b"{\"tool\":\"someone-else\"}\n";
        std::fs::write(&ownership, original).unwrap();

        let error = apply_new_gemini(
            temp.path(),
            Path::new(".gemini/settings.json"),
            Path::new(".gemini/.env"),
            Path::new(".gemini/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("ownership-only collision must remain blocked");

        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read(ownership).unwrap(), original);
        assert!(!gemini.join("settings.json").exists());
        assert!(!gemini.join(".env").exists());
    }

    #[test]
    fn profile_rejects_a_default_outside_the_candidate_list() {
        let mut configuration = configuration();
        configuration.protocols[1].default_model = "missing".into();
        let error = GeminiProfile::for_gemini(
            "https://cpa.example.test",
            SecretString::new("employee-secret"),
            &configuration,
        )
        .expect_err("invalid default must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_parent_traversal() {
        let temp = tempfile::tempdir().unwrap();
        let error = apply_new_gemini(
            temp.path(),
            Path::new("../settings.json"),
            Path::new(".gemini/.env"),
            Path::new(".gemini/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (settings, environment, ownership) in [
            (
                ".gemini/custom.json",
                ".gemini/.env",
                ".gemini/cpa-cloud.ownership.json",
            ),
            (
                ".gemini/settings.json",
                "other/.env",
                ".gemini/cpa-cloud.ownership.json",
            ),
            (
                ".gemini/settings.json",
                ".gemini/.env",
                ".gemini/ownership.json",
            ),
            (
                ".gemini/settings.json",
                ".gemini/settings.json",
                ".gemini/cpa-cloud.ownership.json",
            ),
        ] {
            let error = apply_new_gemini(
                temp.path(),
                Path::new(settings),
                Path::new(environment),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical Gemini CLI paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
