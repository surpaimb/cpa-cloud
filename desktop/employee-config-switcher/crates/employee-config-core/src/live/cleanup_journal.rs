//! Durable, bounded cleanup-phase journal for a synthetic Linux root.
//!
//! This module persists the five provider-agnostic cleanup phases and delegates
//! exact empty-workspace removal to the proof store. It does not issue the
//! initial trusted workspace identity, remove transaction contents, implement
//! a key provider, or enable any real client write. Each phase is an immutable
//! owner-private file published with a no-replace rename after its contents are
//! flushed.

use super::{
    completion_tombstone::{
        verify_removed_completion_tombstone, CompletionTombstoneDurableReceipt,
        CompletionTombstoneError, CompletionTombstoneRemovalReceipt,
    },
    linux_replacement::DurableTerminalHandoff,
    recovery_key::{
        CleanupArtifactRetirementAuthorization, CompletionTombstoneRemovalAuthorization,
        RecoveryKeyCleanupError, RecoveryKeyCleanupPhase, RecoveryKeyCleanupSequence,
        RecoveryKeyProvider, RecoveryKeyReference, ReplacementTransactionId,
        WorkspaceRemovalTarget,
    },
    replacement::ReplacementPhase,
    workspace_removal::{remove_workspace, verify_workspace_removal, WorkspaceRemovalError},
};
#[cfg(target_os = "linux")]
use serde::{Deserialize, Serialize};
use std::{
    io,
    path::{Path, PathBuf},
};

const CLEANUP_DIRECTORY: &str = ".cpa-cloud-employee-config-cleanup";

pub(crate) struct DurablePhaseEvidence(());
pub(crate) struct RetirementEvidence(());
#[cfg(target_os = "linux")]
const FORMAT_VERSION: u32 = 2;
#[cfg(target_os = "linux")]
const MAX_TRANSACTIONS: usize = 64;
#[cfg(target_os = "linux")]
const MAX_EVENTS_PER_TRANSACTION: usize = 5;
#[cfg(target_os = "linux")]
const MAX_EVENT_BYTES: usize = 16 * 1024;
#[cfg(target_os = "linux")]
const FINAL_SUFFIX: &str = ".json";
#[cfg(target_os = "linux")]
const PENDING_SUFFIX: &str = ".pending";

#[derive(Debug, thiserror::Error)]
pub enum CleanupJournalError {
    #[error("durable cleanup journal is not supported on this platform")]
    UnsupportedPlatform,
    #[error("cleanup journal root must not be empty or contain parent traversal: {0}")]
    InvalidRoot(PathBuf),
    #[error("cleanup journal metadata is invalid")]
    InvalidMetadata,
    #[error("cleanup journal is corrupt")]
    Corrupt,
    #[error("incomplete cleanup journal event requires recovery: {0}")]
    IncompleteWrite(PathBuf),
    #[error("cleanup journal transaction is missing: {0:?}")]
    Missing(ReplacementTransactionId),
    #[error("cleanup journal transaction already exists: {0:?}")]
    AlreadyExists(ReplacementTransactionId),
    #[error("cleanup journal namespace exceeds its bounded transaction count")]
    TooMany,
    #[error("cleanup journal namespace or object changed: {0}")]
    RecoveryConflict(PathBuf),
    #[error("cleanup transition failed: {0}")]
    Cleanup(#[from] RecoveryKeyCleanupError),
    #[error("workspace removal failed: {0}")]
    WorkspaceRemoval(#[from] WorkspaceRemovalError),
    #[error("completion tombstone verification failed: {0}")]
    CompletionTombstone(#[from] CompletionTombstoneError),
    #[error("cleanup journal filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
}

pub struct DurableCleanupJournal {
    root: PathBuf,
    sequence: RecoveryKeyCleanupSequence,
}

impl DurableCleanupJournal {
    pub fn phase(&self) -> RecoveryKeyCleanupPhase {
        self.sequence.phase()
    }

    pub fn terminal(&self) -> ReplacementPhase {
        self.sequence.terminal()
    }

    pub fn transaction_id(&self) -> ReplacementTransactionId {
        self.sequence.transaction_id()
    }

    pub fn reference(&self) -> &RecoveryKeyReference {
        self.sequence.reference()
    }

    pub fn record_completion_tombstone_durable(
        &mut self,
        receipt: &CompletionTombstoneDurableReceipt,
    ) -> Result<(), CleanupJournalError> {
        let previous = self.phase();
        self.sequence.record_completion_tombstone_durable(receipt)?;
        self.persist_or_restore(previous)
    }

    pub fn remove_workspace_durable(
        &mut self,
        workspace: &Path,
    ) -> Result<(), CleanupJournalError> {
        let authorization = self.sequence.authorize_workspace_removal()?;
        let verified = remove_workspace(&self.root, &authorization, workspace)?;
        let previous = self.phase();
        self.sequence
            .record_workspace_removal_durable(verified.receipt())?;
        self.persist_or_restore(previous)
    }

    pub fn delete_key<P: RecoveryKeyProvider + ?Sized>(
        &mut self,
        provider: &mut P,
    ) -> Result<(), CleanupJournalError> {
        if self.phase() != RecoveryKeyCleanupPhase::WorkspaceRemovalDurable {
            return Err(CleanupJournalError::Cleanup(
                RecoveryKeyCleanupError::InvalidTransition(self.phase()),
            ));
        }
        let authorization = self.sequence.workspace_removal_authorization();
        let _verified = verify_workspace_removal(&self.root, &authorization)?;
        let previous = self.phase();
        self.sequence.delete_key(provider)?;
        self.persist_or_restore(previous)
    }

    pub fn authorize_completion_tombstone_removal(
        &self,
    ) -> Result<CompletionTombstoneRemovalAuthorization, CleanupJournalError> {
        Ok(self.sequence.authorize_completion_tombstone_removal()?)
    }

    pub fn record_completion_tombstone_removal_durable(
        &mut self,
        receipt: CompletionTombstoneRemovalReceipt,
    ) -> Result<(), CleanupJournalError> {
        let previous = self.phase();
        self.sequence
            .record_completion_tombstone_removal_durable(receipt)?;
        self.persist_or_restore(previous)
    }

    /// Verify the inactive completion proof and return an opaque prerequisite
    /// for a future cross-namespace retirement protocol. This method deletes
    /// nothing and therefore cannot reclaim bounded journal/proof capacity yet.
    pub(crate) fn verify_artifact_retirement_preconditions(
        &self,
    ) -> Result<CleanupArtifactRetirementAuthorization, CleanupJournalError> {
        let proof_authorization = self
            .sequence
            .authorize_completion_tombstone_proof_verification()?;
        let receipt = verify_removed_completion_tombstone(&self.root, &proof_authorization)?;
        Ok(self
            .sequence
            .authorize_artifact_retirement(RetirementEvidence(()), &receipt)?)
    }

    fn persist_or_restore(
        &mut self,
        previous: RecoveryKeyCleanupPhase,
    ) -> Result<(), CleanupJournalError> {
        if let Err(error) = platform::append(&self.root, &self.sequence) {
            self.sequence = RecoveryKeyCleanupSequence::from_durable_phase(
                DurablePhaseEvidence(()),
                previous,
                self.sequence.terminal(),
                self.sequence.transaction_id(),
                self.sequence.reference().clone(),
                self.sequence.replacement_journal_version(),
                self.sequence.root_device(),
                self.sequence.root_inode(),
                self.sequence.workspace_target().clone(),
            )
            .expect("the prior durable cleanup phase remains valid");
            return Err(error);
        }
        Ok(())
    }
}

impl std::fmt::Debug for DurableCleanupJournal {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("DurableCleanupJournal")
            .field("root", &self.root)
            .field("sequence", &self.sequence)
            .finish()
    }
}

/// Starts cleanup only from the exact opaque handoff issued by the durable
/// replacement transaction. Loose construction exists only for downstream
/// `cfg(test)` fixtures.
pub(crate) fn create_durable_cleanup_journal(
    root: &Path,
    handoff: DurableTerminalHandoff,
) -> Result<DurableCleanupJournal, CleanupJournalError> {
    let sequence = RecoveryKeyCleanupSequence::after_durable_terminal(handoff)?;
    platform::create(root, &sequence)?;
    Ok(DurableCleanupJournal {
        root: root.to_path_buf(),
        sequence,
    })
}

#[cfg(test)]
fn create_durable_cleanup_journal_for_test(
    root: &Path,
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    workspace_target: WorkspaceRemovalTarget,
) -> Result<DurableCleanupJournal, CleanupJournalError> {
    #[cfg(target_os = "linux")]
    let (root_device, root_inode) = {
        use std::os::unix::fs::MetadataExt;
        let metadata = std::fs::metadata(root).map_err(|source| CleanupJournalError::Io {
            path: root.to_path_buf(),
            source,
        })?;
        (metadata.dev(), metadata.ino())
    };
    #[cfg(not(target_os = "linux"))]
    let (root_device, root_inode) = (1, 1);
    let sequence = RecoveryKeyCleanupSequence::for_test(
        terminal,
        transaction_id,
        reference,
        root_device,
        root_inode,
        workspace_target,
    )?;
    platform::create(root, &sequence)?;
    Ok(DurableCleanupJournal {
        root: root.to_path_buf(),
        sequence,
    })
}

pub fn load_durable_cleanup_journals(
    root: &Path,
) -> Result<Vec<DurableCleanupJournal>, CleanupJournalError> {
    platform::load(root)?
        .into_iter()
        .map(|sequence| {
            if matches!(
                sequence.phase(),
                RecoveryKeyCleanupPhase::WorkspaceRemovalDurable
                    | RecoveryKeyCleanupPhase::KeyDeletionConfirmed
                    | RecoveryKeyCleanupPhase::Complete
            ) {
                let authorization = sequence.workspace_removal_authorization();
                let _verified = verify_workspace_removal(root, &authorization)?;
            }
            Ok(DurableCleanupJournal {
                root: root.to_path_buf(),
                sequence,
            })
        })
        .collect()
}

#[cfg(target_os = "linux")]
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct RootIdentity {
    device: u64,
    inode: u64,
}

#[cfg(target_os = "linux")]
#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct CleanupEvent {
    version: u32,
    replacement_journal_version: u32,
    sequence: u8,
    transaction_id: String,
    root_identity: RootIdentity,
    terminal: String,
    phase: String,
    provider_kind: String,
    provider_version: u32,
    opaque_reference: String,
    workspace_name: String,
    workspace_device: u64,
    workspace_inode: u64,
}

#[cfg(target_os = "linux")]
fn phase_index(phase: RecoveryKeyCleanupPhase) -> u8 {
    match phase {
        RecoveryKeyCleanupPhase::TerminalRecorded => 0,
        RecoveryKeyCleanupPhase::CompletionTombstoneDurable => 1,
        RecoveryKeyCleanupPhase::WorkspaceRemovalDurable => 2,
        RecoveryKeyCleanupPhase::KeyDeletionConfirmed => 3,
        RecoveryKeyCleanupPhase::Complete => 4,
    }
}

#[cfg(target_os = "linux")]
fn phase_text(phase: RecoveryKeyCleanupPhase) -> &'static str {
    match phase {
        RecoveryKeyCleanupPhase::TerminalRecorded => "terminal_recorded",
        RecoveryKeyCleanupPhase::CompletionTombstoneDurable => "completion_tombstone_durable",
        RecoveryKeyCleanupPhase::WorkspaceRemovalDurable => "workspace_removal_durable",
        RecoveryKeyCleanupPhase::KeyDeletionConfirmed => "key_deletion_confirmed",
        RecoveryKeyCleanupPhase::Complete => "complete",
    }
}

#[cfg(target_os = "linux")]
fn phase_from_index(index: u8) -> Option<RecoveryKeyCleanupPhase> {
    match index {
        0 => Some(RecoveryKeyCleanupPhase::TerminalRecorded),
        1 => Some(RecoveryKeyCleanupPhase::CompletionTombstoneDurable),
        2 => Some(RecoveryKeyCleanupPhase::WorkspaceRemovalDurable),
        3 => Some(RecoveryKeyCleanupPhase::KeyDeletionConfirmed),
        4 => Some(RecoveryKeyCleanupPhase::Complete),
        _ => None,
    }
}

#[cfg(target_os = "linux")]
fn terminal_text(terminal: ReplacementPhase) -> Result<&'static str, CleanupJournalError> {
    match terminal {
        ReplacementPhase::Complete => Ok("complete"),
        ReplacementPhase::RolledBack => Ok("rolled_back"),
        _ => Err(CleanupJournalError::InvalidMetadata),
    }
}

#[cfg(target_os = "linux")]
fn terminal_from_text(value: &str) -> Result<ReplacementPhase, CleanupJournalError> {
    match value {
        "complete" => Ok(ReplacementPhase::Complete),
        "rolled_back" => Ok(ReplacementPhase::RolledBack),
        _ => Err(CleanupJournalError::Corrupt),
    }
}

#[cfg(target_os = "linux")]
fn transaction_hex(transaction_id: ReplacementTransactionId) -> String {
    hex::encode(transaction_id.as_bytes())
}

#[cfg(target_os = "linux")]
fn event_name(transaction_id: ReplacementTransactionId, sequence: u8) -> String {
    format!(
        "{}.{}{FINAL_SUFFIX}",
        transaction_hex(transaction_id),
        sequence
    )
}

#[cfg(target_os = "linux")]
fn pending_name(transaction_id: ReplacementTransactionId, sequence: u8) -> String {
    format!(
        "{}.{}{PENDING_SUFFIX}",
        transaction_hex(transaction_id),
        sequence
    )
}

#[cfg(target_os = "linux")]
fn parse_transaction(value: &str) -> Result<ReplacementTransactionId, CleanupJournalError> {
    if value.len() != 32
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(CleanupJournalError::Corrupt);
    }
    let bytes: [u8; 16] = hex::decode(value)
        .map_err(|_| CleanupJournalError::Corrupt)?
        .try_into()
        .map_err(|_| CleanupJournalError::Corrupt)?;
    ReplacementTransactionId::from_bytes(bytes).map_err(|_| CleanupJournalError::Corrupt)
}

#[cfg(target_os = "linux")]
fn event_for(
    sequence: &RecoveryKeyCleanupSequence,
    root_identity: RootIdentity,
) -> Result<CleanupEvent, CleanupJournalError> {
    Ok(CleanupEvent {
        version: FORMAT_VERSION,
        replacement_journal_version: sequence.replacement_journal_version(),
        sequence: phase_index(sequence.phase()),
        transaction_id: transaction_hex(sequence.transaction_id()),
        root_identity,
        terminal: terminal_text(sequence.terminal())?.to_owned(),
        phase: phase_text(sequence.phase()).to_owned(),
        provider_kind: sequence.reference().provider_kind().to_owned(),
        provider_version: sequence.reference().provider_version(),
        opaque_reference: hex::encode(sequence.reference().opaque_reference()),
        workspace_name: sequence.workspace_name().to_owned(),
        workspace_device: sequence.workspace_target().device(),
        workspace_inode: sequence.workspace_target().inode(),
    })
}

#[cfg(target_os = "linux")]
fn parse_reference(event: &CleanupEvent) -> Result<RecoveryKeyReference, CleanupJournalError> {
    if event.opaque_reference.is_empty()
        || event.opaque_reference.len() > 8 * 1024
        || event.opaque_reference.len() % 2 != 0
        || !event
            .opaque_reference
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(CleanupJournalError::Corrupt);
    }
    RecoveryKeyReference::new(
        event.provider_kind.clone(),
        event.provider_version,
        hex::decode(&event.opaque_reference).map_err(|_| CleanupJournalError::Corrupt)?,
    )
    .map_err(|_| CleanupJournalError::Corrupt)
}

#[cfg(target_os = "linux")]
fn io_error(path: impl Into<PathBuf>, source: io::Error) -> CleanupJournalError {
    CleanupJournalError::Io {
        path: path.into(),
        source,
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;
    use std::{
        collections::BTreeMap,
        ffi::{CStr, CString, OsStr, OsString},
        fs::File,
        io::{Read, Write},
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
    struct BeforePublishLockBarrier {
        barrier: std::sync::Arc<std::sync::Barrier>,
        remaining: usize,
    }

    #[cfg(test)]
    static BEFORE_PUBLISH_LOCK_BARRIERS: std::sync::OnceLock<
        std::sync::Mutex<BTreeMap<PathBuf, BeforePublishLockBarrier>>,
    > = std::sync::OnceLock::new();

    #[cfg(test)]
    pub(super) fn install_before_publish_lock_barrier(
        directory_path: PathBuf,
        barrier: std::sync::Arc<std::sync::Barrier>,
    ) {
        // Hold both callers after opening the stable directory and immediately
        // before locking it. A pre-lock snapshot implementation therefore lets
        // both callers validate stale state before this deterministic boundary.
        let barriers =
            BEFORE_PUBLISH_LOCK_BARRIERS.get_or_init(|| std::sync::Mutex::new(BTreeMap::new()));
        let replaced = barriers.lock().unwrap().insert(
            directory_path,
            BeforePublishLockBarrier {
                barrier,
                remaining: 2,
            },
        );
        assert!(
            replaced.is_none(),
            "test publication barrier already installed"
        );
    }

    #[cfg(test)]
    fn wait_before_publish_lock(directory_path: &Path) {
        let barriers =
            BEFORE_PUBLISH_LOCK_BARRIERS.get_or_init(|| std::sync::Mutex::new(BTreeMap::new()));
        let barrier = {
            let mut barriers = barriers.lock().unwrap();
            let mut remove = false;
            let barrier = barriers.get_mut(directory_path).map(|entry| {
                entry.remaining -= 1;
                remove = entry.remaining == 0;
                std::sync::Arc::clone(&entry.barrier)
            });
            if remove {
                barriers.remove(directory_path);
            }
            barrier
        };
        if let Some(barrier) = barrier {
            barrier.wait();
        }
    }

    #[derive(Clone, Copy)]
    enum PublishExpectation {
        Initial,
        Advance { expected_previous: u8 },
    }

    pub(super) fn create(
        root: &Path,
        sequence: &RecoveryKeyCleanupSequence,
    ) -> Result<(), CleanupJournalError> {
        publish_event(root, sequence, PublishExpectation::Initial)
    }

    pub(super) fn append(
        root: &Path,
        sequence: &RecoveryKeyCleanupSequence,
    ) -> Result<(), CleanupJournalError> {
        let expected_previous = phase_index(sequence.phase())
            .checked_sub(1)
            .ok_or(CleanupJournalError::InvalidMetadata)?;
        publish_event(
            root,
            sequence,
            PublishExpectation::Advance { expected_previous },
        )
    }

    fn publish_event(
        root: &Path,
        sequence: &RecoveryKeyCleanupSequence,
        expectation: PublishExpectation,
    ) -> Result<(), CleanupJournalError> {
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        if root_identity.device != sequence.root_device()
            || root_identity.inode != sequence.root_inode()
        {
            return Err(CleanupJournalError::RecoveryConflict(root.to_path_buf()));
        }
        let directory_path = root.join(CLEANUP_DIRECTORY);
        let (directory, created) = match expectation {
            PublishExpectation::Initial => open_or_create_private_directory(
                root_dir.as_raw_fd(),
                OsStr::new(CLEANUP_DIRECTORY),
                &directory_path,
            )?,
            PublishExpectation::Advance { .. } => (
                match open_private_directory_at(
                    root_dir.as_raw_fd(),
                    OsStr::new(CLEANUP_DIRECTORY),
                    &directory_path,
                ) {
                    Ok(directory) => directory,
                    Err(CleanupJournalError::Io { source, .. })
                        if source.kind() == io::ErrorKind::NotFound =>
                    {
                        return Err(CleanupJournalError::Missing(sequence.transaction_id()));
                    }
                    Err(error) => return Err(error),
                },
                false,
            ),
        };
        let directory_identity =
            identity(&directory).map_err(|source| io_error(&directory_path, source))?;
        #[cfg(test)]
        wait_before_publish_lock(&directory_path);
        lock_exclusive(&directory, &directory_path)?;
        if created {
            root_dir
                .sync_all()
                .map_err(|source| io_error(root, source))?;
        }

        let existing = load_locked(
            root,
            &root_dir,
            root_identity,
            &directory_path,
            &directory,
            directory_identity,
        )?;
        match expectation {
            PublishExpectation::Initial => {
                if existing
                    .iter()
                    .any(|item| item.transaction_id() == sequence.transaction_id())
                {
                    return Err(CleanupJournalError::AlreadyExists(
                        sequence.transaction_id(),
                    ));
                }
                if existing.len() >= MAX_TRANSACTIONS {
                    return Err(CleanupJournalError::TooMany);
                }
            }
            PublishExpectation::Advance { expected_previous } => {
                let Some(current) = existing
                    .iter()
                    .find(|item| item.transaction_id() == sequence.transaction_id())
                else {
                    return Err(CleanupJournalError::Missing(sequence.transaction_id()));
                };
                if phase_index(current.phase()) != expected_previous
                    || current.terminal() != sequence.terminal()
                    || current.reference() != sequence.reference()
                    || current.replacement_journal_version()
                        != sequence.replacement_journal_version()
                    || current.root_device() != sequence.root_device()
                    || current.root_inode() != sequence.root_inode()
                    || current.workspace_target() != sequence.workspace_target()
                {
                    return Err(CleanupJournalError::RecoveryConflict(
                        directory_path.clone(),
                    ));
                }
            }
        }

        let event = event_for(sequence, root_identity)?;
        let mut bytes =
            serde_json::to_vec(&event).map_err(|_| CleanupJournalError::InvalidMetadata)?;
        bytes.push(b'\n');
        if bytes.len() > MAX_EVENT_BYTES {
            return Err(CleanupJournalError::InvalidMetadata);
        }
        let index = phase_index(sequence.phase());
        let stage_name = pending_name(sequence.transaction_id(), index);
        let stage_path = directory_path.join(&stage_name);
        let final_name = event_name(sequence.transaction_id(), index);
        let final_path = directory_path.join(&final_name);
        match open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path) {
            Err(CleanupJournalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound => {}
            Ok(_) => return Err(CleanupJournalError::RecoveryConflict(final_path)),
            Err(error) => return Err(error),
        }
        let mut file =
            create_private_file_at(directory.as_raw_fd(), OsStr::new(&stage_name), &stage_path)?;
        file.write_all(&bytes)
            .map_err(|source| io_error(&stage_path, source))?;
        verify_private_file(&file, &stage_path)?;
        file.sync_all()
            .map_err(|source| io_error(&stage_path, source))?;
        let file_identity = identity(&file).map_err(|source| io_error(&stage_path, source))?;
        rename_no_replace(
            directory.as_raw_fd(),
            OsStr::new(&stage_name),
            OsStr::new(&final_name),
            &final_path,
        )?;
        directory
            .sync_all()
            .map_err(|source| io_error(&directory_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let reopened =
            open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        if identity(&reopened).map_err(|source| io_error(&final_path, source))? != file_identity {
            return Err(CleanupJournalError::RecoveryConflict(final_path));
        }
        let rebound = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(CLEANUP_DIRECTORY),
            &directory_path,
        )?;
        if identity(&rebound).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CleanupJournalError::RecoveryConflict(
                directory_path.to_path_buf(),
            ));
        }
        Ok(())
    }

    pub(super) fn load(
        root: &Path,
    ) -> Result<Vec<RecoveryKeyCleanupSequence>, CleanupJournalError> {
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let directory_path = root.join(CLEANUP_DIRECTORY);
        let directory = match open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(CLEANUP_DIRECTORY),
            &directory_path,
        ) {
            Ok(directory) => directory,
            Err(CleanupJournalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                return Ok(Vec::new());
            }
            Err(error) => return Err(error),
        };
        let directory_identity =
            identity(&directory).map_err(|source| io_error(&directory_path, source))?;
        lock_shared(&directory, &directory_path)?;
        load_locked(
            root,
            &root_dir,
            root_identity,
            &directory_path,
            &directory,
            directory_identity,
        )
    }

    fn load_locked(
        root: &Path,
        root_dir: &File,
        root_identity: RootIdentity,
        directory_path: &Path,
        directory: &File,
        directory_identity: RootIdentity,
    ) -> Result<Vec<RecoveryKeyCleanupSequence>, CleanupJournalError> {
        let names_before = read_directory_names(&directory, &directory_path)?;
        let mut grouped: BTreeMap<String, Vec<(u8, CleanupEvent)>> = BTreeMap::new();

        for name in &names_before {
            let text = name.to_str().ok_or(CleanupJournalError::Corrupt)?;
            if text.ends_with(PENDING_SUFFIX) {
                return Err(CleanupJournalError::IncompleteWrite(
                    directory_path.join(name),
                ));
            }
            let stem = text
                .strip_suffix(FINAL_SUFFIX)
                .ok_or(CleanupJournalError::Corrupt)?;
            let (transaction_text, index_text) =
                stem.rsplit_once('.').ok_or(CleanupJournalError::Corrupt)?;
            let transaction_id = parse_transaction(transaction_text)?;
            let index: u8 = index_text
                .parse()
                .map_err(|_| CleanupJournalError::Corrupt)?;
            if index as usize >= MAX_EVENTS_PER_TRANSACTION || phase_from_index(index).is_none() {
                return Err(CleanupJournalError::Corrupt);
            }
            let path = directory_path.join(name);
            let mut file = open_private_file_at(directory.as_raw_fd(), name, &path)?;
            let opened_identity = identity(&file).map_err(|source| io_error(&path, source))?;
            let bytes = read_bounded(&mut file, MAX_EVENT_BYTES, &path)?;
            if !bytes.ends_with(b"\n") {
                return Err(CleanupJournalError::IncompleteWrite(path));
            }
            let event: CleanupEvent =
                serde_json::from_slice(&bytes).map_err(|_| CleanupJournalError::Corrupt)?;
            if event.version != FORMAT_VERSION
                || event.sequence != index
                || event.transaction_id != transaction_text
                || event.root_identity != root_identity
                || event.phase != phase_text(phase_from_index(index).unwrap())
                || parse_transaction(&event.transaction_id)? != transaction_id
            {
                return Err(CleanupJournalError::Corrupt);
            }
            let reopened = open_private_file_at(directory.as_raw_fd(), name, &path)?;
            if identity(&reopened).map_err(|source| io_error(&path, source))? != opened_identity {
                return Err(CleanupJournalError::RecoveryConflict(path));
            }
            reopened
                .sync_all()
                .map_err(|source| io_error(&path, source))?;
            grouped
                .entry(transaction_text.to_owned())
                .or_default()
                .push((index, event));
        }

        if grouped.len() > MAX_TRANSACTIONS {
            return Err(CleanupJournalError::TooMany);
        }
        let mut sequences = Vec::with_capacity(grouped.len());
        for (transaction_text, mut events) in grouped {
            events.sort_by_key(|(index, _)| *index);
            for (expected, (actual, _)) in events.iter().enumerate() {
                if *actual as usize != expected {
                    return Err(CleanupJournalError::Corrupt);
                }
            }
            let (_, first) = events.first().ok_or(CleanupJournalError::Corrupt)?;
            let (_, last) = events.last().ok_or(CleanupJournalError::Corrupt)?;
            for (_, event) in &events {
                if event.terminal != first.terminal
                    || event.replacement_journal_version != first.replacement_journal_version
                    || event.provider_kind != first.provider_kind
                    || event.provider_version != first.provider_version
                    || event.opaque_reference != first.opaque_reference
                    || event.workspace_name != first.workspace_name
                    || event.workspace_device != first.workspace_device
                    || event.workspace_inode != first.workspace_inode
                {
                    return Err(CleanupJournalError::Corrupt);
                }
            }
            let phase = phase_from_index(last.sequence).ok_or(CleanupJournalError::Corrupt)?;
            let transaction_id = parse_transaction(&transaction_text)?;
            let workspace_target = WorkspaceRemovalTarget::from_durable(
                DurablePhaseEvidence(()),
                transaction_id,
                first.workspace_name.clone(),
                first.workspace_device,
                first.workspace_inode,
            )?;
            sequences.push(RecoveryKeyCleanupSequence::from_durable_phase(
                DurablePhaseEvidence(()),
                phase,
                terminal_from_text(&first.terminal)?,
                transaction_id,
                parse_reference(first)?,
                first.replacement_journal_version,
                first.root_identity.device,
                first.root_identity.inode,
                workspace_target,
            )?);
        }

        directory
            .sync_all()
            .map_err(|source| io_error(&directory_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let names_after = read_directory_names(&directory, &directory_path)?;
        if names_before != names_after {
            return Err(CleanupJournalError::RecoveryConflict(
                directory_path.to_path_buf(),
            ));
        }
        verify_private_directory(&directory, &directory_path)?;
        let rebound = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(CLEANUP_DIRECTORY),
            &directory_path,
        )?;
        if identity(&rebound).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CleanupJournalError::RecoveryConflict(
                directory_path.to_path_buf(),
            ));
        }
        Ok(sequences)
    }

    fn open_root(path: &Path) -> Result<File, CleanupJournalError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|component| matches!(component, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(CleanupJournalError::InvalidRoot(path.to_path_buf()));
        }
        let anchor = if path.is_absolute() {
            Path::new("/")
        } else {
            Path::new(".")
        };
        let c_anchor =
            CString::new(anchor.as_os_str().as_bytes()).expect("Linux root anchors contain no NUL");
        let fd = unsafe {
            libc::open(
                c_anchor.as_ptr(),
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
                Component::RootDir | Component::CurDir => continue,
                Component::Normal(name) => {
                    display.push(name);
                    directory = open_directory_at(directory.as_raw_fd(), name, &display)?;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(CleanupJournalError::InvalidRoot(path.to_path_buf()));
                }
            }
        }
        Ok(directory)
    }

    fn open_or_create_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<(File, bool), CleanupJournalError> {
        let c_name = os_c_string(name)?;
        let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
        let created = result == 0;
        if result != 0 {
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::AlreadyExists {
                return Err(io_error(display, error));
            }
        }
        let directory = open_private_directory_at(parent, name, display)?;
        if created {
            set_mode(&directory, 0o700, display)?;
        }
        verify_private_directory(&directory, display)?;
        Ok((directory, created))
    }

    fn open_private_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CleanupJournalError> {
        let directory = open_directory_at(parent, name, display)?;
        verify_private_directory(&directory, display)?;
        Ok(directory)
    }

    fn open_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CleanupJournalError> {
        open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))
    }

    fn create_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CleanupJournalError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0o600,
        )
        .map_err(|source| io_error(display, source))?;
        set_mode(&file, 0o600, display)?;
        verify_private_file(&file, display)?;
        Ok(file)
    }

    fn open_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CleanupJournalError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))?;
        verify_private_file(&file, display)?;
        Ok(file)
    }

    fn open_file_at(
        parent: RawFd,
        name: &OsStr,
        flags: i32,
        mode: libc::mode_t,
    ) -> io::Result<File> {
        let c_name = CString::new(name.as_bytes())
            .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
        let fd = unsafe { libc::openat(parent, c_name.as_ptr(), flags, mode as libc::c_uint) };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(unsafe { File::from_raw_fd(fd) })
    }

    fn rename_no_replace(
        parent: RawFd,
        from: &OsStr,
        to: &OsStr,
        display: &Path,
    ) -> Result<(), CleanupJournalError> {
        let from = os_c_string(from)?;
        let to = os_c_string(to)?;
        if unsafe {
            libc::renameat2(
                parent,
                from.as_ptr(),
                parent,
                to.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn verify_private_directory(file: &File, display: &Path) -> Result<(), CleanupJournalError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_dir()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o700
        {
            return Err(CleanupJournalError::RecoveryConflict(display.to_path_buf()));
        }
        Ok(())
    }

    fn lock_exclusive(directory: &File, display: &Path) -> Result<(), CleanupJournalError> {
        if unsafe { libc::flock(directory.as_raw_fd(), libc::LOCK_EX) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn lock_shared(directory: &File, display: &Path) -> Result<(), CleanupJournalError> {
        if unsafe { libc::flock(directory.as_raw_fd(), libc::LOCK_SH) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn verify_private_file(file: &File, display: &Path) -> Result<(), CleanupJournalError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(CleanupJournalError::RecoveryConflict(display.to_path_buf()));
        }
        Ok(())
    }

    fn set_mode(
        file: &File,
        mode: libc::mode_t,
        display: &Path,
    ) -> Result<(), CleanupJournalError> {
        if unsafe { libc::fchmod(file.as_raw_fd(), mode) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn identity(file: &File) -> io::Result<RootIdentity> {
        let metadata = file.metadata()?;
        Ok(RootIdentity {
            device: metadata.dev(),
            inode: metadata.ino(),
        })
    }

    fn read_bounded(
        file: &mut File,
        limit: usize,
        display: &Path,
    ) -> Result<Vec<u8>, CleanupJournalError> {
        let mut bytes = Vec::new();
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(display, source))?;
        if bytes.len() > limit {
            return Err(CleanupJournalError::Corrupt);
        }
        Ok(bytes)
    }

    fn read_directory_names(
        directory: &File,
        display: &Path,
    ) -> Result<Vec<OsString>, CleanupJournalError> {
        let reopened = unsafe {
            libc::openat(
                directory.as_raw_fd(),
                c".".as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            )
        };
        if reopened < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let raw = unsafe { libc::fdopendir(reopened) };
        if raw.is_null() {
            let error = io::Error::last_os_error();
            unsafe {
                libc::close(reopened);
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
            if bytes == b"." || bytes == b".." {
                continue;
            }
            if names.len() == MAX_TRANSACTIONS * MAX_EVENTS_PER_TRANSACTION {
                return Err(CleanupJournalError::TooMany);
            }
            names.push(OsString::from_vec(bytes.to_vec()));
        }
        names.sort_by(|left, right| left.as_bytes().cmp(right.as_bytes()));
        Ok(names)
    }

    fn os_c_string(name: &OsStr) -> Result<CString, CleanupJournalError> {
        CString::new(name.as_bytes()).map_err(|_| CleanupJournalError::InvalidMetadata)
    }

    struct DirectoryStream(*mut libc::DIR);

    impl Drop for DirectoryStream {
        fn drop(&mut self) {
            unsafe {
                libc::closedir(self.0);
            }
        }
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;

    pub(super) fn create(
        _root: &Path,
        _sequence: &RecoveryKeyCleanupSequence,
    ) -> Result<(), CleanupJournalError> {
        Err(CleanupJournalError::UnsupportedPlatform)
    }

    pub(super) fn append(
        _root: &Path,
        _sequence: &RecoveryKeyCleanupSequence,
    ) -> Result<(), CleanupJournalError> {
        Err(CleanupJournalError::UnsupportedPlatform)
    }

    pub(super) fn load(
        _root: &Path,
    ) -> Result<Vec<RecoveryKeyCleanupSequence>, CleanupJournalError> {
        Err(CleanupJournalError::UnsupportedPlatform)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(target_os = "linux")]
    use crate::live::completion_tombstone::{
        persist_completion_tombstone, remove_completion_tombstone,
    };
    #[cfg(target_os = "linux")]
    use crate::live::recovery_key::{
        RecoveryKeyCreateOutcome, RecoveryKeyDeleteOutcome, RecoveryKeyOpenOutcome,
    };
    #[cfg(target_os = "linux")]
    use std::sync::{Arc, Barrier};
    #[cfg(target_os = "linux")]
    use std::thread;
    #[cfg(target_os = "linux")]
    use std::{
        fs,
        os::unix::fs::{MetadataExt, PermissionsExt},
    };

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }

    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-cleanup-reference".to_vec()).unwrap()
    }

    #[cfg(target_os = "linux")]
    fn workspace_name(suffix: u8) -> String {
        format!("tx-{}", transaction_hex(transaction(suffix)))
    }

    #[cfg(target_os = "linux")]
    fn empty_workspace(root: &Path, suffix: u8) -> PathBuf {
        let lock = root.join(".cpa-cloud-employee-config.lock");
        if !lock.exists() {
            fs::write(&lock, b"").unwrap();
            fs::set_permissions(&lock, fs::Permissions::from_mode(0o600)).unwrap();
        }
        let transaction_root = root.join(".cpa-cloud-employee-config-transactions");
        if !transaction_root.exists() {
            fs::create_dir(&transaction_root).unwrap();
            fs::set_permissions(&transaction_root, fs::Permissions::from_mode(0o700)).unwrap();
        }
        let workspace = transaction_root.join(workspace_name(suffix));
        fs::create_dir(&workspace).unwrap();
        fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700)).unwrap();
        workspace
    }

    #[cfg(target_os = "linux")]
    fn workspace_target(workspace: &Path) -> WorkspaceRemovalTarget {
        let metadata = fs::metadata(workspace).unwrap();
        let workspace_name = workspace.file_name().unwrap().to_str().unwrap();
        let transaction_id =
            parse_transaction(workspace_name.strip_prefix("tx-").unwrap()).unwrap();
        WorkspaceRemovalTarget::from_durable(
            DurablePhaseEvidence(()),
            transaction_id,
            workspace_name.to_owned(),
            metadata.dev(),
            metadata.ino(),
        )
        .unwrap()
    }

    #[cfg(target_os = "linux")]
    struct DeletedProvider {
        delete_calls: usize,
    }

    #[cfg(target_os = "linux")]
    impl RecoveryKeyProvider for DeletedProvider {
        fn create(
            &mut self,
            _transaction_id: ReplacementTransactionId,
        ) -> RecoveryKeyCreateOutcome {
            panic!("cleanup journal must not create a key")
        }

        fn open(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            panic!("cleanup journal must not open a key")
        }

        fn delete(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            self.delete_calls += 1;
            RecoveryKeyDeleteOutcome::Deleted
        }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_reconstructs_every_durable_cleanup_phase_and_finishes_from_proof() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x61);
        let reference = reference();
        let tombstone_receipt = persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            &reference,
        )
        .unwrap();
        let workspace = empty_workspace(&root_path, 0x61);
        let workspace_target = workspace_target(&workspace);
        let workspace_device = workspace_target.device();
        let workspace_inode = workspace_target.inode();

        let mut journal = create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            reference.clone(),
            workspace_target,
        )
        .unwrap();
        assert_eq!(journal.phase(), RecoveryKeyCleanupPhase::TerminalRecorded);
        journal
            .record_completion_tombstone_durable(&tombstone_receipt)
            .unwrap();
        drop(journal);

        let mut journal = load_durable_cleanup_journals(&root_path).unwrap().remove(0);
        assert_eq!(
            journal.phase(),
            RecoveryKeyCleanupPhase::CompletionTombstoneDurable
        );
        journal.remove_workspace_durable(&workspace).unwrap();
        assert!(!workspace.exists());
        drop(journal);

        let mut journal = load_durable_cleanup_journals(&root_path).unwrap().remove(0);
        assert_eq!(
            journal.phase(),
            RecoveryKeyCleanupPhase::WorkspaceRemovalDurable
        );
        fs::create_dir(&workspace).unwrap();
        fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700)).unwrap();
        let mut provider = DeletedProvider { delete_calls: 0 };
        assert!(matches!(
            journal.delete_key(&mut provider),
            Err(CleanupJournalError::WorkspaceRemoval(
                WorkspaceRemovalError::RecoveryConflict(_)
            ))
        ));
        assert_eq!(provider.delete_calls, 0);
        fs::remove_dir(&workspace).unwrap();
        journal.delete_key(&mut provider).unwrap();
        assert_eq!(provider.delete_calls, 1);
        drop(journal);

        let mut restarted = load_durable_cleanup_journals(&root_path).unwrap().remove(0);
        assert_eq!(
            restarted.phase(),
            RecoveryKeyCleanupPhase::KeyDeletionConfirmed
        );
        let authorization = restarted.authorize_completion_tombstone_removal().unwrap();
        let receipt = remove_completion_tombstone(&root_path, &authorization).unwrap();
        restarted
            .record_completion_tombstone_removal_durable(receipt)
            .unwrap();
        drop(restarted);

        let loaded = load_durable_cleanup_journals(&root_path).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].phase(), RecoveryKeyCleanupPhase::Complete);
        assert_eq!(loaded[0].transaction_id(), transaction_id);
        assert_eq!(loaded[0].reference(), &reference);
        let retirement = loaded[0]
            .verify_artifact_retirement_preconditions()
            .unwrap();
        assert_eq!(retirement.terminal(), ReplacementPhase::Complete);
        assert_eq!(retirement.transaction_id(), transaction_id);
        assert_eq!(retirement.reference(), &reference);
        assert_eq!(retirement.workspace_name(), workspace_name(0x61));
        assert_eq!(retirement.workspace_device(), workspace_device);
        assert_eq!(retirement.workspace_inode(), workspace_inode);
        let rendered = format!("{retirement:?}");
        assert!(!rendered.contains("opaque-cleanup-reference"));
        assert!(!format!("{:?}", loaded[0]).contains("opaque-cleanup-reference"));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn pending_or_noncontiguous_events_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x62);
        let workspace = empty_workspace(&root_path, 0x62);
        let mut journal = create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            reference(),
            workspace_target(&workspace),
        )
        .unwrap();
        let tombstone_receipt = persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            journal.reference(),
        )
        .unwrap();
        let directory = root_path.join(CLEANUP_DIRECTORY);
        fs::write(directory.join(pending_name(transaction_id, 1)), b"partial").unwrap();
        assert!(matches!(
            journal.record_completion_tombstone_durable(&tombstone_receipt),
            Err(CleanupJournalError::IncompleteWrite(_))
        ));
        assert_eq!(journal.phase(), RecoveryKeyCleanupPhase::TerminalRecorded);
        assert!(matches!(
            load_durable_cleanup_journals(&root_path),
            Err(CleanupJournalError::IncompleteWrite(_))
        ));
        fs::remove_file(directory.join(pending_name(transaction_id, 1))).unwrap();
        journal
            .record_completion_tombstone_durable(&tombstone_receipt)
            .unwrap();
        fs::remove_file(directory.join(event_name(transaction_id, 0))).unwrap();
        assert!(matches!(
            load_durable_cleanup_journals(&root_path),
            Err(CleanupJournalError::Corrupt)
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn duplicate_transaction_and_invalid_transition_are_refused() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x63);
        let workspace = empty_workspace(&root_path, 0x63);
        let mut journal = create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::RolledBack,
            transaction_id,
            reference(),
            workspace_target(&workspace),
        )
        .unwrap();
        assert!(matches!(
            create_durable_cleanup_journal_for_test(
                &root_path,
                ReplacementPhase::RolledBack,
                transaction_id,
                reference(),
                workspace_target(&workspace),
            ),
            Err(CleanupJournalError::AlreadyExists(_))
        ));
        assert!(matches!(
            journal.remove_workspace_durable(Path::new("unused")),
            Err(CleanupJournalError::Cleanup(_))
        ));
        assert_eq!(journal.phase(), RecoveryKeyCleanupPhase::TerminalRecorded);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn production_reload_binding_refuses_legacy_workspace_and_unknown_version() {
        let transaction_id = transaction(0x64);
        assert!(matches!(
            WorkspaceRemovalTarget::from_durable(
                DurablePhaseEvidence(()),
                transaction_id,
                workspace_name(0x64),
                1,
                1,
            ),
            Err(RecoveryKeyCleanupError::InvalidWorkspace)
        ));

        let canonical = WorkspaceRemovalTarget::from_durable(
            DurablePhaseEvidence(()),
            transaction_id,
            format!("tx-{}", transaction_hex(transaction_id)),
            1,
            1,
        )
        .unwrap();
        assert!(matches!(
            RecoveryKeyCleanupSequence::from_durable_phase(
                DurablePhaseEvidence(()),
                RecoveryKeyCleanupPhase::TerminalRecorded,
                ReplacementPhase::Complete,
                transaction_id,
                reference(),
                crate::live::linux_replacement::REPLACEMENT_JOURNAL_FORMAT_VERSION + 1,
                1,
                1,
                canonical,
            ),
            Err(RecoveryKeyCleanupError::InvalidTerminal)
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_refuses_an_unsupported_replacement_journal_version() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x65);
        let workspace = empty_workspace(&root_path, 0x65);
        create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            reference(),
            workspace_target(&workspace),
        )
        .unwrap();

        let event_path = root_path
            .join(CLEANUP_DIRECTORY)
            .join(event_name(transaction_id, 0));
        let mut event: CleanupEvent =
            serde_json::from_slice(&fs::read(&event_path).unwrap()).unwrap();
        event.replacement_journal_version += 1;
        fs::write(&event_path, serde_json::to_vec(&event).unwrap()).unwrap();

        assert!(matches!(
            load_durable_cleanup_journals(&root_path),
            Err(CleanupJournalError::Cleanup(
                RecoveryKeyCleanupError::InvalidTerminal
            ))
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_refuses_a_legacy_workspace_shape_not_issued_by_the_handoff() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x66);
        let workspace = empty_workspace(&root_path, 0x66);
        create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            reference(),
            workspace_target(&workspace),
        )
        .unwrap();

        let event_path = root_path
            .join(CLEANUP_DIRECTORY)
            .join(event_name(transaction_id, 0));
        let mut event: CleanupEvent =
            serde_json::from_slice(&fs::read(&event_path).unwrap()).unwrap();
        event.workspace_name = "tx-1-deadbeef-0".to_owned();
        fs::write(&event_path, serde_json::to_vec(&event).unwrap()).unwrap();

        assert!(matches!(
            load_durable_cleanup_journals(&root_path),
            Err(CleanupJournalError::Cleanup(
                RecoveryKeyCleanupError::InvalidWorkspace
            ))
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn concurrent_sixty_fourth_create_cannot_overfill_the_namespace() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        for byte in 1..MAX_TRANSACTIONS as u8 {
            let workspace = empty_workspace(&root_path, byte);
            create_durable_cleanup_journal_for_test(
                &root_path,
                ReplacementPhase::Complete,
                transaction(byte),
                reference(),
                workspace_target(&workspace),
            )
            .unwrap();
        }

        let barrier = Arc::new(Barrier::new(2));
        platform::install_before_publish_lock_barrier(
            root_path.join(CLEANUP_DIRECTORY),
            Arc::clone(&barrier),
        );
        let mut workers = Vec::new();
        for byte in [0x70, 0x71] {
            let root_path = root_path.clone();
            workers.push(thread::spawn(move || {
                let workspace = empty_workspace(&root_path, byte);
                create_durable_cleanup_journal_for_test(
                    &root_path,
                    ReplacementPhase::Complete,
                    transaction(byte),
                    reference(),
                    workspace_target(&workspace),
                )
            }));
        }
        let results: Vec<_> = workers
            .into_iter()
            .map(|worker| worker.join().unwrap())
            .collect();
        assert_eq!(results.iter().filter(|result| result.is_ok()).count(), 1);
        assert_eq!(
            results
                .iter()
                .filter(|result| matches!(result, Err(CleanupJournalError::TooMany)))
                .count(),
            1
        );
        assert_eq!(
            load_durable_cleanup_journals(&root_path).unwrap().len(),
            MAX_TRANSACTIONS
        );
        let names = fs::read_dir(root_path.join(CLEANUP_DIRECTORY))
            .unwrap()
            .map(|entry| entry.unwrap().file_name())
            .collect::<Vec<_>>();
        assert_eq!(names.len(), MAX_TRANSACTIONS);
        assert!(names
            .iter()
            .all(|name| !name.to_string_lossy().ends_with(PENDING_SUFFIX)));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn concurrent_append_validates_the_prefix_under_the_publication_lock() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x72);
        let workspace = empty_workspace(&root_path, 0x72);
        create_durable_cleanup_journal_for_test(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            reference(),
            workspace_target(&workspace),
        )
        .unwrap();
        let tombstone_receipt = persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            &reference(),
        )
        .unwrap();
        let first = load_durable_cleanup_journals(&root_path).unwrap().remove(0);
        let second = load_durable_cleanup_journals(&root_path).unwrap().remove(0);
        let barrier = Arc::new(Barrier::new(2));
        platform::install_before_publish_lock_barrier(
            root_path.join(CLEANUP_DIRECTORY),
            Arc::clone(&barrier),
        );
        let mut workers = Vec::new();
        for mut journal in [first, second] {
            let tombstone_receipt = tombstone_receipt.clone();
            workers.push(thread::spawn(move || {
                journal.record_completion_tombstone_durable(&tombstone_receipt)
            }));
        }
        let results: Vec<_> = workers
            .into_iter()
            .map(|worker| worker.join().unwrap())
            .collect();
        assert_eq!(results.iter().filter(|result| result.is_ok()).count(), 1);
        let expected_conflict_path = root_path.join(CLEANUP_DIRECTORY);
        assert_eq!(
            results
                .iter()
                .filter(|result| {
                    matches!(
                        result,
                        Err(CleanupJournalError::RecoveryConflict(path))
                            if path == &expected_conflict_path
                    )
                })
                .count(),
            1
        );

        let loaded = load_durable_cleanup_journals(&root_path).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(
            loaded[0].phase(),
            RecoveryKeyCleanupPhase::CompletionTombstoneDurable
        );
        let names = fs::read_dir(root_path.join(CLEANUP_DIRECTORY))
            .unwrap()
            .map(|entry| entry.unwrap().file_name())
            .collect::<Vec<_>>();
        assert_eq!(names.len(), 2);
        assert!(names
            .iter()
            .all(|name| !name.to_string_lossy().ends_with(PENDING_SUFFIX)));
    }

    #[cfg(not(target_os = "linux"))]
    #[test]
    fn unsupported_platforms_fail_before_creating_material() {
        let root = tempfile::tempdir().unwrap();
        assert!(matches!(
            create_durable_cleanup_journal(
                root.path(),
                DurableTerminalHandoff::for_test(
                    crate::live::linux_replacement::ReplacementOutcome::Complete,
                    transaction(0x64),
                    reference(),
                    1,
                    1,
                    1,
                    1,
                ),
            ),
            Err(CleanupJournalError::UnsupportedPlatform)
        ));
        assert!(!root.path().join(CLEANUP_DIRECTORY).exists());
    }
}
