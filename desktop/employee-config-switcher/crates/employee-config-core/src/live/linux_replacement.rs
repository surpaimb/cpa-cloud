//! Linux-only, single-target existing-file replacement for synthetic roots.
//!
//! Production construction of [`SyntheticRootApproval`] is deliberately
//! unavailable. The Tauri command and real client paths remain fail-closed.
//! This module exists to exercise descriptor-relative compare-and-swap,
//! durable commit/rollback decisions and restart recovery before any live
//! integration is considered.

use super::{
    backup::{
        load_encrypted_backup_for_replacement_with_provider, load_encrypted_backup_with_provider,
        prepare_encrypted_backup, BackupError, ReplacementTargetExpectation,
    },
    recovery_key::{
        RecoveryKeyCreateOutcome, RecoveryKeyProvider, RecoveryKeyReference,
        ReplacementTransactionId,
    },
};
use serde::{Deserialize, Serialize};
use std::{
    io,
    path::{Component, Path, PathBuf},
};

const REPLACEMENT_DIRECTORY: &str = ".cpa-cloud-employee-config-replacements";
const TARGET_LOCK_FILE: &str = "single-target.lock";
const JOURNAL_FILE: &str = "journal.jsonl";
const PENDING_EVENT_FILE: &str = "journal.pending";
const PENDING_EVENT_CAPACITY: usize = 16 * 1024;
const PENDING_EVENT_MAGIC: &[u8; 8] = b"CPAPND01";
const PENDING_EVENT_HEADER: usize = 8 + 4 + 32;
const PENDING_SLOT_PREFIX: &str = ".journal-pending-";
const PENDING_SLOTS_READY_FILE: &str = ".journal-pending-ready";
const DESIRED_STAGE: &str = "desired.stage";
const ROLLBACK_STAGE: &str = "rollback.stage";
const ROLLBACK_STAGE_PENDING: &str = "rollback.stage.pending";
const FORMAT_VERSION: u32 = 1;
const MAX_TARGET_BYTES: usize = 4 * 1024 * 1024;
const MAX_JOURNAL_BYTES: usize = 128 * 1024;
const MAX_EVENTS: usize = 24;

#[derive(Debug, Clone)]
pub(crate) struct SyntheticRootApproval {
    root: PathBuf,
}

#[cfg(test)]
impl SyntheticRootApproval {
    fn for_test(root: &Path) -> Self {
        Self {
            root: root.to_path_buf(),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SyntheticValidation {
    Accept,
    Reject,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ReplacementOutcome {
    Complete,
    RolledBack,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct ReplacementReceipt {
    pub transaction_id: ReplacementTransactionId,
    pub outcome: ReplacementOutcome,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum FaultPoint {
    AfterKeyCreateIntent,
    AfterKeyCreate,
    AfterBegin,
    AfterBackup,
    AfterStage,
    RollbackReservationFailure,
    PendingSlotReservationFailure,
    JournalReservationFailure,
    AfterCommitPrepared,
    AfterExchangeBeforeVerify,
    ExternalEditBeforeExchange,
    ExternalEditThenCrashAfterExchange,
    ExternalSymlinkThenCrashAfterExchange,
    ExternalPublicModeThenCrashAfterExchange,
    ExternalHardlinkThenCrashAfterExchange,
    ExternalSymlinkThenCrashAfterRollbackExchange,
    AfterPublish,
    AfterPublished,
    AfterVerified,
    AfterRollbackPrepared,
    AfterRollbackStageIntent,
    AfterRollbackStageWrite,
    AfterRollbackStagePublish,
    AfterRollbackStage,
    AfterRestore,
    AfterRestored,
    AfterTerminal,
}

#[derive(Debug, thiserror::Error)]
pub(crate) enum SyntheticReplacementError {
    #[error("existing-file replacement is not supported on this platform")]
    UnsupportedPlatform,
    #[error("synthetic replacement target must be one bounded UTF-8 leaf")]
    InvalidTarget,
    #[error("synthetic replacement root is invalid")]
    InvalidRoot,
    #[error("synthetic replacement target is too large")]
    TooLarge,
    #[error("synthetic replacement target or transaction state changed")]
    RecoveryConflict,
    #[error("synthetic replacement journal is corrupt")]
    CorruptJournal,
    #[error("synthetic replacement stopped before its durable commit decision")]
    Uncommitted,
    #[error("recovery-key provider cannot create a key")]
    KeyCreateBlocked,
    #[error("recovery-key provider does not guarantee transaction-idempotent creation")]
    KeyCreateNotIdempotent,
    #[error("encrypted rollback material failed: {0}")]
    Backup(#[from] BackupError),
    #[error("filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[error("injected interruption after {0:?}")]
    Injected(FaultPoint),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct FileIdentity {
    volume: u64,
    file: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct TargetState {
    identity: FileIdentity,
    digest: String,
    len: usize,
    mode: u32,
    uid: u32,
    gid: u32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "event", rename_all = "snake_case", deny_unknown_fields)]
enum JournalEvent {
    KeyCreateIntent {
        version: u32,
        transaction_id: String,
        root_identity: FileIdentity,
        target: String,
        original: TargetState,
        desired_digest: String,
        desired_len: usize,
    },
    KeyCreated {
        provider_kind: String,
        provider_version: u32,
        provider_reference: String,
    },
    BackedUp {
        workspace: String,
    },
    Staged {
        identity: FileIdentity,
    },
    CommitPrepared,
    Published,
    Verified,
    RollbackPrepared,
    RollbackStageIntent,
    RollbackStaged {
        identity: FileIdentity,
    },
    Restored,
    Complete,
    RolledBack,
}

#[derive(Debug, Clone)]
struct JournalState {
    transaction_id: ReplacementTransactionId,
    root_identity: FileIdentity,
    target: String,
    original: TargetState,
    desired_digest: String,
    desired_len: usize,
    reference: Option<RecoveryKeyReference>,
    backup_workspace: Option<String>,
    desired_identity: Option<FileIdentity>,
    commit_prepared: bool,
    published: bool,
    verified: bool,
    rollback_prepared: bool,
    rollback_stage_intent: bool,
    rollback_identity: Option<FileIdentity>,
    restored: bool,
    terminal: Option<ReplacementOutcome>,
}

pub(crate) fn replace_existing_synthetic<P: RecoveryKeyProvider + ?Sized>(
    approval: &SyntheticRootApproval,
    target: &Path,
    desired: &[u8],
    transaction_id: ReplacementTransactionId,
    provider: &mut P,
    validation: SyntheticValidation,
) -> Result<ReplacementReceipt, SyntheticReplacementError> {
    platform::replace(
        approval,
        target,
        desired,
        transaction_id,
        provider,
        validation,
        None,
    )
}

pub(crate) fn recover_existing_synthetic<P: RecoveryKeyProvider + ?Sized>(
    approval: &SyntheticRootApproval,
    transaction_id: ReplacementTransactionId,
    provider: &mut P,
    validation: SyntheticValidation,
) -> Result<ReplacementReceipt, SyntheticReplacementError> {
    platform::recover(approval, transaction_id, provider, validation, None)
}

fn validate_target(target: &Path) -> Result<&str, SyntheticReplacementError> {
    let mut components = target.components();
    let Some(Component::Normal(name)) = components.next() else {
        return Err(SyntheticReplacementError::InvalidTarget);
    };
    if components.next().is_some() || name.is_empty() {
        return Err(SyntheticReplacementError::InvalidTarget);
    }
    let text = name
        .to_str()
        .ok_or(SyntheticReplacementError::InvalidTarget)?;
    if text.len() > 4096 || text.contains('\0') {
        return Err(SyntheticReplacementError::InvalidTarget);
    }
    Ok(text)
}

fn transaction_text(transaction_id: ReplacementTransactionId) -> String {
    hex::encode(transaction_id.as_bytes())
}

fn parse_transaction_id(
    value: &str,
) -> Result<ReplacementTransactionId, SyntheticReplacementError> {
    let bytes = hex::decode(value).map_err(|_| SyntheticReplacementError::CorruptJournal)?;
    let bytes: [u8; 16] = bytes
        .try_into()
        .map_err(|_| SyntheticReplacementError::CorruptJournal)?;
    ReplacementTransactionId::from_bytes(bytes)
        .map_err(|_| SyntheticReplacementError::CorruptJournal)
}

fn io_error(path: impl Into<PathBuf>, source: io::Error) -> SyntheticReplacementError {
    SyntheticReplacementError::Io {
        path: path.into(),
        source,
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;
    use sha2::{Digest, Sha256};
    use std::{
        ffi::{CString, OsStr},
        fs::File,
        io::{Read, Seek, SeekFrom, Write},
        os::{
            fd::{AsRawFd, FromRawFd, RawFd},
            unix::{ffi::OsStrExt, fs::MetadataExt},
        },
    };
    use zeroize::Zeroizing;

    struct OpenTransaction {
        root_path: PathBuf,
        workspace_path: PathBuf,
        root: File,
        transaction_directory: File,
        _target_lock: File,
        workspace: File,
        journal: File,
        state: JournalState,
    }

    #[derive(Debug, Clone, Copy, PartialEq, Eq)]
    struct RawEntryState {
        device: u64,
        inode: u64,
        mode: u32,
        links: u64,
        uid: u32,
        gid: u32,
    }

    pub(super) fn replace<P: RecoveryKeyProvider + ?Sized>(
        approval: &SyntheticRootApproval,
        target: &Path,
        desired: &[u8],
        transaction_id: ReplacementTransactionId,
        provider: &mut P,
        validation: SyntheticValidation,
        fault: Option<FaultPoint>,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        if !provider.create_is_transaction_idempotent() {
            return Err(SyntheticReplacementError::KeyCreateNotIdempotent);
        }
        let target_name = validate_target(target)?.to_owned();
        if desired.len() > MAX_TARGET_BYTES {
            return Err(SyntheticReplacementError::TooLarge);
        }
        let root = open_root(&approval.root)?;
        verify_private_directory(&root, &approval.root)?;
        let transaction_directory = open_or_create_private_directory(
            root.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            approval.root.join(REPLACEMENT_DIRECTORY),
        )?;
        root.sync_all()
            .map_err(|source| io_error(&approval.root, source))?;
        let target_lock = open_or_create_private_file(
            transaction_directory.as_raw_fd(),
            OsStr::new(TARGET_LOCK_FILE),
            approval
                .root
                .join(REPLACEMENT_DIRECTORY)
                .join(TARGET_LOCK_FILE),
        )?;
        transaction_directory
            .sync_all()
            .map_err(|source| io_error(approval.root.join(REPLACEMENT_DIRECTORY), source))?;
        lock_exclusive(
            &target_lock,
            &approval
                .root
                .join(REPLACEMENT_DIRECTORY)
                .join(TARGET_LOCK_FILE),
        )?;
        let original = read_target_state(&root, &target_name, &approval.root.join(target))?;
        let workspace_name = format!("tx-{}", transaction_text(transaction_id));
        let workspace_path = approval
            .root
            .join(REPLACEMENT_DIRECTORY)
            .join(&workspace_name);
        create_private_directory(
            transaction_directory.as_raw_fd(),
            OsStr::new(&workspace_name),
            &workspace_path,
        )?;
        let workspace = open_private_directory(
            transaction_directory.as_raw_fd(),
            OsStr::new(&workspace_name),
            &workspace_path,
        )?;
        transaction_directory
            .sync_all()
            .map_err(|source| io_error(approval.root.join(REPLACEMENT_DIRECTORY), source))?;
        let mut journal = create_private_file(
            workspace.as_raw_fd(),
            OsStr::new(JOURNAL_FILE),
            workspace_path.join(JOURNAL_FILE),
        )?;
        lock_exclusive(&journal, &workspace_path.join(JOURNAL_FILE))?;
        let intent = JournalEvent::KeyCreateIntent {
            version: FORMAT_VERSION,
            transaction_id: transaction_text(transaction_id),
            root_identity: identity(&root, &approval.root)?,
            target: target_name.clone(),
            original: original.clone(),
            desired_digest: digest(desired),
            desired_len: desired.len(),
        };
        append_event(&mut journal, &workspace, &workspace_path, &intent)?;
        inject(fault, FaultPoint::AfterKeyCreateIntent)?;
        let created = match provider.create(transaction_id) {
            RecoveryKeyCreateOutcome::Created(created) => created,
            RecoveryKeyCreateOutcome::Locked | RecoveryKeyCreateOutcome::Unavailable => {
                return Err(SyntheticReplacementError::KeyCreateBlocked);
            }
        };
        inject(fault, FaultPoint::AfterKeyCreate)?;
        let key_created = JournalEvent::KeyCreated {
            provider_kind: created.reference.provider_kind().to_owned(),
            provider_version: created.reference.provider_version(),
            provider_reference: hex::encode(created.reference.opaque_reference()),
        };
        append_event(&mut journal, &workspace, &workspace_path, &key_created)?;
        inject(fault, FaultPoint::AfterBegin)?;

        let backup = prepare_encrypted_backup(
            &approval.root,
            target,
            transaction_id,
            &created.reference,
            &created.key,
        )?;
        require_target_state(&root, &target_name, &approval.root.join(target), &original)?;
        append_event(
            &mut journal,
            &workspace,
            &workspace_path,
            &JournalEvent::BackedUp {
                workspace: backup.workspace.clone(),
            },
        )?;
        inject(fault, FaultPoint::AfterBackup)?;

        let desired_state = create_stage(
            &workspace,
            DESIRED_STAGE,
            desired,
            &original,
            &workspace_path.join(DESIRED_STAGE),
        )?;
        append_event(
            &mut journal,
            &workspace,
            &workspace_path,
            &JournalEvent::Staged {
                identity: desired_state.identity,
            },
        )?;
        inject(fault, FaultPoint::AfterStage)?;

        let reloaded = load_encrypted_backup_with_provider(
            &approval.root,
            &backup.workspace,
            transaction_id,
            &created.reference,
            provider,
        )?;
        if reloaded.as_slice()
            != read_target_bytes(&root, &target_name, &original, &approval.root.join(target))?
                .as_slice()
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        require_target_state(&root, &target_name, &approval.root.join(target), &original)?;
        let mut staged = open_private_or_original_mode_file(
            workspace.as_raw_fd(),
            OsStr::new(DESIRED_STAGE),
            &workspace_path.join(DESIRED_STAGE),
        )?;
        if state_from_open_file(&mut staged, &workspace_path.join(DESIRED_STAGE))? != desired_state
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        reserve_rollback_stage(
            &workspace,
            &original,
            &workspace_path.join(ROLLBACK_STAGE_PENDING),
            fault,
        )?;
        reserve_pending_event_slots(&workspace, &workspace_path, fault)?;
        reserve_journal_space(&journal, &workspace_path.join(JOURNAL_FILE), fault)?;
        require_journal_budget(&journal, 16 * 1024)?;
        append_event(
            &mut journal,
            &workspace,
            &workspace_path,
            &JournalEvent::CommitPrepared,
        )?;
        inject(fault, FaultPoint::AfterCommitPrepared)?;
        let state = read_journal(&mut journal)?;
        let mut transaction = OpenTransaction {
            root_path: approval.root.clone(),
            workspace_path,
            root,
            transaction_directory,
            _target_lock: target_lock,
            workspace,
            journal,
            state,
        };
        drive(&mut transaction, provider, validation, fault)
    }

    pub(super) fn recover<P: RecoveryKeyProvider + ?Sized>(
        approval: &SyntheticRootApproval,
        transaction_id: ReplacementTransactionId,
        provider: &mut P,
        validation: SyntheticValidation,
        fault: Option<FaultPoint>,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        if !provider.create_is_transaction_idempotent() {
            return Err(SyntheticReplacementError::KeyCreateNotIdempotent);
        }
        let mut transaction = open_transaction(approval, transaction_id)?;
        drive(&mut transaction, provider, validation, fault)
    }

    fn drive<P: RecoveryKeyProvider + ?Sized>(
        transaction: &mut OpenTransaction,
        provider: &mut P,
        validation: SyntheticValidation,
        fault: Option<FaultPoint>,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        if transaction.state.reference.is_none() {
            if !provider.create_is_transaction_idempotent() {
                return Err(SyntheticReplacementError::KeyCreateNotIdempotent);
            }
            let created = match provider.create(transaction.state.transaction_id) {
                RecoveryKeyCreateOutcome::Created(created) => created,
                RecoveryKeyCreateOutcome::Locked | RecoveryKeyCreateOutcome::Unavailable => {
                    return Err(SyntheticReplacementError::KeyCreateBlocked);
                }
            };
            let event = JournalEvent::KeyCreated {
                provider_kind: created.reference.provider_kind().to_owned(),
                provider_version: created.reference.provider_version(),
                provider_reference: hex::encode(created.reference.opaque_reference()),
            };
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &event,
            )?;
            transaction.state.reference = Some(created.reference);
            return Err(SyntheticReplacementError::Uncommitted);
        }
        if let Some(outcome) = transaction.state.terminal {
            verify_terminal_target(transaction, outcome)?;
            return Ok(ReplacementReceipt {
                transaction_id: transaction.state.transaction_id,
                outcome,
            });
        }
        if !transaction.state.commit_prepared {
            return Err(SyntheticReplacementError::Uncommitted);
        }
        let desired_identity = transaction
            .state
            .desired_identity
            .ok_or(SyntheticReplacementError::CorruptJournal)?;
        let desired_state = TargetState {
            identity: desired_identity,
            digest: transaction.state.desired_digest.clone(),
            len: transaction.state.desired_len,
            mode: transaction.state.original.mode,
            uid: transaction.state.original.uid,
            gid: transaction.state.original.gid,
        };

        if !transaction.state.published {
            let current = read_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
            )?;
            if current == transaction.state.original {
                require_stage_state(transaction, DESIRED_STAGE, &desired_state)?;
                revalidate_namespace(transaction)?;
                exchange_stage(
                    transaction,
                    DESIRED_STAGE,
                    &transaction.state.original,
                    &desired_state,
                    fault,
                )?;
                inject(fault, FaultPoint::AfterPublish)?;
            } else if current == desired_state {
                match read_stage_state(transaction, DESIRED_STAGE) {
                    Ok(displaced) if displaced == transaction.state.original => {}
                    Ok(displaced) => {
                        undo_unverified_exchange(
                            transaction,
                            DESIRED_STAGE,
                            &desired_state,
                            &displaced,
                        )?;
                        return Err(SyntheticReplacementError::RecoveryConflict);
                    }
                    Err(_) => {
                        let displaced = raw_entry_state(
                            transaction.workspace.as_raw_fd(),
                            OsStr::new(DESIRED_STAGE),
                            &transaction.workspace_path.join(DESIRED_STAGE),
                        )?;
                        undo_unverified_exchange_raw(
                            transaction,
                            DESIRED_STAGE,
                            &desired_state,
                            displaced,
                        )?;
                        return Err(SyntheticReplacementError::RecoveryConflict);
                    }
                }
            } else {
                return Err(SyntheticReplacementError::RecoveryConflict);
            }
            require_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
                &desired_state,
            )?;
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &JournalEvent::Published,
            )?;
            transaction.state.published = true;
            inject(fault, FaultPoint::AfterPublished)?;
        }
        cleanup_swapped_stage(transaction, DESIRED_STAGE, &transaction.state.original)?;

        if !transaction.state.rollback_prepared && !transaction.state.verified {
            require_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
                &desired_state,
            )?;
            match validation {
                SyntheticValidation::Accept => {
                    append_event(
                        &mut transaction.journal,
                        &transaction.workspace,
                        &transaction.workspace_path,
                        &JournalEvent::Verified,
                    )?;
                    transaction.state.verified = true;
                    inject(fault, FaultPoint::AfterVerified)?;
                }
                SyntheticValidation::Reject => {
                    let backup_workspace = transaction
                        .state
                        .backup_workspace
                        .as_deref()
                        .ok_or(SyntheticReplacementError::CorruptJournal)?;
                    let original = load_encrypted_backup_for_replacement_with_provider(
                        &transaction.root_path,
                        backup_workspace,
                        transaction.state.transaction_id,
                        transaction
                            .state
                            .reference
                            .as_ref()
                            .ok_or(SyntheticReplacementError::CorruptJournal)?,
                        &ReplacementTargetExpectation {
                            volume: desired_identity.volume,
                            file: desired_identity.file,
                            digest: transaction.state.desired_digest.clone(),
                            len: transaction.state.desired_len,
                        },
                        provider,
                    )?;
                    if digest(&original) != transaction.state.original.digest
                        || original.len() != transaction.state.original.len
                    {
                        return Err(SyntheticReplacementError::RecoveryConflict);
                    }
                    require_journal_budget(&transaction.journal, 8 * 1024)?;
                    append_event(
                        &mut transaction.journal,
                        &transaction.workspace,
                        &transaction.workspace_path,
                        &JournalEvent::RollbackPrepared,
                    )?;
                    transaction.state.rollback_prepared = true;
                    inject(fault, FaultPoint::AfterRollbackPrepared)?;
                }
            }
        }

        if transaction.state.verified {
            require_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
                &desired_state,
            )?;
            if entry_exists(
                transaction.workspace.as_raw_fd(),
                OsStr::new(ROLLBACK_STAGE_PENDING),
            )? {
                unlink_entry(
                    transaction.workspace.as_raw_fd(),
                    OsStr::new(ROLLBACK_STAGE_PENDING),
                    &transaction.workspace_path.join(ROLLBACK_STAGE_PENDING),
                )?;
                transaction
                    .workspace
                    .sync_all()
                    .map_err(|source| io_error(&transaction.workspace_path, source))?;
            }
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &JournalEvent::Complete,
            )?;
            transaction.state.terminal = Some(ReplacementOutcome::Complete);
            inject(fault, FaultPoint::AfterTerminal)?;
            verify_terminal_target(transaction, ReplacementOutcome::Complete)?;
            return Ok(ReplacementReceipt {
                transaction_id: transaction.state.transaction_id,
                outcome: ReplacementOutcome::Complete,
            });
        }

        if !transaction.state.rollback_prepared {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        if !transaction.state.rollback_stage_intent {
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &JournalEvent::RollbackStageIntent,
            )?;
            transaction.state.rollback_stage_intent = true;
            inject(fault, FaultPoint::AfterRollbackStageIntent)?;
        }
        if transaction.state.rollback_identity.is_none() {
            require_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
                &desired_state,
            )?;
            let backup_workspace = transaction
                .state
                .backup_workspace
                .as_deref()
                .ok_or(SyntheticReplacementError::CorruptJournal)?;
            let original = load_encrypted_backup_for_replacement_with_provider(
                &transaction.root_path,
                backup_workspace,
                transaction.state.transaction_id,
                transaction
                    .state
                    .reference
                    .as_ref()
                    .ok_or(SyntheticReplacementError::CorruptJournal)?,
                &ReplacementTargetExpectation {
                    volume: desired_identity.volume,
                    file: desired_identity.file,
                    digest: transaction.state.desired_digest.clone(),
                    len: transaction.state.desired_len,
                },
                provider,
            )?;
            if digest(&original) != transaction.state.original.digest
                || original.len() != transaction.state.original.len
            {
                return Err(SyntheticReplacementError::RecoveryConflict);
            }
            let rollback = create_or_adopt_rollback_stage(
                &transaction.workspace,
                &original,
                &transaction.state.original,
                &transaction.workspace_path,
                fault,
            )?;
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &JournalEvent::RollbackStaged {
                    identity: rollback.identity,
                },
            )?;
            transaction.state.rollback_identity = Some(rollback.identity);
            inject(fault, FaultPoint::AfterRollbackStage)?;
        }

        let rollback_identity = transaction
            .state
            .rollback_identity
            .ok_or(SyntheticReplacementError::CorruptJournal)?;
        let restored_state = TargetState {
            identity: rollback_identity,
            ..transaction.state.original.clone()
        };
        if !transaction.state.restored {
            let current = read_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
            )?;
            if current == desired_state {
                require_stage_state(transaction, ROLLBACK_STAGE, &restored_state)?;
                revalidate_namespace(transaction)?;
                exchange_stage(
                    transaction,
                    ROLLBACK_STAGE,
                    &desired_state,
                    &restored_state,
                    fault,
                )?;
                inject(fault, FaultPoint::AfterRestore)?;
            } else if current == restored_state {
                match read_stage_state(transaction, ROLLBACK_STAGE) {
                    Ok(displaced) if displaced == desired_state => {}
                    Ok(displaced) => {
                        undo_unverified_exchange(
                            transaction,
                            ROLLBACK_STAGE,
                            &restored_state,
                            &displaced,
                        )?;
                        return Err(SyntheticReplacementError::RecoveryConflict);
                    }
                    Err(_) => {
                        let displaced = raw_entry_state(
                            transaction.workspace.as_raw_fd(),
                            OsStr::new(ROLLBACK_STAGE),
                            &transaction.workspace_path.join(ROLLBACK_STAGE),
                        )?;
                        undo_unverified_exchange_raw(
                            transaction,
                            ROLLBACK_STAGE,
                            &restored_state,
                            displaced,
                        )?;
                        return Err(SyntheticReplacementError::RecoveryConflict);
                    }
                }
            } else {
                return Err(SyntheticReplacementError::RecoveryConflict);
            }
            require_target_state(
                &transaction.root,
                &transaction.state.target,
                &transaction.root_path.join(&transaction.state.target),
                &restored_state,
            )?;
            append_event(
                &mut transaction.journal,
                &transaction.workspace,
                &transaction.workspace_path,
                &JournalEvent::Restored,
            )?;
            transaction.state.restored = true;
            inject(fault, FaultPoint::AfterRestored)?;
        }
        cleanup_swapped_stage(transaction, ROLLBACK_STAGE, &desired_state)?;
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            &restored_state,
        )?;
        append_event(
            &mut transaction.journal,
            &transaction.workspace,
            &transaction.workspace_path,
            &JournalEvent::RolledBack,
        )?;
        transaction.state.terminal = Some(ReplacementOutcome::RolledBack);
        inject(fault, FaultPoint::AfterTerminal)?;
        verify_terminal_target(transaction, ReplacementOutcome::RolledBack)?;
        Ok(ReplacementReceipt {
            transaction_id: transaction.state.transaction_id,
            outcome: ReplacementOutcome::RolledBack,
        })
    }

    fn open_transaction(
        approval: &SyntheticRootApproval,
        transaction_id: ReplacementTransactionId,
    ) -> Result<OpenTransaction, SyntheticReplacementError> {
        let root = open_root(&approval.root)?;
        verify_private_directory(&root, &approval.root)?;
        let transaction_path = approval.root.join(REPLACEMENT_DIRECTORY);
        let transaction_directory = open_private_directory(
            root.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &transaction_path,
        )?;
        let target_lock = open_private_file(
            transaction_directory.as_raw_fd(),
            OsStr::new(TARGET_LOCK_FILE),
            &transaction_path.join(TARGET_LOCK_FILE),
        )?;
        lock_exclusive(&target_lock, &transaction_path.join(TARGET_LOCK_FILE))?;
        let workspace_name = format!("tx-{}", transaction_text(transaction_id));
        let workspace_path = transaction_path.join(&workspace_name);
        let workspace = open_private_directory(
            transaction_directory.as_raw_fd(),
            OsStr::new(&workspace_name),
            &workspace_path,
        )?;
        let journal_path = workspace_path.join(JOURNAL_FILE);
        let mut journal = open_private_file(
            workspace.as_raw_fd(),
            OsStr::new(JOURNAL_FILE),
            &journal_path,
        )?;
        lock_exclusive(&journal, &journal_path)?;
        repair_pending_event(&workspace, &workspace_path, &mut journal)?;
        repair_reserved_pending_event(&workspace, &workspace_path, &mut journal)?;
        let state = read_journal(&mut journal)?;
        if state.transaction_id != transaction_id
            || state.root_identity != identity(&root, &approval.root)?
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(OpenTransaction {
            root_path: approval.root.clone(),
            workspace_path,
            root,
            transaction_directory,
            _target_lock: target_lock,
            workspace,
            journal,
            state,
        })
    }

    fn read_journal(journal: &mut File) -> Result<JournalState, SyntheticReplacementError> {
        let bytes = read_journal_bytes(journal)?;
        parse_journal_bytes(&bytes)
    }

    fn read_journal_bytes(journal: &mut File) -> Result<Vec<u8>, SyntheticReplacementError> {
        journal
            .seek(SeekFrom::Start(0))
            .map_err(|source| io_error(JOURNAL_FILE, source))?;
        let mut bytes = Vec::new();
        journal
            .take((MAX_JOURNAL_BYTES + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(JOURNAL_FILE, source))?;
        if bytes.len() > MAX_JOURNAL_BYTES {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        Ok(bytes)
    }

    fn parse_journal_bytes(bytes: &[u8]) -> Result<JournalState, SyntheticReplacementError> {
        if bytes.is_empty() || !bytes.ends_with(b"\n") {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let lines = bytes[..bytes.len() - 1]
            .split(|byte| *byte == b'\n')
            .collect::<Vec<_>>();
        if lines.is_empty() || lines.len() > MAX_EVENTS || lines.iter().any(|line| line.is_empty())
        {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let events = lines
            .iter()
            .map(|line| {
                serde_json::from_slice::<JournalEvent>(line)
                    .map_err(|_| SyntheticReplacementError::CorruptJournal)
            })
            .collect::<Result<Vec<_>, _>>()?;
        replay(&events)
    }

    fn replay(events: &[JournalEvent]) -> Result<JournalState, SyntheticReplacementError> {
        let Some(JournalEvent::KeyCreateIntent {
            version,
            transaction_id,
            root_identity,
            target,
            original,
            desired_digest,
            desired_len,
        }) = events.first()
        else {
            return Err(SyntheticReplacementError::CorruptJournal);
        };
        if *version != FORMAT_VERSION
            || validate_target(Path::new(target)).is_err()
            || *desired_len > MAX_TARGET_BYTES
            || digest_text_invalid(desired_digest)
            || digest_text_invalid(&original.digest)
        {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let mut state = JournalState {
            transaction_id: parse_transaction_id(transaction_id)?,
            root_identity: *root_identity,
            target: target.clone(),
            original: original.clone(),
            desired_digest: desired_digest.clone(),
            desired_len: *desired_len,
            reference: None,
            backup_workspace: None,
            desired_identity: None,
            commit_prepared: false,
            published: false,
            verified: false,
            rollback_prepared: false,
            rollback_stage_intent: false,
            rollback_identity: None,
            restored: false,
            terminal: None,
        };
        for event in &events[1..] {
            match event {
                JournalEvent::KeyCreated {
                    provider_kind,
                    provider_version,
                    provider_reference,
                } if state.reference.is_none() && state.backup_workspace.is_none() => {
                    let reference_bytes = hex::decode(provider_reference)
                        .map_err(|_| SyntheticReplacementError::CorruptJournal)?;
                    state.reference = Some(
                        RecoveryKeyReference::new(
                            provider_kind.clone(),
                            *provider_version,
                            reference_bytes,
                        )
                        .map_err(|_| SyntheticReplacementError::CorruptJournal)?,
                    );
                }
                JournalEvent::BackedUp { workspace }
                    if state.reference.is_some()
                        && state.backup_workspace.is_none()
                        && state.desired_identity.is_none() =>
                {
                    state.backup_workspace = Some(workspace.clone())
                }
                JournalEvent::Staged { identity }
                    if state.backup_workspace.is_some() && state.desired_identity.is_none() =>
                {
                    state.desired_identity = Some(*identity)
                }
                JournalEvent::CommitPrepared
                    if state.desired_identity.is_some() && !state.commit_prepared =>
                {
                    state.commit_prepared = true
                }
                JournalEvent::Published if state.commit_prepared && !state.published => {
                    state.published = true
                }
                JournalEvent::Verified
                    if state.published && !state.verified && !state.rollback_prepared =>
                {
                    state.verified = true
                }
                JournalEvent::RollbackPrepared
                    if state.published && !state.verified && !state.rollback_prepared =>
                {
                    state.rollback_prepared = true
                }
                JournalEvent::RollbackStageIntent
                    if state.rollback_prepared
                        && !state.rollback_stage_intent
                        && state.rollback_identity.is_none() =>
                {
                    state.rollback_stage_intent = true
                }
                JournalEvent::RollbackStaged { identity }
                    if state.rollback_stage_intent && state.rollback_identity.is_none() =>
                {
                    state.rollback_identity = Some(*identity)
                }
                JournalEvent::Restored if state.rollback_identity.is_some() && !state.restored => {
                    state.restored = true
                }
                JournalEvent::Complete if state.verified && state.terminal.is_none() => {
                    state.terminal = Some(ReplacementOutcome::Complete)
                }
                JournalEvent::RolledBack if state.restored && state.terminal.is_none() => {
                    state.terminal = Some(ReplacementOutcome::RolledBack)
                }
                JournalEvent::KeyCreateIntent { .. } | _ => {
                    return Err(SyntheticReplacementError::CorruptJournal)
                }
            }
        }
        Ok(state)
    }

    fn verify_terminal_target(
        transaction: &OpenTransaction,
        outcome: ReplacementOutcome,
    ) -> Result<(), SyntheticReplacementError> {
        let identity = match outcome {
            ReplacementOutcome::Complete => transaction.state.desired_identity,
            ReplacementOutcome::RolledBack => transaction.state.rollback_identity,
        }
        .ok_or(SyntheticReplacementError::CorruptJournal)?;
        let expected = match outcome {
            ReplacementOutcome::Complete => TargetState {
                identity,
                digest: transaction.state.desired_digest.clone(),
                len: transaction.state.desired_len,
                mode: transaction.state.original.mode,
                uid: transaction.state.original.uid,
                gid: transaction.state.original.gid,
            },
            ReplacementOutcome::RolledBack => TargetState {
                identity,
                ..transaction.state.original.clone()
            },
        };
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            &expected,
        )
    }

    fn append_event(
        journal: &mut File,
        workspace: &File,
        workspace_path: &Path,
        event: &JournalEvent,
    ) -> Result<(), SyntheticReplacementError> {
        let mut bytes =
            serde_json::to_vec(event).map_err(|_| SyntheticReplacementError::CorruptJournal)?;
        bytes.push(b'\n');
        if entry_exists(workspace.as_raw_fd(), OsStr::new(PENDING_EVENT_FILE))? {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let current = journal
            .metadata()
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?
            .len() as usize;
        if current + bytes.len() > MAX_JOURNAL_BYTES {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let envelope = encode_pending_event(&bytes)?;
        if pending_slots_ready(workspace, workspace_path)? {
            let event_count = read_journal_bytes(journal)?
                .iter()
                .filter(|byte| **byte == b'\n')
                .count();
            if event_count >= MAX_EVENTS {
                return Err(SyntheticReplacementError::CorruptJournal);
            }
            write_reserved_pending_event(workspace, workspace_path, event_count, &envelope)?;
            raw_append(journal, workspace_path, &bytes)?;
            return Ok(());
        }
        let mut pending = create_private_file(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_EVENT_FILE),
            workspace_path.join(PENDING_EVENT_FILE),
        )?;
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        let allocation = unsafe {
            libc::posix_fallocate(
                pending.as_raw_fd(),
                0,
                PENDING_EVENT_CAPACITY as libc::off_t,
            )
        };
        if allocation != 0 {
            return Err(io_error(
                workspace_path.join(PENDING_EVENT_FILE),
                io::Error::from_raw_os_error(allocation),
            ));
        }
        pending
            .set_len(PENDING_EVENT_CAPACITY as u64)
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        pending
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        pending
            .seek(SeekFrom::Start(0))
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        pending
            .write_all(&envelope)
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        pending
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        raw_append(journal, workspace_path, &bytes)?;
        unlink_entry(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_EVENT_FILE),
            &workspace_path.join(PENDING_EVENT_FILE),
        )?;
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        Ok(())
    }

    fn pending_slots_ready(
        workspace: &File,
        workspace_path: &Path,
    ) -> Result<bool, SyntheticReplacementError> {
        if !entry_exists(workspace.as_raw_fd(), OsStr::new(PENDING_SLOTS_READY_FILE))? {
            return Ok(false);
        }
        let marker = open_private_file(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_SLOTS_READY_FILE),
            &workspace_path.join(PENDING_SLOTS_READY_FILE),
        )?;
        if marker
            .metadata()
            .map_err(|source| io_error(workspace_path.join(PENDING_SLOTS_READY_FILE), source))?
            .len()
            != 0
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(true)
    }

    pub(super) fn encode_pending_event(bytes: &[u8]) -> Result<Vec<u8>, SyntheticReplacementError> {
        if bytes.is_empty() || bytes.len() > PENDING_EVENT_CAPACITY - PENDING_EVENT_HEADER {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let mut envelope = vec![0_u8; PENDING_EVENT_CAPACITY];
        envelope[..8].copy_from_slice(PENDING_EVENT_MAGIC);
        envelope[8..12].copy_from_slice(&(bytes.len() as u32).to_le_bytes());
        envelope[12..44].copy_from_slice(&Sha256::digest(bytes));
        envelope[44..44 + bytes.len()].copy_from_slice(bytes);
        Ok(envelope)
    }

    fn decode_pending_event(bytes: &[u8]) -> Option<Vec<u8>> {
        if bytes.len() != PENDING_EVENT_CAPACITY || &bytes[..8] != PENDING_EVENT_MAGIC {
            return None;
        }
        let length = u32::from_le_bytes(bytes[8..12].try_into().ok()?) as usize;
        if length == 0 || length > PENDING_EVENT_CAPACITY - PENDING_EVENT_HEADER {
            return None;
        }
        let payload = &bytes[44..44 + length];
        if bytes[44 + length..].iter().any(|byte| *byte != 0)
            || Sha256::digest(payload).as_slice() != &bytes[12..44]
            || !payload.ends_with(b"\n")
            || payload[..payload.len() - 1].contains(&b'\n')
            || serde_json::from_slice::<JournalEvent>(&payload[..payload.len() - 1]).is_err()
        {
            return None;
        }
        Some(payload.to_vec())
    }

    pub(super) fn pending_slot_name(index: usize) -> String {
        format!("{PENDING_SLOT_PREFIX}{index:02}")
    }

    fn reserve_pending_event_slots(
        workspace: &File,
        workspace_path: &Path,
        fault: Option<FaultPoint>,
    ) -> Result<(), SyntheticReplacementError> {
        for index in 0..MAX_EVENTS {
            let name = pending_slot_name(index);
            let display = workspace_path.join(&name);
            let slot =
                create_private_file(workspace.as_raw_fd(), OsStr::new(&name), display.clone())?;
            if fault == Some(FaultPoint::PendingSlotReservationFailure) && index == MAX_EVENTS / 2 {
                return Err(io_error(
                    &display,
                    io::Error::from_raw_os_error(libc::ENOSPC),
                ));
            }
            let allocation = unsafe {
                libc::posix_fallocate(slot.as_raw_fd(), 0, PENDING_EVENT_CAPACITY as libc::off_t)
            };
            if allocation != 0 {
                return Err(io_error(&display, io::Error::from_raw_os_error(allocation)));
            }
            slot.set_len(PENDING_EVENT_CAPACITY as u64)
                .map_err(|source| io_error(&display, source))?;
            slot.sync_all()
                .map_err(|source| io_error(&display, source))?;
        }
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        let marker = create_private_file(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_SLOTS_READY_FILE),
            workspace_path.join(PENDING_SLOTS_READY_FILE),
        )?;
        marker
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(PENDING_SLOTS_READY_FILE), source))?;
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        Ok(())
    }

    fn write_reserved_pending_event(
        workspace: &File,
        workspace_path: &Path,
        index: usize,
        envelope: &[u8],
    ) -> Result<(), SyntheticReplacementError> {
        let name = pending_slot_name(index);
        let display = workspace_path.join(&name);
        let mut slot = open_private_file(workspace.as_raw_fd(), OsStr::new(&name), &display)?;
        if slot
            .metadata()
            .map_err(|source| io_error(&display, source))?
            .len()
            != PENDING_EVENT_CAPACITY as u64
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        slot.seek(SeekFrom::Start(0))
            .map_err(|source| io_error(&display, source))?;
        slot.write_all(envelope)
            .map_err(|source| io_error(&display, source))?;
        slot.sync_all()
            .map_err(|source| io_error(&display, source))?;
        Ok(())
    }

    fn repair_reserved_pending_event(
        workspace: &File,
        workspace_path: &Path,
        journal: &mut File,
    ) -> Result<(), SyntheticReplacementError> {
        if !pending_slots_ready(workspace, workspace_path)? {
            return Ok(());
        }
        let bytes = read_journal_bytes(journal)?;
        let prefix_len = bytes
            .iter()
            .rposition(|byte| *byte == b'\n')
            .map_or(0, |index| index + 1);
        let prefix = &bytes[..prefix_len];
        if prefix.is_empty() || parse_journal_bytes(prefix).is_err() {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let event_count = prefix.iter().filter(|byte| **byte == b'\n').count();
        if event_count >= MAX_EVENTS {
            return if prefix_len == bytes.len() {
                Ok(())
            } else {
                Err(SyntheticReplacementError::CorruptJournal)
            };
        }
        let name = pending_slot_name(event_count);
        let display = workspace_path.join(&name);
        let mut slot = open_private_file(workspace.as_raw_fd(), OsStr::new(&name), &display)?;
        let mut envelope = Vec::new();
        slot.take((PENDING_EVENT_CAPACITY + 1) as u64)
            .read_to_end(&mut envelope)
            .map_err(|source| io_error(&display, source))?;
        let Some(pending) = decode_pending_event(&envelope) else {
            return if prefix_len == bytes.len() {
                Ok(())
            } else {
                Err(SyntheticReplacementError::CorruptJournal)
            };
        };
        let suffix = &bytes[prefix_len..];
        if !pending.starts_with(suffix) || prefix_len + pending.len() > MAX_JOURNAL_BYTES {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        let mut candidate = prefix.to_vec();
        candidate.extend_from_slice(&pending);
        parse_journal_bytes(&candidate)?;
        journal
            .seek(SeekFrom::Start(prefix_len as u64))
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .write_all(&pending)
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .set_len(candidate.len() as u64)
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        Ok(())
    }

    fn raw_append(
        journal: &mut File,
        workspace_path: &Path,
        bytes: &[u8],
    ) -> Result<(), SyntheticReplacementError> {
        journal
            .seek(SeekFrom::End(0))
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .write_all(&bytes)
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        Ok(())
    }

    fn repair_pending_event(
        workspace: &File,
        workspace_path: &Path,
        journal: &mut File,
    ) -> Result<(), SyntheticReplacementError> {
        if !entry_exists(workspace.as_raw_fd(), OsStr::new(PENDING_EVENT_FILE))? {
            return Ok(());
        }
        let mut pending = open_private_file(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_EVENT_FILE),
            &workspace_path.join(PENDING_EVENT_FILE),
        )?;
        let mut pending_envelope = Vec::new();
        pending
            .take((PENDING_EVENT_CAPACITY + 1) as u64)
            .read_to_end(&mut pending_envelope)
            .map_err(|source| io_error(workspace_path.join(PENDING_EVENT_FILE), source))?;
        let bytes = read_journal_bytes(journal)?;
        let Some(pending_bytes) = decode_pending_event(&pending_envelope) else {
            if parse_journal_bytes(&bytes).is_err() {
                truncate_incomplete_journal_tail(journal, workspace_path, &bytes)?;
            }
            finish_pending_repair(workspace, workspace_path)?;
            return Ok(());
        };
        if parse_journal_bytes(&bytes).is_ok() && bytes.ends_with(&pending_bytes) {
            finish_pending_repair(workspace, workspace_path)?;
            return Ok(());
        }
        let prefix_len = bytes
            .iter()
            .rposition(|byte| *byte == b'\n')
            .map_or(0, |index| index + 1);
        let (prefix, suffix) = bytes.split_at(prefix_len);
        if (!prefix.is_empty() && parse_journal_bytes(prefix).is_err())
            || !pending_bytes.starts_with(suffix)
            || prefix.len() + pending_bytes.len() > MAX_JOURNAL_BYTES
        {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        journal
            .set_len(prefix.len() as u64)
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        raw_append(journal, workspace_path, &pending_bytes)?;
        let repaired = read_journal_bytes(journal)?;
        parse_journal_bytes(&repaired)?;
        finish_pending_repair(workspace, workspace_path)?;
        Ok(())
    }

    fn truncate_incomplete_journal_tail(
        journal: &mut File,
        workspace_path: &Path,
        bytes: &[u8],
    ) -> Result<(), SyntheticReplacementError> {
        let prefix_len = bytes
            .iter()
            .rposition(|byte| *byte == b'\n')
            .map_or(0, |index| index + 1);
        let prefix = &bytes[..prefix_len];
        if prefix.is_empty() || parse_journal_bytes(prefix).is_err() {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        journal
            .set_len(prefix_len as u64)
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        journal
            .sync_all()
            .map_err(|source| io_error(workspace_path.join(JOURNAL_FILE), source))?;
        Ok(())
    }

    fn finish_pending_repair(
        workspace: &File,
        workspace_path: &Path,
    ) -> Result<(), SyntheticReplacementError> {
        unlink_entry(
            workspace.as_raw_fd(),
            OsStr::new(PENDING_EVENT_FILE),
            &workspace_path.join(PENDING_EVENT_FILE),
        )?;
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        Ok(())
    }

    fn require_journal_budget(
        journal: &File,
        reserved_bytes: usize,
    ) -> Result<(), SyntheticReplacementError> {
        let current = journal
            .metadata()
            .map_err(|source| io_error(JOURNAL_FILE, source))?
            .len() as usize;
        if current
            .checked_add(reserved_bytes)
            .is_none_or(|required| required > MAX_JOURNAL_BYTES)
        {
            return Err(SyntheticReplacementError::CorruptJournal);
        }
        Ok(())
    }

    fn reserve_journal_space(
        journal: &File,
        display: &Path,
        fault: Option<FaultPoint>,
    ) -> Result<(), SyntheticReplacementError> {
        if fault == Some(FaultPoint::JournalReservationFailure) {
            return Err(io_error(
                display,
                io::Error::from_raw_os_error(libc::ENOSPC),
            ));
        }
        if unsafe {
            libc::fallocate(
                journal.as_raw_fd(),
                libc::FALLOC_FL_KEEP_SIZE,
                0,
                MAX_JOURNAL_BYTES as libc::off_t,
            )
        } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        journal
            .sync_all()
            .map_err(|source| io_error(display, source))?;
        Ok(())
    }

    fn create_stage(
        workspace: &File,
        name: &str,
        bytes: &[u8],
        original: &TargetState,
        display: &Path,
    ) -> Result<TargetState, SyntheticReplacementError> {
        let mut stage = create_private_file(
            workspace.as_raw_fd(),
            OsStr::new(name),
            display.to_path_buf(),
        )?;
        if unsafe { libc::fchown(stage.as_raw_fd(), original.uid, original.gid) } != 0
            || unsafe { libc::fchmod(stage.as_raw_fd(), original.mode) } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        stage
            .write_all(bytes)
            .map_err(|source| io_error(display, source))?;
        stage
            .sync_all()
            .map_err(|source| io_error(display, source))?;
        workspace
            .sync_all()
            .map_err(|source| io_error(display.parent().unwrap_or(display), source))?;
        let state = state_from_open_file(&mut stage, display)?;
        if state.digest != digest(bytes)
            || state.len != bytes.len()
            || state.mode != original.mode
            || state.uid != original.uid
            || state.gid != original.gid
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(state)
    }

    fn reserve_rollback_stage(
        workspace: &File,
        original: &TargetState,
        display: &Path,
        fault: Option<FaultPoint>,
    ) -> Result<(), SyntheticReplacementError> {
        let reserve = create_private_file(
            workspace.as_raw_fd(),
            OsStr::new(ROLLBACK_STAGE_PENDING),
            display.to_path_buf(),
        )?;
        if unsafe { libc::fchown(reserve.as_raw_fd(), original.uid, original.gid) } != 0
            || unsafe { libc::fchmod(reserve.as_raw_fd(), 0o600) } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        if fault == Some(FaultPoint::RollbackReservationFailure) {
            return Err(io_error(
                display,
                io::Error::from_raw_os_error(libc::ENOSPC),
            ));
        }
        if original.len > 0 {
            let result = unsafe {
                libc::posix_fallocate(reserve.as_raw_fd(), 0, original.len as libc::off_t)
            };
            if result != 0 {
                return Err(io_error(display, io::Error::from_raw_os_error(result)));
            }
        }
        reserve
            .set_len(original.len as u64)
            .map_err(|source| io_error(display, source))?;
        reserve
            .sync_all()
            .map_err(|source| io_error(display, source))?;
        workspace
            .sync_all()
            .map_err(|source| io_error(display.parent().unwrap_or(display), source))?;
        Ok(())
    }

    fn create_or_adopt_rollback_stage(
        workspace: &File,
        bytes: &[u8],
        expected: &TargetState,
        workspace_path: &Path,
        fault: Option<FaultPoint>,
    ) -> Result<TargetState, SyntheticReplacementError> {
        let final_exists = entry_exists(workspace.as_raw_fd(), OsStr::new(ROLLBACK_STAGE))?;
        let pending_exists =
            entry_exists(workspace.as_raw_fd(), OsStr::new(ROLLBACK_STAGE_PENDING))?;
        if final_exists && pending_exists {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        if final_exists {
            let mut stage = open_private_or_original_mode_file(
                workspace.as_raw_fd(),
                OsStr::new(ROLLBACK_STAGE),
                &workspace_path.join(ROLLBACK_STAGE),
            )?;
            let state = state_from_open_file(&mut stage, &workspace_path.join(ROLLBACK_STAGE))?;
            if state.digest == expected.digest
                && state.len == expected.len
                && state.mode == expected.mode
                && state.uid == expected.uid
                && state.gid == expected.gid
            {
                return Ok(state);
            }
            return Err(SyntheticReplacementError::RecoveryConflict);
        }

        if !pending_exists {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let pending_display = workspace_path.join(ROLLBACK_STAGE_PENDING);
        let mut pending = open_private_or_original_mode_file(
            workspace.as_raw_fd(),
            OsStr::new(ROLLBACK_STAGE_PENDING),
            &pending_display,
        )?;
        let before = state_from_open_file(&mut pending, &pending_display)?;
        if (before.mode != expected.mode && before.mode != 0o600)
            || before.uid != expected.uid
            || before.gid != expected.gid
            || before.len != expected.len
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        if before.digest != expected.digest {
            if before.mode != 0o600 {
                return Err(SyntheticReplacementError::RecoveryConflict);
            }
            drop(pending);
            pending = open_file_at(
                workspace.as_raw_fd(),
                OsStr::new(ROLLBACK_STAGE_PENDING),
                libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                0,
            )
            .map_err(|source| io_error(&pending_display, source))?;
            pending
                .seek(SeekFrom::Start(0))
                .map_err(|source| io_error(&pending_display, source))?;
            pending
                .write_all(bytes)
                .map_err(|source| io_error(&pending_display, source))?;
            pending
                .set_len(expected.len as u64)
                .map_err(|source| io_error(&pending_display, source))?;
            pending
                .sync_all()
                .map_err(|source| io_error(&pending_display, source))?;
            if unsafe { libc::fchmod(pending.as_raw_fd(), expected.mode) } != 0 {
                return Err(io_error(&pending_display, io::Error::last_os_error()));
            }
            pending
                .sync_all()
                .map_err(|source| io_error(&pending_display, source))?;
            workspace
                .sync_all()
                .map_err(|source| io_error(workspace_path, source))?;
            inject(fault, FaultPoint::AfterRollbackStageWrite)?;
        } else if before.mode != expected.mode {
            if before.mode != 0o600 {
                return Err(SyntheticReplacementError::RecoveryConflict);
            }
            if unsafe { libc::fchmod(pending.as_raw_fd(), expected.mode) } != 0 {
                return Err(io_error(&pending_display, io::Error::last_os_error()));
            }
            pending
                .sync_all()
                .map_err(|source| io_error(&pending_display, source))?;
        }
        let final_state = state_from_open_file(&mut pending, &pending_display)?;
        if final_state.digest != expected.digest
            || final_state.len != expected.len
            || final_state.mode != expected.mode
            || final_state.uid != expected.uid
            || final_state.gid != expected.gid
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        drop(pending);
        let from = c_string(OsStr::new(ROLLBACK_STAGE_PENDING))?;
        let to = c_string(OsStr::new(ROLLBACK_STAGE))?;
        if unsafe {
            libc::renameat2(
                workspace.as_raw_fd(),
                from.as_ptr(),
                workspace.as_raw_fd(),
                to.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        } != 0
        {
            return Err(io_error(
                workspace_path.join(ROLLBACK_STAGE),
                io::Error::last_os_error(),
            ));
        }
        workspace
            .sync_all()
            .map_err(|source| io_error(workspace_path, source))?;
        inject(fault, FaultPoint::AfterRollbackStagePublish)?;
        let mut stage = open_private_or_original_mode_file(
            workspace.as_raw_fd(),
            OsStr::new(ROLLBACK_STAGE),
            &workspace_path.join(ROLLBACK_STAGE),
        )?;
        state_from_open_file(&mut stage, &workspace_path.join(ROLLBACK_STAGE))
    }

    fn require_stage_state(
        transaction: &OpenTransaction,
        name: &str,
        expected: &TargetState,
    ) -> Result<(), SyntheticReplacementError> {
        let mut file = open_private_or_original_mode_file(
            transaction.workspace.as_raw_fd(),
            OsStr::new(name),
            &transaction.workspace_path.join(name),
        )?;
        let state = state_from_open_file(&mut file, &transaction.workspace_path.join(name))?;
        if &state == expected {
            Ok(())
        } else {
            Err(SyntheticReplacementError::RecoveryConflict)
        }
    }

    fn exchange_stage(
        transaction: &OpenTransaction,
        stage: &str,
        expected_current: &TargetState,
        expected_replacement: &TargetState,
        fault: Option<FaultPoint>,
    ) -> Result<(), SyntheticReplacementError> {
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            expected_current,
        )?;
        #[cfg(test)]
        if matches!(
            fault,
            Some(
                FaultPoint::ExternalEditBeforeExchange
                    | FaultPoint::ExternalEditThenCrashAfterExchange
                    | FaultPoint::ExternalSymlinkThenCrashAfterExchange
                    | FaultPoint::ExternalPublicModeThenCrashAfterExchange
                    | FaultPoint::ExternalHardlinkThenCrashAfterExchange
            )
        ) || (fault == Some(FaultPoint::ExternalSymlinkThenCrashAfterRollbackExchange)
            && stage == ROLLBACK_STAGE)
        {
            simulate_external_edit_before_exchange(transaction, fault.unwrap())?;
        }
        let from = c_string(OsStr::new(stage))?;
        let to = c_string(OsStr::new(&transaction.state.target))?;
        let result = unsafe {
            libc::renameat2(
                transaction.workspace.as_raw_fd(),
                from.as_ptr(),
                transaction.root.as_raw_fd(),
                to.as_ptr(),
                libc::RENAME_EXCHANGE,
            )
        };
        if result != 0 {
            return Err(io_error(
                transaction.root_path.join(&transaction.state.target),
                io::Error::last_os_error(),
            ));
        }
        transaction
            .root
            .sync_all()
            .map_err(|source| io_error(&transaction.root_path, source))?;
        transaction
            .workspace
            .sync_all()
            .map_err(|source| io_error(&transaction.workspace_path, source))?;
        inject(fault, FaultPoint::AfterExchangeBeforeVerify)?;
        inject(fault, FaultPoint::ExternalEditThenCrashAfterExchange)?;
        inject(fault, FaultPoint::ExternalSymlinkThenCrashAfterExchange)?;
        inject(fault, FaultPoint::ExternalPublicModeThenCrashAfterExchange)?;
        inject(fault, FaultPoint::ExternalHardlinkThenCrashAfterExchange)?;
        if stage == ROLLBACK_STAGE {
            inject(
                fault,
                FaultPoint::ExternalSymlinkThenCrashAfterRollbackExchange,
            )?;
        }
        let target_after = read_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
        );
        let displaced_after = read_stage_state(transaction, stage);
        if target_after
            .as_ref()
            .is_ok_and(|state| state == expected_replacement)
            && displaced_after
                .as_ref()
                .is_ok_and(|state| state == expected_current)
        {
            return Ok(());
        }

        // Exchange keeps both names alive. If either lookup changed in the
        // check-to-syscall window, restore the displaced entry only when the
        // two names still identify exactly the objects produced by our swap.
        if let (Ok(target_state), Ok(displaced_state)) = (&target_after, &displaced_after) {
            if target_state == expected_replacement && displaced_state != expected_current {
                undo_unverified_exchange(
                    transaction,
                    stage,
                    expected_replacement,
                    displaced_state,
                )?;
            }
        }
        if target_after
            .as_ref()
            .is_ok_and(|state| state == expected_replacement)
            && displaced_after.is_err()
        {
            let displaced = raw_entry_state(
                transaction.workspace.as_raw_fd(),
                OsStr::new(stage),
                &transaction.workspace_path.join(stage),
            )?;
            undo_unverified_exchange_raw(transaction, stage, expected_replacement, displaced)?;
        }
        Err(SyntheticReplacementError::RecoveryConflict)
    }

    fn undo_unverified_exchange(
        transaction: &OpenTransaction,
        stage: &str,
        expected_target: &TargetState,
        displaced: &TargetState,
    ) -> Result<(), SyntheticReplacementError> {
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            expected_target,
        )?;
        require_stage_state(transaction, stage, displaced)?;
        let from = c_string(OsStr::new(stage))?;
        let to = c_string(OsStr::new(&transaction.state.target))?;
        if unsafe {
            libc::renameat2(
                transaction.workspace.as_raw_fd(),
                from.as_ptr(),
                transaction.root.as_raw_fd(),
                to.as_ptr(),
                libc::RENAME_EXCHANGE,
            )
        } != 0
        {
            return Err(io_error(
                transaction.root_path.join(&transaction.state.target),
                io::Error::last_os_error(),
            ));
        }
        transaction
            .root
            .sync_all()
            .map_err(|source| io_error(&transaction.root_path, source))?;
        transaction
            .workspace
            .sync_all()
            .map_err(|source| io_error(&transaction.workspace_path, source))?;
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            displaced,
        )?;
        require_stage_state(transaction, stage, expected_target)
    }

    fn undo_unverified_exchange_raw(
        transaction: &OpenTransaction,
        stage: &str,
        expected_target: &TargetState,
        displaced: RawEntryState,
    ) -> Result<(), SyntheticReplacementError> {
        require_target_state(
            &transaction.root,
            &transaction.state.target,
            &transaction.root_path.join(&transaction.state.target),
            expected_target,
        )?;
        if raw_entry_state(
            transaction.workspace.as_raw_fd(),
            OsStr::new(stage),
            &transaction.workspace_path.join(stage),
        )? != displaced
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let from = c_string(OsStr::new(stage))?;
        let to = c_string(OsStr::new(&transaction.state.target))?;
        if unsafe {
            libc::renameat2(
                transaction.workspace.as_raw_fd(),
                from.as_ptr(),
                transaction.root.as_raw_fd(),
                to.as_ptr(),
                libc::RENAME_EXCHANGE,
            )
        } != 0
        {
            return Err(io_error(
                transaction.root_path.join(&transaction.state.target),
                io::Error::last_os_error(),
            ));
        }
        transaction
            .root
            .sync_all()
            .map_err(|source| io_error(&transaction.root_path, source))?;
        transaction
            .workspace
            .sync_all()
            .map_err(|source| io_error(&transaction.workspace_path, source))?;
        if raw_entry_state(
            transaction.root.as_raw_fd(),
            OsStr::new(&transaction.state.target),
            &transaction.root_path.join(&transaction.state.target),
        )? != displaced
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        require_stage_state(transaction, stage, expected_target)
    }

    #[cfg(test)]
    fn simulate_external_edit_before_exchange(
        transaction: &OpenTransaction,
        fault: FaultPoint,
    ) -> Result<(), SyntheticReplacementError> {
        let target = c_string(OsStr::new(&transaction.state.target))?;
        let saved = c_string(OsStr::new("external-window-original"))?;
        if unsafe {
            libc::renameat2(
                transaction.root.as_raw_fd(),
                target.as_ptr(),
                transaction.root.as_raw_fd(),
                saved.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        } != 0
        {
            return Err(io_error(
                transaction.root_path.join("external-window-original"),
                io::Error::last_os_error(),
            ));
        }
        let target_display = transaction.root_path.join(&transaction.state.target);
        match fault {
            FaultPoint::ExternalSymlinkThenCrashAfterExchange
            | FaultPoint::ExternalSymlinkThenCrashAfterRollbackExchange => {
                let link = CString::new("external-window-original").expect("fixed link");
                if unsafe {
                    libc::symlinkat(link.as_ptr(), transaction.root.as_raw_fd(), target.as_ptr())
                } != 0
                {
                    return Err(io_error(&target_display, io::Error::last_os_error()));
                }
            }
            FaultPoint::ExternalHardlinkThenCrashAfterExchange => {
                let backing_name = OsStr::new("external-window-backing");
                let backing_display = transaction.root_path.join(backing_name);
                let mut backing = create_private_file(
                    transaction.root.as_raw_fd(),
                    backing_name,
                    backing_display.clone(),
                )?;
                backing
                    .write_all(b"external-window-edit")
                    .map_err(|source| io_error(&backing_display, source))?;
                backing
                    .sync_all()
                    .map_err(|source| io_error(&backing_display, source))?;
                let backing = c_string(backing_name)?;
                if unsafe {
                    libc::linkat(
                        transaction.root.as_raw_fd(),
                        backing.as_ptr(),
                        transaction.root.as_raw_fd(),
                        target.as_ptr(),
                        0,
                    )
                } != 0
                {
                    return Err(io_error(&target_display, io::Error::last_os_error()));
                }
            }
            _ => {
                let mut external = create_private_file(
                    transaction.root.as_raw_fd(),
                    OsStr::new(&transaction.state.target),
                    target_display.clone(),
                )?;
                external
                    .write_all(b"external-window-edit")
                    .map_err(|source| io_error(&target_display, source))?;
                if fault == FaultPoint::ExternalPublicModeThenCrashAfterExchange
                    && unsafe { libc::fchmod(external.as_raw_fd(), 0o644) } != 0
                {
                    return Err(io_error(&target_display, io::Error::last_os_error()));
                }
                external
                    .sync_all()
                    .map_err(|source| io_error(&target_display, source))?;
            }
        }
        transaction
            .root
            .sync_all()
            .map_err(|source| io_error(&transaction.root_path, source))?;
        Ok(())
    }

    fn read_stage_state(
        transaction: &OpenTransaction,
        name: &str,
    ) -> Result<TargetState, SyntheticReplacementError> {
        let mut file = open_private_or_original_mode_file(
            transaction.workspace.as_raw_fd(),
            OsStr::new(name),
            &transaction.workspace_path.join(name),
        )?;
        state_from_open_file(&mut file, &transaction.workspace_path.join(name))
    }

    fn cleanup_swapped_stage(
        transaction: &OpenTransaction,
        name: &str,
        expected: &TargetState,
    ) -> Result<(), SyntheticReplacementError> {
        if !stage_exists(transaction, name)? {
            return Ok(());
        }
        require_stage_state(transaction, name, expected)?;
        unlink_entry(
            transaction.workspace.as_raw_fd(),
            OsStr::new(name),
            &transaction.workspace_path.join(name),
        )?;
        transaction
            .workspace
            .sync_all()
            .map_err(|source| io_error(&transaction.workspace_path, source))?;
        Ok(())
    }

    fn revalidate_namespace(
        transaction: &OpenTransaction,
    ) -> Result<(), SyntheticReplacementError> {
        let root = open_root(&transaction.root_path)?;
        if identity(&root, &transaction.root_path)?
            != identity(&transaction.root, &transaction.root_path)?
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let tx = open_private_directory(
            root.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &transaction.root_path.join(REPLACEMENT_DIRECTORY),
        )?;
        if identity(&tx, &transaction.root_path.join(REPLACEMENT_DIRECTORY))?
            != identity(
                &transaction.transaction_directory,
                &transaction.root_path.join(REPLACEMENT_DIRECTORY),
            )?
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let workspace_name = transaction
            .workspace_path
            .file_name()
            .ok_or(SyntheticReplacementError::RecoveryConflict)?;
        let workspace =
            open_private_directory(tx.as_raw_fd(), workspace_name, &transaction.workspace_path)?;
        if identity(&workspace, &transaction.workspace_path)?
            != identity(&transaction.workspace, &transaction.workspace_path)?
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let journal = open_private_file(
            workspace.as_raw_fd(),
            OsStr::new(JOURNAL_FILE),
            &transaction.workspace_path.join(JOURNAL_FILE),
        )?;
        if identity(&journal, &transaction.workspace_path.join(JOURNAL_FILE))?
            != identity(
                &transaction.journal,
                &transaction.workspace_path.join(JOURNAL_FILE),
            )?
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(())
    }

    fn read_target_state(
        root: &File,
        name: &str,
        display: &Path,
    ) -> Result<TargetState, SyntheticReplacementError> {
        let mut file = open_file_at(
            root.as_raw_fd(),
            OsStr::new(name),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|_| SyntheticReplacementError::RecoveryConflict)?;
        state_from_open_file(&mut file, display)
    }

    fn raw_entry_state(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<RawEntryState, SyntheticReplacementError> {
        let c_name = c_string(name)?;
        let mut stat = std::mem::MaybeUninit::<libc::stat>::zeroed();
        if unsafe {
            libc::fstatat(
                parent,
                c_name.as_ptr(),
                stat.as_mut_ptr(),
                libc::AT_SYMLINK_NOFOLLOW,
            )
        } != 0
        {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let stat = unsafe { stat.assume_init() };
        Ok(RawEntryState {
            device: stat.st_dev as u64,
            inode: stat.st_ino as u64,
            mode: stat.st_mode as u32,
            links: stat.st_nlink as u64,
            uid: stat.st_uid,
            gid: stat.st_gid,
        })
    }

    fn read_target_bytes(
        root: &File,
        name: &str,
        expected: &TargetState,
        display: &Path,
    ) -> Result<Zeroizing<Vec<u8>>, SyntheticReplacementError> {
        let mut file = open_file_at(
            root.as_raw_fd(),
            OsStr::new(name),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|_| SyntheticReplacementError::RecoveryConflict)?;
        let state = state_from_open_file(&mut file, display)?;
        if &state != expected {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        file.seek(SeekFrom::Start(0))
            .map_err(|source| io_error(display, source))?;
        read_bounded(&mut file, display)
    }

    fn require_target_state(
        root: &File,
        name: &str,
        display: &Path,
        expected: &TargetState,
    ) -> Result<(), SyntheticReplacementError> {
        if read_target_state(root, name, display)? == *expected {
            Ok(())
        } else {
            Err(SyntheticReplacementError::RecoveryConflict)
        }
    }

    fn state_from_open_file(
        file: &mut File,
        display: &Path,
    ) -> Result<TargetState, SyntheticReplacementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        let mode = metadata.mode() & 0o7777;
        if mode & 0o400 == 0 || mode & 0o077 != 0 || mode & 0o7000 != 0 {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        file.seek(SeekFrom::Start(0))
            .map_err(|source| io_error(display, source))?;
        let bytes = read_bounded(file, display)?;
        let final_metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        let identity = identity(file, display)?;
        if final_metadata.nlink() != 1
            || final_metadata.mode() & 0o7777 != mode
            || final_metadata.uid() != metadata.uid()
            || final_metadata.gid() != metadata.gid()
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(TargetState {
            identity,
            digest: digest(&bytes),
            len: bytes.len(),
            mode,
            uid: metadata.uid(),
            gid: metadata.gid(),
        })
    }

    fn read_bounded(
        file: &mut File,
        display: &Path,
    ) -> Result<Zeroizing<Vec<u8>>, SyntheticReplacementError> {
        let mut bytes = Zeroizing::new(Vec::new());
        file.take((MAX_TARGET_BYTES + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(display, source))?;
        if bytes.len() > MAX_TARGET_BYTES {
            return Err(SyntheticReplacementError::TooLarge);
        }
        Ok(bytes)
    }

    fn stage_exists(
        transaction: &OpenTransaction,
        name: &str,
    ) -> Result<bool, SyntheticReplacementError> {
        let c_name = c_string(OsStr::new(name))?;
        let result = unsafe {
            libc::faccessat(
                transaction.workspace.as_raw_fd(),
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
            Err(io_error(transaction.workspace_path.join(name), error))
        }
    }

    fn open_root(path: &Path) -> Result<File, SyntheticReplacementError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|component| matches!(component, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(SyntheticReplacementError::InvalidRoot);
        }
        let anchor = if path.is_absolute() {
            Path::new("/")
        } else {
            Path::new(".")
        };
        let c_anchor = CString::new(anchor.as_os_str().as_bytes()).expect("fixed anchor");
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
                Component::RootDir | Component::CurDir => {}
                Component::Normal(name) => {
                    display.push(name);
                    directory = open_file_at(
                        directory.as_raw_fd(),
                        name,
                        libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                        0,
                    )
                    .map_err(|source| io_error(&display, source))?;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(SyntheticReplacementError::InvalidRoot)
                }
            }
        }
        Ok(directory)
    }

    fn open_or_create_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, SyntheticReplacementError> {
        let c_name = c_string(name)?;
        let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
        let created = result == 0;
        if result != 0 {
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::AlreadyExists {
                return Err(io_error(&display, error));
            }
        }
        let directory = open_private_directory(parent, name, &display)?;
        if created && unsafe { libc::fchmod(directory.as_raw_fd(), 0o700) } != 0 {
            return Err(io_error(&display, io::Error::last_os_error()));
        }
        verify_private_directory(&directory, &display)?;
        Ok(directory)
    }

    fn create_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<(), SyntheticReplacementError> {
        let c_name = c_string(name)?;
        if unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn open_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, SyntheticReplacementError> {
        let directory = open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))?;
        verify_private_directory(&directory, display)?;
        Ok(directory)
    }

    fn verify_private_directory(
        file: &File,
        display: &Path,
    ) -> Result<(), SyntheticReplacementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_dir()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o700
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(())
    }

    fn create_private_file(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, SyntheticReplacementError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDWR | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0o600,
        )
        .map_err(|source| io_error(&display, source))?;
        if unsafe { libc::fchmod(file.as_raw_fd(), 0o600) } != 0 {
            return Err(io_error(&display, io::Error::last_os_error()));
        }
        Ok(file)
    }

    fn open_or_create_private_file(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, SyntheticReplacementError> {
        let c_name = c_string(name)?;
        let fd = unsafe {
            libc::openat(
                parent,
                c_name.as_ptr(),
                libc::O_RDWR | libc::O_CREAT | libc::O_CLOEXEC | libc::O_NOFOLLOW,
                0o600,
            )
        };
        if fd < 0 {
            return Err(io_error(&display, io::Error::last_os_error()));
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let metadata = file
            .metadata()
            .map_err(|source| io_error(&display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(file)
    }

    fn open_private_file(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, SyntheticReplacementError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))?;
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(SyntheticReplacementError::RecoveryConflict);
        }
        Ok(file)
    }

    fn open_private_or_original_mode_file(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, SyntheticReplacementError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))?;
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

    fn entry_exists(parent: RawFd, name: &OsStr) -> Result<bool, SyntheticReplacementError> {
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

    fn unlink_entry(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<(), SyntheticReplacementError> {
        let c_name = c_string(name)?;
        if unsafe { libc::unlinkat(parent, c_name.as_ptr(), 0) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn identity(file: &File, display: &Path) -> Result<FileIdentity, SyntheticReplacementError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        Ok(FileIdentity {
            volume: metadata.dev(),
            file: metadata.ino(),
        })
    }

    fn c_string(name: &OsStr) -> Result<CString, SyntheticReplacementError> {
        CString::new(name.as_bytes()).map_err(|_| SyntheticReplacementError::InvalidTarget)
    }

    fn lock_exclusive(file: &File, display: &Path) -> Result<(), SyntheticReplacementError> {
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn digest(bytes: &[u8]) -> String {
        hex::encode(Sha256::digest(bytes))
    }
    fn digest_text_invalid(value: &str) -> bool {
        value.len() != 64
            || !value
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    }
    fn inject(
        actual: Option<FaultPoint>,
        point: FaultPoint,
    ) -> Result<(), SyntheticReplacementError> {
        if actual == Some(point) {
            Err(SyntheticReplacementError::Injected(point))
        } else {
            Ok(())
        }
    }

    #[cfg(test)]
    pub(super) fn replace_with_fault<P: RecoveryKeyProvider + ?Sized>(
        approval: &SyntheticRootApproval,
        target: &Path,
        desired: &[u8],
        transaction_id: ReplacementTransactionId,
        provider: &mut P,
        validation: SyntheticValidation,
        fault: FaultPoint,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        replace(
            approval,
            target,
            desired,
            transaction_id,
            provider,
            validation,
            Some(fault),
        )
    }

    #[cfg(test)]
    pub(super) fn recover_with_fault<P: RecoveryKeyProvider + ?Sized>(
        approval: &SyntheticRootApproval,
        transaction_id: ReplacementTransactionId,
        provider: &mut P,
        validation: SyntheticValidation,
        fault: FaultPoint,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        recover(approval, transaction_id, provider, validation, Some(fault))
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;
    pub(super) fn replace<P: RecoveryKeyProvider + ?Sized>(
        _approval: &SyntheticRootApproval,
        _target: &Path,
        _desired: &[u8],
        _transaction_id: ReplacementTransactionId,
        _provider: &mut P,
        _validation: SyntheticValidation,
        _fault: Option<FaultPoint>,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        Err(SyntheticReplacementError::UnsupportedPlatform)
    }
    pub(super) fn recover<P: RecoveryKeyProvider + ?Sized>(
        _approval: &SyntheticRootApproval,
        _transaction_id: ReplacementTransactionId,
        _provider: &mut P,
        _validation: SyntheticValidation,
        _fault: Option<FaultPoint>,
    ) -> Result<ReplacementReceipt, SyntheticReplacementError> {
        Err(SyntheticReplacementError::UnsupportedPlatform)
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use crate::live::{
        backup::BackupKey,
        recovery_key::{CreatedRecoveryKey, RecoveryKeyDeleteOutcome, RecoveryKeyOpenOutcome},
    };
    use std::{
        fs::{self, File, OpenOptions},
        io::{Seek, SeekFrom, Write},
        os::unix::fs::{MetadataExt, PermissionsExt},
        sync::{
            atomic::{AtomicUsize, Ordering},
            Arc, Barrier,
        },
        thread,
    };
    use tempfile::TempDir;
    use zeroize::Zeroizing;

    struct TestProvider {
        byte: u8,
        idempotent: bool,
        creates: AtomicUsize,
        opens: AtomicUsize,
    }

    impl TestProvider {
        fn new(byte: u8) -> Self {
            Self {
                byte,
                idempotent: true,
                creates: AtomicUsize::new(0),
                opens: AtomicUsize::new(0),
            }
        }

        fn key_for(&self, transaction_id: ReplacementTransactionId) -> [u8; 32] {
            let mut key = [self.byte; 32];
            for (index, byte) in key.iter_mut().enumerate() {
                *byte ^= transaction_id.as_bytes()[index % 16];
            }
            key
        }

        fn reference_for(&self, transaction_id: ReplacementTransactionId) -> RecoveryKeyReference {
            let mut opaque = b"opaque-reference".to_vec();
            opaque.extend_from_slice(transaction_id.as_bytes());
            RecoveryKeyReference::new("synthetic-test", 1, opaque).unwrap()
        }
    }

    impl RecoveryKeyProvider for TestProvider {
        fn create_is_transaction_idempotent(&self) -> bool {
            self.idempotent
        }

        fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
            self.creates.fetch_add(1, Ordering::SeqCst);
            RecoveryKeyCreateOutcome::Created(CreatedRecoveryKey {
                reference: self.reference_for(transaction_id),
                key: BackupKey::from_bytes(Zeroizing::new(self.key_for(transaction_id))),
            })
        }
        fn open(
            &mut self,
            transaction_id: ReplacementTransactionId,
            reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            self.opens.fetch_add(1, Ordering::SeqCst);
            if reference == &self.reference_for(transaction_id) {
                RecoveryKeyOpenOutcome::Available(BackupKey::from_bytes(Zeroizing::new(
                    self.key_for(transaction_id),
                )))
            } else {
                RecoveryKeyOpenOutcome::WrongIdentity
            }
        }
        fn delete(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            RecoveryKeyDeleteOutcome::Unavailable
        }
    }

    struct BlockingProvider {
        inner: TestProvider,
        entered: Arc<Barrier>,
        release: Arc<Barrier>,
    }

    impl RecoveryKeyProvider for BlockingProvider {
        fn create_is_transaction_idempotent(&self) -> bool {
            true
        }

        fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
            self.entered.wait();
            self.release.wait();
            self.inner.create(transaction_id)
        }

        fn open(
            &mut self,
            transaction_id: ReplacementTransactionId,
            reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            self.inner.open(transaction_id, reference)
        }

        fn delete(
            &mut self,
            transaction_id: ReplacementTransactionId,
            reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            self.inner.delete(transaction_id, reference)
        }
    }

    fn fixture() -> (TempDir, SyntheticRootApproval) {
        let root = tempfile::tempdir().unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        fs::write(root.path().join("config.json"), b"old-employee-key").unwrap();
        fs::set_permissions(
            root.path().join("config.json"),
            fs::Permissions::from_mode(0o600),
        )
        .unwrap();
        let approval = SyntheticRootApproval::for_test(root.path());
        (root, approval)
    }

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }

    fn workspace(root: &Path, tx: ReplacementTransactionId) -> PathBuf {
        root.join(REPLACEMENT_DIRECTORY)
            .join(format!("tx-{}", transaction_text(tx)))
    }

    fn simulate_torn_event(root: &Path, tx: ReplacementTransactionId, event: JournalEvent) {
        let workspace = workspace(root, tx);
        let mut bytes = serde_json::to_vec(&event).unwrap();
        bytes.push(b'\n');
        let journal_path = workspace.join(JOURNAL_FILE);
        let journal_bytes = fs::read(&journal_path).unwrap();
        let event_count = journal_bytes.iter().filter(|byte| **byte == b'\n').count();
        let pending = workspace.join(platform::pending_slot_name(event_count));
        let mut pending_file = OpenOptions::new()
            .read(true)
            .write(true)
            .open(&pending)
            .unwrap();
        pending_file.seek(SeekFrom::Start(0)).unwrap();
        pending_file
            .write_all(&platform::encode_pending_event(&bytes).unwrap())
            .unwrap();
        pending_file.sync_all().unwrap();
        let mut journal = OpenOptions::new().append(true).open(journal_path).unwrap();
        journal.write_all(&bytes[..bytes.len() / 2]).unwrap();
        journal.sync_all().unwrap();
    }

    #[test]
    fn committed_interruptions_recover_idempotently() {
        for (index, fault) in [
            FaultPoint::AfterCommitPrepared,
            FaultPoint::AfterExchangeBeforeVerify,
            FaultPoint::AfterPublish,
            FaultPoint::AfterPublished,
            FaultPoint::AfterVerified,
            FaultPoint::AfterTerminal,
        ]
        .into_iter()
        .enumerate()
        {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0x40 + index as u8);
            let tx = transaction(1 + index as u8);
            assert!(platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                tx,
                &mut provider,
                SyntheticValidation::Accept,
                fault
            )
            .is_err());
            let receipt = recover_existing_synthetic(
                &approval,
                tx,
                &mut provider,
                SyntheticValidation::Accept,
            )
            .unwrap();
            assert_eq!(receipt.outcome, ReplacementOutcome::Complete);
            assert_eq!(
                fs::read(root.path().join("config.json")).unwrap(),
                b"new-employee-key"
            );
            let opens = provider.opens.load(Ordering::SeqCst);
            assert_eq!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept
                )
                .unwrap(),
                receipt
            );
            assert_eq!(provider.opens.load(Ordering::SeqCst), opens);
        }
    }

    #[test]
    fn torn_post_mutation_journal_tail_is_repaired_from_durable_pending_event() {
        for (index, validation, fault, event, outcome) in [
            (
                0,
                SyntheticValidation::Accept,
                FaultPoint::AfterPublish,
                JournalEvent::Published,
                ReplacementOutcome::Complete,
            ),
            (
                1,
                SyntheticValidation::Reject,
                FaultPoint::AfterRestore,
                JournalEvent::Restored,
                ReplacementOutcome::RolledBack,
            ),
            (
                2,
                SyntheticValidation::Accept,
                FaultPoint::AfterVerified,
                JournalEvent::Complete,
                ReplacementOutcome::Complete,
            ),
            (
                3,
                SyntheticValidation::Reject,
                FaultPoint::AfterRestored,
                JournalEvent::RolledBack,
                ReplacementOutcome::RolledBack,
            ),
        ] {
            let (root, approval) = fixture();
            let tx = transaction(100 + index);
            let mut provider = TestProvider::new(0xb0 + index);
            assert!(platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                tx,
                &mut provider,
                validation,
                fault,
            )
            .is_err());
            let journal_path = workspace(root.path(), tx).join(JOURNAL_FILE);
            let reserved_blocks = fs::metadata(&journal_path).unwrap().blocks();
            simulate_torn_event(root.path(), tx, event);
            let receipt =
                recover_existing_synthetic(&approval, tx, &mut provider, validation).unwrap();
            assert_eq!(receipt.outcome, outcome);
            assert!(!workspace(root.path(), tx).join(PENDING_EVENT_FILE).exists());
            assert!(fs::metadata(journal_path).unwrap().blocks() >= reserved_blocks);
        }
    }

    #[test]
    fn torn_rollback_decision_and_stage_records_recover_from_reserved_slots() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xb8);
        let tx = transaction(108);
        assert!(platform::replace_with_fault(
            &approval,
            Path::new("config.json"),
            b"new-employee-key",
            tx,
            &mut provider,
            SyntheticValidation::Reject,
            FaultPoint::AfterPublished,
        )
        .is_err());
        simulate_torn_event(root.path(), tx, JournalEvent::RollbackPrepared);
        assert_eq!(
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Reject,)
                .unwrap()
                .outcome,
            ReplacementOutcome::RolledBack
        );

        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xb9);
        let tx = transaction(109);
        assert!(platform::replace_with_fault(
            &approval,
            Path::new("config.json"),
            b"new-employee-key",
            tx,
            &mut provider,
            SyntheticValidation::Reject,
            FaultPoint::AfterRollbackStagePublish,
        )
        .is_err());
        let metadata = fs::metadata(workspace(root.path(), tx).join(ROLLBACK_STAGE)).unwrap();
        simulate_torn_event(
            root.path(),
            tx,
            JournalEvent::RollbackStaged {
                identity: FileIdentity {
                    volume: metadata.dev(),
                    file: metadata.ino(),
                },
            },
        );
        assert_eq!(
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Reject,)
                .unwrap()
                .outcome,
            ReplacementOutcome::RolledBack
        );
    }

    #[test]
    fn exchange_detects_and_undoes_edit_in_check_to_syscall_window() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xc1);
        let tx = transaction(111);
        assert!(matches!(
            platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                tx,
                &mut provider,
                SyntheticValidation::Accept,
                FaultPoint::ExternalEditBeforeExchange,
            ),
            Err(SyntheticReplacementError::RecoveryConflict)
        ));
        assert_eq!(
            fs::read(root.path().join("config.json")).unwrap(),
            b"external-window-edit"
        );
    }

    #[test]
    fn restart_undoes_unverified_exchange_for_unsafe_external_entries() {
        for (index, fault) in [
            FaultPoint::ExternalEditThenCrashAfterExchange,
            FaultPoint::ExternalSymlinkThenCrashAfterExchange,
            FaultPoint::ExternalPublicModeThenCrashAfterExchange,
            FaultPoint::ExternalHardlinkThenCrashAfterExchange,
        ]
        .into_iter()
        .enumerate()
        {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0xc4 + index as u8);
            let tx = transaction(114 + index as u8);
            assert!(matches!(
                platform::replace_with_fault(
                    &approval,
                    Path::new("config.json"),
                    b"new-employee-key",
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept,
                    fault,
                ),
                Err(SyntheticReplacementError::Injected(point)) if point == fault
            ));
            assert!(matches!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept,
                ),
                Err(SyntheticReplacementError::RecoveryConflict)
            ));
            let target = root.path().join("config.json");
            match fault {
                FaultPoint::ExternalSymlinkThenCrashAfterExchange => {
                    assert!(fs::symlink_metadata(&target)
                        .unwrap()
                        .file_type()
                        .is_symlink());
                }
                FaultPoint::ExternalPublicModeThenCrashAfterExchange => {
                    assert_eq!(fs::metadata(&target).unwrap().mode() & 0o7777, 0o644);
                    assert_eq!(fs::read(&target).unwrap(), b"external-window-edit");
                }
                FaultPoint::ExternalHardlinkThenCrashAfterExchange => {
                    assert_eq!(fs::metadata(&target).unwrap().nlink(), 2);
                    assert_eq!(fs::read(&target).unwrap(), b"external-window-edit");
                }
                FaultPoint::ExternalEditThenCrashAfterExchange => {
                    assert_eq!(fs::read(&target).unwrap(), b"external-window-edit");
                }
                _ => unreachable!(),
            }
            assert!(matches!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept,
                ),
                Err(SyntheticReplacementError::RecoveryConflict)
            ));
        }
    }

    #[test]
    fn restart_undoes_unsafe_external_entry_raced_into_rollback_exchange() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xca);
        let tx = transaction(119);
        assert!(matches!(
            platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"rejected-new-key",
                tx,
                &mut provider,
                SyntheticValidation::Reject,
                FaultPoint::ExternalSymlinkThenCrashAfterRollbackExchange,
            ),
            Err(SyntheticReplacementError::Injected(
                FaultPoint::ExternalSymlinkThenCrashAfterRollbackExchange
            ))
        ));
        assert!(matches!(
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Reject,),
            Err(SyntheticReplacementError::RecoveryConflict)
        ));
        assert!(fs::symlink_metadata(root.path().join("config.json"))
            .unwrap()
            .file_type()
            .is_symlink());
    }

    #[test]
    fn shared_target_lock_blocks_a_second_transaction() {
        let (root, approval) = fixture();
        let entered = Arc::new(Barrier::new(2));
        let release = Arc::new(Barrier::new(2));
        let first_approval = approval.clone();
        let first_entered = Arc::clone(&entered);
        let first_release = Arc::clone(&release);
        let first = thread::spawn(move || {
            let mut provider = BlockingProvider {
                inner: TestProvider::new(0xc2),
                entered: first_entered,
                release: first_release,
            };
            replace_existing_synthetic(
                &first_approval,
                Path::new("config.json"),
                b"first-new-key",
                transaction(112),
                &mut provider,
                SyntheticValidation::Accept,
            )
        });
        entered.wait();
        let mut second_provider = TestProvider::new(0xc3);
        assert!(matches!(
            replace_existing_synthetic(
                &approval,
                Path::new("config.json"),
                b"second-new-key",
                transaction(113),
                &mut second_provider,
                SyntheticValidation::Accept,
            ),
            Err(SyntheticReplacementError::Io { .. })
        ));
        assert_eq!(second_provider.creates.load(Ordering::SeqCst), 0);
        assert_eq!(
            fs::read(root.path().join("config.json")).unwrap(),
            b"old-employee-key"
        );
        release.wait();
        assert_eq!(
            first.join().unwrap().unwrap().outcome,
            ReplacementOutcome::Complete
        );
    }

    #[test]
    fn precommit_interruptions_never_publish() {
        for (index, fault) in [
            FaultPoint::AfterKeyCreateIntent,
            FaultPoint::AfterKeyCreate,
            FaultPoint::AfterBegin,
            FaultPoint::AfterBackup,
            FaultPoint::AfterStage,
        ]
        .into_iter()
        .enumerate()
        {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0x50 + index as u8);
            let tx = transaction(20 + index as u8);
            assert!(platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                tx,
                &mut provider,
                SyntheticValidation::Accept,
                fault
            )
            .is_err());
            assert!(matches!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept
                ),
                Err(SyntheticReplacementError::Uncommitted)
            ));
            assert_eq!(
                fs::read(root.path().join("config.json")).unwrap(),
                b"old-employee-key"
            );
        }
    }

    #[test]
    fn key_create_intent_makes_an_unrecorded_idempotent_create_recoverable() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xd1);
        let tx = transaction(120);
        assert!(matches!(
            platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                tx,
                &mut provider,
                SyntheticValidation::Accept,
                FaultPoint::AfterKeyCreate,
            ),
            Err(SyntheticReplacementError::Injected(
                FaultPoint::AfterKeyCreate
            ))
        ));
        assert_eq!(provider.creates.load(Ordering::SeqCst), 1);
        assert!(matches!(
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Accept,),
            Err(SyntheticReplacementError::Uncommitted)
        ));
        assert_eq!(provider.creates.load(Ordering::SeqCst), 2);
        let journal = fs::read(workspace(root.path(), tx).join(JOURNAL_FILE)).unwrap();
        assert!(journal
            .windows(b"key_created".len())
            .any(|window| window == b"key_created"));
        assert_eq!(
            fs::read(root.path().join("config.json")).unwrap(),
            b"old-employee-key"
        );
    }

    #[test]
    fn provider_contract_is_transaction_scoped_and_non_idempotent_providers_are_rejected() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xd4);
        let first = transaction(123);
        let second = transaction(124);
        assert_eq!(provider.reference_for(first), provider.reference_for(first));
        assert_ne!(
            provider.reference_for(first),
            provider.reference_for(second)
        );
        assert_eq!(provider.key_for(first), provider.key_for(first));
        assert_ne!(provider.key_for(first), provider.key_for(second));

        provider.idempotent = false;
        assert!(matches!(
            replace_existing_synthetic(
                &approval,
                Path::new("config.json"),
                b"new-employee-key",
                first,
                &mut provider,
                SyntheticValidation::Accept,
            ),
            Err(SyntheticReplacementError::KeyCreateNotIdempotent)
        ));
        assert_eq!(provider.creates.load(Ordering::SeqCst), 0);
        assert!(!root.path().join(REPLACEMENT_DIRECTORY).exists());
    }

    #[test]
    fn rollback_reservation_enospc_fails_before_commit_or_publication() {
        for (index, fault) in [
            FaultPoint::RollbackReservationFailure,
            FaultPoint::PendingSlotReservationFailure,
            FaultPoint::JournalReservationFailure,
        ]
        .into_iter()
        .enumerate()
        {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0xd2 + index as u8);
            let tx = transaction(121 + index as u8);
            assert!(matches!(
                platform::replace_with_fault(
                    &approval,
                    Path::new("config.json"),
                    b"new-employee-key",
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept,
                    fault,
                ),
                Err(SyntheticReplacementError::Io { .. })
            ));
            assert_eq!(
                fs::read(root.path().join("config.json")).unwrap(),
                b"old-employee-key"
            );
            assert!(matches!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept,
                ),
                Err(SyntheticReplacementError::Uncommitted)
            ));
            if fault == FaultPoint::PendingSlotReservationFailure {
                assert!(!workspace(root.path(), tx)
                    .join(PENDING_SLOTS_READY_FILE)
                    .exists());
            }
        }
    }

    #[test]
    fn commit_prepares_physical_journal_and_pending_slot_capacity() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xd5);
        let tx = transaction(125);
        assert!(platform::replace_with_fault(
            &approval,
            Path::new("config.json"),
            b"new-employee-key",
            tx,
            &mut provider,
            SyntheticValidation::Accept,
            FaultPoint::AfterCommitPrepared,
        )
        .is_err());
        let workspace = workspace(root.path(), tx);
        assert!(workspace.join(PENDING_SLOTS_READY_FILE).is_file());
        let journal = fs::metadata(workspace.join(JOURNAL_FILE)).unwrap();
        assert!(journal.blocks() * 512 >= MAX_JOURNAL_BYTES as u64);
        for index in 0..MAX_EVENTS {
            let slot = fs::metadata(workspace.join(platform::pending_slot_name(index))).unwrap();
            assert_eq!(slot.len(), PENDING_EVENT_CAPACITY as u64);
            assert!(slot.blocks() * 512 >= PENDING_EVENT_CAPACITY as u64);
        }
    }

    #[test]
    fn partial_preallocated_rollback_stage_is_rewritten_and_adopted() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xd3);
        let tx = transaction(122);
        assert!(platform::replace_with_fault(
            &approval,
            Path::new("config.json"),
            b"rejected-new-key",
            tx,
            &mut provider,
            SyntheticValidation::Reject,
            FaultPoint::AfterRollbackStageIntent,
        )
        .is_err());
        let pending = workspace(root.path(), tx).join(ROLLBACK_STAGE_PENDING);
        let mut file = OpenOptions::new().write(true).open(&pending).unwrap();
        file.write_all(b"bad").unwrap();
        file.sync_all().unwrap();
        let receipt =
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Reject)
                .unwrap();
        assert_eq!(receipt.outcome, ReplacementOutcome::RolledBack);
        assert_eq!(
            fs::read(root.path().join("config.json")).unwrap(),
            b"old-employee-key"
        );
    }

    #[test]
    fn rollback_interruptions_restore_original_bytes_and_recover_idempotently() {
        for (index, fault) in [
            FaultPoint::AfterPublished,
            FaultPoint::AfterRollbackPrepared,
            FaultPoint::AfterRollbackStageIntent,
            FaultPoint::AfterRollbackStageWrite,
            FaultPoint::AfterRollbackStagePublish,
            FaultPoint::AfterRollbackStage,
            FaultPoint::AfterRestore,
            FaultPoint::AfterRestored,
            FaultPoint::AfterTerminal,
        ]
        .into_iter()
        .enumerate()
        {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0x60 + index as u8);
            let tx = transaction(40 + index as u8);
            assert!(platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"rejected-new-key",
                tx,
                &mut provider,
                SyntheticValidation::Reject,
                fault
            )
            .is_err());
            let receipt = recover_existing_synthetic(
                &approval,
                tx,
                &mut provider,
                SyntheticValidation::Reject,
            )
            .unwrap();
            assert_eq!(receipt.outcome, ReplacementOutcome::RolledBack);
            assert_eq!(
                fs::read(root.path().join("config.json")).unwrap(),
                b"old-employee-key"
            );
            assert_eq!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Reject
                )
                .unwrap(),
                receipt
            );
        }
    }

    #[test]
    fn read_only_private_modes_can_publish_and_rollback_without_reopen_failure() {
        for (index, mode) in [0o400, 0o500].into_iter().enumerate() {
            let (root, approval) = fixture();
            let target = root.path().join("config.json");
            fs::set_permissions(&target, fs::Permissions::from_mode(mode)).unwrap();
            let mut provider = TestProvider::new(0xe0 + index as u8);
            let receipt = replace_existing_synthetic(
                &approval,
                Path::new("config.json"),
                b"rejected-new-key",
                transaction(126 + index as u8),
                &mut provider,
                SyntheticValidation::Reject,
            )
            .unwrap();
            assert_eq!(receipt.outcome, ReplacementOutcome::RolledBack);
            assert_eq!(fs::read(&target).unwrap(), b"old-employee-key");
            assert_eq!(fs::metadata(&target).unwrap().mode() & 0o7777, mode);
        }
    }

    #[test]
    fn stale_symlink_and_hardlink_targets_are_preserved_as_conflicts() {
        for case in 0..3 {
            let (root, approval) = fixture();
            let mut provider = TestProvider::new(0x70 + case);
            let tx = transaction(70 + case);
            assert!(platform::replace_with_fault(
                &approval,
                Path::new("config.json"),
                b"new-key",
                tx,
                &mut provider,
                SyntheticValidation::Accept,
                FaultPoint::AfterCommitPrepared
            )
            .is_err());
            let target = root.path().join("config.json");
            match case {
                0 => {
                    fs::rename(&target, root.path().join("old.saved")).unwrap();
                    fs::write(&target, b"external-edit").unwrap();
                    fs::set_permissions(&target, fs::Permissions::from_mode(0o600)).unwrap();
                }
                1 => {
                    fs::rename(&target, root.path().join("old.saved")).unwrap();
                    std::os::unix::fs::symlink("old.saved", &target).unwrap();
                }
                _ => {
                    fs::hard_link(&target, root.path().join("extra-link")).unwrap();
                }
            }
            assert!(matches!(
                recover_existing_synthetic(
                    &approval,
                    tx,
                    &mut provider,
                    SyntheticValidation::Accept
                ),
                Err(SyntheticReplacementError::RecoveryConflict)
            ));
            if case == 0 {
                assert_eq!(fs::read(&target).unwrap(), b"external-edit");
            }
            if case == 1 {
                assert!(fs::symlink_metadata(&target)
                    .unwrap()
                    .file_type()
                    .is_symlink());
            }
            if case == 2 {
                assert_eq!(fs::metadata(&target).unwrap().nlink(), 2);
            }
        }
    }

    #[test]
    fn rollback_refuses_to_overwrite_an_external_post_publish_edit() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0x81);
        let tx = transaction(81);
        assert!(platform::replace_with_fault(
            &approval,
            Path::new("config.json"),
            b"rejected-new-key",
            tx,
            &mut provider,
            SyntheticValidation::Reject,
            FaultPoint::AfterPublished
        )
        .is_err());
        let target = root.path().join("config.json");
        fs::rename(&target, root.path().join("published.saved")).unwrap();
        fs::write(&target, b"external-after-publish").unwrap();
        fs::set_permissions(&target, fs::Permissions::from_mode(0o600)).unwrap();
        assert!(matches!(
            recover_existing_synthetic(&approval, tx, &mut provider, SyntheticValidation::Reject),
            Err(SyntheticReplacementError::RecoveryConflict)
                | Err(SyntheticReplacementError::Backup(
                    BackupError::RecoveryConflict(_)
                ))
        ));
        assert_eq!(fs::read(target).unwrap(), b"external-after-publish");
    }

    #[test]
    fn durable_metadata_does_not_contain_old_plaintext_or_recovery_key() {
        let (root, approval) = fixture();
        let mut provider = TestProvider::new(0xa5);
        let tx = transaction(90);
        let recovery_key = provider.key_for(tx);
        let receipt = replace_existing_synthetic(
            &approval,
            Path::new("config.json"),
            b"new-employee-key",
            tx,
            &mut provider,
            SyntheticValidation::Accept,
        )
        .unwrap();
        assert_eq!(receipt.outcome, ReplacementOutcome::Complete);
        let mut stack = vec![root.path().to_path_buf()];
        while let Some(path) = stack.pop() {
            for entry in fs::read_dir(path).unwrap() {
                let entry = entry.unwrap();
                let path = entry.path();
                if path.is_dir() {
                    stack.push(path);
                    continue;
                }
                if path == root.path().join("config.json") {
                    continue;
                }
                let bytes = fs::read(path).unwrap();
                assert!(!bytes
                    .windows(b"old-employee-key".len())
                    .any(|window| window == b"old-employee-key"));
                assert!(!bytes.windows(32).any(|window| window == recovery_key));
            }
        }
    }
}
