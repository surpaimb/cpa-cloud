//! Pure configuration planning core for the CPA Cloud employee client.
//!
//! This crate deliberately has no Tauri dependency. It can discover server
//! capabilities, detect client locations, and produce compare-before-commit
//! plans. The development preview exposes a reviewed create-only transaction
//! for isolated synthetic roots; the Tauri apply command and real client-file
//! replacement remain fail-closed.

pub mod claude_code;
pub mod codex;
pub mod detection;
pub mod discovery;
pub mod error;
pub mod gemini;
pub mod grok_build;
pub mod jsonc_document;
pub mod live;
pub mod openclaw;
pub mod opencode;
pub mod pi;
pub mod plan;
pub mod secret;

pub use claude_code::{
    apply_new_claude_code, plan_new_claude_code, ClaudeCodePlan, ClaudeCodeProfile,
};
pub use codex::{apply_new_codex, plan_new_codex, CodexPlan, CodexProfile};
pub use detection::{detect_clients, ClientDetection};
pub use discovery::{discover_configuration, ClientConfiguration, DiscoveryRequest};
pub use error::AppError;
pub use gemini::{apply_new_gemini, plan_new_gemini, GeminiPlan, GeminiProfile};
pub use grok_build::{apply_new_grok_build, plan_new_grok_build, GrokBuildPlan, GrokBuildProfile};
pub use openclaw::{apply_new_openclaw, plan_new_openclaw, OpenClawPlan, OpenClawProfile};
pub use opencode::{apply_new_opencode, plan_opencode, ConfigurationProfile, OpenCodePlan};
pub use pi::{apply_new_pi, plan_new_pi, PiPlan, PiProfile};
pub use plan::FilePlan;
pub use secret::SecretString;
