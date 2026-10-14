//! OpenClaw provider projection for `~/.openclaw/openclaw.json`.
//!
//! Portions adapted from CC Switch, Copyright 2025 Jason Young, under the
//! MIT License. See `PROVENANCE.md` and `third_party/cc-switch/LICENSE`.
//!
//! The `models.providers` provider-node shape and `agents.defaults.model`
//! selection path are adapted from CC Switch at fixed MIT revision
//! `c21106ff20e351d40f412cb51d1f24da177b113d`,
//! `src-tauri/src/openclaw_config.rs`. CPA endpoint/protocol mapping,
//! ownership, validation, and transaction handling are independently authored.

use std::path::Path;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use crate::live::engine::{apply_create_only, CreateFilePlan, CreateOnlyReceipt};
use crate::plan::{
    file_plan, normalize_cpa_base_url, read_optional, transaction_error,
    validate_transaction_relative, FilePlan,
};
use crate::{AppError, ClientConfiguration, SecretString};

const PROVIDER_ID: &str = "cpa-cloud";

#[derive(Debug, Clone)]
pub struct OpenClawProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
    pub default_model: String,
}

impl OpenClawProfile {
    pub fn for_openclaw(
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
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 OpenClaw".into(),
            ));
        };
        if protocol.candidates.is_empty() {
            return Err(AppError::Config(
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 OpenClaw".into(),
            ));
        }
        if protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 OpenClaw 提供有效的默认模型".into(),
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
pub struct OpenClawPlan {
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

pub fn plan_new_openclaw(
    config_path: &Path,
    ownership_path: &Path,
    profile: &OpenClawProfile,
) -> Result<OpenClawPlan, AppError> {
    validate_profile(profile)?;
    let previous_config = read_optional(config_path)?;
    let previous_ownership = read_optional(ownership_path)?;
    if previous_config.is_some() || previous_ownership.is_some() {
        return Err(AppError::Conflict(
            "OpenClaw 已存在 openclaw.json 或 CPA 所有权文件；create-only 安全阶段不会替换它们"
                .into(),
        ));
    }

    let provider = provider_value(profile)?;
    let mut providers = serde_json::Map::new();
    providers.insert(PROVIDER_ID.to_string(), provider);
    let selected_model = format!("{PROVIDER_ID}/{}", profile.default_model);
    let document = json!({
        "models": {
            "mode": "merge",
            "providers": providers
        },
        "agents": {
            "defaults": {
                "model": {
                    "primary": selected_model.clone()
                }
            }
        }
    });
    let mut config_bytes = serde_json::to_vec_pretty(&document)
        .map_err(|error| AppError::Config(format!("无法生成 OpenClaw openclaw.json：{error}")))?;
    config_bytes.push(b'\n');
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "openclaw".into(),
        provider_id: PROVIDER_ID.into(),
        config_digest: value_digest(&document)?,
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 OpenClaw 所有权记录：{error}")))?;

    Ok(OpenClawPlan {
        config: file_plan(config_path, None, config_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model,
    })
}

/// Create a complete OpenClaw configuration only when both managed files are absent.
pub fn apply_new_openclaw(
    root: &Path,
    config_relative: &Path,
    ownership_relative: &Path,
    profile: &OpenClawProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(config_relative, "OpenClaw")?;
    validate_transaction_relative(ownership_relative, "OpenClaw")?;
    validate_openclaw_paths(config_relative, ownership_relative)?;
    let plan = plan_new_openclaw(
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
    .map_err(|error| transaction_error("OpenClaw", error))
}

fn validate_openclaw_paths(
    config_relative: &Path,
    ownership_relative: &Path,
) -> Result<(), AppError> {
    let config_name = config_relative.file_name().and_then(|value| value.to_str());
    let ownership_name = ownership_relative
        .file_name()
        .and_then(|value| value.to_str());
    if config_name != Some("openclaw.json")
        || ownership_name != Some("cpa-cloud.ownership.json")
        || config_relative.parent() != ownership_relative.parent()
        || config_relative == ownership_relative
    {
        return Err(AppError::Config(
            "OpenClaw 安全写入只接受同一配置目录内的 openclaw.json 和 cpa-cloud.ownership.json"
                .into(),
        ));
    }
    Ok(())
}

fn provider_value(profile: &OpenClawProfile) -> Result<Value, AppError> {
    let base_url = normalize_cpa_base_url(&profile.base_url)?;
    let models = profile
        .models
        .iter()
        .map(|model| json!({ "id": model, "name": model }))
        .collect::<Vec<_>>();
    Ok(json!({
        "baseUrl": base_url,
        "apiKey": profile.employee_key.expose_secret().trim(),
        "api": "openai-responses",
        "models": models
    }))
}

fn validate_profile(profile: &OpenClawProfile) -> Result<(), AppError> {
    if profile.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config(
            "当前 Key 没有可用于 OpenClaw 的模型".into(),
        ));
    }
    if profile.models.iter().any(|model| {
        model.is_empty() || model.len() > 200 || model.chars().any(|ch| ch.is_control())
    }) {
        return Err(AppError::Config("模型标识无效".into()));
    }
    if profile.default_model.is_empty() || !profile.models.contains(&profile.default_model) {
        return Err(AppError::Config("OpenClaw 默认模型无效".into()));
    }
    Ok(())
}

fn value_digest(value: &Value) -> Result<String, AppError> {
    let bytes = serde_json::to_vec(value)
        .map_err(|error| AppError::Config(format!("无法计算 OpenClaw 配置摘要：{error}")))?;
    Ok(hex::encode(Sha256::digest(bytes)))
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

    fn profile() -> OpenClawProfile {
        OpenClawProfile::for_openclaw(
            "https://cpa.example.test",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn openclaw_uses_responses_candidates_and_default() {
        let profile = profile();
        assert_eq!(profile.models, vec!["alpha", "beta"]);
        assert_eq!(profile.default_model, "beta");
    }

    #[test]
    fn plan_registers_provider_and_selects_the_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_openclaw(
            &temp.path().join("openclaw.json"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "cpa-cloud/beta");
        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        let value: Value = json5::from_str(&source).unwrap();
        let provider = &value["models"]["providers"][PROVIDER_ID];
        assert_eq!(value["models"]["mode"], "merge");
        assert_eq!(provider["api"], "openai-responses");
        assert_eq!(provider["baseUrl"], "https://cpa.example.test/v1");
        assert_eq!(provider["apiKey"], "employee-secret");
        assert_eq!(provider["models"][0]["id"], "alpha");
        assert_eq!(
            value["agents"]["defaults"]["model"]["primary"],
            "cpa-cloud/beta"
        );

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn create_only_transaction_publishes_config_and_ownership_together() {
        let temp = tempfile::tempdir().unwrap();
        std::fs::create_dir(temp.path().join(".openclaw")).unwrap();
        let receipt = apply_new_openclaw(
            temp.path(),
            Path::new(".openclaw/openclaw.json"),
            Path::new(".openclaw/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(receipt.created.len(), 2);
        assert!(temp.path().join(".openclaw/openclaw.json").exists());
        assert!(temp
            .path()
            .join(".openclaw/cpa-cloud.ownership.json")
            .exists());
    }

    #[test]
    fn create_only_transaction_refuses_existing_openclaw_config() {
        let temp = tempfile::tempdir().unwrap();
        let directory = temp.path().join(".openclaw");
        std::fs::create_dir(&directory).unwrap();
        let config = directory.join("openclaw.json");
        let original = "{ models: { mode: 'merge', providers: {} } }\n";
        std::fs::write(&config, original).unwrap();
        let error = apply_new_openclaw(
            temp.path(),
            Path::new(".openclaw/openclaw.json"),
            Path::new(".openclaw/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("existing OpenClaw config must remain blocked");
        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read_to_string(config).unwrap(), original);
    }

    #[test]
    fn profile_rejects_a_default_outside_the_candidate_list() {
        let mut configuration = configuration();
        configuration.protocols[1].default_model = "missing".into();
        let error = OpenClawProfile::for_openclaw(
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
        let error = apply_new_openclaw(
            temp.path(),
            Path::new("../openclaw.json"),
            Path::new("ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_openclaw_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (config, ownership) in [
            (
                ".openclaw/custom.json",
                ".openclaw/cpa-cloud.ownership.json",
            ),
            (
                ".openclaw/openclaw.json",
                "other/cpa-cloud.ownership.json",
            ),
            (".openclaw/openclaw.json", ".openclaw/ownership.json"),
            (".openclaw/openclaw.json", ".openclaw/openclaw.json"),
        ] {
            let error = apply_new_openclaw(
                temp.path(),
                Path::new(config),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical OpenClaw paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
