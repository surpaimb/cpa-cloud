//! Hermes Agent named custom-provider projection for `config.yaml`.
//!
//! This module is independently authored from the fixed official Hermes
//! configuration contract recorded in `PROVENANCE.md`. It creates a complete
//! configuration only below an isolated synthetic root. Existing Hermes
//! configuration remains blocked until the replacement transaction gates are
//! complete.

use std::collections::HashSet;
use std::path::Path;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::live::engine::{apply_create_only, CreateFilePlan, CreateOnlyReceipt};
use crate::plan::{
    file_plan, normalize_cpa_base_url, read_optional, transaction_error,
    validate_transaction_relative, FilePlan,
};
use crate::{AppError, ClientConfiguration, SecretString};

const PROVIDER_ID: &str = "cpa-cloud";

#[derive(Debug, Clone)]
pub struct HermesProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
    pub default_model: String,
}

impl HermesProfile {
    pub fn for_hermes(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let Some(protocol) = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "openai-chat")
        else {
            return Err(AppError::Config(
                "当前员工 Key 没有 OpenAI Chat 协议模型，不能配置 Hermes".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 Hermes 提供有效的默认 Chat 模型".into(),
            ));
        }
        Ok(Self {
            base_url: base_url.into(),
            employee_key,
            models: protocol.candidates.clone(),
            default_model: protocol.default_model.clone(),
        })
    }
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct HermesPlan {
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

pub fn plan_new_hermes(
    config_path: &Path,
    ownership_path: &Path,
    profile: &HermesProfile,
) -> Result<HermesPlan, AppError> {
    validate_profile(profile)?;
    if read_optional(config_path)?.is_some() || read_optional(ownership_path)?.is_some() {
        return Err(AppError::Conflict(
            "Hermes 已存在 config.yaml 或 CPA 所有权文件；create-only 安全阶段不会替换它们".into(),
        ));
    }

    let base_url = normalize_cpa_base_url(&profile.base_url)?;
    let config_bytes = render_config(profile, &base_url)?.into_bytes();
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "hermes".into(),
        provider_id: PROVIDER_ID.into(),
        config_digest: hex::encode(Sha256::digest(&config_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Hermes 所有权记录：{error}")))?;

    Ok(HermesPlan {
        config: file_plan(config_path, None, config_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model: profile.default_model.clone(),
    })
}

/// Create a complete Hermes configuration only when both managed files are
/// absent. Hermes-owned `.env`, `auth.json`, and all other state remain
/// untouched.
pub fn apply_new_hermes(
    root: &Path,
    config_relative: &Path,
    ownership_relative: &Path,
    profile: &HermesProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(config_relative, "Hermes")?;
    validate_transaction_relative(ownership_relative, "Hermes")?;
    validate_hermes_paths(config_relative, ownership_relative)?;
    let plan = plan_new_hermes(
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
    .map_err(|error| transaction_error("Hermes", error))
}

fn validate_hermes_paths(
    config_relative: &Path,
    ownership_relative: &Path,
) -> Result<(), AppError> {
    let config_name = config_relative.file_name().and_then(|value| value.to_str());
    let ownership_name = ownership_relative
        .file_name()
        .and_then(|value| value.to_str());
    if config_name != Some("config.yaml")
        || ownership_name != Some("cpa-cloud.ownership.json")
        || config_relative.parent() != ownership_relative.parent()
        || config_relative == ownership_relative
    {
        return Err(AppError::Config(
            "Hermes 安全写入只接受同一配置目录内的 config.yaml 和 cpa-cloud.ownership.json".into(),
        ));
    }
    Ok(())
}

fn render_config(profile: &HermesProfile, base_url: &str) -> Result<String, AppError> {
    let quote = |value: &str| {
        serde_json::to_string(value)
            .map_err(|error| AppError::Config(format!("无法编码 Hermes YAML 字符串：{error}")))
    };
    let mut source = String::new();
    source.push_str("model:\n");
    source.push_str(&format!("  provider: {}\n", quote("custom:cpa-cloud")?));
    source.push_str(&format!("  default: {}\n", quote(&profile.default_model)?));
    source.push_str("  base_url: \"\"\n");
    source.push_str("  api_mode: \"chat_completions\"\n");
    source.push_str("providers:\n");
    source.push_str(&format!("  {PROVIDER_ID}:\n"));
    source.push_str("    name: \"CPA Cloud\"\n");
    source.push_str(&format!("    api: {}\n", quote(base_url)?));
    source.push_str(&format!(
        "    api_key: {}\n",
        quote(profile.employee_key.expose_secret().trim())?
    ));
    source.push_str("    transport: \"chat_completions\"\n");
    source.push_str(&format!(
        "    default_model: {}\n",
        quote(&profile.default_model)?
    ));
    source.push_str("    discover_models: false\n");
    source.push_str("    models:\n");
    for model in &profile.models {
        source.push_str(&format!("      - {}\n", quote(model)?));
    }
    Ok(source)
}

fn validate_profile(profile: &HermesProfile) -> Result<(), AppError> {
    let key = profile.employee_key.expose_secret().trim();
    if key.is_empty() || key.chars().any(|ch| ch.is_control()) {
        return Err(AppError::Config("员工 Key 不能为空或包含控制字符".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config("Hermes 模型列表不能为空".into()));
    }
    let mut unique = HashSet::new();
    if profile.models.iter().any(|model| {
        model.is_empty()
            || model.len() > 200
            || model.chars().any(|ch| ch.is_control())
            || !unique.insert(model)
    }) {
        return Err(AppError::Config(
            "Hermes 模型标识无效、重复或包含控制字符".into(),
        ));
    }
    if !profile.models.contains(&profile.default_model) {
        return Err(AppError::Config(
            "Hermes 默认模型不在员工 Key 可用列表中".into(),
        ));
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
                    candidates: vec!["alpha".into(), "vendor/model.beta".into()],
                    default_model: "vendor/model.beta".into(),
                    default_source: "public_id_ascending".into(),
                },
                crate::discovery::ProtocolConfiguration {
                    protocol: "openai-responses".into(),
                    candidates: vec!["responses-only".into()],
                    default_model: "responses-only".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> HermesProfile {
        HermesProfile::for_hermes(
            "https://cpa.example.test/company/v1",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn plan_registers_chat_models_and_selects_the_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_hermes(
            &temp.path().join("config.yaml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "vendor/model.beta");

        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        assert!(source.contains("provider: \"custom:cpa-cloud\""));
        assert!(source.contains("default: \"vendor/model.beta\""));
        assert!(source.contains("api: \"https://cpa.example.test/company/v1\""));
        assert!(source.contains("api_key: \"employee-secret\""));
        assert!(source.contains("transport: \"chat_completions\""));
        assert!(source.contains("discover_models: false"));
        assert!(source.contains("- \"alpha\""));
        assert!(source.contains("- \"vendor/model.beta\""));
        assert!(!source.contains("responses-only"));

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn fixed_source_fixture_records_the_current_yaml_contract() {
        let fixture: serde_json::Value = serde_json::from_str(include_str!(
            "../tests/fixtures/hermes-agent-0e427d7/config-contract.json"
        ))
        .unwrap();
        assert_eq!(fixture["config_path"], "~/.hermes/config.yaml");
        assert_eq!(fixture["provider_selector"], "custom:<name>");
        assert_eq!(fixture["transport"], "chat_completions");
        assert_eq!(fixture["inline_credential_field"], "api_key");
    }

    #[test]
    fn yaml_string_encoding_prevents_key_and_model_injection() {
        let temp = tempfile::tempdir().unwrap();
        let mut profile = profile();
        profile.employee_key = SecretString::new("quote\" and slash\\");
        profile.models = vec!["plain".into(), "colon: # still scalar".into()];
        profile.default_model = "colon: # still scalar".into();
        let plan = plan_new_hermes(
            &temp.path().join("config.yaml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile,
        )
        .unwrap();
        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        assert!(source.contains("api_key: \"quote\\\" and slash\\\\\""));
        assert!(source.contains("- \"colon: # still scalar\""));
        assert_eq!(source.matches("providers:").count(), 1);
    }

    #[test]
    fn create_only_transaction_leaves_hermes_state_untouched() {
        let temp = tempfile::tempdir().unwrap();
        let hermes = temp.path().join(".hermes");
        std::fs::create_dir(&hermes).unwrap();
        let sentinels = [
            (".env", b"env-sentinel".as_slice()),
            ("auth.json", b"auth-sentinel".as_slice()),
            ("SOUL.md", b"soul-sentinel".as_slice()),
        ];
        let mut before = Vec::new();
        for (name, bytes) in sentinels {
            let path = hermes.join(name);
            std::fs::write(&path, bytes).unwrap();
            before.push((
                path.clone(),
                bytes.to_vec(),
                crate::live::engine::test_file_identity(&path).unwrap(),
                std::fs::metadata(&path).unwrap().modified().unwrap(),
            ));
        }

        let receipt = apply_new_hermes(
            temp.path(),
            Path::new(".hermes/config.yaml"),
            Path::new(".hermes/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();

        assert_eq!(receipt.created.len(), 2);
        for (path, bytes, identity, modified) in before {
            assert_eq!(std::fs::read(&path).unwrap(), bytes);
            assert_eq!(
                crate::live::engine::test_file_identity(&path).unwrap(),
                identity
            );
            assert_eq!(
                std::fs::metadata(path).unwrap().modified().unwrap(),
                modified
            );
        }
    }

    #[test]
    fn create_only_transaction_refuses_each_managed_file_collision() {
        for existing in ["config.yaml", "cpa-cloud.ownership.json"] {
            let temp = tempfile::tempdir().unwrap();
            let hermes = temp.path().join(".hermes");
            std::fs::create_dir(&hermes).unwrap();
            let existing_path = hermes.join(existing);
            std::fs::write(&existing_path, b"owned-by-someone-else").unwrap();

            let error = apply_new_hermes(
                temp.path(),
                Path::new(".hermes/config.yaml"),
                Path::new(".hermes/cpa-cloud.ownership.json"),
                &profile(),
            )
            .expect_err("each managed-file collision must remain blocked");

            assert!(matches!(error, AppError::Conflict(_)));
            assert_eq!(
                std::fs::read(existing_path).unwrap(),
                b"owned-by-someone-else"
            );
        }
    }

    #[test]
    fn profile_rejects_a_default_outside_the_candidate_list() {
        let mut configuration = configuration();
        configuration.protocols[0].default_model = "missing".into();
        let error = HermesProfile::for_hermes(
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
        let error = apply_new_hermes(
            temp.path(),
            Path::new("../config.yaml"),
            Path::new(".hermes/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (config, ownership) in [
            (".hermes/custom.yaml", ".hermes/cpa-cloud.ownership.json"),
            (".hermes/config.yaml", "other/cpa-cloud.ownership.json"),
            (".hermes/config.yaml", ".hermes/ownership.json"),
            (".hermes/config.yaml", ".hermes/config.yaml"),
        ] {
            let error = apply_new_hermes(
                temp.path(),
                Path::new(config),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical Hermes paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
