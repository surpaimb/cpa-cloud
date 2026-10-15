//! Durable proof for removal of an empty synthetic transaction workspace.
//!
//! This Linux-only boundary removes only one exact owner-private empty
//! workspace below the create-only transaction directory. A private proof is
//! flushed before the directory is removed, then published only after the
//! transaction directory and synthetic root are flushed. An interrupted
//! pending proof blocks recovery; absence alone is never treated as success.

use super::{
    recovery_key::{
        RecoveryKeyReference, ReplacementTransactionId, WorkspaceRemovalAuthorization,
    },
    replacement::ReplacementPhase,
};
#[cfg(target_os = "linux")]
use serde::{Deserialize, Serialize};
use std::{fs::File, io, path::{Component, Path, PathBuf}};

const TRANSACTION_DIRECTORY: &str = ".cpa-cloud-employee-config-transactions";
const TRANSACTION_LOCK: &str = ".cpa-cloud-employee-config.lock";
const PROOF_DIRECTORY: &str = ".cpa-cloud-employee-config-workspace-removals";
#[cfg(target_os = "linux")]
const FORMAT_VERSION: u32 = 1;
#[cfg(target_os = "linux")]
const MAX_PROOFS: usize = 64;
#[cfg(target_os = "linux")]
const MAX_PROOF_BYTES: usize = 16 * 1024;
#[cfg(target_os = "linux")]
const FINAL_SUFFIX: &str = ".removed.json";
#[cfg(target_os = "linux")]
const PENDING_SUFFIX: &str = ".pending";

pub(crate) struct WorkspaceRemovalReceipt {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
}

impl WorkspaceRemovalReceipt {
    pub(crate) fn terminal(&self) -> ReplacementPhase {
        self.terminal
    }

    pub(crate) fn transaction_id(&self) -> ReplacementTransactionId {
        self.transaction_id
    }

    pub(crate) fn reference(&self) -> &RecoveryKeyReference {
        &self.reference
    }

    #[cfg(test)]
    pub(crate) fn for_test(authorization: &WorkspaceRemovalAuthorization) -> Self {
        Self {
            terminal: authorization.terminal(),
            transaction_id: authorization.transaction_id(),
            reference: authorization.reference().clone(),
        }
    }
}

pub(crate) struct VerifiedWorkspaceRemoval {
    receipt: WorkspaceRemovalReceipt,
    _transaction_lock: Option<File>,
}

impl VerifiedWorkspaceRemoval {
    pub(crate) fn receipt(&self) -> &WorkspaceRemovalReceipt {
        &self.receipt
    }
}

#[derive(Debug, thiserror::Error)]
pub enum WorkspaceRemovalError {
    #[error("durable workspace removal is not supported on this platform")]
    UnsupportedPlatform,
    #[error("workspace-removal root must not be empty or contain parent traversal: {0}")]
    InvalidRoot(PathBuf),
    #[error("workspace is not a canonical direct transaction child: {0}")]
    InvalidWorkspace(PathBuf),
    #[error("workspace-removal proof namespace exceeds its bounded count")]
    TooMany,
    #[error("workspace-removal proof is missing: {0:?}")]
    Missing(ReplacementTransactionId),
    #[error("incomplete workspace-removal proof requires recovery: {0}")]
    IncompleteWrite(PathBuf),
    #[error("workspace-removal proof is corrupt")]
    Corrupt,
    #[error("workspace-removal namespace or object changed: {0}")]
    RecoveryConflict(PathBuf),
    #[error("workspace-removal filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[doc(hidden)]
    #[error("injected workspace-removal interruption")]
    InjectedCrash,
}

pub(crate) fn remove_workspace(
    root: &Path,
    authorization: &WorkspaceRemovalAuthorization,
    workspace: &Path,
) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
    platform::remove(root, authorization, workspace)
}

pub(crate) fn verify_workspace_removal(
    root: &Path,
    authorization: &WorkspaceRemovalAuthorization,
) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
    platform::verify(root, authorization)
}

#[cfg(target_os = "linux")]
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct FileIdentity {
    device: u64,
    inode: u64,
}

#[cfg(target_os = "linux")]
#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct WorkspaceRemovalProof {
    version: u32,
    transaction_id: String,
    root_identity: FileIdentity,
    terminal: String,
    provider_kind: String,
    provider_version: u32,
    opaque_reference: String,
    workspace_name: String,
    workspace_parent_identity: FileIdentity,
    workspace_identity: FileIdentity,
}

#[cfg(target_os = "linux")]
fn transaction_hex(transaction_id: ReplacementTransactionId) -> String {
    hex::encode(transaction_id.as_bytes())
}

#[cfg(target_os = "linux")]
fn proof_name(transaction_id: ReplacementTransactionId) -> String {
    format!("{}{FINAL_SUFFIX}", transaction_hex(transaction_id))
}

#[cfg(target_os = "linux")]
fn pending_name(transaction_id: ReplacementTransactionId) -> String {
    format!("{}{PENDING_SUFFIX}", transaction_hex(transaction_id))
}

#[cfg(target_os = "linux")]
fn terminal_text(terminal: ReplacementPhase) -> Result<&'static str, WorkspaceRemovalError> {
    match terminal {
        ReplacementPhase::Complete => Ok("complete"),
        ReplacementPhase::RolledBack => Ok("rolled_back"),
        _ => Err(WorkspaceRemovalError::Corrupt),
    }
}

#[cfg(target_os = "linux")]
fn io_error(path: impl Into<PathBuf>, source: io::Error) -> WorkspaceRemovalError {
    WorkspaceRemovalError::Io {
        path: path.into(),
        source,
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;
    use std::{
        ffi::{CStr, CString, OsStr, OsString},
        io::{Read, Write},
        os::{
            fd::{AsRawFd, FromRawFd, RawFd},
            unix::{
                ffi::{OsStrExt, OsStringExt},
                fs::MetadataExt,
            },
        },
    };

    #[derive(Clone, Copy, PartialEq, Eq)]
    enum RemovalFault {
        Never,
        AfterDeleteBeforeProof,
    }

    pub(super) fn remove(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
        workspace: &Path,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        remove_inner(root, authorization, workspace, RemovalFault::Never, || Ok(()))
    }

    #[cfg(test)]
    pub(super) fn remove_with_hook<F>(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
        workspace: &Path,
        after_delete: bool,
        before_delete: F,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError>
    where
        F: FnMut() -> Result<(), WorkspaceRemovalError>,
    {
        remove_inner(
            root,
            authorization,
            workspace,
            if after_delete {
                RemovalFault::AfterDeleteBeforeProof
            } else {
                RemovalFault::Never
            },
            before_delete,
        )
    }

    fn remove_inner<F>(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
        workspace: &Path,
        fault: RemovalFault,
        mut before_delete: F,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError>
    where
        F: FnMut() -> Result<(), WorkspaceRemovalError>,
    {
        let workspace_name = validate_workspace_path(root, workspace)?;
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let transaction_lock = acquire_transaction_lock(root, &root_dir)?;
        let transaction_path = root.join(TRANSACTION_DIRECTORY);
        let transaction_dir = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TRANSACTION_DIRECTORY),
            &transaction_path,
        )?;
        let transaction_identity =
            identity(&transaction_dir).map_err(|source| io_error(&transaction_path, source))?;

        let proof_path = root.join(PROOF_DIRECTORY);
        let proof_dir = match open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(PROOF_DIRECTORY),
            &proof_path,
        ) {
            Ok(directory) => Some(directory),
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                None
            }
            Err(error) => return Err(error),
        };

        if let Some(proof_dir) = proof_dir {
            lock_exclusive(&proof_dir, &proof_path)?;
            let proof_count = validate_proof_namespace(&proof_dir, &proof_path)?;
            if final_exists(&proof_dir, authorization, &proof_path)? {
                let receipt = verify_locked(
                    root,
                    &root_dir,
                    root_identity,
                    authorization,
                    &transaction_dir,
                    transaction_identity,
                    &proof_dir,
                    &proof_path,
                )?;
                return Ok(VerifiedWorkspaceRemoval {
                    receipt,
                    _transaction_lock: Some(transaction_lock),
                });
            }
            if proof_count >= MAX_PROOFS {
                return Err(WorkspaceRemovalError::TooMany);
            }
            return remove_new(
                root,
                &root_dir,
                root_identity,
                authorization,
                workspace,
                workspace_name,
                &transaction_dir,
                transaction_identity,
                proof_dir,
                &proof_path,
                transaction_lock,
                fault,
                &mut before_delete,
            );
        }

        let workspace_dir = open_private_directory_at(
            transaction_dir.as_raw_fd(),
            workspace_name,
            workspace,
        )?;
        ensure_empty(&workspace_dir, workspace)?;
        let (proof_dir, created) = open_or_create_private_directory(
            root_dir.as_raw_fd(),
            OsStr::new(PROOF_DIRECTORY),
            &proof_path,
        )?;
        lock_exclusive(&proof_dir, &proof_path)?;
        if created {
            root_dir
                .sync_all()
                .map_err(|source| io_error(root, source))?;
        }
        let proof_count = validate_proof_namespace(&proof_dir, &proof_path)?;
        if proof_count >= MAX_PROOFS {
            return Err(WorkspaceRemovalError::TooMany);
        }
        remove_new_with_workspace(
            root,
            &root_dir,
            root_identity,
            authorization,
            workspace,
            workspace_name,
            &transaction_dir,
            transaction_identity,
            workspace_dir,
            proof_dir,
            &proof_path,
            transaction_lock,
            fault,
            &mut before_delete,
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn remove_new<F>(
        root: &Path,
        root_dir: &File,
        root_identity: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        workspace: &Path,
        workspace_name: &OsStr,
        transaction_dir: &File,
        transaction_identity: FileIdentity,
        proof_dir: File,
        proof_path: &Path,
        transaction_lock: File,
        fault: RemovalFault,
        before_delete: &mut F,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError>
    where
        F: FnMut() -> Result<(), WorkspaceRemovalError>,
    {
        let workspace_dir = open_private_directory_at(
            transaction_dir.as_raw_fd(),
            workspace_name,
            workspace,
        )?;
        ensure_empty(&workspace_dir, workspace)?;
        remove_new_with_workspace(
            root,
            root_dir,
            root_identity,
            authorization,
            workspace,
            workspace_name,
            transaction_dir,
            transaction_identity,
            workspace_dir,
            proof_dir,
            proof_path,
            transaction_lock,
            fault,
            before_delete,
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn remove_new_with_workspace<F>(
        root: &Path,
        root_dir: &File,
        root_identity: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        workspace: &Path,
        workspace_name: &OsStr,
        transaction_dir: &File,
        transaction_identity: FileIdentity,
        workspace_dir: File,
        proof_dir: File,
        proof_path: &Path,
        transaction_lock: File,
        fault: RemovalFault,
        before_delete: &mut F,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError>
    where
        F: FnMut() -> Result<(), WorkspaceRemovalError>,
    {
        let final_name = proof_name(authorization.transaction_id());
        let pending_name = pending_name(authorization.transaction_id());
        let final_path = proof_path.join(&final_name);
        let pending_path = proof_path.join(&pending_name);
        let workspace_identity =
            identity(&workspace_dir).map_err(|source| io_error(workspace, source))?;
        let proof = WorkspaceRemovalProof {
            version: FORMAT_VERSION,
            transaction_id: transaction_hex(authorization.transaction_id()),
            root_identity,
            terminal: terminal_text(authorization.terminal())?.to_owned(),
            provider_kind: authorization.reference().provider_kind().to_owned(),
            provider_version: authorization.reference().provider_version(),
            opaque_reference: hex::encode(authorization.reference().opaque_reference()),
            workspace_name: workspace_name
                .to_str()
                .ok_or_else(|| WorkspaceRemovalError::InvalidWorkspace(workspace.to_path_buf()))?
                .to_owned(),
            workspace_parent_identity: transaction_identity,
            workspace_identity,
        };
        let mut bytes = serde_json::to_vec(&proof).map_err(|_| WorkspaceRemovalError::Corrupt)?;
        bytes.push(b'\n');
        if bytes.len() > MAX_PROOF_BYTES {
            return Err(WorkspaceRemovalError::Corrupt);
        }
        let mut pending = create_private_file_at(
            proof_dir.as_raw_fd(),
            OsStr::new(&pending_name),
            &pending_path,
        )?;
        pending
            .write_all(&bytes)
            .map_err(|source| io_error(&pending_path, source))?;
        pending
            .sync_all()
            .map_err(|source| io_error(&pending_path, source))?;
        let pending_identity =
            identity(&pending).map_err(|source| io_error(&pending_path, source))?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;

        before_delete()?;
        verify_named_directory(
            transaction_dir,
            workspace_name,
            workspace,
            workspace_identity,
        )?;
        ensure_empty(&workspace_dir, workspace)?;
        if identity(transaction_dir).map_err(|source| io_error(root, source))?
            != transaction_identity
            || identity(root_dir).map_err(|source| io_error(root, source))? != root_identity
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(
                workspace.to_path_buf(),
            ));
        }
        remove_named_directory(transaction_dir, workspace_name, workspace)?;
        transaction_dir
            .sync_all()
            .map_err(|source| io_error(root.join(TRANSACTION_DIRECTORY), source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        ensure_child_absent(transaction_dir, workspace_name, workspace)?;
        if fault == RemovalFault::AfterDeleteBeforeProof {
            return Err(WorkspaceRemovalError::InjectedCrash);
        }

        rename_no_replace(
            proof_dir.as_raw_fd(),
            OsStr::new(&pending_name),
            OsStr::new(&final_name),
            &final_path,
        )?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let reopened = open_private_file_at(proof_dir.as_raw_fd(), OsStr::new(&final_name), &final_path)?;
        if identity(&reopened).map_err(|source| io_error(&final_path, source))?
            != pending_identity
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(final_path));
        }
        let receipt = validate_proof(
            &proof,
            root_identity,
            authorization,
            transaction_identity,
        )?;
        Ok(VerifiedWorkspaceRemoval {
            receipt,
            _transaction_lock: Some(transaction_lock),
        })
    }

    pub(super) fn verify(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let transaction_lock = acquire_transaction_lock(root, &root_dir)?;
        let transaction_path = root.join(TRANSACTION_DIRECTORY);
        let transaction_dir = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(TRANSACTION_DIRECTORY),
            &transaction_path,
        )?;
        let transaction_identity =
            identity(&transaction_dir).map_err(|source| io_error(&transaction_path, source))?;
        let proof_path = root.join(PROOF_DIRECTORY);
        let proof_dir = open_private_directory_at(
            root_dir.as_raw_fd(),
            OsStr::new(PROOF_DIRECTORY),
            &proof_path,
        )?;
        lock_exclusive(&proof_dir, &proof_path)?;
        validate_proof_namespace(&proof_dir, &proof_path)?;
        let receipt = verify_locked(
            root,
            &root_dir,
            root_identity,
            authorization,
            &transaction_dir,
            transaction_identity,
            &proof_dir,
            &proof_path,
        )?;
        Ok(VerifiedWorkspaceRemoval {
            receipt,
            _transaction_lock: Some(transaction_lock),
        })
    }

    #[allow(clippy::too_many_arguments)]
    fn verify_locked(
        root: &Path,
        root_dir: &File,
        root_identity: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        transaction_dir: &File,
        transaction_identity: FileIdentity,
        proof_dir: &File,
        proof_path: &Path,
    ) -> Result<WorkspaceRemovalReceipt, WorkspaceRemovalError> {
        let name = proof_name(authorization.transaction_id());
        let path = proof_path.join(&name);
        let mut file = match open_private_file_at(proof_dir.as_raw_fd(), OsStr::new(&name), &path) {
            Ok(file) => file,
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                return Err(WorkspaceRemovalError::Missing(
                    authorization.transaction_id(),
                ));
            }
            Err(error) => return Err(error),
        };
        let opened_identity = identity(&file).map_err(|source| io_error(&path, source))?;
        let bytes = read_bounded(&mut file, MAX_PROOF_BYTES, &path)?;
        if !bytes.ends_with(b"\n") {
            return Err(WorkspaceRemovalError::IncompleteWrite(path));
        }
        let proof: WorkspaceRemovalProof =
            serde_json::from_slice(&bytes).map_err(|_| WorkspaceRemovalError::Corrupt)?;
        let receipt = validate_proof(
            &proof,
            root_identity,
            authorization,
            transaction_identity,
        )?;
        let workspace = root
            .join(TRANSACTION_DIRECTORY)
            .join(&proof.workspace_name);
        ensure_child_absent(
            transaction_dir,
            OsStr::new(&proof.workspace_name),
            &workspace,
        )?;
        file.sync_all().map_err(|source| io_error(&path, source))?;
        transaction_dir
            .sync_all()
            .map_err(|source| io_error(root.join(TRANSACTION_DIRECTORY), source))?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let reopened = open_private_file_at(proof_dir.as_raw_fd(), OsStr::new(&name), &path)?;
        if identity(&reopened).map_err(|source| io_error(&path, source))? != opened_identity
            || identity(transaction_dir).map_err(|source| io_error(root, source))?
                != transaction_identity
            || identity(root_dir).map_err(|source| io_error(root, source))? != root_identity
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(path));
        }
        ensure_child_absent(
            transaction_dir,
            OsStr::new(&proof.workspace_name),
            &workspace,
        )?;
        Ok(receipt)
    }

    fn validate_proof(
        proof: &WorkspaceRemovalProof,
        root_identity: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        transaction_identity: FileIdentity,
    ) -> Result<WorkspaceRemovalReceipt, WorkspaceRemovalError> {
        if proof.version != FORMAT_VERSION
            || proof.transaction_id != transaction_hex(authorization.transaction_id())
            || proof.root_identity != root_identity
            || proof.terminal != terminal_text(authorization.terminal())?
            || proof.provider_kind != authorization.reference().provider_kind()
            || proof.provider_version != authorization.reference().provider_version()
            || proof.opaque_reference != hex::encode(authorization.reference().opaque_reference())
            || proof.workspace_parent_identity != transaction_identity
            || !valid_workspace_name(&proof.workspace_name)
        {
            return Err(WorkspaceRemovalError::Corrupt);
        }
        Ok(WorkspaceRemovalReceipt {
            terminal: authorization.terminal(),
            transaction_id: authorization.transaction_id(),
            reference: authorization.reference().clone(),
        })
    }

    fn final_exists(
        proof_dir: &File,
        authorization: &WorkspaceRemovalAuthorization,
        proof_path: &Path,
    ) -> Result<bool, WorkspaceRemovalError> {
        let name = proof_name(authorization.transaction_id());
        let path = proof_path.join(&name);
        match open_private_file_at(proof_dir.as_raw_fd(), OsStr::new(&name), &path) {
            Ok(_) => Ok(true),
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                Ok(false)
            }
            Err(error) => Err(error),
        }
    }

    fn validate_proof_namespace(
        directory: &File,
        display: &Path,
    ) -> Result<usize, WorkspaceRemovalError> {
        let names = read_directory_names(directory, display)?;
        if names.len() > MAX_PROOFS {
            return Err(WorkspaceRemovalError::TooMany);
        }
        for name in &names {
            let text = name.to_str().ok_or(WorkspaceRemovalError::Corrupt)?;
            if text.ends_with(PENDING_SUFFIX) {
                return Err(WorkspaceRemovalError::IncompleteWrite(display.join(name)));
            }
            let transaction = text
                .strip_suffix(FINAL_SUFFIX)
                .ok_or(WorkspaceRemovalError::Corrupt)?;
            if transaction.len() != 32
                || !transaction
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            {
                return Err(WorkspaceRemovalError::Corrupt);
            }
        }
        Ok(names.len())
    }

    fn validate_workspace_path<'a>(
        root: &Path,
        workspace: &'a Path,
    ) -> Result<&'a OsStr, WorkspaceRemovalError> {
        let name = workspace
            .file_name()
            .ok_or_else(|| WorkspaceRemovalError::InvalidWorkspace(workspace.to_path_buf()))?;
        let text = name
            .to_str()
            .ok_or_else(|| WorkspaceRemovalError::InvalidWorkspace(workspace.to_path_buf()))?;
        if workspace != root.join(TRANSACTION_DIRECTORY).join(name) || !valid_workspace_name(text) {
            return Err(WorkspaceRemovalError::InvalidWorkspace(
                workspace.to_path_buf(),
            ));
        }
        Ok(name)
    }

    fn valid_workspace_name(name: &str) -> bool {
        let mut parts = name.split('-');
        let (Some(prefix), Some(pid), Some(nonce), Some(count), None) = (
            parts.next(),
            parts.next(),
            parts.next(),
            parts.next(),
            parts.next(),
        ) else {
            return false;
        };
        prefix == "tx"
            && pid
                .parse::<u32>()
                .is_ok_and(|value| value.to_string() == pid)
            && u128::from_str_radix(nonce, 16)
                .is_ok_and(|value| format!("{value:x}") == nonce)
            && u64::from_str_radix(count, 16)
                .is_ok_and(|value| format!("{value:x}") == count)
    }

    fn acquire_transaction_lock(root: &Path, root_dir: &File) -> Result<File, WorkspaceRemovalError> {
        let path = root.join(TRANSACTION_LOCK);
        let file = open_private_file_at(root_dir.as_raw_fd(), OsStr::new(TRANSACTION_LOCK), &path)?;
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } != 0 {
            return Err(io_error(&path, io::Error::last_os_error()));
        }
        Ok(file)
    }

    fn open_root(path: &Path) -> Result<File, WorkspaceRemovalError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|component| matches!(component, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(WorkspaceRemovalError::InvalidRoot(path.to_path_buf()));
        }
        let anchor = if path.is_absolute() { Path::new("/") } else { Path::new(".") };
        let c_anchor = CString::new(anchor.as_os_str().as_bytes())
            .expect("Linux root anchors contain no NUL");
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
                    return Err(WorkspaceRemovalError::InvalidRoot(path.to_path_buf()));
                }
            }
        }
        Ok(directory)
    }

    fn open_or_create_private_directory(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<(File, bool), WorkspaceRemovalError> {
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
        Ok((directory, created))
    }

    fn open_private_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let directory = open_directory_at(parent, name, display)?;
        verify_private_directory(&directory, display)?;
        Ok(directory)
    }

    fn open_directory_at(
        parent: RawFd,
        name: &OsStr,
        display: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
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
    ) -> Result<File, WorkspaceRemovalError> {
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
    ) -> Result<File, WorkspaceRemovalError> {
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

    fn open_file_at(parent: RawFd, name: &OsStr, flags: i32, mode: libc::mode_t) -> io::Result<File> {
        let name = CString::new(name.as_bytes())
            .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
        let fd = unsafe { libc::openat(parent, name.as_ptr(), flags, mode as libc::c_uint) };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(unsafe { File::from_raw_fd(fd) })
    }

    fn verify_named_directory(
        parent: &File,
        name: &OsStr,
        display: &Path,
        expected: FileIdentity,
    ) -> Result<(), WorkspaceRemovalError> {
        let reopened = open_private_directory_at(parent.as_raw_fd(), name, display)?;
        if identity(&reopened).map_err(|source| io_error(display, source))? != expected {
            return Err(WorkspaceRemovalError::RecoveryConflict(display.to_path_buf()));
        }
        Ok(())
    }

    fn ensure_empty(directory: &File, display: &Path) -> Result<(), WorkspaceRemovalError> {
        if read_directory_names(directory, display)?.is_empty() {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(display.to_path_buf()))
        }
    }

    fn remove_named_directory(
        parent: &File,
        name: &OsStr,
        display: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        let name = os_c_string(name)?;
        if unsafe { libc::unlinkat(parent.as_raw_fd(), name.as_ptr(), libc::AT_REMOVEDIR) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn ensure_child_absent(
        parent: &File,
        name: &OsStr,
        display: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        let name = os_c_string(name)?;
        let mut stat: libc::stat = unsafe { std::mem::zeroed() };
        if unsafe {
            libc::fstatat(
                parent.as_raw_fd(),
                name.as_ptr(),
                &mut stat,
                libc::AT_SYMLINK_NOFOLLOW,
            )
        } == 0
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(display.to_path_buf()));
        }
        let error = io::Error::last_os_error();
        if error.kind() == io::ErrorKind::NotFound {
            Ok(())
        } else {
            Err(io_error(display, error))
        }
    }

    fn rename_no_replace(
        parent: RawFd,
        from: &OsStr,
        to: &OsStr,
        display: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
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

    fn verify_private_directory(file: &File, display: &Path) -> Result<(), WorkspaceRemovalError> {
        let metadata = file.metadata().map_err(|source| io_error(display, source))?;
        if !metadata.is_dir()
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o700
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(display.to_path_buf()));
        }
        Ok(())
    }

    fn verify_private_file(file: &File, display: &Path) -> Result<(), WorkspaceRemovalError> {
        let metadata = file.metadata().map_err(|source| io_error(display, source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
            || metadata.mode() & 0o7777 != 0o600
        {
            return Err(WorkspaceRemovalError::RecoveryConflict(display.to_path_buf()));
        }
        Ok(())
    }

    fn set_mode(file: &File, mode: libc::mode_t, display: &Path) -> Result<(), WorkspaceRemovalError> {
        if unsafe { libc::fchmod(file.as_raw_fd(), mode) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn lock_exclusive(file: &File, display: &Path) -> Result<(), WorkspaceRemovalError> {
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } != 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        Ok(())
    }

    fn identity(file: &File) -> io::Result<FileIdentity> {
        let metadata = file.metadata()?;
        Ok(FileIdentity {
            device: metadata.dev(),
            inode: metadata.ino(),
        })
    }

    fn read_bounded(
        file: &mut File,
        limit: usize,
        display: &Path,
    ) -> Result<Vec<u8>, WorkspaceRemovalError> {
        let mut bytes = Vec::new();
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(display, source))?;
        if bytes.len() > limit {
            return Err(WorkspaceRemovalError::Corrupt);
        }
        Ok(bytes)
    }

    fn read_directory_names(
        directory: &File,
        display: &Path,
    ) -> Result<Vec<OsString>, WorkspaceRemovalError> {
        let duplicate = unsafe { libc::fcntl(directory.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 0) };
        if duplicate < 0 {
            return Err(io_error(display, io::Error::last_os_error()));
        }
        let stream = unsafe { libc::fdopendir(duplicate) };
        if stream.is_null() {
            let source = io::Error::last_os_error();
            unsafe { libc::close(duplicate) };
            return Err(io_error(display, source));
        }
        let mut names = Vec::new();
        loop {
            unsafe { *libc::__errno_location() = 0 };
            let entry = unsafe { libc::readdir(stream) };
            if entry.is_null() {
                let source = io::Error::last_os_error();
                unsafe { libc::closedir(stream) };
                return if source.raw_os_error() == Some(0) {
                    names.sort();
                    Ok(names)
                } else {
                    Err(io_error(display, source))
                };
            }
            let name = unsafe { CStr::from_ptr((*entry).d_name.as_ptr()) }.to_bytes();
            if name != b"." && name != b".." {
                names.push(OsString::from_vec(name.to_vec()));
            }
        }
    }

    fn os_c_string(value: &OsStr) -> Result<CString, WorkspaceRemovalError> {
        CString::new(value.as_bytes()).map_err(|_| WorkspaceRemovalError::Corrupt)
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;

    pub(super) fn remove(
        _root: &Path,
        _authorization: &WorkspaceRemovalAuthorization,
        _workspace: &Path,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        Err(WorkspaceRemovalError::UnsupportedPlatform)
    }

    pub(super) fn verify(
        _root: &Path,
        _authorization: &WorkspaceRemovalAuthorization,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        Err(WorkspaceRemovalError::UnsupportedPlatform)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::live::recovery_key::RecoveryKeyCleanupSequence;
    #[cfg(target_os = "linux")]
    use std::{fs, os::unix::fs::PermissionsExt};

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }

    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-workspace-reference".to_vec())
            .unwrap()
    }

    fn authorization(byte: u8) -> WorkspaceRemovalAuthorization {
        let mut cleanup = RecoveryKeyCleanupSequence::after_durable_terminal(
            ReplacementPhase::Complete,
            transaction(byte),
            reference(),
        )
        .unwrap();
        cleanup.record_completion_tombstone_durable().unwrap();
        cleanup.authorize_workspace_removal().unwrap()
    }

    #[cfg(target_os = "linux")]
    fn fixture(byte: u8) -> (tempfile::TempDir, PathBuf) {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        let lock = root_path.join(TRANSACTION_LOCK);
        fs::write(&lock, b"").unwrap();
        fs::set_permissions(&lock, fs::Permissions::from_mode(0o600)).unwrap();
        let transaction_root = root_path.join(TRANSACTION_DIRECTORY);
        fs::create_dir(&transaction_root).unwrap();
        fs::set_permissions(&transaction_root, fs::Permissions::from_mode(0o700)).unwrap();
        let workspace = transaction_root.join(format!("tx-1-{:x}-0", byte as u128 + 1));
        fs::create_dir(&workspace).unwrap();
        fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700)).unwrap();
        (root, workspace)
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn exact_empty_workspace_is_removed_and_proof_reloads() {
        let (root, workspace) = fixture(0x21);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let authorization = authorization(0x21);
        let verified = remove_workspace(&root_path, &authorization, &workspace).unwrap();
        assert!(!workspace.exists());
        assert_eq!(verified.receipt().transaction_id(), transaction(0x21));
        drop(verified);

        let restarted = verify_workspace_removal(&root_path, &authorization).unwrap();
        assert_eq!(restarted.receipt().terminal(), ReplacementPhase::Complete);
        assert_eq!(restarted.receipt().reference(), &reference());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn interruption_after_delete_leaves_pending_and_never_infers_success() {
        let (root, workspace) = fixture(0x22);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let authorization = authorization(0x22);
        assert!(matches!(
            platform::remove_with_hook(&root_path, &authorization, &workspace, true, || Ok(())),
            Err(WorkspaceRemovalError::InjectedCrash)
        ));
        assert!(!workspace.exists());
        assert!(matches!(
            verify_workspace_removal(&root_path, &authorization),
            Err(WorkspaceRemovalError::IncompleteWrite(_))
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn workspace_replacement_before_delete_is_preserved_and_blocks_proof() {
        let (root, workspace) = fixture(0x23);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let authorization = authorization(0x23);
        let moved = workspace.with_extension("moved");
        let result = platform::remove_with_hook(
            &root_path,
            &authorization,
            &workspace,
            false,
            || {
                fs::rename(&workspace, &moved).map_err(|source| io_error(&workspace, source))?;
                fs::create_dir(&workspace).map_err(|source| io_error(&workspace, source))?;
                fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700))
                    .map_err(|source| io_error(&workspace, source))?;
                Ok(())
            },
        );
        let error = match result {
            Ok(_) => panic!("workspace replacement must block removal"),
            Err(error) => error,
        };
        assert!(matches!(error, WorkspaceRemovalError::RecoveryConflict(_)));
        assert!(workspace.exists());
        assert!(moved.exists());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn nonempty_workspace_is_preserved_before_proof_namespace_creation() {
        let (root, workspace) = fixture(0x24);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let sentinel = workspace.join("journal.jsonl");
        fs::write(&sentinel, b"sentinel").unwrap();
        fs::set_permissions(&sentinel, fs::Permissions::from_mode(0o600)).unwrap();
        let authorization = authorization(0x24);
        assert!(matches!(
            remove_workspace(&root_path, &authorization, &workspace),
            Err(WorkspaceRemovalError::RecoveryConflict(_))
        ));
        assert_eq!(fs::read(&sentinel).unwrap(), b"sentinel");
        assert!(!root_path.join(PROOF_DIRECTORY).exists());
    }

    #[test]
    fn unsupported_platform_fails_before_creating_proof_material() {
        if cfg!(target_os = "linux") {
            return;
        }
        let root = tempfile::tempdir().unwrap();
        let authorization = authorization(0x25);
        assert!(matches!(
            remove_workspace(root.path(), &authorization, Path::new("workspace")),
            Err(WorkspaceRemovalError::UnsupportedPlatform)
        ));
        assert!(!root.path().join(PROOF_DIRECTORY).exists());
    }
}
