//! MiniMax Code BYOK provider projection for `config.yaml`.
//!
//! This module is independently authored from the fixed official MiniMax Code
//! configuration contract recorded in `PROVENANCE.md`. It creates a complete
//! configuration only below an isolated synthetic root. Existing MiniMax Code
//! configuration remains blocked until the replacement transaction and native
//! lock compatibility gates are complete.

use std::collections::HashSet;
use std::path::{Path, PathBuf};

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
pub struct MiniMaxCodeProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub models: Vec<String>,
    pub default_model: String,
}

impl MiniMaxCodeProfile {
    pub fn for_minimax_code(
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
                "当前员工 Key 没有 OpenAI Responses 协议模型，不能配置 MiniMax Code".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 MiniMax Code 提供有效的默认 Responses 模型".into(),
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
pub struct MiniMaxCodePlan {
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

pub fn plan_new_minimax_code(
    config_path: &Path,
    ownership_path: &Path,
    profile: &MiniMaxCodeProfile,
) -> Result<MiniMaxCodePlan, AppError> {
    validate_profile(profile)?;
    if read_optional(config_path)?.is_some() || read_optional(ownership_path)?.is_some() {
        return Err(AppError::Conflict(
            "MiniMax Code 已存在 config.yaml 或 CPA 所有权文件；create-only 安全阶段不会替换它们"
                .into(),
        ));
    }
    let native_lock = native_lock_path(config_path)?;
    match std::fs::symlink_metadata(&native_lock) {
        Ok(_) => {
            return Err(AppError::Conflict(
                "MiniMax Code config.yaml.lock 已存在；不会绕过客户端原生配置锁".into(),
            ));
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(_) => {
            return Err(AppError::Conflict(
                "无法安全检查 MiniMax Code config.yaml.lock；本次不会写入".into(),
            ));
        }
    }

    let base_url = normalize_cpa_base_url(&profile.base_url)?;
    let selected_model = format!("custom_provider:{PROVIDER_ID}/{}", profile.default_model);
    let config_bytes = render_config(profile, &base_url, &selected_model)?.into_bytes();
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "minimax-code".into(),
        provider_id: PROVIDER_ID.into(),
        config_digest: hex::encode(Sha256::digest(&config_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 MiniMax Code 所有权记录：{error}")))?;

    Ok(MiniMaxCodePlan {
        config: file_plan(config_path, None, config_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model,
    })
}

/// Create a complete MiniMax Code configuration only when both managed files
/// and the native `config.yaml.lock` are absent. MiniMax Code-owned login,
/// MCP, prompt, plugin, cache, and session state remain untouched.
pub fn apply_new_minimax_code(
    root: &Path,
    config_relative: &Path,
    ownership_relative: &Path,
    profile: &MiniMaxCodeProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(config_relative, "MiniMax Code")?;
    validate_transaction_relative(ownership_relative, "MiniMax Code")?;
    validate_minimax_code_paths(config_relative, ownership_relative)?;
    let plan = plan_new_minimax_code(
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
    .map_err(|error| transaction_error("MiniMax Code", error))
}

fn validate_minimax_code_paths(
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
            "MiniMax Code 安全写入只接受同一数据目录内的 config.yaml 和 cpa-cloud.ownership.json"
                .into(),
        ));
    }
    Ok(())
}

fn native_lock_path(config_path: &Path) -> Result<PathBuf, AppError> {
    let parent = config_path
        .parent()
        .ok_or_else(|| AppError::Config("MiniMax Code config.yaml 必须位于数据目录中".into()))?;
    Ok(parent.join("config.yaml.lock"))
}

fn render_config(
    profile: &MiniMaxCodeProfile,
    base_url: &str,
    selected_model: &str,
) -> Result<String, AppError> {
    let quote = |value: &str| {
        serde_json::to_string(value).map_err(|error| {
            AppError::Config(format!("无法编码 MiniMax Code YAML 字符串：{error}"))
        })
    };
    let mut source = String::new();
    source.push_str(&format!("defaultModel: {}\n", quote(selected_model)?));
    source.push_str("custom_provider:\n");
    source.push_str(&format!("  {PROVIDER_ID}:\n"));
    source.push_str("    name: \"CPA Cloud\"\n");
    source.push_str("    kind: \"custom\"\n");
    source.push_str("    enabled: true\n");
    source.push_str("    api: \"openai-responses\"\n");
    source.push_str("    options:\n");
    source.push_str(&format!(
        "      apiKey: {}\n",
        quote(profile.employee_key.expose_secret().trim())?
    ));
    source.push_str(&format!("      baseURL: {}\n", quote(base_url)?));
    source.push_str("      authMode: \"api-key\"\n");
    source.push_str("    models:\n");
    for model in &profile.models {
        source.push_str(&format!("      {}: {{}}\n", quote(model)?));
    }
    Ok(source)
}

fn validate_profile(profile: &MiniMaxCodeProfile) -> Result<(), AppError> {
    let key = profile.employee_key.expose_secret().trim();
    if key.is_empty() || key.chars().any(|ch| ch.is_control()) {
        return Err(AppError::Config("员工 Key 不能为空或包含控制字符".into()));
    }
    if profile.models.is_empty() {
        return Err(AppError::Config("MiniMax Code 模型列表不能为空".into()));
    }
    let mut unique = HashSet::new();
    if profile.models.iter().any(|model| {
        model.is_empty()
            || model.len() > 200
            || model.chars().any(|ch| ch.is_control())
            || !unique.insert(model)
    }) {
        return Err(AppError::Config(
            "MiniMax Code 模型标识无效、重复或包含控制字符".into(),
        ));
    }
    if !profile.models.contains(&profile.default_model) {
        return Err(AppError::Config(
            "MiniMax Code 默认模型不在员工 Key 可用列表中".into(),
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

    fn profile() -> MiniMaxCodeProfile {
        MiniMaxCodeProfile::for_minimax_code(
            "https://cpa.example.test/company/v1",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn plan_registers_responses_models_and_selects_the_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_minimax_code(
            &temp.path().join("config.yaml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(
            plan.selected_model,
            "custom_provider:cpa-cloud/vendor/model.beta"
        );

        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        assert!(source.contains("defaultModel: \"custom_provider:cpa-cloud/vendor/model.beta\""));
        assert!(source.contains("api: \"openai-responses\""));
        assert!(source.contains("apiKey: \"employee-secret\""));
        assert!(source.contains("baseURL: \"https://cpa.example.test/company/v1\""));
        assert!(source.contains("authMode: \"api-key\""));
        assert!(source.contains("\"alpha\": {}"));
        assert!(source.contains("\"vendor/model.beta\": {}"));
        assert!(!source.contains("chat-only"));

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn fixed_source_fixture_records_the_v056_provider_contract() {
        let fixture: serde_json::Value = serde_json::from_str(include_str!(
            "../tests/fixtures/minimax-code-0.5.6/provider-contract.json"
        ))
        .unwrap();
        assert_eq!(fixture["config_path"], "<data-dir>/config.yaml");
        assert_eq!(fixture["release_asset"]["github_asset_id"], 594_638_510);
        assert_eq!(
            fixture["release_asset"]["url"],
            "https://github.com/MiniMax-AI/minimax-code/releases/download/v0.5.6/minimax-code-0.5.6.tar.gz"
        );
        assert_eq!(fixture["release_asset"]["size_bytes"], 12_707_059);
        assert_eq!(
            fixture["release_asset"]["sha256"],
            "2e3df211e0d18379af7b09bec07a3da106bf1095ce84c4850504769512df2abb"
        );
        assert_eq!(
            fixture["release_asset"]["upstream_windows_acceptance"],
            false
        );
        assert_eq!(
            fixture["release_asset"]["upstream_live_service_acceptance"],
            false
        );
        assert_eq!(
            fixture["release_asset"]["node_engine"],
            ">=22.19 <23 || >=24.2 <27"
        );
        assert_eq!(
            fixture["release_asset"]["checksum_asset"]["github_asset_id"],
            594_638_509
        );
        assert_eq!(
            fixture["release_asset"]["checksum_asset"]["size_bytes"],
            92
        );
        assert_eq!(fixture["provider_tree"], "custom_provider.<name>");
        assert_eq!(fixture["api_format"], "openai-responses");
        assert_eq!(fixture["credential_header"], "Authorization: Bearer");
        assert_eq!(
            fixture["data_dir_resolution"]["primary_profile"],
            "~/.minimax-<profile>"
        );
        assert_eq!(
            fixture["data_dir_resolution"]["legacy_profile"],
            "~/.mavis-<profile>"
        );
        assert_eq!(fixture["native_lock"]["path"], "config.yaml.lock");
        assert_eq!(fixture["native_lock"]["stale_ms"], 10_000);
        assert_eq!(
            fixture["native_lock"]["must_be_held_through_publication"],
            true
        );
        assert_eq!(
            fixture["native_evidence_gate"]["requires_verified_asset"],
            true
        );
        assert_eq!(
            fixture["native_evidence_gate"]["requires_native_writer_read_negative_control"],
            true
        );
        assert_eq!(
            fixture["native_evidence_gate"]["requires_loopback_responses_observation"],
            true
        );
        assert_eq!(fixture["native_evidence_gate"]["satisfied"], false);
    }

    #[test]
    fn yaml_string_encoding_prevents_key_and_model_injection() {
        let temp = tempfile::tempdir().unwrap();
        let mut profile = profile();
        profile.employee_key = SecretString::new("quote\" and slash\\");
        profile.models = vec!["plain".into(), "colon: # still key".into()];
        profile.default_model = "colon: # still key".into();
        let plan = plan_new_minimax_code(
            &temp.path().join("config.yaml"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile,
        )
        .unwrap();
        let source = String::from_utf8(plan.config.desired_bytes.to_vec()).unwrap();
        assert!(source.contains("apiKey: \"quote\\\" and slash\\\\\""));
        assert!(source.contains("\"colon: # still key\": {}"));
        assert_eq!(source.matches("custom_provider:").count(), 2);
    }

    #[test]
    fn create_only_transaction_leaves_minimax_state_untouched() {
        let temp = tempfile::tempdir().unwrap();
        let minimax = temp.path().join(".minimax");
        std::fs::create_dir(&minimax).unwrap();
        let sentinels = [
            ("local-runtime.auth.json", b"auth-sentinel".as_slice()),
            ("mcp.json", b"mcp-sentinel".as_slice()),
            ("AGENTS.md", b"prompt-sentinel".as_slice()),
            (
                "v2/sqlite/runtime-state.sqlite",
                b"session-sentinel".as_slice(),
            ),
        ];
        let mut before = Vec::new();
        for (name, bytes) in sentinels {
            let path = minimax.join(name);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(&path, bytes).unwrap();
            before.push((
                path.clone(),
                bytes.to_vec(),
                crate::live::engine::test_file_identity(&path).unwrap(),
                std::fs::metadata(&path).unwrap().modified().unwrap(),
            ));
        }

        let receipt = apply_new_minimax_code(
            temp.path(),
            Path::new(".minimax/config.yaml"),
            Path::new(".minimax/cpa-cloud.ownership.json"),
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
    fn create_only_transaction_refuses_native_config_lock() {
        for entry_kind in ["file", "directory"] {
            let temp = tempfile::tempdir().unwrap();
            let minimax = temp.path().join(".minimax");
            std::fs::create_dir(&minimax).unwrap();
            let native_lock = minimax.join("config.yaml.lock");
            if entry_kind == "file" {
                std::fs::write(&native_lock, b"unexpected-lock-entry").unwrap();
            } else {
                std::fs::create_dir(&native_lock).unwrap();
            }
            let error = apply_new_minimax_code(
                temp.path(),
                Path::new(".minimax/config.yaml"),
                Path::new(".minimax/cpa-cloud.ownership.json"),
                &profile(),
            )
            .expect_err("every native MiniMax Code lock entry must remain authoritative");
            assert!(matches!(error, AppError::Conflict(_)));
            assert!(!minimax.join("config.yaml").exists());
        }
    }

    #[test]
    fn create_only_transaction_refuses_each_managed_file_collision() {
        for existing in ["config.yaml", "cpa-cloud.ownership.json"] {
            let temp = tempfile::tempdir().unwrap();
            let minimax = temp.path().join(".minimax");
            std::fs::create_dir(&minimax).unwrap();
            let existing_path = minimax.join(existing);
            std::fs::write(&existing_path, b"owned-by-someone-else").unwrap();

            let error = apply_new_minimax_code(
                temp.path(),
                Path::new(".minimax/config.yaml"),
                Path::new(".minimax/cpa-cloud.ownership.json"),
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
        let error = MiniMaxCodeProfile::for_minimax_code(
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
        let error = apply_new_minimax_code(
            temp.path(),
            Path::new("../config.yaml"),
            Path::new(".minimax/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (config, ownership) in [
            (".minimax/custom.yaml", ".minimax/cpa-cloud.ownership.json"),
            (".minimax/config.yaml", "other/cpa-cloud.ownership.json"),
            (".minimax/config.yaml", ".minimax/ownership.json"),
            (".minimax/config.yaml", ".minimax/config.yaml"),
        ] {
            let error = apply_new_minimax_code(
                temp.path(),
                Path::new(config),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical MiniMax Code paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
