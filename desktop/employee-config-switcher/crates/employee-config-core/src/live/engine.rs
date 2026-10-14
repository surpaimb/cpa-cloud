//! Fail-closed file publication primitives.
//!
//! The first writable primitive is deliberately create-only. It can publish
//! files that did not exist when the transaction started, but it never
//! replaces or deletes a pre-existing client file. This means crash recovery
//! does not need to persist a plaintext copy of an old employee key. The Tauri
//! apply command remains disabled until the wider adapter audit is complete.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashSet};
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::path::{Component, Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Mutex, MutexGuard};
use std::time::{SystemTime, UNIX_EPOCH};
use zeroize::Zeroizing;

const TRANSACTION_DIRECTORY: &str = ".cpa-cloud-employee-config-transactions";
const TRANSACTION_LOCK: &str = ".cpa-cloud-employee-config.lock";
const JOURNAL_FILE: &str = "journal.jsonl";
const MAX_FILE_BYTES: usize = 4 * 1024 * 1024;
const MAX_TRANSACTION_FILES: usize = 64;
const MAX_JOURNAL_BYTES: usize = 512 * 1024;
const MAX_JOURNAL_LINE_BYTES: usize = 8 * 1024;
const MAX_JOURNAL_EVENTS: usize = 4 * MAX_TRANSACTION_FILES + 2;
const MAX_JOURNAL_TARGET_BYTES: usize = 4 * 1024;
const MAX_JOURNAL_STAGE_BYTES: usize = 32;
const SHA256_HEX_BYTES: usize = 64;
static TRANSACTION_COUNTER: AtomicU64 = AtomicU64::new(0);
static PROCESS_TRANSACTION_LOCK: Mutex<()> = Mutex::new(());

pub fn digest(contents: Option<&[u8]>) -> Option<String> {
    contents.map(|bytes| hex::encode(Sha256::digest(bytes)))
}

/// Secret-bearing bytes for a path that must not already exist.
///
/// The buffer is zeroized on drop and intentionally cannot be debug-printed or
/// serialized.
pub struct CreateFilePlan {
    relative_path: PathBuf,
    contents: Zeroizing<Vec<u8>>,
}

impl CreateFilePlan {
    pub fn new(relative_path: impl Into<PathBuf>, contents: Vec<u8>) -> Self {
        Self {
            relative_path: relative_path.into(),
            contents: Zeroizing::new(contents),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CreateOnlyReceipt {
    pub created: Vec<PathBuf>,
}

#[derive(Debug, thiserror::Error)]
pub enum TransactionError {
    #[error("transaction root is not a real directory: {0}")]
    InvalidRoot(PathBuf),
    #[error("target must be a non-empty relative path without traversal: {0}")]
    InvalidTarget(PathBuf),
    #[error("path contains a symbolic link or Windows reparse point: {0}")]
    LinkedPath(PathBuf),
    #[error("target already exists; create-only publication refuses to replace it: {0}")]
    Collision(PathBuf),
    #[error("recovery found an external edit and will not overwrite it: {0}")]
    RecoveryConflict(PathBuf),
    #[error("transaction journal is corrupt or has an invalid transition: {0}")]
    CorruptJournal(PathBuf),
    #[error("configuration exceeds the create-only safety limit: {0}")]
    TooLarge(PathBuf),
    #[error("filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[doc(hidden)]
    #[error("injected crash")]
    InjectedCrash,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct FileIdentity {
    volume: u64,
    file: u64,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(tag = "state", rename_all = "snake_case", deny_unknown_fields)]
enum JournalEvent {
    Begin,
    Planned {
        index: usize,
        target: PathBuf,
        stage: String,
        digest: String,
        parent_identity: FileIdentity,
    },
    Staged {
        index: usize,
        stage_identity: FileIdentity,
    },
    Published {
        index: usize,
        target_identity: FileIdentity,
    },
    Verified {
        index: usize,
    },
    Complete,
}

#[derive(Debug)]
struct PlannedFile {
    target: PathBuf,
    stage: String,
    digest: String,
    stage_identity: Option<FileIdentity>,
    parent_identity: FileIdentity,
    target_identity: Option<FileIdentity>,
    verified: bool,
}

#[derive(Debug, Default)]
struct JournalState {
    begun: bool,
    files: BTreeMap<usize, PlannedFile>,
    complete: bool,
}

struct TransactionGuard {
    _process: MutexGuard<'static, ()>,
    _file: File,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum FaultPoint {
    Never,
    AfterWorkspace,
    AfterJournalCreated,
    AfterBegin,
    AfterBeginIoFailure,
    AfterManifest,
    AfterPlanning,
    AfterStageBeforeJournal(usize),
    AfterPublishBeforeJournal(usize),
    AfterPublished(usize),
    AfterVerified(usize),
}

/// Recover incomplete create-only transactions below `root`.
///
/// Recovery never deletes or replaces a published target. If every private
/// stage is durable it verifies identities and digests, then safely rolls the
/// create-only operation forward. An external edit blocks later writes.
pub fn recover_create_only(root: &Path) -> Result<(), TransactionError> {
    let root = checked_root(root)?;
    let _guard = acquire_transaction_lock(&root)?;
    recover_create_only_locked(&root)
}

fn recover_create_only_locked(root: &Path) -> Result<(), TransactionError> {
    let transaction_root = root.join(TRANSACTION_DIRECTORY);
    match fs::symlink_metadata(&transaction_root) {
        Ok(_) => validate_private_recovery_directory(&transaction_root)?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(source) => return Err(io_error(&transaction_root, source)),
    }
    for entry in
        fs::read_dir(&transaction_root).map_err(|source| io_error(&transaction_root, source))?
    {
        let path = entry
            .map_err(|source| io_error(&transaction_root, source))?
            .path();
        validate_workspace_path(&path)?;
        validate_private_recovery_directory(&path)?;
        let _ = recover_workspace(root, &path)?;
    }
    sync_directory(&transaction_root).map_err(|source| io_error(&transaction_root, source))
}

/// Atomically create files whose parent directories already exist.
pub fn apply_create_only(
    root: &Path,
    files: Vec<CreateFilePlan>,
) -> Result<CreateOnlyReceipt, TransactionError> {
    apply_create_only_inner(root, files, FaultPoint::Never)
}

fn apply_create_only_inner(
    root: &Path,
    files: Vec<CreateFilePlan>,
    fault: FaultPoint,
) -> Result<CreateOnlyReceipt, TransactionError> {
    let root = checked_root(root)?;
    let _guard = acquire_transaction_lock(&root)?;
    recover_create_only_locked(&root)?;
    validate_plans(&root, &files)?;

    let transaction_root = ensure_transaction_root(&root)?;
    let workspace = create_workspace(&transaction_root)?;
    if fault == FaultPoint::AfterWorkspace {
        return Err(TransactionError::InjectedCrash);
    }
    let journal_path = workspace.join(JOURNAL_FILE);
    let mut journal = create_private_file(&journal_path)?;
    if fault == FaultPoint::AfterJournalCreated {
        return Err(TransactionError::InjectedCrash);
    }
    let mut created = Vec::with_capacity(files.len());

    let result = (|| {
        append_event(&mut journal, &journal_path, &JournalEvent::Begin)?;
        if fault == FaultPoint::AfterBegin {
            return Err(TransactionError::InjectedCrash);
        }
        if fault == FaultPoint::AfterBeginIoFailure {
            return Err(io_error(
                &journal_path,
                io::Error::other("injected pre-plan I/O failure"),
            ));
        }
        for (index, plan) in files.iter().enumerate() {
            let target = checked_target(&root, &plan.relative_path)?;
            let parent = target
                .parent()
                .ok_or_else(|| TransactionError::InvalidTarget(plan.relative_path.clone()))?;
            let stage = format!("stage-{index:04}.bin");
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Planned {
                    index,
                    target: plan.relative_path.clone(),
                    stage,
                    digest: digest(Some(&plan.contents)).expect("bytes have a digest"),
                    parent_identity: identity_for_path(parent)?,
                },
            )?;
        }
        // Make the journal directory entry and the complete non-secret stage
        // manifest durable before any secret-bearing stage file is created.
        sync_directory(&workspace).map_err(|source| io_error(&workspace, source))?;
        if fault == FaultPoint::AfterManifest {
            return Err(TransactionError::InjectedCrash);
        }
        for (index, plan) in files.iter().enumerate() {
            let stage_path = workspace.join(format!("stage-{index:04}.bin"));
            let mut stage_file = create_private_file(&stage_path)?;
            stage_file
                .write_all(&plan.contents)
                .map_err(|source| io_error(&stage_path, source))?;
            stage_file
                .sync_all()
                .map_err(|source| io_error(&stage_path, source))?;
            // Persist the private stage directory entry before the journal can
            // claim that the stage is available for roll-forward recovery.
            sync_directory(&workspace).map_err(|source| io_error(&workspace, source))?;
            if fault == FaultPoint::AfterStageBeforeJournal(index) {
                return Err(TransactionError::InjectedCrash);
            }
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Staged {
                    index,
                    stage_identity: file_identity(&stage_file, &stage_path)?,
                },
            )?;
        }
        sync_directory(&workspace).map_err(|source| io_error(&workspace, source))?;
        if fault == FaultPoint::AfterPlanning {
            return Err(TransactionError::InjectedCrash);
        }

        for (index, plan) in files.iter().enumerate() {
            let target = checked_target(&root, &plan.relative_path)?;
            let parent = target
                .parent()
                .ok_or_else(|| TransactionError::InvalidTarget(plan.relative_path.clone()))?;
            let state = read_journal(&journal_path)?;
            let planned = state
                .files
                .get(&index)
                .ok_or_else(|| TransactionError::CorruptJournal(journal_path.clone()))?;
            if identity_for_path(parent)? != planned.parent_identity {
                return Err(TransactionError::RecoveryConflict(parent.to_path_buf()));
            }
            let stage_identity = planned
                .stage_identity
                .ok_or_else(|| TransactionError::CorruptJournal(journal_path.clone()))?;
            ensure_absent(&target)?;

            let stage_path = workspace.join(&planned.stage);
            let stage_file =
                File::open(&stage_path).map_err(|source| io_error(&stage_path, source))?;
            if file_identity(&stage_file, &stage_path)? != stage_identity {
                return Err(TransactionError::RecoveryConflict(stage_path));
            }
            fs::hard_link(&stage_path, &target).map_err(|source| {
                if source.kind() == io::ErrorKind::AlreadyExists {
                    TransactionError::Collision(target.clone())
                } else {
                    io_error(&target, source)
                }
            })?;
            sync_directory(parent).map_err(|source| io_error(parent, source))?;
            if fault == FaultPoint::AfterPublishBeforeJournal(index) {
                return Err(TransactionError::InjectedCrash);
            }

            let target_file = File::open(&target).map_err(|source| io_error(&target, source))?;
            let target_identity = file_identity(&target_file, &target)?;
            if target_identity != stage_identity {
                return Err(TransactionError::RecoveryConflict(target));
            }
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Published {
                    index,
                    target_identity,
                },
            )?;
            if fault == FaultPoint::AfterPublished(index) {
                return Err(TransactionError::InjectedCrash);
            }

            verify_target(&target, planned)?;
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Verified { index },
            )?;
            created.push(plan.relative_path.clone());
            if fault == FaultPoint::AfterVerified(index) {
                return Err(TransactionError::InjectedCrash);
            }
        }

        append_event(&mut journal, &journal_path, &JournalEvent::Complete)?;
        sync_directory(&workspace).map_err(|source| io_error(&workspace, source))?;
        Ok(CreateOnlyReceipt { created })
    })();

    match result {
        Ok(receipt) => {
            cleanup_workspace(&workspace, &read_journal(&journal_path)?)?;
            Ok(receipt)
        }
        Err(TransactionError::InjectedCrash) => Err(TransactionError::InjectedCrash),
        Err(error) => match recover_workspace(&root, &workspace) {
            Ok(true) => Ok(CreateOnlyReceipt {
                created: files
                    .iter()
                    .map(|plan| plan.relative_path.clone())
                    .collect(),
            }),
            Ok(false) => Err(error),
            Err(recovery_error) => Err(recovery_error),
        },
    }
}

fn acquire_transaction_lock(root: &Path) -> Result<TransactionGuard, TransactionError> {
    let process = PROCESS_TRANSACTION_LOCK
        .lock()
        .map_err(|_| io_error(root, io::Error::other("transaction mutex poisoned")))?;
    let path = root.join(TRANSACTION_LOCK);
    if let Ok(metadata) = fs::symlink_metadata(&path) {
        ensure_not_link(&path, &metadata)?;
        if !metadata.is_file() {
            return Err(TransactionError::RecoveryConflict(path));
        }
    }

    #[cfg(unix)]
    let file = {
        use std::os::fd::AsRawFd;
        use std::os::unix::fs::OpenOptionsExt;
        let file = OpenOptions::new()
            .create(true)
            .read(true)
            .write(true)
            .mode(0o600)
            .custom_flags(libc::O_NOFOLLOW)
            .open(&path)
            .map_err(|source| io_error(&path, source))?;
        let locked = unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) };
        if locked != 0 {
            return Err(io_error(&path, io::Error::last_os_error()));
        }
        file
    };

    #[cfg(windows)]
    let file = {
        use std::os::windows::fs::OpenOptionsExt;
        use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_OPEN_REPARSE_POINT;
        OpenOptions::new()
            .create(true)
            .read(true)
            .write(true)
            .share_mode(0)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(&path)
            .map_err(|source| io_error(&path, source))?
    };

    validate_transaction_lock_path(&path, &file)?;
    Ok(TransactionGuard {
        _process: process,
        _file: file,
    })
}

fn validate_transaction_lock_path(path: &Path, file: &File) -> Result<(), TransactionError> {
    enforce_private_permissions(path, file)?;
    let metadata = file.metadata().map_err(|source| io_error(path, source))?;
    ensure_not_link(path, &metadata)?;
    #[cfg(unix)]
    if identity_for_path(path)? != file_identity(file, path)? {
        return Err(TransactionError::RecoveryConflict(path.to_path_buf()));
    }
    Ok(())
}

fn validate_plans(root: &Path, files: &[CreateFilePlan]) -> Result<(), TransactionError> {
    if files.is_empty() {
        return Err(TransactionError::InvalidTarget(PathBuf::new()));
    }
    if files.len() > MAX_TRANSACTION_FILES {
        return Err(TransactionError::TooLarge(root.to_path_buf()));
    }
    let mut unique = HashSet::new();
    for plan in files {
        if plan.contents.len() > MAX_FILE_BYTES {
            return Err(TransactionError::TooLarge(plan.relative_path.clone()));
        }
        let Some(target) = plan.relative_path.to_str() else {
            return Err(TransactionError::InvalidTarget(plan.relative_path.clone()));
        };
        if target.len() > MAX_JOURNAL_TARGET_BYTES {
            return Err(TransactionError::TooLarge(plan.relative_path.clone()));
        }
        validate_relative(&plan.relative_path)?;
        if !unique.insert(plan.relative_path.clone()) {
            return Err(TransactionError::InvalidTarget(plan.relative_path.clone()));
        }
        ensure_absent(&checked_target(root, &plan.relative_path)?)?;
    }
    Ok(())
}

fn checked_root(root: &Path) -> Result<PathBuf, TransactionError> {
    let absolute = if root.is_absolute() {
        root.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|source| io_error(root, source))?
            .join(root)
    };
    ensure_real_directory(&absolute)?;
    let canonical = fs::canonicalize(&absolute).map_err(|source| io_error(&absolute, source))?;
    ensure_no_links(&canonical)?;
    Ok(canonical)
}

fn validate_relative(relative: &Path) -> Result<(), TransactionError> {
    if relative.as_os_str().is_empty()
        || relative.components().any(|part| {
            matches!(
                part,
                Component::Prefix(_)
                    | Component::RootDir
                    | Component::ParentDir
                    | Component::CurDir
            )
        })
    {
        return Err(TransactionError::InvalidTarget(relative.to_path_buf()));
    }
    Ok(())
}

fn checked_target(root: &Path, relative: &Path) -> Result<PathBuf, TransactionError> {
    validate_relative(relative)?;
    let target = root.join(relative);
    let parent = target
        .parent()
        .ok_or_else(|| TransactionError::InvalidTarget(relative.to_path_buf()))?;
    ensure_no_links(parent)?;
    ensure_real_directory(parent)?;
    let canonical_parent = fs::canonicalize(parent).map_err(|source| io_error(parent, source))?;
    if !canonical_parent.starts_with(root) {
        return Err(TransactionError::InvalidTarget(relative.to_path_buf()));
    }
    Ok(target)
}

fn ensure_absent(path: &Path) -> Result<(), TransactionError> {
    match fs::symlink_metadata(path) {
        Ok(_) => Err(TransactionError::Collision(path.to_path_buf())),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(source) => Err(io_error(path, source)),
    }
}

fn ensure_transaction_root(root: &Path) -> Result<PathBuf, TransactionError> {
    let path = root.join(TRANSACTION_DIRECTORY);
    match fs::symlink_metadata(&path) {
        Ok(_) => ensure_real_directory(&path)?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            create_private_directory(&path)?;
            sync_directory(root).map_err(|source| io_error(root, source))?;
        }
        Err(source) => return Err(io_error(&path, source)),
    }
    #[cfg(unix)]
    let directory = File::open(&path).map_err(|source| io_error(&path, source))?;
    #[cfg(windows)]
    let directory = open_windows_path(
        &path,
        windows_sys::Win32::Storage::FileSystem::FILE_READ_ATTRIBUTES,
    )?;
    enforce_private_permissions(&path, &directory)?;
    Ok(path)
}

fn create_workspace(transaction_root: &Path) -> Result<PathBuf, TransactionError> {
    for _ in 0..64 {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let count = TRANSACTION_COUNTER.fetch_add(1, Ordering::Relaxed);
        let path = transaction_root.join(format!("tx-{}-{nonce:x}-{count:x}", std::process::id()));
        match create_private_directory(&path) {
            Ok(()) => {
                sync_directory(transaction_root)
                    .map_err(|source| io_error(transaction_root, source))?;
                return Ok(path);
            }
            Err(TransactionError::Io { source, .. })
                if source.kind() == io::ErrorKind::AlreadyExists => {}
            Err(error) => return Err(error),
        }
    }
    Err(io_error(
        transaction_root,
        io::Error::new(io::ErrorKind::AlreadyExists, "transaction ID collision"),
    ))
}

fn append_event(
    journal: &mut File,
    path: &Path,
    event: &JournalEvent,
) -> Result<(), TransactionError> {
    let encoded = serde_json::to_vec(event)
        .map_err(|source| io_error(path, io::Error::other(source)))?;
    if encoded.len() + 1 > MAX_JOURNAL_LINE_BYTES {
        return Err(TransactionError::TooLarge(path.to_path_buf()));
    }
    let current_len = journal
        .metadata()
        .map_err(|source| io_error(path, source))?
        .len();
    if current_len.saturating_add(encoded.len() as u64 + 1) > MAX_JOURNAL_BYTES as u64 {
        return Err(TransactionError::TooLarge(path.to_path_buf()));
    }
    journal
        .write_all(&encoded)
        .map_err(|source| io_error(path, source))?;
    journal
        .write_all(b"\n")
        .map_err(|source| io_error(path, source))?;
    journal.sync_all().map_err(|source| io_error(path, source))
}

fn read_journal(path: &Path) -> Result<JournalState, TransactionError> {
    ensure_real_file(path)?;
    let file = File::open(path).map_err(|source| io_error(path, source))?;
    let metadata = file.metadata().map_err(|source| io_error(path, source))?;
    if metadata.len() > MAX_JOURNAL_BYTES as u64 {
        return Err(TransactionError::CorruptJournal(path.to_path_buf()));
    }
    let mut reader = BufReader::new(file).take(MAX_JOURNAL_BYTES as u64 + 1);
    let mut lines = Vec::new();
    let mut total_bytes = 0usize;
    loop {
        let mut line = Vec::new();
        let bytes_read = reader
            .read_until(b'\n', &mut line)
            .map_err(|source| io_error(path, source))?;
        if bytes_read == 0 {
            break;
        }
        total_bytes = total_bytes.saturating_add(bytes_read);
        if total_bytes > MAX_JOURNAL_BYTES
            || line.len() > MAX_JOURNAL_LINE_BYTES
            || line.last() != Some(&b'\n')
        {
            return Err(TransactionError::CorruptJournal(path.to_path_buf()));
        }
        line.pop();
        if line.is_empty() || lines.len() == MAX_JOURNAL_EVENTS {
            return Err(TransactionError::CorruptJournal(path.to_path_buf()));
        }
        lines.push(line);
    }

    let mut state = JournalState::default();
    for line in lines {
        let event: JournalEvent = serde_json::from_slice(&line)
            .map_err(|_| TransactionError::CorruptJournal(path.to_path_buf()))?;
        match event {
            JournalEvent::Begin => {
                if state.begun || state.complete || !state.files.is_empty() {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                state.begun = true;
            }
            JournalEvent::Planned {
                index,
                target,
                stage,
                digest,
                parent_identity,
            } => {
                let Some(target_text) = target.to_str() else {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                };
                let expected_stage = format!("stage-{index:04}.bin");
                validate_relative(&target)?;
                if !state.begun
                    || state.complete
                    || index >= MAX_TRANSACTION_FILES
                    || index != state.files.len()
                    || target_text.len() > MAX_JOURNAL_TARGET_BYTES
                    || stage.len() > MAX_JOURNAL_STAGE_BYTES
                    || stage != expected_stage
                    || digest.len() != SHA256_HEX_BYTES
                    || !digest
                        .bytes()
                        .all(|byte| matches!(byte, b'0'..=b'9' | b'a'..=b'f'))
                    || state
                        .files
                        .insert(
                            index,
                            PlannedFile {
                                target,
                                stage,
                                digest,
                                stage_identity: None,
                                parent_identity,
                                target_identity: None,
                                verified: false,
                            },
                        )
                        .is_some()
                {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
            }
            JournalEvent::Staged {
                index,
                stage_identity,
            } => {
                if !state.begun || state.complete {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                let planned = state
                    .files
                    .get_mut(&index)
                    .ok_or_else(|| TransactionError::CorruptJournal(path.to_path_buf()))?;
                if planned.stage_identity.replace(stage_identity).is_some()
                    || planned.target_identity.is_some()
                {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
            }
            JournalEvent::Published {
                index,
                target_identity,
            } => {
                if !state.begun || state.complete {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                let planned = state
                    .files
                    .get_mut(&index)
                    .ok_or_else(|| TransactionError::CorruptJournal(path.to_path_buf()))?;
                if planned.stage_identity.is_none()
                    || planned.target_identity.replace(target_identity).is_some()
                {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
            }
            JournalEvent::Verified { index } => {
                if !state.begun || state.complete {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                let planned = state
                    .files
                    .get_mut(&index)
                    .ok_or_else(|| TransactionError::CorruptJournal(path.to_path_buf()))?;
                if planned.target_identity.is_none() || planned.verified {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                planned.verified = true;
            }
            JournalEvent::Complete => {
                if !state.begun
                    || state.complete
                    || state.files.is_empty()
                    || state.files.values().any(|planned| !planned.verified)
                {
                    return Err(TransactionError::CorruptJournal(path.to_path_buf()));
                }
                state.complete = true;
            }
        }
    }
    if !state.begun {
        return Err(TransactionError::CorruptJournal(path.to_path_buf()));
    }
    Ok(state)
}

/// Returns `true` when the transaction is or becomes committed, `false` when
/// no target had been published and incomplete staging was safely abandoned.
fn recover_workspace(root: &Path, workspace: &Path) -> Result<bool, TransactionError> {
    validate_private_recovery_directory(workspace)?;
    let journal_path = workspace.join(JOURNAL_FILE);
    if !path_exists(&journal_path)? {
        if fs::read_dir(workspace)
            .map_err(|source| io_error(workspace, source))?
            .next()
            .is_none()
        {
            fs::remove_dir(workspace).map_err(|source| io_error(workspace, source))?;
            return Ok(false);
        }
        return Err(TransactionError::CorruptJournal(journal_path));
    }
    if cleanup_pre_begin_workspace(workspace, &journal_path)? {
        return Ok(false);
    }

    let state = read_journal(&journal_path)?;
    if state.complete {
        cleanup_workspace(workspace, &state)?;
        return Ok(true);
    }
    // `Begin` is durable before any `Planned` event, secret stage, or target
    // publication. A begun journal with no files is therefore an uncommitted
    // pre-manifest orphan, never an all-staged transaction (where `all()` on an
    // empty iterator would otherwise return true).
    if state.files.is_empty() {
        cleanup_workspace(workspace, &state)?;
        return Ok(false);
    }

    let all_staged = state
        .files
        .values()
        .all(|planned| planned.stage_identity.is_some());
    let any_published_path = state.files.values().try_fold(false, |found, planned| {
        let target = checked_target(root, &planned.target)?;
        Ok::<_, TransactionError>(found || path_exists(&target)?)
    })?;
    if !all_staged {
        if any_published_path {
            return Err(TransactionError::RecoveryConflict(workspace.to_path_buf()));
        }
        cleanup_workspace(workspace, &state)?;
        return Ok(false);
    }

    let mut journal = OpenOptions::new()
        .read(true)
        .append(true)
        .open(&journal_path)
        .map_err(|source| io_error(&journal_path, source))?;
    enforce_private_permissions(&journal_path, &journal)?;
    for (index, planned) in &state.files {
        let target = checked_target(root, &planned.target)?;
        let parent = target
            .parent()
            .ok_or_else(|| TransactionError::InvalidTarget(planned.target.clone()))?;
        if identity_for_path(parent)? != planned.parent_identity {
            return Err(TransactionError::RecoveryConflict(parent.to_path_buf()));
        }
        let stage_identity = verify_stage(workspace, planned)?;

        let target_identity = match fs::symlink_metadata(&target) {
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                fs::hard_link(workspace.join(&planned.stage), &target)
                    .map_err(|source| io_error(&target, source))?;
                sync_directory(parent).map_err(|source| io_error(parent, source))?;
                identity_for_path(&target)?
            }
            Err(source) => return Err(io_error(&target, source)),
            Ok(_) => {
                ensure_real_file(&target)?;
                identity_for_path(&target)?
            }
        };
        if target_identity != stage_identity {
            return Err(TransactionError::RecoveryConflict(target));
        }
        if let Some(recorded) = planned.target_identity {
            if recorded != target_identity {
                return Err(TransactionError::RecoveryConflict(target));
            }
        } else {
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Published {
                    index: *index,
                    target_identity,
                },
            )?;
        }
        verify_target(&target, planned)?;
        if !planned.verified {
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Verified { index: *index },
            )?;
        }
    }
    append_event(&mut journal, &journal_path, &JournalEvent::Complete)?;
    sync_directory(workspace).map_err(|source| io_error(workspace, source))?;
    cleanup_workspace(workspace, &state)?;
    Ok(true)
}

/// A workspace cannot contain secret staging or a published target before its
/// journal has a durable `Begin` event. Recovery may therefore remove exactly
/// two pre-begin shapes: an empty workspace (handled above), or a workspace
/// whose only entry is an empty, real journal file. Any additional entry is
/// ambiguous and must remain in place for operator inspection.
fn cleanup_pre_begin_workspace(
    workspace: &Path,
    journal_path: &Path,
) -> Result<bool, TransactionError> {
    ensure_real_file(journal_path)?;
    let metadata =
        fs::symlink_metadata(journal_path).map_err(|source| io_error(journal_path, source))?;
    if metadata.len() != 0 {
        return Ok(false);
    }

    let mut saw_journal = false;
    for entry in fs::read_dir(workspace).map_err(|source| io_error(workspace, source))? {
        let entry = entry.map_err(|source| io_error(workspace, source))?;
        if saw_journal || entry.file_name().to_str() != Some(JOURNAL_FILE) {
            return Err(TransactionError::CorruptJournal(workspace.to_path_buf()));
        }
        saw_journal = true;
    }
    if !saw_journal {
        return Err(TransactionError::CorruptJournal(workspace.to_path_buf()));
    }

    fs::remove_file(journal_path).map_err(|source| io_error(journal_path, source))?;
    sync_directory(workspace).map_err(|source| io_error(workspace, source))?;
    fs::remove_dir(workspace).map_err(|source| io_error(workspace, source))?;
    if let Some(parent) = workspace.parent() {
        sync_directory(parent).map_err(|source| io_error(parent, source))?;
    }
    Ok(true)
}

fn cleanup_workspace(workspace: &Path, state: &JournalState) -> Result<(), TransactionError> {
    let expected: HashSet<&str> = state
        .files
        .values()
        .map(|planned| planned.stage.as_str())
        .chain(std::iter::once(JOURNAL_FILE))
        .collect();
    for entry in fs::read_dir(workspace).map_err(|source| io_error(workspace, source))? {
        let entry = entry.map_err(|source| io_error(workspace, source))?;
        let name = entry.file_name();
        let name = name
            .to_str()
            .ok_or_else(|| TransactionError::CorruptJournal(workspace.to_path_buf()))?;
        if !expected.contains(name) {
            return Err(TransactionError::CorruptJournal(workspace.to_path_buf()));
        }
    }
    for planned in state.files.values() {
        let stage = workspace.join(&planned.stage);
        if path_exists(&stage)? {
            ensure_real_file(&stage)?;
            if let Some(expected) = planned.stage_identity {
                if identity_for_path(&stage)? != expected {
                    return Err(TransactionError::RecoveryConflict(stage));
                }
            }
            fs::remove_file(&stage).map_err(|source| io_error(&stage, source))?;
        }
    }
    sync_directory(workspace).map_err(|source| io_error(workspace, source))?;
    let journal = workspace.join(JOURNAL_FILE);
    if path_exists(&journal)? {
        ensure_real_file(&journal)?;
        fs::remove_file(&journal).map_err(|source| io_error(&journal, source))?;
        sync_directory(workspace).map_err(|source| io_error(workspace, source))?;
    }
    fs::remove_dir(workspace).map_err(|source| io_error(workspace, source))?;
    if let Some(parent) = workspace.parent() {
        sync_directory(parent).map_err(|source| io_error(parent, source))?;
    }
    Ok(())
}

fn verify_target(target: &Path, planned: &PlannedFile) -> Result<(), TransactionError> {
    ensure_real_file(target)?;
    let file = File::open(target).map_err(|source| io_error(target, source))?;
    let stage_identity = planned
        .stage_identity
        .ok_or_else(|| TransactionError::CorruptJournal(target.to_path_buf()))?;
    if file_identity(&file, target)? != stage_identity
        || digest_path(target)?.as_str() != planned.digest.as_str()
    {
        return Err(TransactionError::RecoveryConflict(target.to_path_buf()));
    }
    Ok(())
}

fn verify_stage(workspace: &Path, planned: &PlannedFile) -> Result<FileIdentity, TransactionError> {
    let path = workspace.join(&planned.stage);
    ensure_real_file(&path)?;
    let expected = planned
        .stage_identity
        .ok_or_else(|| TransactionError::CorruptJournal(path.clone()))?;
    if identity_for_path(&path)? != expected
        || digest_path(&path)?.as_str() != planned.digest.as_str()
    {
        return Err(TransactionError::RecoveryConflict(path));
    }
    Ok(expected)
}

fn digest_path(path: &Path) -> Result<String, TransactionError> {
    let bytes = Zeroizing::new(read_bounded(path)?);
    Ok(digest(Some(&bytes)).expect("bytes have a digest"))
}

fn path_exists(path: &Path) -> Result<bool, TransactionError> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(source) => Err(io_error(path, source)),
    }
}

fn read_bounded(path: &Path) -> Result<Vec<u8>, TransactionError> {
    let metadata = fs::metadata(path).map_err(|source| io_error(path, source))?;
    if metadata.len() > MAX_FILE_BYTES as u64 {
        return Err(TransactionError::TooLarge(path.to_path_buf()));
    }
    fs::read(path).map_err(|source| io_error(path, source))
}

fn ensure_no_links(path: &Path) -> Result<(), TransactionError> {
    let mut ancestors: Vec<&Path> = path.ancestors().collect();
    ancestors.reverse();
    for ancestor in ancestors {
        if ancestor.as_os_str().is_empty() {
            continue;
        }
        match fs::symlink_metadata(ancestor) {
            Ok(metadata) => ensure_not_link(ancestor, &metadata)?,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Err(TransactionError::InvalidRoot(ancestor.to_path_buf()))
            }
            Err(source) => return Err(io_error(ancestor, source)),
        }
    }
    Ok(())
}

fn ensure_real_directory(path: &Path) -> Result<(), TransactionError> {
    let metadata = fs::symlink_metadata(path).map_err(|source| io_error(path, source))?;
    ensure_not_link(path, &metadata)?;
    if !metadata.is_dir() {
        return Err(TransactionError::InvalidRoot(path.to_path_buf()));
    }
    Ok(())
}

fn ensure_real_file(path: &Path) -> Result<(), TransactionError> {
    let metadata = fs::symlink_metadata(path).map_err(|source| io_error(path, source))?;
    ensure_not_link(path, &metadata)?;
    if !metadata.is_file() {
        return Err(TransactionError::RecoveryConflict(path.to_path_buf()));
    }
    Ok(())
}

fn validate_workspace_path(path: &Path) -> Result<(), TransactionError> {
    let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
        return Err(TransactionError::CorruptJournal(path.to_path_buf()));
    };
    let mut parts = name.split('-');
    let (Some(prefix), Some(pid), Some(nonce), Some(count), None) = (
        parts.next(),
        parts.next(),
        parts.next(),
        parts.next(),
        parts.next(),
    ) else {
        return Err(TransactionError::CorruptJournal(path.to_path_buf()));
    };
    let valid = prefix == "tx"
        && pid
            .parse::<u32>()
            .is_ok_and(|value| value.to_string() == pid)
        && u128::from_str_radix(nonce, 16).is_ok_and(|value| format!("{value:x}") == nonce)
        && u64::from_str_radix(count, 16).is_ok_and(|value| format!("{value:x}") == count);
    if valid {
        Ok(())
    } else {
        Err(TransactionError::CorruptJournal(path.to_path_buf()))
    }
}

fn validate_private_recovery_directory(path: &Path) -> Result<(), TransactionError> {
    ensure_real_directory(path)?;
    let before = identity_for_path(path)?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        let metadata = fs::symlink_metadata(path).map_err(|source| io_error(path, source))?;
        if metadata.uid() != unsafe { libc::geteuid() }
            || metadata.permissions().mode() & 0o777 != 0o700
        {
            return Err(TransactionError::RecoveryConflict(path.to_path_buf()));
        }
    }

    #[cfg(target_os = "macos")]
    {
        let file = File::open(path).map_err(|source| io_error(path, source))?;
        verify_no_macos_extended_acl(&file, path)?;
    }

    #[cfg(windows)]
    verify_private_windows_dacl(path)?;

    if identity_for_path(path)? != before {
        return Err(TransactionError::RecoveryConflict(path.to_path_buf()));
    }
    Ok(())
}

fn ensure_not_link(path: &Path, metadata: &fs::Metadata) -> Result<(), TransactionError> {
    if metadata.file_type().is_symlink() || is_windows_reparse_point(metadata) {
        return Err(TransactionError::LinkedPath(path.to_path_buf()));
    }
    Ok(())
}

#[cfg(windows)]
fn is_windows_reparse_point(metadata: &fs::Metadata) -> bool {
    use std::os::windows::fs::MetadataExt;
    metadata.file_attributes()
        & windows_sys::Win32::Storage::FileSystem::FILE_ATTRIBUTE_REPARSE_POINT
        != 0
}

#[cfg(not(windows))]
fn is_windows_reparse_point(_metadata: &fs::Metadata) -> bool {
    false
}

fn create_private_directory(path: &Path) -> Result<(), TransactionError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        let mut builder = fs::DirBuilder::new();
        builder.mode(0o700);
        builder
            .create(path)
            .map_err(|source| io_error(path, source))?;
    }
    #[cfg(windows)]
    fs::create_dir(path).map_err(|source| io_error(path, source))?;
    #[cfg(unix)]
    let directory = File::open(path).map_err(|source| io_error(path, source))?;
    #[cfg(windows)]
    let directory = open_windows_path(
        path,
        windows_sys::Win32::Storage::FileSystem::FILE_READ_ATTRIBUTES,
    )?;
    enforce_private_permissions(path, &directory)
}

fn create_private_file(path: &Path) -> Result<File, TransactionError> {
    #[cfg(unix)]
    let file = {
        use std::os::unix::fs::OpenOptionsExt;
        OpenOptions::new()
            .create_new(true)
            .read(true)
            .write(true)
            .mode(0o600)
            .open(path)
            .map_err(|source| io_error(path, source))?
    };
    #[cfg(windows)]
    let file = OpenOptions::new()
        .create_new(true)
        .read(true)
        .write(true)
        .open(path)
        .map_err(|source| io_error(path, source))?;
    enforce_private_permissions(path, &file)?;
    Ok(file)
}

#[cfg(unix)]
fn enforce_private_permissions(path: &Path, file: &File) -> Result<(), TransactionError> {
    use std::os::fd::AsRawFd;
    let metadata = file.metadata().map_err(|source| io_error(path, source))?;
    let mode = if metadata.is_dir() { 0o700 } else { 0o600 };
    if unsafe { libc::fchmod(file.as_raw_fd(), mode) } != 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    #[cfg(target_os = "macos")]
    verify_no_macos_extended_acl(file, path)?;
    Ok(())
}

#[cfg(target_os = "macos")]
fn verify_no_macos_extended_acl(file: &File, path: &Path) -> Result<(), TransactionError> {
    use std::ffi::{c_int, c_void};
    use std::os::fd::AsRawFd;
    use std::ptr::null_mut;

    type Acl = *mut c_void;
    const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
    const ACL_FIRST_ENTRY: c_int = 0;

    unsafe extern "C" {
        fn acl_get_fd_np(fd: c_int, acl_type: c_int) -> Acl;
        fn acl_get_entry(acl: Acl, entry_id: c_int, entry: *mut *mut c_void) -> c_int;
        fn acl_free(object: *mut c_void) -> c_int;
    }

    let acl = unsafe { acl_get_fd_np(file.as_raw_fd(), ACL_TYPE_EXTENDED) };
    if acl.is_null() {
        let source = io::Error::last_os_error();
        // With a live descriptor, Darwin's ENOENT unambiguously means that the
        // object has no extended ACL; it cannot mean that a path disappeared.
        return if source.raw_os_error() == Some(libc::ENOENT) {
            Ok(())
        } else {
            Err(io_error(path, source))
        };
    }

    let mut entry = null_mut();
    unsafe { *libc::__error() = 0 };
    let result = unsafe { acl_get_entry(acl, ACL_FIRST_ENTRY, &mut entry) };
    let errno = unsafe { *libc::__error() };
    unsafe { acl_free(acl) };

    if result == 0 {
        return Err(TransactionError::RecoveryConflict(path.to_path_buf()));
    }
    if result == -1 && errno == libc::EINVAL {
        return Ok(());
    }
    Err(io_error(
        path,
        if errno == 0 {
            io::Error::other("unexpected macOS ACL enumeration result")
        } else {
            io::Error::from_raw_os_error(errno)
        },
    ))
}

#[cfg(windows)]
struct WindowsTokenHandle(windows_sys::Win32::Foundation::HANDLE);

#[cfg(windows)]
impl Drop for WindowsTokenHandle {
    fn drop(&mut self) {
        unsafe { windows_sys::Win32::Foundation::CloseHandle(self.0) };
    }
}

#[cfg(windows)]
fn windows_current_user_token_info(path: &Path) -> Result<Vec<u8>, TransactionError> {
    use std::ptr::null_mut;
    use windows_sys::Win32::Foundation::{GetLastError, ERROR_INSUFFICIENT_BUFFER};
    use windows_sys::Win32::Security::{GetTokenInformation, TokenUser, TOKEN_QUERY};
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

    let mut token = null_mut();
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    let token = WindowsTokenHandle(token);
    let mut needed = 0u32;
    unsafe { GetTokenInformation(token.0, TokenUser, null_mut(), 0, &mut needed) };
    if needed == 0 || unsafe { GetLastError() } != ERROR_INSUFFICIENT_BUFFER {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    let mut info = vec![0u8; needed as usize];
    let read = unsafe {
        GetTokenInformation(
            token.0,
            TokenUser,
            info.as_mut_ptr().cast(),
            needed,
            &mut needed,
        )
    };
    if read == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    Ok(info)
}

#[cfg(windows)]
fn enforce_private_permissions(path: &Path, _file: &File) -> Result<(), TransactionError> {
    use std::os::windows::ffi::OsStrExt;
    use std::ptr::null_mut;
    use windows_sys::Win32::Foundation::{LocalFree, ERROR_SUCCESS};
    use windows_sys::Win32::Security::Authorization::{
        ConvertStringSecurityDescriptorToSecurityDescriptorW, SetNamedSecurityInfoW,
        SDDL_REVISION_1, SE_FILE_OBJECT,
    };
    use windows_sys::Win32::Security::{
        SetFileSecurityW, DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION,
        PROTECTED_DACL_SECURITY_INFORMATION, TOKEN_USER,
    };

    let token_info = windows_current_user_token_info(path)?;
    let token_user = unsafe { std::ptr::read_unaligned(token_info.as_ptr().cast::<TOKEN_USER>()) };
    if token_user.User.Sid.is_null() {
        return Err(io_error(
            path,
            io::Error::new(
                io::ErrorKind::PermissionDenied,
                "process user SID is missing",
            ),
        ));
    }
    let mut wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
    let owner_status = unsafe {
        SetNamedSecurityInfoW(
            wide.as_mut_ptr(),
            SE_FILE_OBJECT,
            OWNER_SECURITY_INFORMATION,
            token_user.User.Sid,
            null_mut(),
            null_mut(),
            null_mut(),
        )
    };
    if owner_status != ERROR_SUCCESS {
        return Err(io_error(
            path,
            io::Error::from_raw_os_error(owner_status as i32),
        ));
    }

    let mut descriptor = null_mut();
    let sddl: Vec<u16> = "D:P(A;;FA;;;OW)\0".encode_utf16().collect();
    let converted = unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            sddl.as_ptr(),
            SDDL_REVISION_1,
            &mut descriptor,
            null_mut(),
        )
    };
    if converted == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    let applied = unsafe {
        SetFileSecurityW(
            wide.as_ptr(),
            DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
            descriptor,
        )
    };
    unsafe { LocalFree(descriptor) };
    if applied == 0 {
        Err(io_error(path, io::Error::last_os_error()))
    } else {
        verify_private_windows_dacl(path)
    }
}

#[cfg(windows)]
fn verify_private_windows_dacl(path: &Path) -> Result<(), TransactionError> {
    use std::os::windows::ffi::OsStrExt;
    use std::ptr::null_mut;
    use windows_sys::Win32::Foundation::{GetLastError, LocalFree, ERROR_INSUFFICIENT_BUFFER};
    use windows_sys::Win32::Security::Authorization::{
        ConvertSecurityDescriptorToStringSecurityDescriptorW, SDDL_REVISION_1,
    };
    use windows_sys::Win32::Security::{
        GetFileSecurityW, GetSecurityDescriptorOwner, DACL_SECURITY_INFORMATION,
        OWNER_SECURITY_INFORMATION,
    };

    let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
    let requested = DACL_SECURITY_INFORMATION | OWNER_SECURITY_INFORMATION;
    let mut needed = 0u32;
    unsafe { GetFileSecurityW(wide.as_ptr(), requested, null_mut(), 0, &mut needed) };
    if needed == 0 || unsafe { GetLastError() } != ERROR_INSUFFICIENT_BUFFER {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    let mut descriptor = vec![0u8; needed as usize];
    let read = unsafe {
        GetFileSecurityW(
            wide.as_ptr(),
            requested,
            descriptor.as_mut_ptr().cast(),
            needed,
            &mut needed,
        )
    };
    if read == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }

    let mut owner = null_mut();
    let mut owner_defaulted = 0;
    let owner_read = unsafe {
        GetSecurityDescriptorOwner(
            descriptor.as_mut_ptr().cast(),
            &mut owner,
            &mut owner_defaulted,
        )
    };
    if owner_read == 0 || owner.is_null() {
        return Err(io_error(path, io::Error::last_os_error()));
    }

    let token_info = windows_current_user_token_info(path)?;
    if !windows_owner_matches_current_user(owner, &token_info) {
        return Err(io_error(
            path,
            io::Error::new(
                io::ErrorKind::PermissionDenied,
                "security descriptor owner is not the current process user",
            ),
        ));
    }

    let mut rendered = null_mut();
    let mut rendered_len = 0u32;
    let converted = unsafe {
        ConvertSecurityDescriptorToStringSecurityDescriptorW(
            descriptor.as_mut_ptr().cast(),
            SDDL_REVISION_1,
            DACL_SECURITY_INFORMATION,
            &mut rendered,
            &mut rendered_len,
        )
    };
    if converted == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    let actual = unsafe {
        String::from_utf16_lossy(std::slice::from_raw_parts(rendered, rendered_len as usize))
    };
    unsafe { LocalFree(rendered.cast()) };
    let actual = actual.trim_end_matches('\0');
    if actual != "D:P(A;;FA;;;OW)" {
        return Err(io_error(
            path,
            io::Error::new(
                io::ErrorKind::PermissionDenied,
                format!("protected owner-only DACL read-back did not match: {actual}"),
            ),
        ));
    }
    Ok(())
}

#[cfg(windows)]
fn windows_owner_matches_current_user(
    owner: windows_sys::Win32::Security::PSID,
    token_info: &[u8],
) -> bool {
    use windows_sys::Win32::Security::{EqualSid, TOKEN_USER};

    if owner.is_null() || token_info.len() < std::mem::size_of::<TOKEN_USER>() {
        return false;
    }
    let token_user = unsafe { std::ptr::read_unaligned(token_info.as_ptr().cast::<TOKEN_USER>()) };
    !token_user.User.Sid.is_null() && unsafe { EqualSid(owner, token_user.User.Sid) } != 0
}

#[cfg(unix)]
fn identity_for_path(path: &Path) -> Result<FileIdentity, TransactionError> {
    let file = File::open(path).map_err(|source| io_error(path, source))?;
    file_identity(&file, path)
}

#[cfg(windows)]
fn identity_for_path(path: &Path) -> Result<FileIdentity, TransactionError> {
    use windows_sys::Win32::Storage::FileSystem::FILE_READ_ATTRIBUTES;
    let file = open_windows_path(path, FILE_READ_ATTRIBUTES)?;
    file_identity(&file, path)
}

#[cfg(test)]
pub(crate) fn test_file_identity(path: &Path) -> Result<(u64, u64), TransactionError> {
    let identity = identity_for_path(path)?;
    Ok((identity.volume, identity.file))
}

#[cfg(unix)]
fn file_identity(file: &File, path: &Path) -> Result<FileIdentity, TransactionError> {
    use std::os::unix::fs::MetadataExt;
    let metadata = file.metadata().map_err(|source| io_error(path, source))?;
    Ok(FileIdentity {
        volume: metadata.dev(),
        file: metadata.ino(),
    })
}

#[cfg(windows)]
fn file_identity(file: &File, path: &Path) -> Result<FileIdentity, TransactionError> {
    use std::mem::zeroed;
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Storage::FileSystem::{
        GetFileInformationByHandle, BY_HANDLE_FILE_INFORMATION,
    };
    let mut info: BY_HANDLE_FILE_INFORMATION = unsafe { zeroed() };
    let ok = unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut info) };
    if ok == 0 {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    if info.dwFileAttributes & windows_sys::Win32::Storage::FileSystem::FILE_ATTRIBUTE_REPARSE_POINT
        != 0
    {
        return Err(TransactionError::LinkedPath(path.to_path_buf()));
    }
    Ok(FileIdentity {
        volume: info.dwVolumeSerialNumber as u64,
        file: ((info.nFileIndexHigh as u64) << 32) | info.nFileIndexLow as u64,
    })
}

#[cfg(unix)]
fn sync_directory(path: &Path) -> io::Result<()> {
    File::open(path)?.sync_all()
}

#[cfg(windows)]
fn sync_directory(path: &Path) -> io::Result<()> {
    use windows_sys::Win32::Foundation::GENERIC_WRITE;
    open_windows_path(path, GENERIC_WRITE)
        .map_err(|error| match error {
            TransactionError::Io { source, .. } => source,
            other => io::Error::other(other),
        })?
        .sync_all()
}

#[cfg(windows)]
fn open_windows_path(path: &Path, access: u32) -> Result<File, TransactionError> {
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::io::FromRawHandle;
    use std::ptr::null;
    use windows_sys::Win32::Foundation::INVALID_HANDLE_VALUE;
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_SHARE_DELETE,
        FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
    };

    let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
    let handle = unsafe {
        CreateFileW(
            wide.as_ptr(),
            access,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            null(),
            OPEN_EXISTING,
            FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
            std::ptr::null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        return Err(io_error(path, io::Error::last_os_error()));
    }
    Ok(unsafe { File::from_raw_handle(handle) })
}

fn io_error(path: &Path, source: io::Error) -> TransactionError {
    TransactionError::Io {
        path: path.to_path_buf(),
        source,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn plan(path: &str, contents: &str) -> CreateFilePlan {
        CreateFilePlan::new(path, contents.as_bytes().to_vec())
    }

    fn assert_corrupt_journal_is_preserved(contents: &[u8]) {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterBegin,
        )
        .expect_err("fault is injected");
        let workspace = fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let journal = workspace.join(JOURNAL_FILE);
        fs::write(&journal, contents).unwrap();

        let error = recover_create_only(root.path()).expect_err("malicious journal must block");
        assert!(matches!(error, TransactionError::CorruptJournal(_)));
        assert_eq!(fs::read(&journal).unwrap(), contents);
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn rejects_too_many_files_before_creating_a_workspace() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        let files = (0..=MAX_TRANSACTION_FILES)
            .map(|index| plan(&format!("tool/config-{index}.json"), "value"))
            .collect();

        let error = apply_create_only(root.path(), files).expect_err("file count must be bounded");

        assert!(matches!(error, TransactionError::TooLarge(_)));
        assert!(!root.path().join(TRANSACTION_DIRECTORY).exists());
    }

    #[test]
    fn oversized_journal_is_rejected_and_preserved() {
        assert_corrupt_journal_is_preserved(&vec![b'x'; MAX_JOURNAL_BYTES + 1]);
    }

    #[test]
    fn oversized_journal_line_is_rejected_and_preserved() {
        let mut contents = vec![b'x'; MAX_JOURNAL_LINE_BYTES];
        contents.push(b'\n');
        assert_corrupt_journal_is_preserved(&contents);
    }

    #[test]
    fn unterminated_journal_event_is_rejected_and_preserved() {
        assert_corrupt_journal_is_preserved(b"{\"state\":\"begin\"}");
    }

    #[test]
    fn excessive_journal_event_count_is_rejected_and_preserved() {
        let contents = "{}\n".repeat(MAX_JOURNAL_EVENTS + 1);
        assert_corrupt_journal_is_preserved(contents.as_bytes());
    }

    #[test]
    fn oversized_journal_fields_are_rejected_and_preserved() {
        let digest = "0".repeat(SHA256_HEX_BYTES);
        let cases = [
            serde_json::json!({
                "state": "planned",
                "index": 0,
                "target": "x".repeat(MAX_JOURNAL_TARGET_BYTES + 1),
                "stage": "stage-0000.bin",
                "digest": digest,
                "parent_identity": { "volume": 0, "file": 0 }
            }),
            serde_json::json!({
                "state": "planned",
                "index": 0,
                "target": "tool/config.json",
                "stage": "s".repeat(MAX_JOURNAL_STAGE_BYTES + 1),
                "digest": "0".repeat(SHA256_HEX_BYTES),
                "parent_identity": { "volume": 0, "file": 0 }
            }),
            serde_json::json!({
                "state": "planned",
                "index": 0,
                "target": "tool/config.json",
                "stage": "stage-0000.bin",
                "digest": "0".repeat(SHA256_HEX_BYTES + 1),
                "parent_identity": { "volume": 0, "file": 0 }
            }),
            serde_json::json!({
                "state": "planned",
                "index": 0,
                "target": "tool/config.json",
                "stage": "stage-0000.bin",
                "digest": "G".repeat(SHA256_HEX_BYTES),
                "parent_identity": { "volume": 0, "file": 0 }
            }),
            serde_json::json!({
                "state": "planned",
                "index": 1,
                "target": "tool/config.json",
                "stage": "stage-0001.bin",
                "digest": "0".repeat(SHA256_HEX_BYTES),
                "parent_identity": { "volume": 0, "file": 0 }
            }),
        ];

        for event in cases {
            let contents = format!("{{\"state\":\"begin\"}}\n{event}\n");
            assert_corrupt_journal_is_preserved(contents.as_bytes());
        }
    }

    #[test]
    fn maximum_well_formed_journal_event_count_is_accepted() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join(JOURNAL_FILE);
        let mut contents = String::new();
        let mut push = |event: JournalEvent| {
            contents.push_str(&serde_json::to_string(&event).unwrap());
            contents.push('\n');
        };
        push(JournalEvent::Begin);
        for index in 0..MAX_TRANSACTION_FILES {
            push(JournalEvent::Planned {
                index,
                target: PathBuf::from(format!("tool/config-{index}.json")),
                stage: format!("stage-{index:04}.bin"),
                digest: "0".repeat(SHA256_HEX_BYTES),
                parent_identity: FileIdentity { volume: 0, file: 0 },
            });
        }
        for index in 0..MAX_TRANSACTION_FILES {
            push(JournalEvent::Staged {
                index,
                stage_identity: FileIdentity {
                    volume: 0,
                    file: index as u64,
                },
            });
        }
        for index in 0..MAX_TRANSACTION_FILES {
            push(JournalEvent::Published {
                index,
                target_identity: FileIdentity {
                    volume: 0,
                    file: index as u64,
                },
            });
        }
        for index in 0..MAX_TRANSACTION_FILES {
            push(JournalEvent::Verified { index });
        }
        push(JournalEvent::Complete);
        drop(push);
        fs::write(&path, contents).unwrap();

        let state = read_journal(&path).expect("exact event limit must remain valid");

        assert!(state.complete);
        assert_eq!(state.files.len(), MAX_TRANSACTION_FILES);
    }

    #[test]
    fn creates_multiple_files_and_removes_plaintext_staging() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool-a")).unwrap();
        fs::create_dir(root.path().join("tool-b")).unwrap();
        let secret = "cpa_test_secret_never_in_journal";
        let receipt = apply_create_only(
            root.path(),
            vec![
                plan("tool-a/config.json", secret),
                plan("tool-b/config.toml", "base_url = 'https://example.test'"),
            ],
        )
        .unwrap();
        assert_eq!(receipt.created.len(), 2);
        assert_eq!(
            fs::read_to_string(root.path().join("tool-a/config.json")).unwrap(),
            secret
        );
        assert_eq!(
            fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
                .unwrap()
                .count(),
            0
        );
    }

    #[test]
    fn existing_file_is_never_replaced() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        let target = root.path().join("tool/config.json");
        fs::write(&target, "user-owned").unwrap();
        let error = apply_create_only(root.path(), vec![plan("tool/config.json", "new")])
            .expect_err("existing target must fail");
        assert!(matches!(error, TransactionError::Collision(_)));
        assert_eq!(fs::read_to_string(target).unwrap(), "user-owned");
    }

    #[test]
    fn crash_after_unjournaled_publish_is_safely_rolled_forward() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        let error = apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterPublishBeforeJournal(0),
        )
        .expect_err("fault is injected");
        assert!(matches!(error, TransactionError::InjectedCrash));
        recover_create_only(root.path()).unwrap();
        assert_eq!(
            fs::read_to_string(root.path().join("tool/config.json")).unwrap(),
            "synthetic-key"
        );
    }

    #[test]
    fn crash_after_verified_publish_rolls_forward_all_files() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![
                plan("tool/a.json", "a-secret"),
                plan("tool/b.json", "b-secret"),
            ],
            FaultPoint::AfterVerified(1),
        )
        .expect_err("fault is injected");
        recover_create_only(root.path()).unwrap();
        assert_eq!(
            fs::read_to_string(root.path().join("tool/a.json")).unwrap(),
            "a-secret"
        );
        assert_eq!(
            fs::read_to_string(root.path().join("tool/b.json")).unwrap(),
            "b-secret"
        );
    }

    #[test]
    fn crash_after_stage_write_before_stage_event_leaves_no_plaintext_residue() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterStageBeforeJournal(0),
        )
        .expect_err("fault is injected");
        let workspace = fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        assert_eq!(
            fs::read(workspace.join("stage-0000.bin")).unwrap(),
            b"synthetic-key"
        );
        let journal = fs::read_to_string(workspace.join(JOURNAL_FILE)).unwrap();
        assert!(!journal.contains("\"state\":\"staged\""));
        recover_create_only(root.path()).unwrap();
        assert!(!root.path().join("tool/config.json").exists());
        assert_eq!(
            fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
                .unwrap()
                .count(),
            0
        );
    }

    #[test]
    fn crash_after_manifest_before_staging_is_cleaned() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterManifest,
        )
        .expect_err("fault is injected");
        recover_create_only(root.path()).unwrap();
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn crash_after_workspace_before_journal_is_cleaned() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterWorkspace,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        assert_eq!(fs::read_dir(&workspace).unwrap().count(), 0);

        recover_create_only(root.path()).unwrap();
        assert_eq!(fs::read_dir(transaction_root).unwrap().count(), 0);
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn crash_after_empty_journal_before_begin_is_cleaned() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterJournalCreated,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        assert_eq!(fs::metadata(workspace.join(JOURNAL_FILE)).unwrap().len(), 0);

        recover_create_only(root.path()).unwrap();
        assert_eq!(fs::read_dir(transaction_root).unwrap().count(), 0);
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn crash_after_begin_before_first_plan_is_not_committed() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterBegin,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        assert_eq!(
            fs::read_to_string(workspace.join(JOURNAL_FILE)).unwrap(),
            "{\"state\":\"begin\"}\n"
        );

        recover_create_only(root.path()).unwrap();
        assert_eq!(fs::read_dir(transaction_root).unwrap().count(), 0);
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn io_failure_after_begin_returns_error_instead_of_success_receipt() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();

        let error = apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterBeginIoFailure,
        )
        .expect_err("ordinary I/O failure must not become a successful receipt");

        assert!(matches!(error, TransactionError::Io { .. }));
        assert_eq!(
            fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
                .unwrap()
                .count(),
            0
        );
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn empty_journal_with_an_unexpected_entry_blocks_recovery() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterJournalCreated,
        )
        .expect_err("fault is injected");

        let workspace = fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        fs::write(workspace.join("unexpected.bin"), b"operator-evidence").unwrap();

        let error = recover_create_only(root.path()).expect_err("ambiguous orphan must block");
        assert!(matches!(error, TransactionError::CorruptJournal(_)));
        assert_eq!(
            fs::read(workspace.join("unexpected.bin")).unwrap(),
            b"operator-evidence"
        );
        assert!(workspace.join(JOURNAL_FILE).exists());
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn recovery_refuses_an_unrecognized_workspace_name() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterJournalCreated,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let foreign = transaction_root.join("foreign-workspace");
        fs::rename(&workspace, &foreign).unwrap();

        let error = recover_create_only(root.path()).expect_err("unknown workspace must block");
        assert!(matches!(error, TransactionError::CorruptJournal(_)));
        assert!(foreign.join(JOURNAL_FILE).exists());
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[cfg(unix)]
    #[test]
    fn recovery_refuses_non_private_transaction_directories() {
        use std::os::unix::fs::PermissionsExt;

        for weaken_workspace in [false, true] {
            let root = tempfile::tempdir().unwrap();
            fs::create_dir(root.path().join("tool")).unwrap();
            apply_create_only_inner(
                root.path(),
                vec![plan("tool/config.json", "synthetic-key")],
                FaultPoint::AfterJournalCreated,
            )
            .expect_err("fault is injected");

            let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
            let workspace = fs::read_dir(&transaction_root)
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path();
            let weakened = if weaken_workspace {
                &workspace
            } else {
                &transaction_root
            };
            fs::set_permissions(weakened, fs::Permissions::from_mode(0o755)).unwrap();

            let error = recover_create_only(root.path())
                .expect_err("non-private recovery directory must block");
            assert!(matches!(error, TransactionError::RecoveryConflict(_)));
            assert!(workspace.join(JOURNAL_FILE).exists());
            assert!(!root.path().join("tool/config.json").exists());
        }
    }

    #[test]
    fn external_replacement_blocks_recovery() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        let target = root.path().join("tool/config.json");
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterPublished(0),
        )
        .expect_err("fault is injected");
        fs::remove_file(&target).unwrap();
        fs::write(&target, "external-edit").unwrap();
        let error = recover_create_only(root.path()).expect_err("identity mismatch must block");
        assert!(matches!(error, TransactionError::RecoveryConflict(_)));
        assert_eq!(fs::read_to_string(target).unwrap(), "external-edit");
    }

    #[test]
    fn journal_never_contains_secret_bytes() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        let secret = "cpa_test_secret_not_in_journal";
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", secret)],
            FaultPoint::AfterPlanning,
        )
        .expect_err("fault is injected");
        let workspace = fs::read_dir(root.path().join(TRANSACTION_DIRECTORY))
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let journal = fs::read_to_string(workspace.join(JOURNAL_FILE)).unwrap();
        assert!(!journal.contains(secret));
    }

    #[test]
    fn operating_system_lock_refuses_a_second_holder() {
        let root = tempfile::tempdir().unwrap();
        let checked = checked_root(root.path()).unwrap();
        let _guard = acquire_transaction_lock(&checked).unwrap();
        let path = checked.join(TRANSACTION_LOCK);

        #[cfg(unix)]
        {
            use std::os::fd::AsRawFd;
            let second = OpenOptions::new()
                .read(true)
                .write(true)
                .open(path)
                .unwrap();
            let result = unsafe { libc::flock(second.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) };
            assert_ne!(result, 0);
        }
        #[cfg(windows)]
        {
            use std::os::windows::fs::OpenOptionsExt;
            use windows_sys::Win32::Foundation::ERROR_SHARING_VIOLATION;
            let error = OpenOptions::new()
                .read(true)
                .write(true)
                .share_mode(0)
                .open(path)
                .expect_err("exclusive lock handle must deny a second open");
            assert_eq!(error.raw_os_error(), Some(ERROR_SHARING_VIOLATION as i32));
        }
    }

    #[cfg(windows)]
    #[test]
    fn windows_target_owner_and_dacl_are_read_back_as_private() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only(root.path(), vec![plan("tool/config.json", "secret")]).unwrap();
        verify_private_windows_dacl(&root.path().join("tool/config.json")).unwrap();
    }

    #[cfg(windows)]
    #[test]
    fn windows_owner_comparison_rejects_a_foreign_sid() {
        use std::ptr::null_mut;
        use windows_sys::Win32::Foundation::LocalFree;
        use windows_sys::Win32::Security::Authorization::ConvertStringSidToSidW;

        let root = tempfile::tempdir().unwrap();
        let token_info = windows_current_user_token_info(root.path()).unwrap();
        let local_system: Vec<u16> = "S-1-5-18\0".encode_utf16().collect();
        let mut foreign = null_mut();
        assert_ne!(
            unsafe { ConvertStringSidToSidW(local_system.as_ptr(), &mut foreign) },
            0
        );
        assert!(!windows_owner_matches_current_user(foreign, &token_info));
        unsafe { LocalFree(foreign) };
    }

    #[cfg(windows)]
    #[test]
    fn windows_recovery_refuses_a_weakened_workspace_dacl() {
        use std::os::windows::ffi::OsStrExt;
        use std::ptr::null_mut;
        use windows_sys::Win32::Foundation::LocalFree;
        use windows_sys::Win32::Security::Authorization::{
            ConvertStringSecurityDescriptorToSecurityDescriptorW, SDDL_REVISION_1,
        };
        use windows_sys::Win32::Security::{
            SetFileSecurityW, DACL_SECURITY_INFORMATION, PROTECTED_DACL_SECURITY_INFORMATION,
        };

        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterJournalCreated,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let mut descriptor = null_mut();
        let sddl: Vec<u16> = "D:P(A;;FA;;;WD)\0".encode_utf16().collect();
        assert_ne!(
            unsafe {
                ConvertStringSecurityDescriptorToSecurityDescriptorW(
                    sddl.as_ptr(),
                    SDDL_REVISION_1,
                    &mut descriptor,
                    null_mut(),
                )
            },
            0
        );
        let wide: Vec<u16> = workspace.as_os_str().encode_wide().chain(Some(0)).collect();
        let applied = unsafe {
            SetFileSecurityW(
                wide.as_ptr(),
                DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                descriptor,
            )
        };
        unsafe { LocalFree(descriptor) };
        assert_ne!(applied, 0);

        let error = recover_create_only(root.path())
            .expect_err("a world-accessible workspace DACL must block recovery");
        assert!(matches!(error, TransactionError::Io { .. }));
        assert!(workspace.join(JOURNAL_FILE).exists());
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn recovery_refuses_a_workspace_with_an_extended_acl() {
        use std::process::Command;

        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "synthetic-key")],
            FaultPoint::AfterJournalCreated,
        )
        .expect_err("fault is injected");

        let transaction_root = root.path().join(TRANSACTION_DIRECTORY);
        let workspace = fs::read_dir(&transaction_root)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let status = Command::new("chmod")
            .arg("+a")
            .arg("everyone allow read")
            .arg(&workspace)
            .status()
            .unwrap();
        assert!(status.success());

        let error = recover_create_only(root.path())
            .expect_err("extended ACL must block private-workspace recovery");
        assert!(matches!(error, TransactionError::RecoveryConflict(_)));
        assert!(workspace.join(JOURNAL_FILE).exists());
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn a_removed_lock_path_cannot_be_validated_through_its_held_inode() {
        use std::os::unix::fs::OpenOptionsExt;

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join(TRANSACTION_LOCK);
        let file = OpenOptions::new()
            .create_new(true)
            .read(true)
            .write(true)
            .mode(0o600)
            .open(&path)
            .unwrap();
        fs::remove_file(&path).unwrap();

        let error = validate_transaction_lock_path(&path, &file)
            .expect_err("the lock path must still name the held inode");
        assert!(matches!(error, TransactionError::Io { .. }));
    }

    #[cfg(unix)]
    #[test]
    fn rejects_symlinked_parent() {
        use std::os::unix::fs::symlink;
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        symlink(outside.path(), root.path().join("tool")).unwrap();
        let error = apply_create_only(root.path(), vec![plan("tool/config.json", "secret")])
            .expect_err("symlink must fail");
        assert!(matches!(error, TransactionError::LinkedPath(_)));
        assert!(!outside.path().join("config.json").exists());
    }

    #[cfg(unix)]
    #[test]
    fn private_posix_modes_are_enforced() {
        use std::os::unix::fs::PermissionsExt;
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("tool")).unwrap();
        apply_create_only_inner(
            root.path(),
            vec![plan("tool/config.json", "secret")],
            FaultPoint::AfterPublished(0),
        )
        .expect_err("fault is injected");
        let target_mode = fs::metadata(root.path().join("tool/config.json"))
            .unwrap()
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(target_mode, 0o600);
        let root_mode = fs::metadata(root.path().join(TRANSACTION_DIRECTORY))
            .unwrap()
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(root_mode, 0o700);
    }
}
