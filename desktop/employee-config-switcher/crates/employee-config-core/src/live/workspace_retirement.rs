//! Crash-resumable retirement of one synthetic Linux replacement workspace.
//!
//! The only production authorization is derived from the exact terminal
//! handoff.  It names a fixed set of replacement-journal and encrypted-backup
//! objects captured before cleanup begins.  Each object is moved by descriptor
//! relative, no-follow rename into an owner-private quarantine, both directory
//! namespaces are flushed, and an immutable deletion authorization is made
//! durable before the quarantined object is unlinked.  Unknown entries and
//! identity or content changes are preserved and fail closed.

use super::{
    recovery_key::{RecoveryKeyReference, ReplacementTransactionId},
    replacement::ReplacementPhase,
};
use serde::{Deserialize, Serialize};
use std::{
    io,
    path::{Path, PathBuf},
};

pub(crate) const REPLACEMENT_DIRECTORY: &str = ".cpa-cloud-employee-config-replacements";
pub(crate) const BACKUP_DIRECTORY: &str = ".cpa-cloud-employee-config-backups";
const RETIREMENT_DIRECTORY: &str = ".cpa-cloud-employee-config-workspace-retirements";
const TARGET_LOCK_FILE: &str = "single-target.lock";
const PLAN_FILE: &str = "plan.json";
const COMPLETE_FILE: &str = "complete.json";
const JOURNAL_FILE: &str = "journal.jsonl";
const PENDING_SLOTS_READY_FILE: &str = ".journal-pending-ready";
const PENDING_SLOT_PREFIX: &str = ".journal-pending-";
const BACKUP_FILE: &str = "rollback.aes256gcm";
const MANIFEST_FILE: &str = "rollback.json";
const FORMAT_VERSION: u32 = 1;
const BACKUP_FORMAT_VERSION: u32 = 3;
const MAX_PLAN_BYTES: usize = 32 * 1024;
const MAX_MARKER_BYTES: usize = 4 * 1024;
const MAX_JOURNAL_BYTES: usize = 128 * 1024;
const MAX_PENDING_BYTES: usize = 16 * 1024;
const MAX_MANIFEST_BYTES: usize = 16 * 1024;
const MAX_BACKUP_BYTES: usize = 4 * 1024 * 1024 + 64;
const PENDING_SLOT_COUNT: usize = 24;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct RetirementIdentity {
    device: u64,
    inode: u64,
}

impl RetirementIdentity {
    pub(crate) fn new(device: u64, inode: u64) -> Self {
        Self { device, inode }
    }

    pub(crate) fn device(self) -> u64 {
        self.device
    }

    pub(crate) fn inode(self) -> u64 {
        self.inode
    }
}

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct RetirementArtifact {
    name: String,
    identity: RetirementIdentity,
    len: u64,
    mode: u32,
    uid: u32,
    gid: u32,
    digest: String,
}

impl RetirementArtifact {
    pub(crate) fn name(&self) -> &str {
        &self.name
    }

    fn valid(&self) -> bool {
        valid_artifact_name(&self.name)
            && self.identity.device != 0
            && self.identity.inode != 0
            && self.mode == 0o600
            && valid_digest(&self.digest)
    }
}

impl std::fmt::Debug for RetirementArtifact {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("RetirementArtifact")
            .field("name", &self.name)
            .field("identity", &self.identity)
            .field("len", &self.len)
            .field("mode", &format_args!("{:o}", self.mode))
            .field("uid", &self.uid)
            .field("gid", &self.gid)
            .field("digest", &"[REDACTED]")
            .finish()
    }
}

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct WorkspaceRetirementPlan {
    version: u32,
    transaction_id: String,
    root_identity: RetirementIdentity,
    replacement_parent_identity: RetirementIdentity,
    workspace_name: String,
    workspace_identity: RetirementIdentity,
    workspace_artifacts: Vec<RetirementArtifact>,
    backup_parent_identity: RetirementIdentity,
    backup_workspace_name: String,
    backup_workspace_identity: RetirementIdentity,
    backup_artifacts: Vec<RetirementArtifact>,
}

impl WorkspaceRetirementPlan {
    pub(crate) fn transaction_id(&self) -> &str {
        &self.transaction_id
    }

    pub(crate) fn root_identity(&self) -> RetirementIdentity {
        self.root_identity
    }

    pub(crate) fn workspace_name(&self) -> &str {
        &self.workspace_name
    }

    pub(crate) fn workspace_identity(&self) -> RetirementIdentity {
        self.workspace_identity
    }

    pub(crate) fn backup_workspace_name(&self) -> &str {
        &self.backup_workspace_name
    }

    pub(crate) fn validate_durable(
        self,
        transaction_id: ReplacementTransactionId,
        root_device: u64,
        root_inode: u64,
        workspace_name: &str,
        workspace_device: u64,
        workspace_inode: u64,
    ) -> Result<Self, WorkspaceRetirementError> {
        let expected_transaction = transaction_hex(transaction_id);
        if self.version != FORMAT_VERSION
            || self.transaction_id != expected_transaction
            || self.root_identity != RetirementIdentity::new(root_device, root_inode)
            || self.workspace_name != workspace_name
            || self.workspace_identity != RetirementIdentity::new(workspace_device, workspace_inode)
            || !valid_workspace_name(&self.workspace_name, &expected_transaction)
            || !valid_backup_workspace_name(&self.backup_workspace_name)
            || self.replacement_parent_identity.device == 0
            || self.replacement_parent_identity.inode == 0
            || self.backup_parent_identity.device == 0
            || self.backup_parent_identity.inode == 0
            || self.backup_workspace_identity.device == 0
            || self.backup_workspace_identity.inode == 0
            || !valid_workspace_artifacts(&self.workspace_artifacts)
            || !valid_backup_artifacts(&self.backup_artifacts)
        {
            return Err(WorkspaceRetirementError::Corrupt);
        }
        Ok(self)
    }
}

impl std::fmt::Debug for WorkspaceRetirementPlan {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("WorkspaceRetirementPlan")
            .field("version", &self.version)
            .field("transaction_id", &self.transaction_id)
            .field("root_identity", &self.root_identity)
            .field(
                "replacement_parent_identity",
                &self.replacement_parent_identity,
            )
            .field("workspace_name", &self.workspace_name)
            .field("workspace_identity", &self.workspace_identity)
            .field("workspace_artifacts", &self.workspace_artifacts)
            .field("backup_parent_identity", &self.backup_parent_identity)
            .field("backup_workspace_name", &self.backup_workspace_name)
            .field("backup_workspace_identity", &self.backup_workspace_identity)
            .field("backup_artifacts", &self.backup_artifacts)
            .finish()
    }
}

pub(crate) struct WorkspaceRetirementAuthorization {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    plan: WorkspaceRetirementPlan,
}

impl WorkspaceRetirementAuthorization {
    pub(crate) fn new(
        terminal: ReplacementPhase,
        transaction_id: ReplacementTransactionId,
        reference: RecoveryKeyReference,
        plan: WorkspaceRetirementPlan,
    ) -> Self {
        Self {
            terminal,
            transaction_id,
            reference,
            plan,
        }
    }

    pub(crate) fn plan(&self) -> &WorkspaceRetirementPlan {
        &self.plan
    }
}

impl std::fmt::Debug for WorkspaceRetirementAuthorization {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("WorkspaceRetirementAuthorization")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .field("plan", &self.plan)
            .finish()
    }
}

pub(crate) struct WorkspaceContentsRetiredReceipt {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    workspace_name: String,
    workspace_device: u64,
    workspace_inode: u64,
}

impl WorkspaceContentsRetiredReceipt {
    pub(crate) fn workspace_name(&self) -> &str {
        &self.workspace_name
    }
}

impl std::fmt::Debug for WorkspaceContentsRetiredReceipt {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("WorkspaceContentsRetiredReceipt")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .field("workspace_name", &self.workspace_name)
            .field("workspace_device", &self.workspace_device)
            .field("workspace_inode", &self.workspace_inode)
            .finish()
    }
}

#[derive(Debug, thiserror::Error)]
pub enum WorkspaceRetirementError {
    #[error("workspace content retirement is not supported on this platform")]
    UnsupportedPlatform,
    #[error("workspace retirement root is invalid: {0}")]
    InvalidRoot(PathBuf),
    #[error("workspace retirement plan is corrupt")]
    Corrupt,
    #[error("workspace retirement namespace or object changed: {0}")]
    RecoveryConflict(PathBuf),
    #[error("workspace retirement has an incomplete durable record: {0}")]
    IncompleteWrite(PathBuf),
    #[error("workspace retirement filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[doc(hidden)]
    #[error("injected workspace-retirement interruption")]
    InjectedCrash,
}

pub(crate) fn capture_terminal_plan(
    root: &Path,
    transaction_id: ReplacementTransactionId,
    reference: &RecoveryKeyReference,
    root_device: u64,
    root_inode: u64,
    workspace_name: &str,
    workspace_device: u64,
    workspace_inode: u64,
    backup_workspace_name: &str,
    expected_manifest_digest: &str,
    expected_ciphertext_digest: &str,
) -> Result<WorkspaceRetirementPlan, WorkspaceRetirementError> {
    platform::capture(
        root,
        transaction_id,
        reference,
        root_device,
        root_inode,
        workspace_name,
        workspace_device,
        workspace_inode,
        backup_workspace_name,
        expected_manifest_digest,
        expected_ciphertext_digest,
    )
}

pub(crate) fn retire_workspace_contents(
    root: &Path,
    authorization: &WorkspaceRetirementAuthorization,
) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
    platform::retire(root, authorization)
}

#[cfg(all(test, target_os = "linux"))]
pub(crate) fn retire_workspace_contents_with_fault(
    root: &Path,
    authorization: &WorkspaceRetirementAuthorization,
    boundary: usize,
) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
    platform::retire_with_fault(root, authorization, platform::FaultSchedule::at(boundary))
}

#[cfg(all(test, target_os = "linux"))]
pub(crate) fn install_before_pending_publish_revalidation_barrier(
    path: PathBuf,
    barrier: std::sync::Arc<std::sync::Barrier>,
) {
    platform::install_before_pending_publish_revalidation_barrier(path, barrier)
}

#[cfg(all(test, target_os = "linux"))]
pub(crate) fn install_before_quarantine_revalidation_barrier(
    path: PathBuf,
    barrier: std::sync::Arc<std::sync::Barrier>,
) {
    platform::install_before_quarantine_revalidation_barrier(path, barrier)
}

fn transaction_hex(transaction_id: ReplacementTransactionId) -> String {
    hex::encode(transaction_id.as_bytes())
}

fn valid_digest(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

fn valid_workspace_name(value: &str, transaction: &str) -> bool {
    value == format!("tx-{transaction}")
}

fn valid_backup_workspace_name(value: &str) -> bool {
    value.len() == 39
        && value.starts_with("backup-")
        && value[7..]
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

fn valid_artifact_name(value: &str) -> bool {
    value == JOURNAL_FILE
        || value == PENDING_SLOTS_READY_FILE
        || value == BACKUP_FILE
        || value == MANIFEST_FILE
        || value
            .strip_prefix(PENDING_SLOT_PREFIX)
            .is_some_and(|index| {
                index.len() == 2 && index.bytes().all(|byte| byte.is_ascii_digit())
            })
}

fn expected_workspace_names() -> Vec<String> {
    let mut names = vec![JOURNAL_FILE.to_owned(), PENDING_SLOTS_READY_FILE.to_owned()];
    names.extend((0..PENDING_SLOT_COUNT).map(|index| format!("{PENDING_SLOT_PREFIX}{index:02}")));
    names.sort();
    names
}

fn valid_workspace_artifacts(artifacts: &[RetirementArtifact]) -> bool {
    artifacts.len() == PENDING_SLOT_COUNT + 2
        && artifacts.iter().all(RetirementArtifact::valid)
        && artifacts.iter().all(|artifact| {
            if artifact.name == JOURNAL_FILE {
                artifact.len > 0 && artifact.len <= MAX_JOURNAL_BYTES as u64
            } else if artifact.name == PENDING_SLOTS_READY_FILE {
                artifact.len == 0
            } else {
                artifact.len == MAX_PENDING_BYTES as u64
            }
        })
        && artifacts
            .iter()
            .map(|artifact| artifact.name.clone())
            .collect::<Vec<_>>()
            == expected_workspace_names()
}

fn valid_backup_artifacts(artifacts: &[RetirementArtifact]) -> bool {
    artifacts.len() == 2
        && artifacts.iter().all(RetirementArtifact::valid)
        && artifacts.iter().all(|artifact| {
            if artifact.name == BACKUP_FILE {
                artifact.len > 0 && artifact.len <= MAX_BACKUP_BYTES as u64
            } else {
                artifact.len > 0 && artifact.len <= MAX_MANIFEST_BYTES as u64
            }
        })
        && artifacts
            .iter()
            .map(|artifact| artifact.name.as_str())
            .collect::<Vec<_>>()
            == vec![BACKUP_FILE, MANIFEST_FILE]
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;
    use serde::Deserialize;
    use sha2::{Digest, Sha256};
    use std::{
        ffi::{CStr, CString, OsStr, OsString},
        fs::File,
        io::{Read, Seek, SeekFrom, Write},
        os::{
            fd::{AsRawFd, FromRawFd, RawFd},
            unix::{
                ffi::{OsStrExt, OsStringExt},
                fs::MetadataExt,
            },
        },
        path::Component,
    };

    #[cfg(test)]
    static BEFORE_PENDING_PUBLISH_REVALIDATION: std::sync::OnceLock<
        std::sync::Mutex<std::collections::BTreeMap<PathBuf, std::sync::Arc<std::sync::Barrier>>>,
    > = std::sync::OnceLock::new();

    #[cfg(test)]
    static BEFORE_QUARANTINE_REVALIDATION: std::sync::OnceLock<
        std::sync::Mutex<std::collections::BTreeMap<PathBuf, std::sync::Arc<std::sync::Barrier>>>,
    > = std::sync::OnceLock::new();

    #[cfg(test)]
    pub(super) fn install_before_pending_publish_revalidation_barrier(
        path: PathBuf,
        barrier: std::sync::Arc<std::sync::Barrier>,
    ) {
        install_barrier(&BEFORE_PENDING_PUBLISH_REVALIDATION, path, barrier);
    }

    #[cfg(test)]
    pub(super) fn install_before_quarantine_revalidation_barrier(
        path: PathBuf,
        barrier: std::sync::Arc<std::sync::Barrier>,
    ) {
        install_barrier(&BEFORE_QUARANTINE_REVALIDATION, path, barrier);
    }

    #[cfg(test)]
    fn install_barrier(
        cell: &'static std::sync::OnceLock<
            std::sync::Mutex<std::collections::BTreeMap<PathBuf, std::sync::Arc<std::sync::Barrier>>>,
        >,
        path: PathBuf,
        barrier: std::sync::Arc<std::sync::Barrier>,
    ) {
        let barriers = cell.get_or_init(|| {
            std::sync::Mutex::new(std::collections::BTreeMap::new())
        });
        let replaced = barriers.lock().unwrap().insert(path, barrier);
        assert!(replaced.is_none(), "test retirement barrier already installed");
    }

    #[cfg(test)]
    fn wait_at_barrier(
        cell: &'static std::sync::OnceLock<
            std::sync::Mutex<std::collections::BTreeMap<PathBuf, std::sync::Arc<std::sync::Barrier>>>,
        >,
        path: &Path,
    ) {
        let barriers = cell.get_or_init(|| {
            std::sync::Mutex::new(std::collections::BTreeMap::new())
        });
        let barrier = barriers.lock().unwrap().remove(path);
        if let Some(barrier) = barrier {
            barrier.wait();
            barrier.wait();
        }
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct BackupManifest {
        version: u32,
        transaction_id: String,
        recovery_key_provider_kind: String,
        recovery_key_provider_version: u32,
        recovery_key_reference_digest: String,
        target: String,
        root_identity: ManifestIdentity,
        target_identity: ManifestIdentity,
        original_mode: u32,
        original_uid: u32,
        original_gid: u32,
        original_digest: String,
        original_len: usize,
        nonce: String,
        ciphertext_digest: String,
    }

    #[derive(Deserialize, PartialEq, Eq)]
    #[serde(deny_unknown_fields)]
    struct ManifestIdentity {
        volume: u64,
        file: u64,
    }

    #[derive(Serialize)]
    struct StepMarker<'a> {
        version: u32,
        plan_digest: &'a str,
        index: usize,
        name: &'a str,
        device: u64,
        inode: u64,
    }

    #[derive(Serialize)]
    struct CompletionRecord<'a> {
        version: u32,
        plan_digest: &'a str,
        transaction_id: &'a str,
        workspace_name: &'a str,
    }

    #[derive(Clone, Copy, PartialEq, Eq)]
    enum ObjectKind {
        File,
        Directory,
    }

    #[cfg(test)]
    #[derive(Clone, Copy)]
    pub(super) struct FaultSchedule {
        boundary: usize,
        seen: usize,
    }

    #[cfg(test)]
    impl FaultSchedule {
        pub(super) fn at(boundary: usize) -> Self {
            Self { boundary, seen: 0 }
        }
    }

    struct FaultState {
        #[cfg(test)]
        schedule: Option<FaultSchedule>,
    }

    impl FaultState {
        fn none() -> Self {
            Self {
                #[cfg(test)]
                schedule: None,
            }
        }

        fn boundary(&mut self) -> Result<(), WorkspaceRetirementError> {
            #[cfg(test)]
            if let Some(schedule) = self.schedule.as_mut() {
                schedule.seen += 1;
                if schedule.seen == schedule.boundary {
                    return Err(WorkspaceRetirementError::InjectedCrash);
                }
            }
            Ok(())
        }
    }

    pub(super) fn capture(
        root: &Path,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
        root_device: u64,
        root_inode: u64,
        workspace_name: &str,
        workspace_device: u64,
        workspace_inode: u64,
        backup_workspace_name: &str,
        expected_manifest_digest: &str,
        expected_ciphertext_digest: &str,
    ) -> Result<WorkspaceRetirementPlan, WorkspaceRetirementError> {
        let transaction = transaction_hex(transaction_id);
        if !valid_workspace_name(workspace_name, &transaction)
            || !valid_backup_workspace_name(backup_workspace_name)
            || !valid_digest(expected_manifest_digest)
            || !valid_digest(expected_ciphertext_digest)
        {
            return Err(WorkspaceRetirementError::Corrupt);
        }
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir, root)?;
        if root_identity != RetirementIdentity::new(root_device, root_inode) {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                root.to_path_buf(),
            ));
        }
        let replacement_path = root.join(REPLACEMENT_DIRECTORY);
        let replacement_parent = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        let workspace_path = replacement_path.join(workspace_name);
        let workspace = open_private_directory_at(
            replacement_parent.as_raw_fd(),
            OsStr::new(workspace_name),
            &workspace_path,
        )?;
        if identity(&workspace, &workspace_path)?
            != RetirementIdentity::new(workspace_device, workspace_inode)
        {
            return Err(WorkspaceRetirementError::RecoveryConflict(workspace_path));
        }
        require_exact_names(&workspace, &workspace_path, &expected_workspace_names())?;
        let mut workspace_artifacts = Vec::with_capacity(PENDING_SLOT_COUNT + 2);
        for name in expected_workspace_names() {
            let max = if name == JOURNAL_FILE {
                MAX_JOURNAL_BYTES
            } else {
                MAX_PENDING_BYTES
            };
            workspace_artifacts.push(capture_file(
                &workspace,
                &name,
                &workspace_path.join(&name),
                max,
            )?);
        }

        let backup_parent_path = root.join(BACKUP_DIRECTORY);
        let backup_parent = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            &backup_parent_path,
        )?;
        let backup_workspace_path = backup_parent_path.join(backup_workspace_name);
        let backup_workspace = open_private_directory_at(
            backup_parent.as_raw_fd(),
            OsStr::new(backup_workspace_name),
            &backup_workspace_path,
        )?;
        require_exact_names(
            &backup_workspace,
            &backup_workspace_path,
            &[BACKUP_FILE.to_owned(), MANIFEST_FILE.to_owned()],
        )?;
        let backup_artifact = capture_file(
            &backup_workspace,
            BACKUP_FILE,
            &backup_workspace_path.join(BACKUP_FILE),
            MAX_BACKUP_BYTES,
        )?;
        let manifest_artifact = capture_file(
            &backup_workspace,
            MANIFEST_FILE,
            &backup_workspace_path.join(MANIFEST_FILE),
            MAX_MANIFEST_BYTES,
        )?;
        validate_backup_manifest(
            &backup_workspace,
            &backup_workspace_path,
            transaction_id,
            reference,
            root_identity,
            &backup_artifact,
            &manifest_artifact,
            expected_manifest_digest,
            expected_ciphertext_digest,
        )?;
        let plan = WorkspaceRetirementPlan {
            version: FORMAT_VERSION,
            transaction_id: transaction,
            root_identity,
            replacement_parent_identity: identity(&replacement_parent, &replacement_path)?,
            workspace_name: workspace_name.to_owned(),
            workspace_identity: identity(&workspace, &workspace_path)?,
            workspace_artifacts: {
                workspace_artifacts.sort_by(|left, right| left.name.cmp(&right.name));
                workspace_artifacts
            },
            backup_parent_identity: identity(&backup_parent, &backup_parent_path)?,
            backup_workspace_name: backup_workspace_name.to_owned(),
            backup_workspace_identity: identity(&backup_workspace, &backup_workspace_path)?,
            backup_artifacts: vec![backup_artifact, manifest_artifact],
        };
        let reopened_root = open_root(root)?;
        require_same_identity(&reopened_root, &root_dir, root)?;
        let reopened_replacement = open_private_directory_at(
            reopened_root.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        require_same_identity(
            &reopened_replacement,
            &replacement_parent,
            &replacement_path,
        )?;
        let reopened_workspace = open_private_directory_at(
            reopened_replacement.as_raw_fd(),
            OsStr::new(workspace_name),
            &workspace_path,
        )?;
        require_same_identity(&reopened_workspace, &workspace, &workspace_path)?;
        let reopened_backup_parent = open_private_directory_at(
            reopened_root.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            &backup_parent_path,
        )?;
        require_same_identity(&reopened_backup_parent, &backup_parent, &backup_parent_path)?;
        let reopened_backup_workspace = open_private_directory_at(
            reopened_backup_parent.as_raw_fd(),
            OsStr::new(backup_workspace_name),
            &backup_workspace_path,
        )?;
        require_same_identity(
            &reopened_backup_workspace,
            &backup_workspace,
            &backup_workspace_path,
        )?;
        plan.validate_durable(
            transaction_id,
            root_device,
            root_inode,
            workspace_name,
            workspace_device,
            workspace_inode,
        )
    }

    fn validate_backup_manifest(
        workspace: &File,
        workspace_path: &Path,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
        root_identity: RetirementIdentity,
        backup_artifact: &RetirementArtifact,
        manifest_artifact: &RetirementArtifact,
        expected_manifest_digest: &str,
        expected_ciphertext_digest: &str,
    ) -> Result<(), WorkspaceRetirementError> {
        let mut manifest = open_private_file_at(
            workspace.as_raw_fd(),
            OsStr::new(MANIFEST_FILE),
            &workspace_path.join(MANIFEST_FILE),
        )?;
        let bytes = read_bounded(
            &mut manifest,
            MAX_MANIFEST_BYTES,
            &workspace_path.join(MANIFEST_FILE),
        )?;
        if identity(&manifest, &workspace_path.join(MANIFEST_FILE))? != manifest_artifact.identity
            || bytes.len() as u64 != manifest_artifact.len
            || digest(&bytes) != manifest_artifact.digest
            || manifest_artifact.digest != expected_manifest_digest
            || backup_artifact.digest != expected_ciphertext_digest
        {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                workspace_path.join(MANIFEST_FILE),
            ));
        }
        let manifest: BackupManifest =
            serde_json::from_slice(&bytes).map_err(|_| WorkspaceRetirementError::Corrupt)?;
        let reference_digest = digest(reference.opaque_reference());
        if manifest.version != BACKUP_FORMAT_VERSION
            || manifest.transaction_id != transaction_hex(transaction_id)
            || manifest.recovery_key_provider_kind != reference.provider_kind()
            || manifest.recovery_key_provider_version != reference.provider_version()
            || manifest.recovery_key_reference_digest != reference_digest
            || manifest.root_identity
                != (ManifestIdentity {
                    volume: root_identity.device,
                    file: root_identity.inode,
                })
            || manifest.ciphertext_digest != backup_artifact.digest
            || manifest.ciphertext_digest != expected_ciphertext_digest
            || !valid_digest(&manifest.original_digest)
            || manifest.original_len > 4 * 1024 * 1024
            || manifest.original_mode & 0o400 == 0
            || manifest.original_mode & 0o077 != 0
            || manifest.original_mode & 0o7000 != 0
            || manifest.original_uid != unsafe { libc::geteuid() }
            || manifest.nonce.len() != 24
            || !manifest
                .nonce
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            || manifest.target.is_empty()
            || manifest.target_identity.volume == 0
            || manifest.target_identity.file == 0
        {
            return Err(WorkspaceRetirementError::Corrupt);
        }
        Ok(())
    }

    pub(super) fn retire(
        root: &Path,
        authorization: &WorkspaceRetirementAuthorization,
    ) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
        retire_inner(root, authorization, &mut FaultState::none())
    }

    #[cfg(test)]
    pub(super) fn retire_with_fault(
        root: &Path,
        authorization: &WorkspaceRetirementAuthorization,
        schedule: FaultSchedule,
    ) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
        retire_inner(
            root,
            authorization,
            &mut FaultState {
                schedule: Some(schedule),
            },
        )
    }

    fn retire_inner(
        root: &Path,
        authorization: &WorkspaceRetirementAuthorization,
        fault: &mut FaultState,
    ) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
        let plan = authorization.plan();
        let root_dir = open_root(root)?;
        require_identity(&root_dir, plan.root_identity, root)?;
        let replacement_path = root.join(REPLACEMENT_DIRECTORY);
        let replacement_parent = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        require_identity(
            &replacement_parent,
            plan.replacement_parent_identity,
            &replacement_path,
        )?;
        let target_lock_path = replacement_path.join(TARGET_LOCK_FILE);
        let target_lock = open_private_file_at(
            replacement_parent.as_raw_fd(),
            OsStr::new(TARGET_LOCK_FILE),
            &target_lock_path,
        )?;
        lock_exclusive(&target_lock, &target_lock_path)?;
        let workspace_path = replacement_path.join(&plan.workspace_name);
        let workspace = open_private_directory_at(
            replacement_parent.as_raw_fd(),
            OsStr::new(&plan.workspace_name),
            &workspace_path,
        )?;
        require_identity(&workspace, plan.workspace_identity, &workspace_path)?;

        let backup_parent_path = root.join(BACKUP_DIRECTORY);
        let backup_parent = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            &backup_parent_path,
        )?;
        require_identity(
            &backup_parent,
            plan.backup_parent_identity,
            &backup_parent_path,
        )?;

        let retirement_path = root.join(RETIREMENT_DIRECTORY);
        let retirement_root = open_or_create_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(RETIREMENT_DIRECTORY),
            &retirement_path,
        )?;
        sync_directory(&root_dir, root, fault)?;
        lock_exclusive(&retirement_root, &retirement_path)?;
        let transaction_path = retirement_path.join(&plan.transaction_id);
        let retirement_tx = open_or_create_private_directory_at(
            retirement_root.as_raw_fd(),
            OsStr::new(&plan.transaction_id),
            &transaction_path,
        )?;
        sync_directory(&retirement_root, &retirement_path, fault)?;
        persist_exact_plan(&retirement_tx, &transaction_path, plan, fault)?;
        let plan_bytes = serde_json::to_vec(plan).map_err(|_| WorkspaceRetirementError::Corrupt)?;
        let plan_digest = digest(&plan_bytes);

        validate_retirement_namespace(&retirement_tx, &transaction_path, plan)?;
        validate_source_names(
            &workspace,
            &workspace_path,
            &plan.workspace_artifacts,
            &retirement_tx,
            plan.backup_artifacts.len() + 1,
        )?;
        let backup_workspace_path = backup_parent_path.join(&plan.backup_workspace_name);
        let backup_exists = entry_exists(
            backup_parent.as_raw_fd(),
            OsStr::new(&plan.backup_workspace_name),
        )?;
        if backup_exists {
            let backup_workspace = open_private_directory_at(
                backup_parent.as_raw_fd(),
                OsStr::new(&plan.backup_workspace_name),
                &backup_workspace_path,
            )?;
            require_identity(
                &backup_workspace,
                plan.backup_workspace_identity,
                &backup_workspace_path,
            )?;
            validate_source_names(
                &backup_workspace,
                &backup_workspace_path,
                &plan.backup_artifacts,
                &retirement_tx,
                0,
            )?;
        }

        let mut index = 0usize;
        if backup_exists {
            let backup_workspace = open_private_directory_at(
                backup_parent.as_raw_fd(),
                OsStr::new(&plan.backup_workspace_name),
                &backup_workspace_path,
            )?;
            for artifact in &plan.backup_artifacts {
                retire_one(
                    &backup_workspace,
                    &backup_workspace_path,
                    &retirement_tx,
                    &transaction_path,
                    artifact,
                    index,
                    ObjectKind::File,
                    &plan_digest,
                    fault,
                )?;
                index += 1;
            }
        } else {
            for artifact in &plan.backup_artifacts {
                persist_marker(
                    &retirement_tx,
                    &transaction_path,
                    artifact,
                    index,
                    &plan_digest,
                    fault,
                )?;
                index += 1;
            }
        }

        let backup_directory_artifact = RetirementArtifact {
            name: plan.backup_workspace_name.clone(),
            identity: plan.backup_workspace_identity,
            len: 0,
            mode: 0o700,
            uid: unsafe { libc::geteuid() },
            gid: unsafe { libc::getegid() },
            digest: digest(&[]),
        };
        retire_one(
            &backup_parent,
            &backup_parent_path,
            &retirement_tx,
            &transaction_path,
            &backup_directory_artifact,
            index,
            ObjectKind::Directory,
            &plan_digest,
            fault,
        )?;
        index += 1;

        for artifact in &plan.workspace_artifacts {
            retire_one(
                &workspace,
                &workspace_path,
                &retirement_tx,
                &transaction_path,
                artifact,
                index,
                ObjectKind::File,
                &plan_digest,
                fault,
            )?;
            index += 1;
        }
        ensure_empty(&workspace, &workspace_path)?;
        sync_directory(&workspace, &workspace_path, fault)?;
        sync_directory(&backup_parent, &backup_parent_path, fault)?;
        sync_directory(&retirement_tx, &transaction_path, fault)?;
        persist_completion(&retirement_tx, &transaction_path, plan, &plan_digest, fault)?;
        revalidate_namespaces(
            root,
            &root_dir,
            plan,
            &replacement_parent,
            &workspace,
            &backup_parent,
        )?;
        drop(target_lock);
        Ok(WorkspaceContentsRetiredReceipt {
            terminal: authorization.terminal,
            transaction_id: authorization.transaction_id,
            reference: authorization.reference.clone(),
            workspace_name: plan.workspace_name.clone(),
            workspace_device: plan.workspace_identity.device,
            workspace_inode: plan.workspace_identity.inode,
        })
    }

    fn retire_one(
        source_parent: &File,
        source_path: &Path,
        retirement_tx: &File,
        transaction_path: &Path,
        artifact: &RetirementArtifact,
        index: usize,
        kind: ObjectKind,
        plan_digest: &str,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let quarantine = quarantine_name(index);
        let marker = marker_name(index);
        let source_exists = entry_exists(source_parent.as_raw_fd(), OsStr::new(&artifact.name))?;
        let quarantine_exists = entry_exists(retirement_tx.as_raw_fd(), OsStr::new(&quarantine))?;
        let marker_exists = entry_exists(retirement_tx.as_raw_fd(), OsStr::new(&marker))?;
        if source_exists && quarantine_exists {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                source_path.join(&artifact.name),
            ));
        }
        if source_exists {
            verify_artifact(source_parent, source_path, artifact, kind)?;
            if marker_exists {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    source_path.join(&artifact.name),
                ));
            }
            rename_no_replace(
                source_parent,
                OsStr::new(&artifact.name),
                retirement_tx,
                OsStr::new(&quarantine),
                &source_path.join(&artifact.name),
            )?;
            fault.boundary()?;
            sync_directory(source_parent, source_path, fault)?;
            sync_directory(retirement_tx, transaction_path, fault)?;
        } else if !quarantine_exists && !marker_exists {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                source_path.join(&artifact.name),
            ));
        }
        if entry_exists(retirement_tx.as_raw_fd(), OsStr::new(&quarantine))? {
            let removed_handle = verify_artifact(
                retirement_tx,
                transaction_path,
                &RetirementArtifact {
                    name: quarantine.clone(),
                    ..artifact.clone()
                },
                kind,
            )?;
        }
        persist_marker(
            retirement_tx,
            transaction_path,
            artifact,
            index,
            plan_digest,
            fault,
        )?;
        if entry_exists(retirement_tx.as_raw_fd(), OsStr::new(&quarantine))? {
            #[cfg(test)]
            wait_at_barrier(
                &BEFORE_QUARANTINE_REVALIDATION,
                &transaction_path.join(&quarantine),
            );
            verify_artifact(
                retirement_tx,
                transaction_path,
                &RetirementArtifact {
                    name: quarantine.clone(),
                    ..artifact.clone()
                },
                kind,
            )?;
            unlink_named(
                retirement_tx,
                OsStr::new(&quarantine),
                kind,
                &transaction_path.join(&quarantine),
                fault,
            )?;
            let removed_metadata = removed_handle
                .metadata()
                .map_err(|source| io_error(transaction_path.join(&quarantine), source))?;
            if entry_exists(retirement_tx.as_raw_fd(), OsStr::new(&quarantine))?
                || (kind == ObjectKind::File && removed_metadata.nlink() != 0)
            {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    transaction_path.join(&quarantine),
                ));
            }
        }
        sync_directory(retirement_tx, transaction_path, fault)?;
        Ok(())
    }

    fn persist_exact_plan(
        directory: &File,
        path: &Path,
        plan: &WorkspaceRetirementPlan,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let bytes = serde_json::to_vec(plan).map_err(|_| WorkspaceRetirementError::Corrupt)?;
        if bytes.len() > MAX_PLAN_BYTES {
            return Err(WorkspaceRetirementError::Corrupt);
        }
        persist_exact_file(directory, path, PLAN_FILE, &bytes, MAX_PLAN_BYTES, fault)
    }

    fn persist_marker(
        directory: &File,
        path: &Path,
        artifact: &RetirementArtifact,
        index: usize,
        plan_digest: &str,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let bytes = serde_json::to_vec(&StepMarker {
            version: FORMAT_VERSION,
            plan_digest,
            index,
            name: &artifact.name,
            device: artifact.identity.device,
            inode: artifact.identity.inode,
        })
        .map_err(|_| WorkspaceRetirementError::Corrupt)?;
        persist_exact_file(
            directory,
            path,
            &marker_name(index),
            &bytes,
            MAX_MARKER_BYTES,
            fault,
        )
    }

    fn persist_completion(
        directory: &File,
        path: &Path,
        plan: &WorkspaceRetirementPlan,
        plan_digest: &str,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let bytes = serde_json::to_vec(&CompletionRecord {
            version: FORMAT_VERSION,
            plan_digest,
            transaction_id: &plan.transaction_id,
            workspace_name: &plan.workspace_name,
        })
        .map_err(|_| WorkspaceRetirementError::Corrupt)?;
        persist_exact_file(
            directory,
            path,
            COMPLETE_FILE,
            &bytes,
            MAX_MARKER_BYTES,
            fault,
        )
    }

    fn persist_exact_file(
        directory: &File,
        path: &Path,
        name: &str,
        bytes: &[u8],
        limit: usize,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let pending_name = format!(".{name}.pending");
        if entry_exists(directory.as_raw_fd(), OsStr::new(name))? {
            if entry_exists(directory.as_raw_fd(), OsStr::new(&pending_name))? {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    path.join(pending_name),
                ));
            }
            let mut existing =
                open_private_file_at(directory.as_raw_fd(), OsStr::new(name), &path.join(name))?;
            let current = read_bounded(&mut existing, limit, &path.join(name))?;
            if current != bytes {
                return Err(WorkspaceRetirementError::RecoveryConflict(path.join(name)));
            }
            existing
                .sync_all()
                .map_err(|source| io_error(path.join(name), source))?;
            sync_directory(directory, path, fault)?;
            return Ok(());
        }
        if entry_exists(directory.as_raw_fd(), OsStr::new(&pending_name))? {
            let mut pending = open_private_file_at(
                directory.as_raw_fd(),
                OsStr::new(&pending_name),
                &path.join(&pending_name),
            )?;
            let pending_identity = identity(&pending, &path.join(&pending_name))?;
            let current = read_bounded(&mut pending, limit, &path.join(&pending_name))?;
            if current != bytes {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    path.join(pending_name),
                ));
            }
            pending
                .sync_all()
                .map_err(|source| io_error(path.join(&pending_name), source))?;
            sync_directory(directory, path, fault)?;
            let current_pending = open_exact_file(
                directory,
                path,
                &pending_name,
                bytes,
                limit,
                pending_identity,
            )?;
            #[cfg(test)]
            wait_at_barrier(
                &BEFORE_PENDING_PUBLISH_REVALIDATION,
                &path.join(&pending_name),
            );
            rename_no_replace(
                directory,
                OsStr::new(&pending_name),
                directory,
                OsStr::new(name),
                &path.join(name),
            )?;
            let published = open_exact_file(
                directory,
                path,
                name,
                bytes,
                limit,
                pending_identity,
            )?;
            require_same_identity(&published, &current_pending, &path.join(name))?;
            sync_directory(directory, path, fault)?;
            return Ok(());
        }
        let mut pending = create_private_file_at(
            directory.as_raw_fd(),
            OsStr::new(&pending_name),
            &path.join(&pending_name),
        )?;
        let pending_identity = identity(&pending, &path.join(&pending_name))?;
        pending
            .write_all(bytes)
            .map_err(|source| io_error(path.join(&pending_name), source))?;
        pending
            .sync_all()
            .map_err(|source| io_error(path.join(&pending_name), source))?;
        sync_directory(directory, path, fault)?;
        let current_pending = open_exact_file(
            directory,
            path,
            &pending_name,
            bytes,
            limit,
            pending_identity,
        )?;
        #[cfg(test)]
        wait_at_barrier(
            &BEFORE_PENDING_PUBLISH_REVALIDATION,
            &path.join(&pending_name),
        );
        rename_no_replace(
            directory,
            OsStr::new(&pending_name),
            directory,
            OsStr::new(name),
            &path.join(name),
        )?;
        let published = open_exact_file(
            directory,
            path,
            name,
            bytes,
            limit,
            pending_identity,
        )?;
        require_same_identity(&published, &current_pending, &path.join(name))?;
        sync_directory(directory, path, fault)?;
        Ok(())
    }

    fn open_exact_file(
        directory: &File,
        path: &Path,
        name: &str,
        expected: &[u8],
        limit: usize,
        expected_identity: RetirementIdentity,
    ) -> Result<File, WorkspaceRetirementError> {
        let display = path.join(name);
        let mut file = open_private_file_at(directory.as_raw_fd(), OsStr::new(name), &display)?;
        require_identity(&file, expected_identity, &display)?;
        let bytes = read_bounded(&mut file, limit, &display)?;
        if bytes != expected || identity(&file, &display)? != expected_identity {
            return Err(WorkspaceRetirementError::RecoveryConflict(display));
        }
        Ok(file)
    }

    fn validate_source_names(
        source: &File,
        source_path: &Path,
        artifacts: &[RetirementArtifact],
        retirement_tx: &File,
        start_index: usize,
    ) -> Result<(), WorkspaceRetirementError> {
        let actual = read_directory_names(source, source_path)?;
        for name in actual {
            let Some(text) = name.to_str() else {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    source_path.join(name),
                ));
            };
            if !artifacts.iter().any(|artifact| artifact.name == text) {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    source_path.join(name),
                ));
            }
        }
        for (offset, artifact) in artifacts.iter().enumerate() {
            let source_exists = entry_exists(source.as_raw_fd(), OsStr::new(&artifact.name))?;
            let quarantine_exists = entry_exists(
                retirement_tx.as_raw_fd(),
                OsStr::new(&quarantine_name(start_index + offset)),
            )?;
            if source_exists && quarantine_exists {
                return Err(WorkspaceRetirementError::RecoveryConflict(
                    source_path.join(&artifact.name),
                ));
            }
        }
        Ok(())
    }

    fn validate_retirement_namespace(
        directory: &File,
        path: &Path,
        plan: &WorkspaceRetirementPlan,
    ) -> Result<(), WorkspaceRetirementError> {
        let max_steps = plan.backup_artifacts.len() + 1 + plan.workspace_artifacts.len();
        for name in read_directory_names(directory, path)? {
            let Some(text) = name.to_str() else {
                return Err(WorkspaceRetirementError::RecoveryConflict(path.join(name)));
            };
            let known = text == PLAN_FILE
                || text == COMPLETE_FILE
                || text == format!(".{COMPLETE_FILE}.pending")
                || (0..max_steps).any(|index| {
                    let marker = marker_name(index);
                    text == marker
                        || text == format!(".{marker}.pending")
                        || text == quarantine_name(index)
                });
            if !known {
                return Err(WorkspaceRetirementError::RecoveryConflict(path.join(name)));
            }
        }
        Ok(())
    }

    fn revalidate_namespaces(
        root: &Path,
        root_dir: &File,
        plan: &WorkspaceRetirementPlan,
        replacement_parent: &File,
        workspace: &File,
        backup_parent: &File,
    ) -> Result<(), WorkspaceRetirementError> {
        let reopened_root = open_root(root)?;
        require_identity(&reopened_root, plan.root_identity, root)?;
        require_identity(root_dir, plan.root_identity, root)?;
        let replacement_path = root.join(REPLACEMENT_DIRECTORY);
        let reopened_replacement = open_private_directory_at(
            reopened_root.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        require_same_identity(&reopened_replacement, replacement_parent, &replacement_path)?;
        let workspace_path = replacement_path.join(&plan.workspace_name);
        let reopened_workspace = open_private_directory_at(
            reopened_replacement.as_raw_fd(),
            OsStr::new(&plan.workspace_name),
            &workspace_path,
        )?;
        require_same_identity(&reopened_workspace, workspace, &workspace_path)?;
        let backup_path = root.join(BACKUP_DIRECTORY);
        let reopened_backup = open_private_directory_at(
            reopened_root.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            &backup_path,
        )?;
        require_same_identity(&reopened_backup, backup_parent, &backup_path)?;
        ensure_child_absent(&reopened_backup, &plan.backup_workspace_name, &backup_path)?;
        ensure_empty(&reopened_workspace, &workspace_path)
    }

    fn capture_file(
        parent: &File,
        name: &str,
        display: &Path,
        limit: usize,
    ) -> Result<RetirementArtifact, WorkspaceRetirementError> {
        capture_open_file(parent, name, display, limit).map(|(_, artifact)| artifact)
    }

    fn capture_open_file(
        parent: &File,
        name: &str,
        display: &Path,
        limit: usize,
    ) -> Result<(File, RetirementArtifact), WorkspaceRetirementError> {
        let mut file = open_private_file_at(parent.as_raw_fd(), OsStr::new(name), display)?;
        let before = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        let captured_identity = identity(&file, display)?;
        let bytes = read_bounded(&mut file, limit, display)?;
        let after = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if identity(&file, display)? != captured_identity
            || before.len() != after.len()
            || before.mode() != after.mode()
            || before.uid() != after.uid()
            || before.gid() != after.gid()
            || before.nlink() != 1
        {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                display.to_path_buf(),
            ));
        }
        Ok((
            file,
            RetirementArtifact {
                name: name.to_owned(),
                identity: captured_identity,
                len: before.len(),
                mode: before.mode() & 0o7777,
                uid: before.uid(),
                gid: before.gid(),
                digest: digest(&bytes),
            },
        ))
    }

    fn verify_artifact(
        parent: &File,
        parent_path: &Path,
        artifact: &RetirementArtifact,
        kind: ObjectKind,
    ) -> Result<File, WorkspaceRetirementError> {
        let display = parent_path.join(&artifact.name);
        match kind {
            ObjectKind::File => {
                let (file, current) =
                    capture_open_file(parent, &artifact.name, &display, artifact.len as usize)?;
                if current != *artifact {
                    return Err(WorkspaceRetirementError::RecoveryConflict(display));
                }
                Ok(file)
            }
            ObjectKind::Directory => {
                let directory = open_private_directory_at(
                    parent.as_raw_fd(),
                    OsStr::new(&artifact.name),
                    &display,
                )?;
                if identity(&directory, &display)? != artifact.identity {
                    return Err(WorkspaceRetirementError::RecoveryConflict(display));
                }
                ensure_empty(&directory, &display)?;
                Ok(directory)
            }
        }
    }

    fn require_exact_names(
        directory: &File,
        path: &Path,
        expected: &[String],
    ) -> Result<(), WorkspaceRetirementError> {
        let actual = read_directory_names(directory, path)?;
        let expected = expected.iter().map(OsString::from).collect::<Vec<_>>();
        if actual != expected {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                path.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn ensure_empty(directory: &File, path: &Path) -> Result<(), WorkspaceRetirementError> {
        if read_directory_names(directory, path)?.is_empty() {
            Ok(())
        } else {
            Err(WorkspaceRetirementError::RecoveryConflict(
                path.to_path_buf(),
            ))
        }
    }

    fn ensure_child_absent(
        parent: &File,
        name: &str,
        path: &Path,
    ) -> Result<(), WorkspaceRetirementError> {
        if entry_exists(parent.as_raw_fd(), OsStr::new(name))? {
            Err(WorkspaceRetirementError::RecoveryConflict(path.join(name)))
        } else {
            Ok(())
        }
    }

    fn marker_name(index: usize) -> String {
        format!("step-{index:02}.authorized.json")
    }

    fn quarantine_name(index: usize) -> String {
        format!("quarantine-{index:02}")
    }

    fn rename_no_replace(
        from_parent: &File,
        from: &OsStr,
        to_parent: &File,
        to: &OsStr,
        display: &Path,
    ) -> Result<(), WorkspaceRetirementError> {
        let from = c_string(from)?;
        let to = c_string(to)?;
        if unsafe {
            libc::renameat2(
                from_parent.as_raw_fd(),
                from.as_ptr(),
                to_parent.as_raw_fd(),
                to.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn unlink_named(
        parent: &File,
        name: &OsStr,
        kind: ObjectKind,
        display: &Path,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        let name = c_string(name)?;
        let flags = if kind == ObjectKind::Directory {
            libc::AT_REMOVEDIR
        } else {
            0
        };
        if unsafe { libc::unlinkat(parent.as_raw_fd(), name.as_ptr(), flags) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        fault.boundary()
    }

    fn sync_directory(
        directory: &File,
        path: &Path,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRetirementError> {
        directory
            .sync_all()
            .map_err(|source| io_error(path, source))?;
        fault.boundary()
    }

    fn open_root(path: &Path) -> Result<File, WorkspaceRetirementError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|component| matches!(component, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(WorkspaceRetirementError::InvalidRoot(path.to_path_buf()));
        }
        let anchor = if path.is_absolute() {
            Path::new("/")
        } else {
            Path::new(".")
        };
        let anchor_name = CString::new(anchor.as_os_str().as_bytes()).expect("fixed anchor");
        let fd = unsafe {
            libc::open(
                anchor_name.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            )
        };
        if fd < 0 {
            return Err(io_error(anchor, io::Error::last_os_error()));
        }
        let mut directory = unsafe { File::from_raw_fd(fd) };
        let mut display = anchor.to_path_buf();
        for component in path.components() {
            match component {
                Component::RootDir | Component::CurDir => {}
                Component::Normal(name) => {
                    display.push(name);
                    directory = open_directory_at(directory.as_raw_fd(), name, &display)?;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(WorkspaceRetirementError::InvalidRoot(path.to_path_buf()))
                }
            }
        }
        verify_private_directory(&directory, path)?;
        Ok(directory)
    }

    fn open_or_create_private_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRetirementError> {
        let c_name = c_string(name)?;
        let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
        if result != 0 && io::Error::last_os_error().kind() != io::ErrorKind::AlreadyExists {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        open_private_directory_at(parent, name, display)
    }

    fn open_private_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRetirementError> {
        let directory = open_directory_at(parent, name, display)?;
        verify_private_directory(&directory, display)?;
        Ok(directory)
    }

    fn open_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRetirementError> {
        let c_name = c_string(name)?;
        let fd = unsafe {
            libc::openat(
                parent,
                c_name.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                0,
            )
        };
        if fd < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(unsafe { File::from_raw_fd(fd) })
    }

    fn create_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRetirementError> {
        let c_name = c_string(name)?;
        let fd = unsafe {
            libc::openat(
                parent,
                c_name.as_ptr(),
                libc::O_RDWR | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                0o600,
            )
        };
        if fd < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let file = unsafe { File::from_raw_fd(fd) };
        verify_private_file(&file, display)?;
        Ok(file)
    }

    fn open_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRetirementError> {
        let c_name = c_string(name)?;
        let fd = unsafe {
            libc::openat(
                parent,
                c_name.as_ptr(),
                libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                0,
            )
        };
        if fd < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let file = unsafe { File::from_raw_fd(fd) };
        verify_private_file(&file, display)?;
        Ok(file)
    }

    fn verify_private_directory(
        file: &File,
        display: &Path,
    ) -> Result<(), WorkspaceRetirementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_dir()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o700
        {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                display.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn verify_private_file(file: &File, display: &Path) -> Result<(), WorkspaceRetirementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(WorkspaceRetirementError::RecoveryConflict(
                display.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn read_bounded(
        file: &mut File,
        limit: usize,
        display: &Path,
    ) -> Result<Vec<u8>, WorkspaceRetirementError> {
        file.seek(SeekFrom::Start(0))
            .map_err(|source| io_error(display, source))?;
        let mut bytes = Vec::new();
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(display, source))?;
        if bytes.len() > limit {
            return Err(WorkspaceRetirementError::Corrupt);
        }
        Ok(bytes)
    }

    fn read_directory_names(
        directory: &File,
        display: &Path,
    ) -> Result<Vec<OsString>, WorkspaceRetirementError> {
        let duplicate = unsafe { libc::dup(directory.as_raw_fd()) };
        if duplicate < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let raw = unsafe { libc::fdopendir(duplicate) };
        if raw.is_null() {
            let error = io::Error::last_os_error();
            unsafe {
                libc::close(duplicate);
            }
            return Err(io_error(display, error));
        }
        let stream = DirectoryStream(raw);
        let mut names = Vec::new();
        loop {
            unsafe {
                *libc::__errno_location() = 0;
            }
            let entry = unsafe { libc::readdir(stream.0) };
            if entry.is_null() {
                let errno = unsafe { *libc::__errno_location() };
                if errno != 0 {
                    return Err(io_error(display, io::Error::from_raw_os_error(errno)));
                }
                break;
            }
            let bytes = unsafe { CStr::from_ptr((*entry).d_name.as_ptr()) }.to_bytes();
            if bytes != b"." && bytes != b".." {
                if names.len() >= 128 {
                    return Err(WorkspaceRetirementError::Corrupt);
                }
                names.push(OsString::from_vec(bytes.to_vec()));
            }
        }
        names.sort_by(|left, right| left.as_bytes().cmp(right.as_bytes()));
        Ok(names)
    }

    struct DirectoryStream(*mut libc::DIR);
    impl Drop for DirectoryStream {
        fn drop(&mut self) {
            unsafe {
                libc::closedir(self.0);
            }
        }
    }

    fn entry_exists(parent: RawFd, name: &OsStr) -> Result<bool, WorkspaceRetirementError> {
        let c_name = c_string(name)?;
        let result = unsafe {
            libc::faccessat(
                parent,
                c_name.as_ptr(),
                libc::F_OK,
                libc::AT_SYMLINK_NOFOLLOW,
            )
        };
        if result == 0 {
            return Ok(true);
        }
        let error = io::Error::last_os_error();
        if error.kind() == io::ErrorKind::NotFound {
            Ok(false)
        } else {
            Err(io_error(PathBuf::from(name), error))
        }
    }

    fn identity(
        file: &File,
        display: &Path,
    ) -> Result<RetirementIdentity, WorkspaceRetirementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        Ok(RetirementIdentity::new(metadata.dev(), metadata.ino()))
    }

    fn require_identity(
        file: &File,
        expected: RetirementIdentity,
        display: &Path,
    ) -> Result<(), WorkspaceRetirementError> {
        if identity(file, display)? == expected {
            Ok(())
        } else {
            Err(WorkspaceRetirementError::RecoveryConflict(
                display.to_path_buf(),
            ))
        }
    }

    fn require_same_identity(
        left: &File,
        right: &File,
        display: &Path,
    ) -> Result<(), WorkspaceRetirementError> {
        if identity(left, display)? == identity(right, display)? {
            Ok(())
        } else {
            Err(WorkspaceRetirementError::RecoveryConflict(
                display.to_path_buf(),
            ))
        }
    }

    fn c_string(name: &OsStr) -> Result<CString, WorkspaceRetirementError> {
        CString::new(name.as_bytes()).map_err(|_| WorkspaceRetirementError::Corrupt)
    }

    fn lock_exclusive(file: &File, display: &Path) -> Result<(), WorkspaceRetirementError> {
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn digest(bytes: &[u8]) -> String {
        hex::encode(Sha256::digest(bytes))
    }

    fn io_error(path: impl Into<PathBuf>, source: io::Error) -> WorkspaceRetirementError {
        WorkspaceRetirementError::Io {
            path: path.into(),
            source,
        }
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;

    pub(super) fn capture(
        _root: &Path,
        _transaction_id: ReplacementTransactionId,
        _reference: &RecoveryKeyReference,
        _root_device: u64,
        _root_inode: u64,
        _workspace_name: &str,
        _workspace_device: u64,
        _workspace_inode: u64,
        _backup_workspace_name: &str,
        _expected_manifest_digest: &str,
        _expected_ciphertext_digest: &str,
    ) -> Result<WorkspaceRetirementPlan, WorkspaceRetirementError> {
        Err(WorkspaceRetirementError::UnsupportedPlatform)
    }

    pub(super) fn retire(
        _root: &Path,
        _authorization: &WorkspaceRetirementAuthorization,
    ) -> Result<WorkspaceContentsRetiredReceipt, WorkspaceRetirementError> {
        Err(WorkspaceRetirementError::UnsupportedPlatform)
    }
}
