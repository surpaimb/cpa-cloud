//! Codex provider projection for the user-level `~/.codex/config.toml` file.
//!
//! This module is independently authored from the public Codex configuration
//! contract recorded in `PROVENANCE.md`. It deliberately creates a complete
//! configuration only below an isolated synthetic root. Existing Codex config
//! remains blocked until the replacement transaction gates are complete.

use std::path::Path;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use toml_edit::{value, DocumentMut, Item, Table};

use crate::live::engine::{apply_create_only, CreateFilePlan, CreateOnlyReceipt};
use crate::plan::{
    file_plan, normalize_cpa_base_url, read_optional, transaction_error,
    validate_transaction_relative, FilePlan,
};
use crate::{AppError, ClientConfiguration, SecretString};

const PROVIDER_ID: &str = "cpa_cloud";

#[derive(Debug, Clone)]
pub struct CodexProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub default_model: String,
}

impl CodexProfile {
    pub fn for_codex(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let Some(protocol) = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "openai-responses")
        else {
            return Err(AppError::Config(
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 Codex".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 Codex 提供有效的默认 Responses 模型".into(),
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
pub struct CodexPlan {
    pub config: FilePlan,
    pub ownership: FilePlan,
    pub selected_model: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    provider_id: String,
    config_digest: String,
}

pub fn plan_new_codex(
    config_path: &Path,
    ownership_path: &Path,
    profile: &CodexProfile,
) -> Result<CodexPlan, AppError> {
    validate_profile(profile)?;
    let previous_config = read_optional(config_path)?;
    let previous_ownership = read_optional(ownership_path)?;
    if previous_config.is_some() || previous_ownership.is_some() {
        return Err(AppError::Conflict(
            "Codex 已存在 config.toml 或 CPA 所有权文件；create-only 安全阶段不会替换它们".into(),
        ));
    }

    let mut document = DocumentMut::new();
    document["model"] = value(profile.default_model.clone());
    document["model_provider"] = value(PROVIDER_ID);

    let mut provider = Table::new();
    provider["name"] = value("CPA Cloud");
    provider["base_url"] = value(normalize_cpa_base_url(&profile.base_url)?);
    provider["experimental_bearer_token"] = value(profile.employee_key.expose_secret().trim());
    provider["wire_api"] = value("responses");
    provider["requires_openai_auth"] = value(false);
    provider["supports_websockets"] = value(false);
    provider["request_max_retries"] = value(0_i64);
    provider["stream_max_retries"] = value(0_i64);

    let mut providers = Table::new();
    providers.insert(PROVIDER_ID, Item::Table(provider));
    document["model_providers"] = Item::Table(providers);

    let config_bytes = document.to_string().into_bytes();
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "codex".into(),
        provider_id: PROVIDER_ID.into(),
        config_digest: hex::encode(Sha256::digest(&config_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Codex 所有权记录：{error}")))?;

    Ok(CodexPlan {
        config: file_plan(config_path, None, config_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model: profile.default_model.clone(),
    })
}

/// Create a complete Codex configuration only when both managed files are absent.
///
/// Codex-owned `auth.json` remains untouched. The generated user-level TOML
/// embeds the employee Key as a bearer token because this preview must require
/// only URL + Key; real writes remain disabled until replacement and native
/// client acceptance gates are complete.
pub fn apply_new_codex(
    root: &Path,
    config_relative: &Path,
    ownership_relative: &Path,
    profile: &CodexProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(config_relative, "Codex")?;
    validate_transaction_relative(ownership_relative, "Codex")?;
    validate_codex_paths(config_relative, ownership_relative)?;
    let plan = plan_new_codex(
        &root.join(config_relative),
        &root.join(ownership_relative),
        profile,
    )?;
    apply_create_only(
        root,
        vec![
            CreateFilePlan::new(config_relative, plan.config.desired_bytes.to_vec()),
            CreateFilePlan::new(ownership_relative, plan.ownership.desired_bytes.to_vec()),
        ],
    )
    .map_err(|error| transaction_error("Codex", error))
}

fn validate_codex_paths(config_relative: &Path, ownership_relative: &Path) -> Result<(), AppError> {
    let config_name = config_relative.file_name().and_then(|value| value.to_str());
    let ownership_name = ownership_relative
        .file_name()
        .and_then(|value| value.to_str());
    if config_name != Some("config.toml")
        || ownership_name != Some("cpa-cloud.ownership.json")
        || config_relative.parent() != ownership_relative.parent()
        || config_relative == ownership_relative
    {
        return Err(AppError::Config(
            "Codex 安全写入只接受同一配置目录内的 config.toml 和 cpa-cloud.ownership.json".into(),
        ));
    }
    Ok(())
}

fn validate_profile(profile: &CodexProfile) -> Result<(), AppError> {
    if profile.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }
    if profile.default_model.is_empty()
        || profile.default_model.len() > 200
        || profile.default_model.chars().any(|ch| ch.is_control())
    {
        return Err(AppError::Config("Codex 默认模型无效".into()));
    }
    Ok(())
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
                    protocol: "openai-responses".into(),
                    candidates: vec!["alpha".into(), "beta".into()],
                    default_model: "beta".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> CodexProfile {
        CodexProfile::for_codex(
            "https://cpa.example.test",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn codex_uses_the_responses_default_model() {
        let profile = profile();
        assert_eq!(profile.default_model, "beta");
    }

    #[test]
    fn plan_selects_the_cpa_provider_and_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_codex(
            &temp.path().join("config.toml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "beta");
        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        let document = source.parse::<DocumentMut>().unwrap();
        let provider = &document["model_providers"][PROVIDER_ID];
        assert_eq!(document["model"].as_str(), Some("beta"));
        assert_eq!(document["model_provider"].as_str(), Some(PROVIDER_ID));
        assert_eq!(provider["name"].as_str(), Some("CPA Cloud"));
        assert_eq!(
            provider["base_url"].as_str(),
            Some("https://cpa.example.test/v1")
        );
        assert_eq!(
            provider["experimental_bearer_token"].as_str(),
            Some("employee-secret")
        );
        assert_eq!(provider["wire_api"].as_str(), Some("responses"));
        assert_eq!(provider["requires_openai_auth"].as_bool(), Some(false));
        assert_eq!(provider["supports_websockets"].as_bool(), Some(false));
        assert_eq!(provider["request_max_retries"].as_integer(), Some(0));
        assert_eq!(provider["stream_max_retries"].as_integer(), Some(0));

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn create_only_transaction_publishes_codex_config_and_ownership_together() {
        let temp = tempfile::tempdir().unwrap();
        let codex = temp.path().join(".codex");
        std::fs::create_dir(&codex).unwrap();
        let auth = codex.join("auth.json");
        std::fs::write(&auth, b"auth-sentinel").unwrap();
        let auth_identity = crate::live::engine::test_file_identity(&auth).unwrap();
        let auth_modified = std::fs::metadata(&auth).unwrap().modified().unwrap();

        let receipt = apply_new_codex(
            temp.path(),
            Path::new(".codex/config.toml"),
            Path::new(".codex/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();

        assert_eq!(receipt.created.len(), 2);
        assert!(temp.path().join(".codex/config.toml").exists());
        assert!(temp.path().join(".codex/cpa-cloud.ownership.json").exists());
        assert_eq!(std::fs::read(&auth).unwrap(), b"auth-sentinel");
        assert_eq!(
            crate::live::engine::test_file_identity(&auth).unwrap(),
            auth_identity
        );
        assert_eq!(
            std::fs::metadata(&auth).unwrap().modified().unwrap(),
            auth_modified
        );
    }

    #[test]
    fn create_only_transaction_refuses_existing_codex_config() {
        let temp = tempfile::tempdir().unwrap();
        let codex = temp.path().join(".codex");
        std::fs::create_dir(&codex).unwrap();
        let config = codex.join("config.toml");
        let original = "model = \"theirs\"\n";
        std::fs::write(&config, original).unwrap();
        let error = apply_new_codex(
            temp.path(),
            Path::new(".codex/config.toml"),
            Path::new(".codex/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("existing Codex config must remain blocked");
        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read_to_string(config).unwrap(), original);
    }

    #[test]
    fn profile_rejects_a_default_outside_the_candidate_list() {
        let mut configuration = configuration();
        configuration.protocols[1].default_model = "missing".into();
        let error = CodexProfile::for_codex(
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
        let error = apply_new_codex(
            temp.path(),
            Path::new("../config.toml"),
            Path::new("ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_codex_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (config, ownership) in [
            (".codex/custom.toml", ".codex/cpa-cloud.ownership.json"),
            (".codex/config.toml", "other/cpa-cloud.ownership.json"),
            (".codex/config.toml", ".codex/ownership.json"),
            (".codex/config.toml", ".codex/config.toml"),
        ] {
            let error = apply_new_codex(
                temp.path(),
                Path::new(config),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical Codex paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
