//! Claude Code gateway projection for the user-level `~/.claude/settings.json`.
//!
//! This module is independently authored from the public Claude Code gateway
//! and settings contracts recorded in `PROVENANCE.md`. It creates a complete
//! settings file only below an isolated synthetic root. Existing Claude Code
//! settings remain blocked until the replacement transaction gates pass.

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
pub struct ClaudeCodeProfile {
    pub base_url: String,
    pub employee_key: SecretString,
    pub default_model: String,
}

impl ClaudeCodeProfile {
    pub fn for_claude_code(
        base_url: impl Into<String>,
        employee_key: SecretString,
        configuration: &ClientConfiguration,
    ) -> Result<Self, AppError> {
        let Some(protocol) = configuration
            .protocols
            .iter()
            .find(|item| item.protocol == "anthropic-messages")
        else {
            return Err(AppError::Config(
                "当前员工 Key 没有 Anthropic Messages 协议模型，不能配置 Claude Code".into(),
            ));
        };
        if protocol.candidates.is_empty()
            || protocol.default_model.is_empty()
            || !protocol.candidates.contains(&protocol.default_model)
        {
            return Err(AppError::Config(
                "服务端没有为 Claude Code 提供有效的默认 Anthropic 模型".into(),
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
pub struct ClaudeCodePlan {
    pub settings: FilePlan,
    pub ownership: FilePlan,
    pub selected_model: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct OwnershipRecord {
    schema_version: u32,
    tool: String,
    config_digest: String,
}

pub fn plan_new_claude_code(
    settings_path: &Path,
    ownership_path: &Path,
    profile: &ClaudeCodeProfile,
) -> Result<ClaudeCodePlan, AppError> {
    validate_profile(profile)?;
    let previous_settings = read_optional(settings_path)?;
    let previous_ownership = read_optional(ownership_path)?;
    if previous_settings.is_some() || previous_ownership.is_some() {
        return Err(AppError::Conflict(
            "Claude Code 已存在 settings.json 或 CPA 所有权文件；create-only 安全阶段不会替换它们"
                .into(),
        ));
    }

    let document = json!({
        "$schema": "https://json.schemastore.org/claude-code-settings.json",
        "model": profile.default_model,
        "env": {
            "ANTHROPIC_BASE_URL": normalize_cpa_gateway_url(&profile.base_url)?,
            "ANTHROPIC_AUTH_TOKEN": profile.employee_key.expose_secret().trim()
        }
    });
    let mut settings_bytes = serde_json::to_vec_pretty(&document)
        .map_err(|error| AppError::Config(format!("无法生成 Claude Code settings.json：{error}")))?;
    settings_bytes.push(b'\n');
    let ownership = OwnershipRecord {
        schema_version: 1,
        tool: "claude-code".into(),
        config_digest: hex::encode(Sha256::digest(&settings_bytes)),
    };
    let ownership_bytes = serde_json::to_vec_pretty(&ownership)
        .map_err(|error| AppError::Config(format!("无法生成 Claude Code 所有权记录：{error}")))?;

    Ok(ClaudeCodePlan {
        settings: file_plan(settings_path, None, settings_bytes),
        ownership: file_plan(ownership_path, None, ownership_bytes),
        selected_model: profile.default_model.clone(),
    })
}

/// Create a complete Claude Code user settings file only when both managed
/// files are absent. Claude Code's separate `~/.claude.json` login and project
/// state file remains untouched.
pub fn apply_new_claude_code(
    root: &Path,
    settings_relative: &Path,
    ownership_relative: &Path,
    profile: &ClaudeCodeProfile,
) -> Result<CreateOnlyReceipt, AppError> {
    validate_transaction_relative(settings_relative, "Claude Code")?;
    validate_transaction_relative(ownership_relative, "Claude Code")?;
    validate_claude_code_paths(settings_relative, ownership_relative)?;
    let plan = plan_new_claude_code(
        &root.join(settings_relative),
        &root.join(ownership_relative),
        profile,
    )?;
    apply_create_only(
        root,
        vec![
            CreateFilePlan::new(settings_relative, plan.settings.desired_bytes.to_vec()),
            CreateFilePlan::new(ownership_relative, plan.ownership.desired_bytes.to_vec()),
        ],
    )
    .map_err(|error| transaction_error("Claude Code", error))
}

fn validate_claude_code_paths(
    settings_relative: &Path,
    ownership_relative: &Path,
) -> Result<(), AppError> {
    let settings_name = settings_relative.file_name().and_then(|value| value.to_str());
    let ownership_name = ownership_relative
        .file_name()
        .and_then(|value| value.to_str());
    if settings_name != Some("settings.json")
        || ownership_name != Some("cpa-cloud.ownership.json")
        || settings_relative.parent() != ownership_relative.parent()
        || settings_relative == ownership_relative
    {
        return Err(AppError::Config(
            "Claude Code 安全写入只接受同一配置目录内的 settings.json 和 cpa-cloud.ownership.json"
                .into(),
        ));
    }
    Ok(())
}

fn validate_profile(profile: &ClaudeCodeProfile) -> Result<(), AppError> {
    if profile.employee_key.expose_secret().trim().is_empty() {
        return Err(AppError::Config("员工 Key 不能为空".into()));
    }
    if profile.default_model.is_empty()
        || profile.default_model.len() > 200
        || profile.default_model.chars().any(|ch| ch.is_control())
    {
        return Err(AppError::Config("Claude Code 默认模型无效".into()));
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
                    protocol: "openai-responses".into(),
                    candidates: vec!["responses-only".into()],
                    default_model: "responses-only".into(),
                    default_source: "public_id_ascending".into(),
                },
                crate::discovery::ProtocolConfiguration {
                    protocol: "anthropic-messages".into(),
                    candidates: vec!["claude-alpha".into(), "claude-beta".into()],
                    default_model: "claude-beta".into(),
                    default_source: "public_id_ascending".into(),
                },
            ],
        }
    }

    fn profile() -> ClaudeCodeProfile {
        ClaudeCodeProfile::for_claude_code(
            "https://cpa.example.test/company/v1",
            SecretString::new("employee-secret"),
            &configuration(),
        )
        .unwrap()
    }

    #[test]
    fn plan_uses_gateway_base_bearer_token_and_server_default() {
        let temp = tempfile::tempdir().unwrap();
        let plan = plan_new_claude_code(
            &temp.path().join("settings.json"),
            &temp.path().join("cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();
        assert_eq!(plan.selected_model, "claude-beta");
        let document: serde_json::Value =
            serde_json::from_slice(&plan.settings.desired_bytes).unwrap();
        assert_eq!(document["model"], "claude-beta");
        assert_eq!(
            document["env"]["ANTHROPIC_BASE_URL"],
            "https://cpa.example.test/company"
        );
        assert_eq!(
            document["env"]["ANTHROPIC_AUTH_TOKEN"],
            "employee-secret"
        );

        let ownership = String::from_utf8(plan.ownership.desired_bytes.to_vec()).unwrap();
        assert!(!ownership.contains("employee-secret"));
        assert!(!format!("{plan:?}").contains("employee-secret"));
    }

    #[test]
    fn create_only_transaction_leaves_claude_login_state_untouched() {
        let temp = tempfile::tempdir().unwrap();
        std::fs::create_dir(temp.path().join(".claude")).unwrap();
        let login = temp.path().join(".claude.json");
        std::fs::write(&login, b"login-sentinel").unwrap();
        let login_identity = crate::live::engine::test_file_identity(&login).unwrap();
        let login_modified = std::fs::metadata(&login).unwrap().modified().unwrap();

        let receipt = apply_new_claude_code(
            temp.path(),
            Path::new(".claude/settings.json"),
            Path::new(".claude/cpa-cloud.ownership.json"),
            &profile(),
        )
        .unwrap();

        assert_eq!(receipt.created.len(), 2);
        assert!(temp.path().join(".claude/settings.json").exists());
        assert!(temp
            .path()
            .join(".claude/cpa-cloud.ownership.json")
            .exists());
        assert_eq!(std::fs::read(&login).unwrap(), b"login-sentinel");
        assert_eq!(
            crate::live::engine::test_file_identity(&login).unwrap(),
            login_identity
        );
        assert_eq!(
            std::fs::metadata(&login).unwrap().modified().unwrap(),
            login_modified
        );
    }

    #[test]
    fn create_only_transaction_refuses_existing_settings() {
        let temp = tempfile::tempdir().unwrap();
        let claude = temp.path().join(".claude");
        std::fs::create_dir(&claude).unwrap();
        let settings = claude.join("settings.json");
        let original = b"{\"model\":\"theirs\"}\n";
        std::fs::write(&settings, original).unwrap();
        let error = apply_new_claude_code(
            temp.path(),
            Path::new(".claude/settings.json"),
            Path::new(".claude/cpa-cloud.ownership.json"),
            &profile(),
        )
        .expect_err("existing Claude Code settings must remain blocked");
        assert!(matches!(error, AppError::Conflict(_)));
        assert_eq!(std::fs::read(settings).unwrap(), original);
    }

    #[test]
    fn profile_rejects_a_default_outside_the_candidate_list() {
        let mut configuration = configuration();
        configuration.protocols[1].default_model = "missing".into();
        let error = ClaudeCodeProfile::for_claude_code(
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
        let error = apply_new_claude_code(
            temp.path(),
            Path::new("../settings.json"),
            Path::new("ownership.json"),
            &profile(),
        )
        .expect_err("parent traversal must fail before planning");
        assert!(matches!(error, AppError::Config(_)));
    }

    #[test]
    fn create_only_transaction_rejects_noncanonical_paths() {
        let temp = tempfile::tempdir().unwrap();
        for (settings, ownership) in [
            (".claude/custom.json", ".claude/cpa-cloud.ownership.json"),
            (".claude/settings.json", "other/cpa-cloud.ownership.json"),
            (".claude/settings.json", ".claude/ownership.json"),
            (".claude/settings.json", ".claude/settings.json"),
        ] {
            let error = apply_new_claude_code(
                temp.path(),
                Path::new(settings),
                Path::new(ownership),
                &profile(),
            )
            .expect_err("noncanonical Claude Code paths must fail before planning");
            assert!(matches!(error, AppError::Config(_)));
        }
    }
}
