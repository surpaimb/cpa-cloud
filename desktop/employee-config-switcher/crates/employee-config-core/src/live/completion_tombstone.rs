//! Durable completion tombstones for a bounded synthetic-root experiment.
//!
//! Linux uses held directory descriptors, no-follow opens, owner-private modes
//! and directory flushes. Windows and macOS remain unsupported. This module
//! does not delete recovery keys, remove transaction workspaces or touch real
//! client directories.

#[cfg(target_os = "linux")]
use super::recovery_key::RecoveryKeyMetadataError;
use super::{
    recovery_key::{
        CompletionTombstoneRemovalAuthorization, CompletionTombstoneRemovalReceipt,
        RecoveryKeyReference, ReplacementTransactionId,
    },
    replacement::ReplacementPhase,
};
use serde::{Deserialize, Serialize};
#[cfg(target_os = "linux")]
use std::ffi::{CStr, CString, OsStr, OsString};
#[cfg(target_os = "linux")]
use std::fs::File;
use std::io;
#[cfg(target_os = "linux")]
use std::io::{Read, Write};
#[cfg(target_os = "linux")]
use std::path::Component;
use std::path::{Path, PathBuf};

const TOMBSTONE_DIRECTORY: &str = ".cpa-cloud-employee-config-completions";
#[cfg(target_os = "linux")]
const FORMAT_VERSION: u32 = 1;
#[cfg(target_os = "linux")]
const MAX_TOMBSTONES: usize = 64;
#[cfg(target_os = "linux")]
const MAX_TOMBSTONE_BYTES: usize = 16 * 1024;
#[cfg(target_os = "linux")]
const TRANSACTION_ID_HEX_BYTES: usize = 32;
#[cfg(target_os = "linux")]
const TOMBSTONE_SUFFIX: &str = ".json";
#[cfg(target_os = "linux")]
const PENDING_SUFFIX: &str = ".pending";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CompletionTerminal {
    Complete,
    RolledBack,
}

impl CompletionTerminal {
    fn from_phase(phase: ReplacementPhase) -> Result<Self, CompletionTombstoneError> {
        match phase {
            ReplacementPhase::Complete => Ok(Self::Complete),
            ReplacementPhase::RolledBack => Ok(Self::RolledBack),
            _ => Err(CompletionTombstoneError::InvalidTerminal),
        }
    }
}

#[derive(Clone, PartialEq, Eq)]
pub struct CompletionTombstone {
    transaction_id: ReplacementTransactionId,
    terminal: CompletionTerminal,
    reference: RecoveryKeyReference,
}

impl CompletionTombstone {
    pub fn transaction_id(&self) -> ReplacementTransactionId {
        self.transaction_id
    }

    pub fn terminal(&self) -> CompletionTerminal {
        self.terminal
    }

    pub fn reference(&self) -> &RecoveryKeyReference {
        &self.reference
    }
}

impl std::fmt::Debug for CompletionTombstone {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CompletionTombstone")
            .field("transaction_id", &self.transaction_id)
            .field("terminal", &self.terminal)
            .field("reference", &self.reference)
            .finish()
    }
}

#[derive(Debug, thiserror::Error)]
pub enum CompletionTombstoneError {
    #[error("completion tombstones are not supported on this platform")]
    UnsupportedPlatform,
    #[error("completion tombstone requires Complete or RolledBack")]
    InvalidTerminal,
    #[error("completion tombstone root must not be empty or contain parent traversal: {0}")]
    InvalidRoot(PathBuf),
    #[error("completion tombstone metadata is invalid")]
    InvalidMetadata,
    #[error("completion tombstone is corrupt")]
    Corrupt,
    #[error("incomplete completion tombstone publication requires recovery: {0}")]
    IncompleteWrite(PathBuf),
    #[error("completion tombstone is missing: {0}")]
    Missing(PathBuf),
    #[error("completion tombstone namespace exceeds its bounded entry count")]
    TooMany,
    #[error("completion tombstone namespace or object changed: {0}")]
    RecoveryConflict(PathBuf),
    #[error("completion tombstone filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
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
struct TombstoneManifest {
    version: u32,
    transaction_id: String,
    root_identity: RootIdentity,
    terminal: CompletionTerminal,
    provider_kind: String,
    provider_version: u32,
    opaque_reference: String,
}

pub fn persist_completion_tombstone(
    root: &Path,
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: &RecoveryKeyReference,
) -> Result<CompletionTombstone, CompletionTombstoneError> {
    let terminal = CompletionTerminal::from_phase(terminal)?;
    platform::persist(root, terminal, transaction_id, reference)
}

pub fn load_completion_tombstones(
    root: &Path,
) -> Result<Vec<CompletionTombstone>, CompletionTombstoneError> {
    platform::load_all(root)
}

/// Remove one exact completion tombstone after recovery-key deletion has been
/// confirmed by the cleanup sequence.
///
/// The Linux implementation revalidates the durable transaction/reference
/// binding, removes only the final record and flushes both the namespace and
/// its root. Missing records fail closed rather than being accepted as an
/// idempotent success. Windows and macOS remain unsupported.
pub fn remove_completion_tombstone(
    root: &Path,
    authorization: &CompletionTombstoneRemovalAuthorization,
) -> Result<CompletionTombstoneRemovalReceipt, CompletionTombstoneError> {
    platform::remove(root, authorization)
}

#[cfg(target_os = "linux")]
fn transaction_hex(transaction_id: ReplacementTransactionId) -> String {
    hex::encode(transaction_id.as_bytes())
}

#[cfg(target_os = "linux")]
fn tombstone_name(transaction_id: ReplacementTransactionId) -> String {
    format!("{}{TOMBSTONE_SUFFIX}", transaction_hex(transaction_id))
}

#[cfg(target_os = "linux")]
fn pending_name(transaction_id: ReplacementTransactionId) -> String {
    format!("{}{PENDING_SUFFIX}", transaction_hex(transaction_id))
}

#[cfg(target_os = "linux")]
fn parse_transaction_hex(
    value: &str,
) -> Result<ReplacementTransactionId, CompletionTombstoneError> {
    if value.len() != TRANSACTION_ID_HEX_BYTES
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(CompletionTombstoneError::Corrupt);
    }
    let bytes = hex::decode(value).map_err(|_| CompletionTombstoneError::Corrupt)?;
    let bytes: [u8; 16] = bytes
        .try_into()
        .map_err(|_| CompletionTombstoneError::Corrupt)?;
    ReplacementTransactionId::from_bytes(bytes).map_err(|_| CompletionTombstoneError::Corrupt)
}

#[cfg(target_os = "linux")]
fn parse_reference(
    manifest: &TombstoneManifest,
) -> Result<RecoveryKeyReference, CompletionTombstoneError> {
    if manifest.opaque_reference.is_empty()
        || manifest.opaque_reference.len() > 8 * 1024
        || manifest.opaque_reference.len() % 2 != 0
        || !manifest
            .opaque_reference
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err(CompletionTombstoneError::Corrupt);
    }
    let opaque =
        hex::decode(&manifest.opaque_reference).map_err(|_| CompletionTombstoneError::Corrupt)?;
    RecoveryKeyReference::new(
        manifest.provider_kind.clone(),
        manifest.provider_version,
        opaque,
    )
    .map_err(|_: RecoveryKeyMetadataError| CompletionTombstoneError::Corrupt)
}

#[cfg(target_os = "linux")]
fn io_error(path: impl Into<PathBuf>, source: io::Error) -> CompletionTombstoneError {
    CompletionTombstoneError::Io {
        path: path.into(),
        source,
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;
    use std::os::fd::{AsRawFd, FromRawFd, RawFd};
    use std::os::unix::ffi::{OsStrExt, OsStringExt};
    use std::os::unix::fs::MetadataExt;

    #[derive(Debug, Clone, Copy, PartialEq, Eq)]
    #[allow(dead_code)]
    pub(super) enum PersistFault {
        None,
        AfterStageCreated,
        AfterStageSynced,
        AfterPublished,
    }

    pub(super) fn persist(
        root: &Path,
        terminal: CompletionTerminal,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> Result<CompletionTombstone, CompletionTombstoneError> {
        persist_inner(
            root,
            terminal,
            transaction_id,
            reference,
            PersistFault::None,
        )
    }

    #[cfg(test)]
    pub(super) fn persist_with_fault(
        root: &Path,
        terminal: CompletionTerminal,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
        fault: PersistFault,
    ) -> Result<CompletionTombstone, CompletionTombstoneError> {
        persist_inner(root, terminal, transaction_id, reference, fault)
    }

    fn persist_inner(
        root: &Path,
        terminal: CompletionTerminal,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
        fault: PersistFault,
    ) -> Result<CompletionTombstone, CompletionTombstoneError> {
        let existing = load_all(root)?;
        if existing
            .iter()
            .any(|tombstone| tombstone.transaction_id == transaction_id)
        {
            return Err(CompletionTombstoneError::RecoveryConflict(
                root.join(TOMBSTONE_DIRECTORY)
                    .join(tombstone_name(transaction_id)),
            ));
        }
        if existing.len() >= MAX_TOMBSTONES {
            return Err(CompletionTombstoneError::TooMany);
        }
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let directory_path = root.join(TOMBSTONE_DIRECTORY);
        let (directory, created) = open_or_create_private_directory(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            directory_path.clone(),
        )?;
        let directory_identity =
            identity(&directory).map_err(|source| io_error(&directory_path, source))?;
        if created {
            root_dir
                .sync_all()
                .map_err(|source| io_error(root, source))?;
        }

        let transaction_text = transaction_hex(transaction_id);
        let manifest = TombstoneManifest {
            version: FORMAT_VERSION,
            transaction_id: transaction_text,
            root_identity,
            terminal,
            provider_kind: reference.provider_kind().to_owned(),
            provider_version: reference.provider_version(),
            opaque_reference: hex::encode(reference.opaque_reference()),
        };
        let bytes =
            serde_json::to_vec(&manifest).map_err(|_| CompletionTombstoneError::InvalidMetadata)?;
        if bytes.len() > MAX_TOMBSTONE_BYTES {
            return Err(CompletionTombstoneError::InvalidMetadata);
        }

        let final_name = tombstone_name(transaction_id);
        let final_path = directory_path.join(&final_name);
        let stage_name = pending_name(transaction_id);
        let stage_path = directory_path.join(&stage_name);
        let mut file =
            create_private_file_at(directory.as_raw_fd(), OsStr::new(&stage_name), &stage_path)?;
        let file_identity = identity(&file).map_err(|source| io_error(&stage_path, source))?;
        fail_at(fault, PersistFault::AfterStageCreated, &stage_path)?;
        file.write_all(&bytes)
            .map_err(|source| io_error(&stage_path, source))?;
        verify_private_file(&file, &stage_path)?;
        file.sync_all()
            .map_err(|source| io_error(&stage_path, source))?;
        fail_at(fault, PersistFault::AfterStageSynced, &stage_path)?;
        rename_no_replace(
            directory.as_raw_fd(),
            OsStr::new(&stage_name),
            OsStr::new(&final_name),
            &final_path,
        )?;
        fail_at(fault, PersistFault::AfterPublished, &final_path)?;
        directory
            .sync_all()
            .map_err(|source| io_error(&directory_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        verify_private_directory(&directory, &directory_path)?;
        let reopened_file =
            open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        if identity(&reopened_file).map_err(|source| io_error(&final_path, source))?
            != file_identity
        {
            return Err(CompletionTombstoneError::RecoveryConflict(final_path));
        }
        let reopened_directory = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        )?;
        if identity(&reopened_directory).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CompletionTombstoneError::RecoveryConflict(directory_path));
        }

        Ok(CompletionTombstone {
            transaction_id,
            terminal,
            reference: reference.clone(),
        })
    }

    pub(super) fn load_all(
        root: &Path,
    ) -> Result<Vec<CompletionTombstone>, CompletionTombstoneError> {
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let directory_path = root.join(TOMBSTONE_DIRECTORY);
        let directory = match open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        ) {
            Ok(directory) => directory,
            Err(CompletionTombstoneError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                return Ok(Vec::new());
            }
            Err(error) => return Err(error),
        };
        let directory_identity =
            identity(&directory).map_err(|source| io_error(&directory_path, source))?;

        let names_before = read_directory_names(&directory, &directory_path)?;
        let mut tombstones = Vec::with_capacity(names_before.len());
        for name in &names_before {
            let name_text = name.to_str().ok_or(CompletionTombstoneError::Corrupt)?;
            if let Some(transaction_text) = name_text.strip_suffix(PENDING_SUFFIX) {
                parse_transaction_hex(transaction_text)?;
                return Err(CompletionTombstoneError::IncompleteWrite(
                    directory_path.join(name),
                ));
            }
            let transaction_text = name_text
                .strip_suffix(TOMBSTONE_SUFFIX)
                .ok_or(CompletionTombstoneError::Corrupt)?;
            let expected_transaction = parse_transaction_hex(transaction_text)?;
            let path = directory_path.join(name);
            let mut file = open_private_file_at(directory.as_raw_fd(), name, &path)?;
            let opened_identity = identity(&file).map_err(|source| io_error(&path, source))?;
            let bytes = read_bounded(&mut file, MAX_TOMBSTONE_BYTES, &path)?;
            verify_private_file(&file, &path)?;
            let reopened = open_private_file_for_repair_at(directory.as_raw_fd(), name, &path)?;
            if identity(&reopened).map_err(|source| io_error(&path, source))? != opened_identity {
                return Err(CompletionTombstoneError::RecoveryConflict(path));
            }

            let manifest: TombstoneManifest =
                serde_json::from_slice(&bytes).map_err(|_| CompletionTombstoneError::Corrupt)?;
            if manifest.version != FORMAT_VERSION
                || manifest.root_identity != root_identity
                || manifest.transaction_id != transaction_text
            {
                return Err(CompletionTombstoneError::Corrupt);
            }
            let transaction_id = parse_transaction_hex(&manifest.transaction_id)?;
            if transaction_id != expected_transaction {
                return Err(CompletionTombstoneError::Corrupt);
            }
            let reference = parse_reference(&manifest)?;
            reopened
                .sync_all()
                .map_err(|source| io_error(&path, source))?;
            tombstones.push(CompletionTombstone {
                transaction_id,
                terminal: manifest.terminal,
                reference,
            });
        }

        directory
            .sync_all()
            .map_err(|source| io_error(&directory_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let names_after = read_directory_names(&directory, &directory_path)?;
        if names_before != names_after {
            return Err(CompletionTombstoneError::RecoveryConflict(directory_path));
        }
        verify_private_directory(&directory, &directory_path)?;
        let reopened_directory = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        )?;
        if identity(&reopened_directory).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CompletionTombstoneError::RecoveryConflict(directory_path));
        }
        Ok(tombstones)
    }

    pub(super) fn remove(
        root: &Path,
        authorization: &CompletionTombstoneRemovalAuthorization,
    ) -> Result<CompletionTombstoneRemovalReceipt, CompletionTombstoneError> {
        let transaction_id = authorization.transaction_id();
        let final_name = tombstone_name(transaction_id);
        let final_path = root.join(TOMBSTONE_DIRECTORY).join(&final_name);
        let loaded = load_all(root)?;
        let Some(expected) = loaded
            .iter()
            .find(|tombstone| tombstone.transaction_id == transaction_id)
        else {
            return Err(CompletionTombstoneError::Missing(final_path));
        };
        if expected.terminal() != CompletionTerminal::from_phase(authorization.terminal())?
            || expected.reference() != authorization.reference()
        {
            return Err(CompletionTombstoneError::RecoveryConflict(final_path));
        }

        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let directory_path = root.join(TOMBSTONE_DIRECTORY);
        let directory = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        )?;
        let directory_identity =
            identity(&directory).map_err(|source| io_error(&directory_path, source))?;
        let mut file =
            open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        let file_identity = identity(&file).map_err(|source| io_error(&final_path, source))?;
        let bytes = read_bounded(&mut file, MAX_TOMBSTONE_BYTES, &final_path)?;
        verify_private_file(&file, &final_path)?;
        let manifest: TombstoneManifest =
            serde_json::from_slice(&bytes).map_err(|_| CompletionTombstoneError::Corrupt)?;
        let transaction_text = transaction_hex(transaction_id);
        let manifest_reference = parse_reference(&manifest)?;
        if manifest.version != FORMAT_VERSION
            || manifest.root_identity != root_identity
            || manifest.transaction_id != transaction_text
            || manifest.terminal != CompletionTerminal::from_phase(authorization.terminal())?
            || parse_transaction_hex(&manifest.transaction_id)? != transaction_id
            || &manifest_reference != authorization.reference()
        {
            return Err(CompletionTombstoneError::RecoveryConflict(final_path));
        }
        let reopened =
            open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        if identity(&reopened).map_err(|source| io_error(&final_path, source))? != file_identity {
            return Err(CompletionTombstoneError::RecoveryConflict(final_path));
        }
        let rebound_directory = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        )?;
        if identity(&rebound_directory).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CompletionTombstoneError::RecoveryConflict(directory_path));
        }

        unlink_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        directory
            .sync_all()
            .map_err(|source| io_error(&directory_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        match open_private_file_at(directory.as_raw_fd(), OsStr::new(&final_name), &final_path) {
            Err(CompletionTombstoneError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound => {}
            Ok(_) => {
                return Err(CompletionTombstoneError::RecoveryConflict(final_path));
            }
            Err(error) => return Err(error),
        }
        verify_private_directory(&directory, &directory_path)?;
        let rebound_directory = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TOMBSTONE_DIRECTORY),
            &directory_path,
        )?;
        if identity(&rebound_directory).map_err(|source| io_error(&directory_path, source))?
            != directory_identity
        {
            return Err(CompletionTombstoneError::RecoveryConflict(directory_path));
        }
        Ok(authorization.durable_removal_receipt())
    }

    fn open_root(path: &Path) -> Result<File, CompletionTombstoneError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|component| matches!(component, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(CompletionTombstoneError::InvalidRoot(path.to_path_buf()));
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
                    return Err(CompletionTombstoneError::InvalidRoot(path.to_path_buf()));
                }
            }
        }
        let metadata = directory
            .metadata()
            .map_err(|source| io_error(path, source))?;
        if !metadata.is_dir() {
            return Err(CompletionTombstoneError::RecoveryConflict(
                path.to_path_buf(),
            ));
        }
        Ok(directory)
    }

    fn open_or_create_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<(File, bool), CompletionTombstoneError> {
        let c_name = os_c_string(name)?;
        let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
        let created = result == 0;
        if result != 0 {
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::AlreadyExists {
                return Err(io_error(&display, error));
            }
        }
        let directory = open_directory_at(parent, name, &display)?;
        if created {
            set_mode(&directory, 0o700, &display)?;
        }
        verify_private_directory(&directory, &display)?;
        Ok((directory, created))
    }

    fn open_private_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CompletionTombstoneError> {
        let directory = open_directory_at(parent, name, display)?;
        verify_private_directory(&directory, display)?;
        Ok(directory)
    }

    fn open_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CompletionTombstoneError> {
        open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))
    }

    fn rename_no_replace(
        parent: RawFd,
        from: &OsStr,
        to: &OsStr,
        display: &Path,
    ) -> Result<(), CompletionTombstoneError> {
        let from = os_c_string(from)?;
        let to = os_c_string(to)?;
        let result = unsafe {
            libc::renameat2(
                parent,
                from.as_ptr(),
                parent,
                to.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        };
        if result != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn unlink_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<(), CompletionTombstoneError> {
        let name = os_c_string(name)?;
        if unsafe { libc::unlinkat(parent, name.as_ptr(), 0) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn fail_at(
        actual: PersistFault,
        expected: PersistFault,
        display: &Path,
    ) -> Result<(), CompletionTombstoneError> {
        if actual == expected {
            return Err(io_error(
                display,
                io::Error::other("injected completion tombstone crash boundary"),
            ));
        }
        Ok(())
    }

    fn verify_private_directory(
        directory: &File,
        display: &Path,
    ) -> Result<(), CompletionTombstoneError> {
        let metadata = directory
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_dir()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o700
        {
            return Err(CompletionTombstoneError::RecoveryConflict(
                display.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn create_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CompletionTombstoneError> {
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
    ) -> Result<File, CompletionTombstoneError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(display, source))?;
        verify_private_file(&file, display)?;
        Ok(file)
    }

    fn open_private_file_for_repair_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, CompletionTombstoneError> {
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

    fn verify_private_file(file: &File, display: &Path) -> Result<(), CompletionTombstoneError> {
        let metadata = file
            .metadata()
            .map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(CompletionTombstoneError::RecoveryConflict(
                display.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn set_mode(
        file: &File,
        mode: libc::mode_t,
        display: &Path,
    ) -> Result<(), CompletionTombstoneError> {
        if unsafe { libc::fchmod(file.as_raw_fd(), mode) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
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

    fn os_c_string(name: &OsStr) -> Result<CString, CompletionTombstoneError> {
        CString::new(name.as_bytes()).map_err(|_| CompletionTombstoneError::InvalidMetadata)
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
    ) -> Result<Vec<u8>, CompletionTombstoneError> {
        let mut bytes = Vec::new();
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(display, source))?;
        if bytes.len() > limit {
            return Err(CompletionTombstoneError::Corrupt);
        }
        Ok(bytes)
    }

    fn read_directory_names(
        directory: &File,
        display: &Path,
    ) -> Result<Vec<OsString>, CompletionTombstoneError> {
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
            if names.len() == MAX_TOMBSTONES {
                return Err(CompletionTombstoneError::TooMany);
            }
            names.push(OsString::from_vec(bytes.to_vec()));
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
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;

    pub(super) fn persist(
        _root: &Path,
        _terminal: CompletionTerminal,
        _transaction_id: ReplacementTransactionId,
        _reference: &RecoveryKeyReference,
    ) -> Result<CompletionTombstone, CompletionTombstoneError> {
        Err(CompletionTombstoneError::UnsupportedPlatform)
    }

    pub(super) fn load_all(
        _root: &Path,
    ) -> Result<Vec<CompletionTombstone>, CompletionTombstoneError> {
        Err(CompletionTombstoneError::UnsupportedPlatform)
    }

    pub(super) fn remove(
        _root: &Path,
        _authorization: &CompletionTombstoneRemovalAuthorization,
    ) -> Result<CompletionTombstoneRemovalReceipt, CompletionTombstoneError> {
        Err(CompletionTombstoneError::UnsupportedPlatform)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(target_os = "linux")]
    use crate::live::recovery_key::{
        RecoveryKeyCleanupPhase, RecoveryKeyCleanupSequence, RecoveryKeyCreateOutcome,
        RecoveryKeyDeleteOutcome, RecoveryKeyOpenOutcome, RecoveryKeyProvider,
    };
    #[cfg(target_os = "linux")]
    use std::fs;

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }

    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-sentinel".to_vec()).unwrap()
    }

    #[cfg(target_os = "linux")]
    struct DeletedProvider;

    #[cfg(target_os = "linux")]
    impl RecoveryKeyProvider for DeletedProvider {
        fn create(
            &mut self,
            _transaction_id: ReplacementTransactionId,
        ) -> RecoveryKeyCreateOutcome {
            panic!("removal authorization must not create a key")
        }

        fn open(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            panic!("removal authorization must not open a key")
        }

        fn delete(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            RecoveryKeyDeleteOutcome::Deleted
        }
    }

    #[cfg(target_os = "linux")]
    fn removal_authorization(
        transaction_id: ReplacementTransactionId,
        reference: RecoveryKeyReference,
    ) -> (
        RecoveryKeyCleanupSequence,
        CompletionTombstoneRemovalAuthorization,
    ) {
        let mut cleanup = RecoveryKeyCleanupSequence::after_durable_terminal(
            ReplacementPhase::Complete,
            transaction_id,
            reference,
        )
        .unwrap();
        cleanup.record_completion_tombstone_durable().unwrap();
        cleanup.record_workspace_removal_durable().unwrap();
        let mut provider = DeletedProvider;
        cleanup.delete_key(&mut provider).unwrap();
        let authorization = cleanup.authorize_completion_tombstone_removal().unwrap();
        (cleanup, authorization)
    }

    #[test]
    fn nonterminal_phase_is_rejected_before_platform_io() {
        let root = tempfile::tempdir().unwrap();
        assert!(matches!(
            persist_completion_tombstone(
                root.path(),
                ReplacementPhase::Preparing,
                transaction(0x10),
                &reference()
            ),
            Err(CompletionTombstoneError::InvalidTerminal)
        ));
        assert!(!root.path().join(TOMBSTONE_DIRECTORY).exists());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn tombstone_is_private_durable_and_reconstructed_after_restart() {
        use std::os::unix::fs::PermissionsExt;

        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let expected = persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x11),
            &reference(),
        )
        .unwrap();
        let directory = root_path.join(TOMBSTONE_DIRECTORY);
        let file = directory.join(tombstone_name(transaction(0x11)));
        assert_eq!(
            fs::metadata(&directory).unwrap().permissions().mode() & 0o777,
            0o700
        );
        assert_eq!(
            fs::metadata(&file).unwrap().permissions().mode() & 0o777,
            0o600
        );

        let loaded = load_completion_tombstones(&root_path).unwrap();
        assert_eq!(loaded, vec![expected]);
        assert_eq!(loaded[0].terminal(), CompletionTerminal::Complete);
        assert_eq!(loaded[0].transaction_id(), transaction(0x11));
        assert_eq!(loaded[0].reference().provider_kind(), "test-provider");
        assert_eq!(loaded[0].reference().opaque_reference(), b"opaque-sentinel");
        assert!(!format!("{:?}", loaded[0]).contains("opaque-sentinel"));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn crash_before_publish_never_exposes_a_partial_final_tombstone() {
        for (byte, fault) in [
            (0x12, platform::PersistFault::AfterStageCreated),
            (0x13, platform::PersistFault::AfterStageSynced),
        ] {
            let root = tempfile::tempdir().unwrap();
            let root_path = fs::canonicalize(root.path()).unwrap();
            let transaction_id = transaction(byte);
            platform::persist_with_fault(
                &root_path,
                CompletionTerminal::Complete,
                transaction_id,
                &reference(),
                fault,
            )
            .expect_err("fault is injected before publication");

            let directory = root_path.join(TOMBSTONE_DIRECTORY);
            assert!(!directory.join(tombstone_name(transaction_id)).exists());
            assert!(directory.join(pending_name(transaction_id)).exists());
            assert!(matches!(
                load_completion_tombstones(&root_path),
                Err(CompletionTombstoneError::IncompleteWrite(_))
            ));
        }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_repairs_a_published_but_not_directory_flushed_tombstone() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x14);
        platform::persist_with_fault(
            &root_path,
            CompletionTerminal::Complete,
            transaction_id,
            &reference(),
            platform::PersistFault::AfterPublished,
        )
        .expect_err("fault is injected before directory flush");

        let directory = root_path.join(TOMBSTONE_DIRECTORY);
        assert!(directory.join(tombstone_name(transaction_id)).exists());
        assert!(!directory.join(pending_name(transaction_id)).exists());
        let loaded = load_completion_tombstones(&root_path).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].transaction_id(), transaction_id);
        assert_eq!(loaded[0].reference(), &reference());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn authorized_removal_revalidates_and_flushes_the_exact_tombstone() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x15);
        let reference = reference();
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            &reference,
        )
        .unwrap();
        let (mut cleanup, authorization) = removal_authorization(transaction_id, reference);

        let receipt = remove_completion_tombstone(&root_path, &authorization).unwrap();
        cleanup
            .record_completion_tombstone_removal_durable(receipt)
            .unwrap();
        assert_eq!(cleanup.phase(), RecoveryKeyCleanupPhase::Complete);
        assert!(load_completion_tombstones(&root_path).unwrap().is_empty());
        assert!(matches!(
            remove_completion_tombstone(&root_path, &authorization),
            Err(CompletionTombstoneError::Missing(_))
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn removal_with_a_different_provider_reference_preserves_the_tombstone() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let transaction_id = transaction(0x16);
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction_id,
            &reference(),
        )
        .unwrap();
        let other_reference =
            RecoveryKeyReference::new("test-provider", 1, b"different-reference".to_vec()).unwrap();
        let (_cleanup, authorization) = removal_authorization(transaction_id, other_reference);

        assert!(matches!(
            remove_completion_tombstone(&root_path, &authorization),
            Err(CompletionTombstoneError::RecoveryConflict(_))
        ));
        let loaded = load_completion_tombstones(&root_path).unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].reference(), &reference());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_enumeration_is_sorted_and_rejects_unknown_entries() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::RolledBack,
            transaction(0x22),
            &reference(),
        )
        .unwrap();
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x11),
            &reference(),
        )
        .unwrap();

        let loaded = load_completion_tombstones(&root_path).unwrap();
        assert_eq!(loaded[0].transaction_id(), transaction(0x11));
        assert_eq!(loaded[1].transaction_id(), transaction(0x22));

        fs::write(
            root_path.join(TOMBSTONE_DIRECTORY).join("unexpected"),
            b"evidence",
        )
        .unwrap();
        assert!(matches!(
            load_completion_tombstones(&root_path),
            Err(CompletionTombstoneError::Corrupt)
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn restart_rejects_transaction_reference_rebinding() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x23),
            &reference(),
        )
        .unwrap();
        let path = root_path
            .join(TOMBSTONE_DIRECTORY)
            .join(tombstone_name(transaction(0x23)));
        let mut manifest: TombstoneManifest =
            serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        manifest.transaction_id = transaction_hex(transaction(0x24));
        fs::write(&path, serde_json::to_vec(&manifest).unwrap()).unwrap();

        assert!(matches!(
            load_completion_tombstones(&root_path),
            Err(CompletionTombstoneError::Corrupt)
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn weakened_or_replaced_tombstones_fail_closed() {
        use std::os::unix::fs::{symlink, PermissionsExt};

        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x33),
            &reference(),
        )
        .unwrap();
        let directory = root_path.join(TOMBSTONE_DIRECTORY);
        let file = directory.join(tombstone_name(transaction(0x33)));

        fs::set_permissions(&file, fs::Permissions::from_mode(0o640)).unwrap();
        assert!(matches!(
            load_completion_tombstones(&root_path),
            Err(CompletionTombstoneError::RecoveryConflict(_))
        ));
        fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).unwrap();

        fs::remove_file(&file).unwrap();
        let outside = tempfile::NamedTempFile::new().unwrap();
        symlink(outside.path(), &file).unwrap();
        assert!(load_completion_tombstones(&root_path).is_err());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn absent_namespace_is_an_empty_restart_set_and_duplicates_do_not_overwrite() {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        assert!(load_completion_tombstones(&root_path).unwrap().is_empty());

        persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x44),
            &reference(),
        )
        .unwrap();
        assert!(persist_completion_tombstone(
            &root_path,
            ReplacementPhase::Complete,
            transaction(0x44),
            &reference(),
        )
        .is_err());
        assert_eq!(load_completion_tombstones(&root_path).unwrap().len(), 1);
    }

    #[cfg(not(target_os = "linux"))]
    #[test]
    fn unsupported_platforms_do_not_create_tombstone_material() {
        let root = tempfile::tempdir().unwrap();
        assert!(matches!(
            persist_completion_tombstone(
                root.path(),
                ReplacementPhase::Complete,
                transaction(0x55),
                &reference()
            ),
            Err(CompletionTombstoneError::UnsupportedPlatform)
        ));
        assert!(!root.path().join(TOMBSTONE_DIRECTORY).exists());
    }
}
