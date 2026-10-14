//! Grok Build custom-model projection for `~/.grok/config.toml`.
//!
//! This module is independently authored from the fixed official Grok Build
//! custom-model contract recorded in `PROVENANCE.md`. It creates a complete
//! configuration only below an isolated synthetic root. Existing Grok config
//! remains blocked until the replacement transaction gates are complete.

use std::collections::HashSet;
use std::path::Path;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use toml_edit::{value, Array, DocumentMut, Item, Table};

use crate::live::engine::{apply_create_only, CreateFilePlan, CreateOnlyReceipt};
use crate::plan::{
    file_plan, normalize_cpa_base_url, read_optional, transaction_error,
    validate_transaction_relative, FilePlan,
};
use crate::{AppError, ClientConfiguration, SecretString};

#[derive(Debug, Clone)]
pub struct GrokBuildProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
    pub default_model: String,
}

impl GrokBuildProfile {
    pub fn for_grok_build(
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
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 Grok Build".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 Grok Build 提供有效的默认 Responses 模型".into(),
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
pub struct GrokBuildPlan {
    pub config: FilePlan,
    pub ownership: FilePlan,
    pub selected_model: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    config_digest: String,
}

pub fn plan_new_grok_build(
    config_path: &Path,
    ownership_path: &Path,
    profile: &GrokBuildProfile,
) -> Result<GrokBuildPlan, AppError> {
    validate_profile(profile)?;
    if read_optional(config_path)?.is_some() || read_optional(ownership_path)?.is_some() {
        return Err(AppError::Conflict(
            "Grok Build 已存在 config.toml 或 CPA 所有权文件；create-only 安全阶段不会替换它们"
                .into(),
        ));
    }

    let mut document = DocumentMut::new();

    let mut models = Table::new();
    models["default"] = value(profile.default_model.clone());
    let mut allowed_models = Array::new();
    for model in &profile.models {
        allowed_models.push(model.as_str());
    }
    models["allowed_models"] = value(allowed_models);
    document["models"] = Item::Table(models);

    let base_url = normalize_cpa_base_url(&profile.base_url)?;
    let mut catalog = Table::new();
    for model in &profile.models {
        let mut entry = Table::new();
        entry["model"] = value(model.clone());
        entry["base_url"] = value(base_url.clone());
        entry["name"] = value(model.clone());
        entry["api_key"] = value(profile.employee_key.expose_secret().trim());
        entry["api_backend"] = value("responses");
        entry["reasoning_summary"] = value("none");
        catalog.insert(model, Item::Table(entry));
    }
    document["model"] = Item::Table(catalog);

    let mut features = Table::new();
    features["telemetry"] = value(false);
    features["feedback"] = value(false);
    document["features"] = Item::Table(features);

    let config_bytes = document.to_string().into_bytes();
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "grok-build".into(),
        config_digest: hex::encode(Sha256::digest(&config_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Grok Build 所有权记录：{error}")))?;

    Ok(GrokBuildPlan {
        config: file_plan(config_path, None, config_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model: profile.default_model.clone(),
    })
}

/// Create a complete Grok Build configuration only when both managed files are
/// absent. Grok-owned `auth.json` remains untouched.
pub fn apply_new_grok_build(
    root: &Path,
    config_relative: &Path,
    ownership_relative: &Path,
    profile: &GrokBuildProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(config_relative, "Grok Build")?;
    validate_transaction_relative(ownership_relative, "Grok Build")?;
    validate_grok_build_paths(config_relative, ownership_relative)?;
    let plan = plan_new_grok_build(
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
    .map_err(|error| transaction_error("Grok Build", error))
}

fn validate_grok_build_paths(
    config_relative: &Path,
    ownership_relative: &Path,
) -> Result<(), AppError> {
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
            "Grok Build 安全写入只接受同一配置目录内的 config.toml 和 cpa-cloud.ownership.json"
                .into(),
        ));
    }
    Ok(())
}

fn validate_profile(profile: &GrokBuildProfile) -> Result<(), AppError> {
    let key = profile.employee_key.expose_secret().trim();
    if key.is_empty() || key.chars().any(|ch| ch.is_control()) {
        return Err(AppError::Config("员工 Key 不能为空或包含控制字符".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config("Grok Build 模型列表不能为空".into()));
    }
    let mut unique = HashSet::new();
    if profile.models.iter().any(|model| {
        model.is_empty()
            || model.len() > 200
            || model.chars().any(|ch| ch.is_control())
            || !unique.insert(model)
    }) {
        return Err(AppError::Config(
            "Grok Build 模型标识无效、重复或包含控制字符".into(),
        ));
    }
    if !profile.models.contains(&profile.default_model) {
        return Err(AppError::Config(
            "Grok Build 默认模型不在员工 Key 可用列表中".into(),
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
                    candidates: vec!["chat-only".into()],
                    default_model: "chat-only".into(),
                    default_source: "public_id_ascending".into(),
                },
                crate::discovery::ProtocolConfiguration {
                    protocol: "openai-responses".into(),
                    candidates: vec!["alpha".into(), "vendor/model.beta".into()],
                    default_model: "vendor/model.beta".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> GrokBuildProfile {
        GrokBuildProfile::for_grok_build(
            "https://cpa.example.test/company/v1",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn plan_registers_responses_models_and_selects_the_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_grok_build(
            &temp.path().join("config.toml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "vendor/model.beta");

        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        let document = source.parse::<DocumentMut>().unwrap();
        assert_eq!(
            document["models"]["default"].as_str(),
            Some("vendor/model.beta")
        );
        let allowed = document["models"]["allowed_models"].as_array().unwrap();
        assert_eq!(allowed.len(), 2);
        assert_eq!(allowed.get(0).and_then(|item| item.as_str()), Some("alpha"));
        assert_eq!(
            allowed.get(1).and_then(|item| item.as_str()),
            Some("vendor/model.beta")
        );
        for model in ["alpha", "vendor/model.beta"] {
            let entry = &document["model"][model];
            assert_eq!(entry["model"].as_str(), Some(model));
            assert_eq!(
                entry["base_url"].as_str(),
                Some("https://cpa.example.test/company/v1")
            );
            assert_eq!(entry["api_key"].as_str(), Some("employee-secret"));
            assert_eq!(entry["api_backend"].as_str(), Some("responses"));
            assert_eq!(entry["reasoning_summary"].as_str(), Some("none"));
        }
        assert_eq!(document["features"]["telemetry"].as_bool(), Some(false));
        assert_eq!(document["features"]["feedback"].as_bool(), Some(false));

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn fixed_source_fixture_includes_the_responses_contract() {
        let fixture: serde_json::Value = serde_json::from_str(include_str!(
            "../tests/fixtures/grok-build-2bdd1d6/custom-models-contract.json"
        ))
        .unwrap();
        assert_eq!(fixture["config_path"], "~/.grok/config.toml");
        assert!(fixture["api_backends"]
            .as_array()
            .unwrap()
            .iter()
            .any(|value| value == "responses"));
        assert_eq!(fixture["credential_header"], "Authorization: Bearer");
    }

    #[test]
    fn toml_encoder_prevents_employee_key_injection() {
        let temp = tempfile::tempdir().unwrap();
        let mut profile = profile();
        profile.employee_key = SecretString::new("quote\" and slash\\");
        let plan = plan_new_grok_build(
            &temp.path().join("config.toml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile,
        )
        .unwrap();
        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        let document = source.parse::<DocumentMut>().unwrap();

        for model in ["alpha", "vendor/model.beta"] {
            assert_eq!(
                document["model"][model]["api_key"].as_str(),
                Some("quote\" and slash\\")
            );
        }
    }

    #[test]
    fn create_only_transaction_leaves_grok_auth_untouched() {
        let temp = tempfile::tempdir().unwrap();
        let grok = temp.path().join(".grok");
        std::fs::create_dir(&grok).unwrap();
        let auth = grok.join("auth.json");
        std::fs::write(&auth, b"auth-sentinel").unwrap();
        let auth_identity = crate::live::engine::test_file_identity(&auth).unwrap();
        let auth_modified = std::fs::metadata(&auth).unwrap().modified().unwrap();

        let receipt = apply_new_grok_build(
            temp.path(),
            Path::new(".grok/config.toml"),
            Path::new(".grok/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();

        assert_eq!(receipt.created.len(), 2);
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
    fn create_only_transaction_refuses_each_managed_file_collision() {
        for existing in ["config.toml", "cpa-cloud.ownership.json"] {
            let temp = tempfile::tempdir().unwrap();
            let grok = temp.path().join(".grok");
            std::fs::create_dir(&grok).unwrap();
            let existing_path = grok.join(existing);
            std::fs::write(&existing_path, b"owned-by-someone-else").unwrap();

            let error = apply_new_grok_build(
                temp.path(),
                Path::new(".grok/config.toml"),
                Path::new(".grok/cpa-cloud.ownership.json"),
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
        configuration.protocols[1].default_model = "missing".into();
        let error = GrokBuildProfile::for_grok_build(
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
        let error = apply_new_grok_build(
            temp.path(),
            Path::new("../config.toml"),
            Path::new(".grok/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (config, ownership) in [
            (".grok/custom.toml", ".grok/cpa-cloud.ownership.json"),
            (".grok/config.toml", "other/cpa-cloud.ownership.json"),
            (".grok/config.toml", ".grok/ownership.json"),
            (".grok/config.toml", ".grok/config.toml"),
        ] {
            let error = apply_new_grok_build(
                temp.path(),
                Path::new(config),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical Grok Build paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
