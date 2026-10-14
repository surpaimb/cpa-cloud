//! Pure configuration planning core for the CPA Cloud employee client.
//!
//! This crate deliberately has no Tauri dependency and performs no writes in
//! the development-preview build. It can discover server capabilities, detect
//! client locations, and produce compare-before-commit plans for review and
//! tests. Publishing those plans is gated until the durable journal and native
//! ACL implementation have completed security review.

pub mod detection;
pub mod discovery;
pub mod error;
pub mod jsonc_document;
pub mod live;
pub mod opencode;
pub mod secret;

pub use detection::{detect_clients, ClientDetection};
pub use discovery::{discover_configuration, ClientConfiguration, DiscoveryRequest};
pub use error::AppError;
pub use opencode::{
    apply_new_opencode, plan_opencode, ConfigurationProfile, FilePlan, OpenCodePlan,
};
pub use secret::SecretString;
