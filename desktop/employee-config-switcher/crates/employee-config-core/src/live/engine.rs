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
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Component, Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};
use zeroize::Zeroizing;

const TRANSACTION_DIRECTORY: &str = ".cpa-cloud-employee-config-transactions";
const JOURNAL_FILE: &str = "journal.jsonl";
const MAX_FILE_BYTES: usize = 4 * 1024 * 1024;
static TRANSACTION_COUNTER: AtomicU64 = AtomicU64::new(0);

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
#[serde(tag = "state", rename_all = "snake_case")]
enum JournalEvent {
    Begin,
    Planned {
        index: usize,
        target: PathBuf,
        stage: String,
        digest: String,
        stage_identity: FileIdentity,
        parent_identity: FileIdentity,
    },
    Published {
        index: usize,
        target_identity: FileIdentity,
    },
    Verified { index: usize },
    Complete,
}

#[derive(Debug)]
struct PlannedFile {
    target: PathBuf,
    stage: String,
    digest: String,
    stage_identity: FileIdentity,
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum FaultPoint {
    Never,
    AfterPlanning,
    AfterPublishBeforeJournal(usize),
    AfterPublished(usize),
    AfterVerified(usize),
}

/// Recover incomplete create-only transactions below `root`.
///
/// A published target is removed only when its stable identity and SHA-256
/// digest still match the transaction. An external edit blocks recovery and
/// therefore blocks later writes.
pub fn recover_create_only(root: &Path) -> Result<(), TransactionError> {
    let root = checked_root(root)?;
    let transaction_root = root.join(TRANSACTION_DIRECTORY);
    match fs::symlink_metadata(&transaction_root) {
        Ok(_) => ensure_real_directory(&transaction_root)?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(source) => return Err(io_error(&transaction_root, source)),
    }
    for entry in fs::read_dir(&transaction_root)
        .map_err(|source| io_error(&transaction_root, source))?
    {
        let path = entry
            .map_err(|source| io_error(&transaction_root, source))?
            .path();
        ensure_real_directory(&path)?;
        recover_workspace(&root, &path)?;
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
    recover_create_only(&root)?;
    validate_plans(&root, &files)?;

    let transaction_root = ensure_transaction_root(&root)?;
    let workspace = create_workspace(&transaction_root)?;
    let journal_path = workspace.join(JOURNAL_FILE);
    let mut journal = create_private_file(&journal_path)?;
    let mut created = Vec::with_capacity(files.len());

    let result = (|| {
        append_event(&mut journal, &journal_path, &JournalEvent::Begin)?;
        for (index, plan) in files.iter().enumerate() {
            let target = checked_target(&root, &plan.relative_path)?;
            let parent = target
                .parent()
                .ok_or_else(|| TransactionError::InvalidTarget(plan.relative_path.clone()))?;
            let stage = format!("stage-{index:04}.bin");
            let stage_path = workspace.join(&stage);
            let mut stage_file = create_private_file(&stage_path)?;
            stage_file
                .write_all(&plan.contents)
                .map_err(|source| io_error(&stage_path, source))?;
            stage_file
                .sync_all()
                .map_err(|source| io_error(&stage_path, source))?;
            append_event(
                &mut journal,
                &journal_path,
                &JournalEvent::Planned {
                    index,
                    target: plan.relative_path.clone(),
                    stage,
                    digest: digest(Some(&plan.contents)).expect("bytes have a digest"),
                    stage_identity: file_identity(&stage_file, &stage_path)?,
                    parent_identity: identity_for_path(parent)?,
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
            ensure_absent(&target)?;

            let stage_path = workspace.join(&planned.stage);
            let stage_file = File::open(&stage_path).map_err(|source| io_error(&stage_path, source))?;
            if file_identity(&stage_file, &stage_path)? != planned.stage_identity {
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
            if target_identity != planned.stage_identity {
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
        Err(error) => recover_workspace(&root, &workspace).and(Err(error)),
    }
}

fn validate_plans(root: &Path, files: &[CreateFilePlan]) -> Result<(), TransactionError> {
    if files.is_empty() {
        return Err(TransactionError::InvalidTarget(PathBuf::new()));
    }
    let mut unique = HashSet::new();
    for plan in files {
        if plan.contents.len() > MAX_FILE_BYTES {
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
    ensure_no_links(&absolute)?;
    ensure_real_directory(&absolute)?;
    fs::canonicalize(&absolute).map_err(|source| io_error(&absolute, source))
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
    enforce_private_permissions(&path)?;
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
    serde_json::to_writer(&mut *journal, event).map_err(|source| {
        io_error(path, io::Error::other(source))
    })?;
    journal
        .write_all(b"\n")
        .map_err(|source| io_error(path, source))?;
    journal.sync_all().map_err(|source| io_error(path, source))
}

fn read_journal(path: &Path) -> Result<JournalState, TransactionError> {
    let file = File::open(path).map_err(|source| io_error(path, source))?;
    let mut state = JournalState::default();
    for line in BufReader::new(file).lines() {
        let line = line.map_err(|source| io_error(path, source))?;
        if line.is_empty() {
            return Err(TransactionError::CorruptJournal(path.to_path_buf()));
        }
        let event: JournalEvent = serde_json::from_str(&line)
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
                stage_identity,
                parent_identity,
            } => {
                validate_relative(&target)?;
                if !state.begun
                    || state.complete
                    || !stage.starts_with("stage-")
                    || stage.contains('/')
                    || stage.contains('\\')
                    || state
                        .files
                        .insert(
                            index,
                            PlannedFile {
                                target,
                                stage,
                                digest,
                                stage_identity,
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
                if planned.target_identity.replace(target_identity).is_some() {
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

fn recover_workspace(root: &Path, workspace: &Path) -> Result<(), TransactionError> {
    ensure_real_directory(workspace)?;
    let journal_path = workspace.join(JOURNAL_FILE);
    let state = read_journal(&journal_path)?;
    if !state.complete {
        for planned in state.files.values().rev() {
            let target = checked_target(root, &planned.target)?;
            let stage_path = workspace.join(&planned.stage);
            let stage = File::open(&stage_path).map_err(|source| io_error(&stage_path, source))?;
            if file_identity(&stage, &stage_path)? != planned.stage_identity {
                return Err(TransactionError::RecoveryConflict(stage_path));
            }
            match fs::symlink_metadata(&target) {
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(source) => return Err(io_error(&target, source)),
                Ok(_) => {
                    ensure_real_file(&target)?;
                    let target_file =
                        File::open(&target).map_err(|source| io_error(&target, source))?;
                    let target_identity = file_identity(&target_file, &target)?;
                    let expected = planned.target_identity.unwrap_or(planned.stage_identity);
                    if target_identity != expected
                        || target_identity != planned.stage_identity
                        || digest(Some(&read_bounded(&target)?)).as_deref()
                            != Some(planned.digest.as_str())
                    {
                        return Err(TransactionError::RecoveryConflict(target));
                    }
                    fs::remove_file(&target).map_err(|source| io_error(&target, source))?;
                    if let Some(parent) = target.parent() {
                        sync_directory(parent).map_err(|source| io_error(parent, source))?;
                    }
                }
            }
        }
    }
    cleanup_workspace(workspace, &state)
}

fn cleanup_workspace(workspace: &Path, state: &JournalState) -> Result<(), TransactionError> {
    for planned in state.files.values() {
        let stage = workspace.join(&planned.stage);
        match fs::remove_file(&stage) {
            Ok(()) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(source) => return Err(io_error(&stage, source)),
        }
    }
    let journal = workspace.join(JOURNAL_FILE);
    fs::remove_file(&journal).map_err(|source| io_error(&journal, source))?;
    fs::remove_dir(workspace).map_err(|source| io_error(workspace, source))?;
    if let Some(parent) = workspace.parent() {
        sync_directory(parent).map_err(|source| io_error(parent, source))?;
    }
    Ok(())
}

fn verify_target(target: &Path, planned: &PlannedFile) -> Result<(), TransactionError> {
    ensure_real_file(target)?;
    let file = File::open(target).map_err(|source| io_error(target, source))?;
    if file_identity(&file, target)? != planned.stage_identity
        || digest(Some(&read_bounded(target)?)).as_deref() != Some(planned.digest.as_str())
    {
        return Err(TransactionError::RecoveryConflict(target.to_path_buf()));
    }
    Ok(())
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
        builder.create(path).map_err(|source| io_error(path, source))?;
    }
    #[cfg(windows)]
    fs::create_dir(path).map_err(|source| io_error(path, source))?;
    enforce_private_permissions(path)
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
    enforce_private_permissions(path)?;
    Ok(file)
}

#[cfg(unix)]
fn enforce_private_permissions(path: &Path) -> Result<(), TransactionError> {
    use std::os::unix::fs::PermissionsExt;
    let metadata = fs::symlink_metadata(path).map_err(|source| io_error(path, source))?;
    let mode = if metadata.is_dir() { 0o700 } else { 0o600 };
    fs::set_permissions(path, fs::Permissions::from_mode(mode))
        .map_err(|source| io_error(path, source))
}

#[cfg(windows)]
fn enforce_private_permissions(path: &Path) -> Result<(), TransactionError> {
    use std::os::windows::ffi::OsStrExt;
    use std::ptr::null_mut;
    use windows_sys::Win32::Foundation::LocalFree;
    use windows_sys::Win32::Security::Authorization::{
        ConvertStringSecurityDescriptorToSecurityDescriptorW, SDDL_REVISION_1,
    };
    use windows_sys::Win32::Security::{
        SetFileSecurityW, DACL_SECURITY_INFORMATION, PROTECTED_DACL_SECURITY_INFORMATION,
    };

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
    let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
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
        Ok(())
    }
}

fn identity_for_path(path: &Path) -> Result<FileIdentity, TransactionError> {
    let file = File::open(path).map_err(|source| io_error(path, source))?;
    file_identity(&file, path)
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
    use std::os::windows::fs::OpenOptionsExt;
    use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_BACKUP_SEMANTICS;
    OpenOptions::new()
        .read(true)
        .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
        .open(path)?
        .sync_all()
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
        assert_eq!(fs::read_to_string(root.path().join("tool-a/config.json")).unwrap(), secret);
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
    fn crash_after_unjournaled_publish_is_recovered_by_identity() {
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
        assert!(!root.path().join("tool/config.json").exists());
    }

    #[test]
    fn crash_after_verified_publish_rolls_back_all_files() {
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
        assert!(!root.path().join("tool/a.json").exists());
        assert!(!root.path().join("tool/b.json").exists());
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
