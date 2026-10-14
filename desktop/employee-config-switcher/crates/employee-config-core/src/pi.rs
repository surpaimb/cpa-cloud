//! Pi provider projection for the native `models.json` file.
//!
//! Portions adapted from CC Switch, Copyright 2025 Jason Young, under the
//! MIT License. See `PROVENANCE.md` and `third_party/cc-switch/LICENSE`.
//!
//! The provider-node shape and the boundary that leaves Pi-owned `auth.json`
//! and `settings.json` untouched are adapted from CC Switch at fixed MIT
//! revision `c21106ff20e351d40f412cb51d1f24da177b113d`,
//! `src-tauri/src/pi_config/mod.rs`. CPA endpoint/protocol mapping, ownership,
//! and transaction handling are independently authored here.

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
pub struct PiProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
}

impl PiProfile {
    pub fn for_pi(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let models = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "openai-responses")
            .map(|item| item.candidates.clone())
            .unwrap_or_default();
        if models.is_empty() {
            return Err(AppError::Config(
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 Pi".into(),
            ));
        }
        Ok(Self {
            base_url: base_url.into(),
            employee_key,
            models,
        })
    }
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct PiPlan {
    pub models: FilePlan,
    pub ownership: FilePlan,
    /// Pi owns model selection in `settings.json`; this adapter never changes it.
    pub selects_default_model: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    provider_id: String,
    provider_digest: String,
}

pub fn plan_new_pi(
    models_path: &Path,
    ownership_path: &Path,
    profile: &PiProfile,
) -> Result<PiPlan, AppError> {
    validate_profile(profile)?;
    let previous_models = read_optional(models_path)?;
    let previous_ownership = read_optional(ownership_path)?;
    if previous_models.is_some() || previous_ownership.is_some() {
        return Err(AppError::Conflict(
            "Pi 已存在 models.json 或 CPA 所有权文件；create-only 安全阶段不会替换它们".into(),
        ));
    }

    let provider = provider_value(profile)?;
    let mut providers = serde_json::Map::new();
    providers.insert(PROVIDER_ID.to_string(), provider);
    let document = json!({ "providers": providers });
    let mut models_bytes = serde_json::to_vec_pretty(&document)
        .map_err(|error| AppError::Config(format!("无法生成 Pi models.json：{error}")))?;
    models_bytes.push(b'\n');
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "pi".into(),
        provider_id: PROVIDER_ID.into(),
        provider_digest: value_digest(&document["providers"][PROVIDER_ID])?,
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Pi 所有权记录：{error}")))?;

    Ok(PiPlan {
        models: file_plan(models_path, None, models_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selects_default_model: false,
    })
}

/// Register a Pi provider only when both managed files are absent.
///
/// Pi's `auth.json` and `settings.json` remain untouched. In particular this
/// does not select a default provider or model.
pub fn apply_new_pi(
    root: &Path,
    models_relative: &Path,
    ownership_relative: &Path,
    profile: &PiProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(models_relative, "Pi")?;
    validate_transaction_relative(ownership_relative, "Pi")?;
    let plan = plan_new_pi(
        &root.join(models_relative),
        &root.join(ownership_relative),
        profile,
    )?;
    apply_create_only(
        root,
        vec![
            CreateFilePlan::new(models_relative, plan.models.desired_bytes.to_vec()),
            CreateFilePlan::new(ownership_relative, plan.ownership.desired_bytes.to_vec()),
        ],
    )
    .map_err(|error| transaction_error("Pi", error))
}

fn provider_value(profile: &PiProfile) -> Result<Value, AppError> {
    let base_url = normalize_cpa_base_url(&profile.base_url)?;
    let models = profile
        .models
        .iter()
        .map(|model| json!({ "id": model, "name": model }))
        .collect::<Vec<_>>();
    Ok(json!({
        "name": "CPA Cloud",
        "baseUrl": base_url,
        "api": "openai-responses",
        "apiKey": profile.employee_key.expose_secret().trim(),
        "models": models
    }))
}

fn validate_profile(profile: &PiProfile) -> Result<(), AppError> {
    if profile.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config("当前 Key 没有可用于 Pi 的模型".into()));
    }
    if profile.models.iter().any(|model| {
        model.is_empty() || model.len() > 200 || model.chars().any(|ch| ch.is_control())
    }) {
        return Err(AppError::Config("模型标识无效".into()));
    }
    Ok(())
}

fn value_digest(value: &Value) -> Result<String, AppError> {
    let bytes = serde_json::to_vec(value)
        .map_err(|error| AppError::Config(format!("无法计算 Pi 配置摘要：{error}")))?;
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
                    default_model: "alpha".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> PiProfile {
        PiProfile::for_pi(
            "https://cpa.example.test",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn pi_uses_only_openai_responses_candidates() {
        let profile = profile();
        assert_eq!(profile.models, vec!["alpha", "beta"]);
    }

    #[test]
    fn plan_registers_provider_without_selecting_a_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_pi(
            &temp.path().join("models.json"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert!(!plan.selects_default_model);
        let value: Value = serde_json::from_slice(&plan.models.desired_bytes).unwrap();
        let provider = &value["providers"][PROVIDER_ID];
        assert_eq!(provider["api"], "openai-responses");
        assert_eq!(provider["baseUrl"], "https://cpa.example.test/v1");
        assert_eq!(provider["apiKey"], "employee-secret");
        assert_eq!(provider["models"][0]["id"], "alpha");
        assert!(value.get("defaultProvider").is_none());
        assert!(value.get("defaultModel").is_none());

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn create_only_transaction_publishes_pi_models_and_ownership_together() {
        let temp = tempfile::tempdir().unwrap();
        std::fs::create_dir(temp.path().join("agent")).unwrap();
        let receipt = apply_new_pi(
            temp.path(),
            Path::new("agent/models.json"),
            Path::new("agent/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(receipt.created.len(), 2);
        assert!(temp.path().join("agent/models.json").exists());
        assert!(temp.path().join("agent/cpa-cloud.ownership.json").exists());
        assert!(!temp.path().join("agent/settings.json").exists());
        assert!(!temp.path().join("agent/auth.json").exists());
    }

    #[test]
    fn create_only_transaction_refuses_existing_pi_models() {
        let temp = tempfile::tempdir().unwrap();
        let agent = temp.path().join("agent");
        std::fs::create_dir(&agent).unwrap();
        let models = agent.join("models.json");
        let original = r#"{"providers":{"theirs":{"models":[]}}}"#;
        std::fs::write(&models, original).unwrap();
        let error = apply_new_pi(
            temp.path(),
            Path::new("agent/models.json"),
            Path::new("agent/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("existing Pi models must remain blocked");
        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read_to_string(models).unwrap(), original);
    }

    #[test]
    fn create_only_transaction_rejects_parent_traversal() {
        let temp = tempfile::tempdir().unwrap();
        let error = apply_new_pi(
            temp.path(),
            Path::new("../models.json"),
            Path::new("ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }
}
