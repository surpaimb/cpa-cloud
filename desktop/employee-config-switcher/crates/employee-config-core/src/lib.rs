//! Pure configuration planning core for the CPA Cloud employee client.
//!
//! This crate deliberately has no Tauri dependency. It can discover server
//! capabilities, detect client locations, and produce compare-before-commit
//! plans. The development preview exposes a reviewed create-only transaction
//! for isolated synthetic roots; the Tauri apply command and real client-file
//! replacement remain fail-closed.

pub mod detection;
pub mod discovery;
pub mod error;
pub mod jsonc_document;
pub mod live;
pub mod opencode;
pub mod pi;
pub mod plan;
pub mod secret;

pub use detection::{detect_clients, ClientDetection};
pub use discovery::{discover_configuration, ClientConfiguration, DiscoveryRequest};
pub use error::AppError;
pub use opencode::{apply_new_opencode, plan_opencode, ConfigurationProfile, OpenCodePlan};
pub use pi::{apply_new_pi, plan_new_pi, PiPlan, PiProfile};
pub use plan::FilePlan;
pub use secret::SecretString;
