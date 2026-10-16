//! Durable proof that one exact synthetic replacement workspace left the live namespace.
//!
//! Linux moves the exact empty directory into an owner-private retained-evidence
//! namespace instead of unlinking a pathname. Both directory namespaces and the
//! synthetic root are flushed before the receipt proof is published.

use super::{
    recovery_key::{
        valid_replacement_workspace_name, RecoveryKeyReference, ReplacementTransactionId,
        WorkspaceRemovalAuthorization,
    },
    replacement::ReplacementPhase,
    workspace_retirement::{REPLACEMENT_DIRECTORY, TARGET_LOCK_FILE},
};
#[cfg(target_os = "linux")]
use serde::{Deserialize, Serialize};
use std::{
    fs::File,
    io,
    path::{Component, Path, PathBuf},
};

const PROOF_DIRECTORY: &str = ".cpa-cloud-employee-config-workspace-removals";
#[cfg(target_os = "linux")]
const FORMAT_VERSION: u32 = 2;
#[cfg(target_os = "linux")]
const MAX_NAMESPACE_ENTRIES: usize = 192;
#[cfg(target_os = "linux")]
const MAX_PROOF_BYTES: usize = 16 * 1024;
#[cfg(target_os = "linux")]
const FINAL_SUFFIX: &str = ".removed.json";
#[cfg(target_os = "linux")]
const PENDING_SUFFIX: &str = ".pending";
#[cfg(target_os = "linux")]
const RETAINED_SUFFIX: &str = ".workspace";

pub(crate) struct WorkspaceRemovalReceipt {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    workspace_name: String,
    workspace_device: u64,
    workspace_inode: u64,
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
    pub(crate) fn workspace_name(&self) -> &str {
        &self.workspace_name
    }
    pub(crate) fn workspace_device(&self) -> u64 {
        self.workspace_device
    }
    pub(crate) fn workspace_inode(&self) -> u64 {
        self.workspace_inode
    }
    #[cfg(test)]
    pub(crate) fn for_test(authorization: &WorkspaceRemovalAuthorization) -> Self {
        receipt(authorization)
    }
}

pub(crate) struct VerifiedWorkspaceRemoval {
    receipt: WorkspaceRemovalReceipt,
    _target_lock: Option<File>,
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

fn receipt(authorization: &WorkspaceRemovalAuthorization) -> WorkspaceRemovalReceipt {
    WorkspaceRemovalReceipt {
        terminal: authorization.terminal(),
        transaction_id: authorization.transaction_id(),
        reference: authorization.reference().clone(),
        workspace_name: authorization.workspace_name().to_owned(),
        workspace_device: authorization.workspace_device(),
        workspace_inode: authorization.workspace_inode(),
    }
}

pub(crate) fn remove_workspace(
    root: &Path,
    authorization: &WorkspaceRemovalAuthorization,
) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
    platform::remove(root, authorization)
}

pub(crate) fn verify_workspace_removal(
    root: &Path,
    authorization: &WorkspaceRemovalAuthorization,
) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
    platform::verify(root, authorization)
}

#[cfg(all(test, target_os = "linux"))]
pub(crate) fn remove_workspace_with_fault(
    root: &Path,
    authorization: &WorkspaceRemovalAuthorization,
    boundary: usize,
) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
    platform::remove_with_fault(root, authorization, boundary)
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
    retained_name: String,
}

#[cfg(target_os = "linux")]
fn transaction_hex(id: ReplacementTransactionId) -> String {
    hex::encode(id.as_bytes())
}
#[cfg(target_os = "linux")]
fn final_name(id: ReplacementTransactionId) -> String {
    format!("{}{FINAL_SUFFIX}", transaction_hex(id))
}
#[cfg(target_os = "linux")]
fn pending_name(id: ReplacementTransactionId) -> String {
    format!("{}{PENDING_SUFFIX}", transaction_hex(id))
}
#[cfg(target_os = "linux")]
fn retained_name(id: ReplacementTransactionId) -> String {
    format!("{}{RETAINED_SUFFIX}", transaction_hex(id))
}
#[cfg(target_os = "linux")]
fn terminal_text(value: ReplacementPhase) -> Result<&'static str, WorkspaceRemovalError> {
    match value {
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

    struct FaultState {
        target: Option<usize>,
        current: usize,
    }
    impl FaultState {
        fn none() -> Self {
            Self {
                target: None,
                current: 0,
            }
        }
        #[cfg(test)]
        fn at(target: usize) -> Self {
            Self {
                target: Some(target),
                current: 0,
            }
        }
        fn checkpoint(&mut self) -> Result<(), WorkspaceRemovalError> {
            self.current += 1;
            if self.target == Some(self.current) {
                Err(WorkspaceRemovalError::InjectedCrash)
            } else {
                Ok(())
            }
        }
    }

    pub(super) fn remove(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        remove_inner(root, authorization, &mut FaultState::none())
    }
    #[cfg(test)]
    pub(super) fn remove_with_fault(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
        boundary: usize,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        remove_inner(root, authorization, &mut FaultState::at(boundary))
    }

    fn remove_inner(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
        fault: &mut FaultState,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        let root_dir = open_root(root)?;
        let root_id = identity(&root_dir).map_err(|source| io_error(root, source))?;
        require_root(root_id, authorization, root)?;
        let replacement_path = root.join(REPLACEMENT_DIRECTORY);
        let replacement = open_private_dir_at(
            root_dir.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        let replacement_id =
            identity(&replacement).map_err(|source| io_error(&replacement_path, source))?;
        let target_lock = acquire_target_lock(&replacement, &replacement_path)?;
        let workspace_path = replacement_path.join(authorization.workspace_name());
        let proof_path = root.join(PROOF_DIRECTORY);

        let proof_dir = match open_private_dir_at(
            root_dir.as_raw_fd(),
            OsStr::new(PROOF_DIRECTORY),
            &proof_path,
        ) {
            Ok(directory) => directory,
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                drop(open_exact_empty(
                    &replacement,
                    authorization,
                    &workspace_path,
                )?);
                let directory = create_private_dir_at(
                    root_dir.as_raw_fd(),
                    OsStr::new(PROOF_DIRECTORY),
                    &proof_path,
                )?;
                root_dir.sync_all().map_err(|source| io_error(root, source))?;
                fault.checkpoint()?;
                directory
            }
            Err(error) => return Err(error),
        };
        lock_exclusive(&proof_dir, &proof_path)?;
        validate_namespace(&proof_dir, &proof_path)?;

        let final_file = final_name(authorization.transaction_id());
        if file_exists(&proof_dir, &final_file, &proof_path)? {
            let result = verify_locked(
                root,
                &root_dir,
                root_id,
                authorization,
                &replacement,
                replacement_id,
                &proof_dir,
                &proof_path,
            )?;
            return Ok(VerifiedWorkspaceRemoval {
                receipt: result,
                _target_lock: Some(target_lock),
            });
        }

        let pending_file = pending_name(authorization.transaction_id());
        let retained = retained_name(authorization.transaction_id());
        let proof = if file_exists(&proof_dir, &pending_file, &proof_path)? {
            read_proof(
                &proof_dir,
                &pending_file,
                &proof_path,
                root_id,
                authorization,
                replacement_id,
            )?
            .0
        } else {
            if dir_exists(&proof_dir, &retained, &proof_path)? {
                return Err(WorkspaceRemovalError::RecoveryConflict(
                    proof_path.join(retained),
                ));
            }
            let workspace = open_exact_empty(&replacement, authorization, &workspace_path)?;
            let proof = WorkspaceRemovalProof {
                version: FORMAT_VERSION,
                transaction_id: transaction_hex(authorization.transaction_id()),
                root_identity: root_id,
                terminal: terminal_text(authorization.terminal())?.to_owned(),
                provider_kind: authorization.reference().provider_kind().to_owned(),
                provider_version: authorization.reference().provider_version(),
                opaque_reference: hex::encode(authorization.reference().opaque_reference()),
                workspace_name: authorization.workspace_name().to_owned(),
                workspace_parent_identity: replacement_id,
                workspace_identity: identity(&workspace)
                    .map_err(|source| io_error(&workspace_path, source))?,
                retained_name: retained,
            };
            persist_pending(&proof_dir, &proof_path, &pending_file, &proof, fault)?;
            root_dir.sync_all().map_err(|source| io_error(root, source))?;
            fault.checkpoint()?;
            proof
        };

        move_or_verify(
            root,
            &root_dir,
            root_id,
            authorization,
            &replacement,
            replacement_id,
            &workspace_path,
            &proof_dir,
            &proof_path,
            &proof,
            fault,
        )?;
        publish(
            root,
            &root_dir,
            root_id,
            authorization,
            &replacement,
            replacement_id,
            &proof_dir,
            &proof_path,
            fault,
        )?;
        let result = verify_locked(
            root,
            &root_dir,
            root_id,
            authorization,
            &replacement,
            replacement_id,
            &proof_dir,
            &proof_path,
        )?;
        Ok(VerifiedWorkspaceRemoval {
            receipt: result,
            _target_lock: Some(target_lock),
        })
    }

    pub(super) fn verify(
        root: &Path,
        authorization: &WorkspaceRemovalAuthorization,
    ) -> Result<VerifiedWorkspaceRemoval, WorkspaceRemovalError> {
        let root_dir = open_root(root)?;
        let root_id = identity(&root_dir).map_err(|source| io_error(root, source))?;
        require_root(root_id, authorization, root)?;
        let replacement_path = root.join(REPLACEMENT_DIRECTORY);
        let replacement = open_private_dir_at(
            root_dir.as_raw_fd(),
            OsStr::new(REPLACEMENT_DIRECTORY),
            &replacement_path,
        )?;
        let replacement_id =
            identity(&replacement).map_err(|source| io_error(&replacement_path, source))?;
        let target_lock = acquire_target_lock(&replacement, &replacement_path)?;
        let proof_path = root.join(PROOF_DIRECTORY);
        let proof_dir = open_private_dir_at(
            root_dir.as_raw_fd(),
            OsStr::new(PROOF_DIRECTORY),
            &proof_path,
        )?;
        lock_exclusive(&proof_dir, &proof_path)?;
        validate_namespace(&proof_dir, &proof_path)?;
        let result = verify_locked(
            root,
            &root_dir,
            root_id,
            authorization,
            &replacement,
            replacement_id,
            &proof_dir,
            &proof_path,
        )?;
        Ok(VerifiedWorkspaceRemoval {
            receipt: result,
            _target_lock: Some(target_lock),
        })
    }

    fn persist_pending(
        proof_dir: &File,
        proof_path: &Path,
        name: &str,
        proof: &WorkspaceRemovalProof,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRemovalError> {
        let path = proof_path.join(name);
        let mut bytes = serde_json::to_vec(proof).map_err(|_| WorkspaceRemovalError::Corrupt)?;
        bytes.push(b'\n');
        if bytes.len() > MAX_PROOF_BYTES {
            return Err(WorkspaceRemovalError::Corrupt);
        }
        let mut file = create_private_file_at(proof_dir.as_raw_fd(), OsStr::new(name), &path)?;
        file.write_all(&bytes)
            .map_err(|source| io_error(&path, source))?;
        file.sync_all().map_err(|source| io_error(&path, source))?;
        fault.checkpoint()?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        fault.checkpoint()
    }

    #[allow(clippy::too_many_arguments)]
    fn move_or_verify(
        root: &Path,
        root_dir: &File,
        root_id: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        replacement: &File,
        replacement_id: FileIdentity,
        workspace_path: &Path,
        proof_dir: &File,
        proof_path: &Path,
        proof: &WorkspaceRemovalProof,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRemovalError> {
        validate_proof(proof, root_id, authorization, replacement_id)?;
        let retained_path = proof_path.join(&proof.retained_name);
        if dir_exists(proof_dir, &proof.retained_name, proof_path)? {
            ensure_absent(
                replacement,
                OsStr::new(authorization.workspace_name()),
                workspace_path,
            )?;
            return verify_retained(proof_dir, proof, &retained_path);
        }
        let workspace = open_exact_empty(replacement, authorization, workspace_path)?;
        require_identity(&workspace, proof.workspace_identity, workspace_path)?;
        require_identity(replacement, replacement_id, &root.join(REPLACEMENT_DIRECTORY))?;
        require_identity(root_dir, root_id, root)?;
        rename_no_replace(
            replacement.as_raw_fd(),
            OsStr::new(authorization.workspace_name()),
            proof_dir.as_raw_fd(),
            OsStr::new(&proof.retained_name),
            &retained_path,
        )?;
        fault.checkpoint()?;
        verify_retained(proof_dir, proof, &retained_path)?;
        ensure_absent(
            replacement,
            OsStr::new(authorization.workspace_name()),
            workspace_path,
        )?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        fault.checkpoint()?;
        replacement
            .sync_all()
            .map_err(|source| io_error(root.join(REPLACEMENT_DIRECTORY), source))?;
        fault.checkpoint()?;
        root_dir.sync_all().map_err(|source| io_error(root, source))?;
        fault.checkpoint()
    }

    #[allow(clippy::too_many_arguments)]
    fn publish(
        root: &Path,
        root_dir: &File,
        root_id: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        replacement: &File,
        replacement_id: FileIdentity,
        proof_dir: &File,
        proof_path: &Path,
        fault: &mut FaultState,
    ) -> Result<(), WorkspaceRemovalError> {
        let pending = pending_name(authorization.transaction_id());
        let final_file = final_name(authorization.transaction_id());
        let final_path = proof_path.join(&final_file);
        let (proof, pending_id, bytes) = read_proof(
            proof_dir,
            &pending,
            proof_path,
            root_id,
            authorization,
            replacement_id,
        )?;
        verify_retained(proof_dir, &proof, &proof_path.join(&proof.retained_name))?;
        ensure_absent(
            replacement,
            OsStr::new(authorization.workspace_name()),
            &root
                .join(REPLACEMENT_DIRECTORY)
                .join(authorization.workspace_name()),
        )?;
        rename_no_replace(
            proof_dir.as_raw_fd(),
            OsStr::new(&pending),
            proof_dir.as_raw_fd(),
            OsStr::new(&final_file),
            &final_path,
        )?;
        fault.checkpoint()?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        fault.checkpoint()?;
        root_dir.sync_all().map_err(|source| io_error(root, source))?;
        fault.checkpoint()?;
        let (_, final_id, final_bytes) = read_proof(
            proof_dir,
            &final_file,
            proof_path,
            root_id,
            authorization,
            replacement_id,
        )?;
        if final_id != pending_id || final_bytes != bytes {
            return Err(WorkspaceRemovalError::RecoveryConflict(final_path));
        }
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    fn verify_locked(
        root: &Path,
        root_dir: &File,
        root_id: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        replacement: &File,
        replacement_id: FileIdentity,
        proof_dir: &File,
        proof_path: &Path,
    ) -> Result<WorkspaceRemovalReceipt, WorkspaceRemovalError> {
        let name = final_name(authorization.transaction_id());
        let pending = pending_name(authorization.transaction_id());
        if file_exists(proof_dir, &pending, proof_path)? {
            return Err(WorkspaceRemovalError::RecoveryConflict(
                proof_path.join(pending),
            ));
        }
        if !file_exists(proof_dir, &name, proof_path)? {
            return Err(WorkspaceRemovalError::Missing(
                authorization.transaction_id(),
            ));
        }
        let (proof, opened_id, bytes) = read_proof(
            proof_dir,
            &name,
            proof_path,
            root_id,
            authorization,
            replacement_id,
        )?;
        let workspace_path = root
            .join(REPLACEMENT_DIRECTORY)
            .join(authorization.workspace_name());
        ensure_absent(
            replacement,
            OsStr::new(authorization.workspace_name()),
            &workspace_path,
        )?;
        verify_retained(proof_dir, &proof, &proof_path.join(&proof.retained_name))?;
        replacement
            .sync_all()
            .map_err(|source| io_error(root.join(REPLACEMENT_DIRECTORY), source))?;
        proof_dir
            .sync_all()
            .map_err(|source| io_error(proof_path, source))?;
        root_dir.sync_all().map_err(|source| io_error(root, source))?;
        let (_, reopened_id, reopened_bytes) = read_proof(
            proof_dir,
            &name,
            proof_path,
            root_id,
            authorization,
            replacement_id,
        )?;
        if reopened_id != opened_id || reopened_bytes != bytes {
            return Err(WorkspaceRemovalError::RecoveryConflict(
                proof_path.join(name),
            ));
        }
        ensure_absent(
            replacement,
            OsStr::new(authorization.workspace_name()),
            &workspace_path,
        )?;
        verify_retained(proof_dir, &proof, &proof_path.join(&proof.retained_name))?;
        validate_proof(&proof, root_id, authorization, replacement_id)
    }

    fn read_proof(
        proof_dir: &File,
        name: &str,
        proof_path: &Path,
        root_id: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        replacement_id: FileIdentity,
    ) -> Result<(WorkspaceRemovalProof, FileIdentity, Vec<u8>), WorkspaceRemovalError> {
        let path = proof_path.join(name);
        let mut file = open_private_file_at(proof_dir.as_raw_fd(), OsStr::new(name), &path)?;
        let opened_id = identity(&file).map_err(|source| io_error(&path, source))?;
        let bytes = read_bounded(&mut file, MAX_PROOF_BYTES, &path)?;
        if !bytes.ends_with(b"\n") {
            return Err(WorkspaceRemovalError::IncompleteWrite(path));
        }
        let proof =
            serde_json::from_slice(&bytes).map_err(|_| WorkspaceRemovalError::Corrupt)?;
        validate_proof(&proof, root_id, authorization, replacement_id)?;
        file.sync_all().map_err(|source| io_error(&path, source))?;
        Ok((proof, opened_id, bytes))
    }

    fn validate_proof(
        proof: &WorkspaceRemovalProof,
        root_id: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        replacement_id: FileIdentity,
    ) -> Result<WorkspaceRemovalReceipt, WorkspaceRemovalError> {
        if proof.version != FORMAT_VERSION
            || proof.transaction_id != transaction_hex(authorization.transaction_id())
            || proof.root_identity != root_id
            || proof.terminal != terminal_text(authorization.terminal())?
            || proof.provider_kind != authorization.reference().provider_kind()
            || proof.provider_version != authorization.reference().provider_version()
            || proof.opaque_reference != hex::encode(authorization.reference().opaque_reference())
            || proof.workspace_name != authorization.workspace_name()
            || proof.workspace_parent_identity != replacement_id
            || proof.workspace_identity.device != authorization.workspace_device()
            || proof.workspace_identity.inode != authorization.workspace_inode()
            || proof.retained_name != retained_name(authorization.transaction_id())
            || !valid_replacement_workspace_name(
                &proof.workspace_name,
                authorization.transaction_id(),
            )
        {
            return Err(WorkspaceRemovalError::Corrupt);
        }
        Ok(receipt(authorization))
    }

    fn open_exact_empty(
        replacement: &File,
        authorization: &WorkspaceRemovalAuthorization,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let workspace = open_private_dir_at(
            replacement.as_raw_fd(),
            OsStr::new(authorization.workspace_name()),
            path,
        )?;
        require_identity(
            &workspace,
            FileIdentity {
                device: authorization.workspace_device(),
                inode: authorization.workspace_inode(),
            },
            path,
        )?;
        ensure_empty(&workspace, path)?;
        Ok(workspace)
    }

    fn verify_retained(
        proof_dir: &File,
        proof: &WorkspaceRemovalProof,
        path: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        let directory = open_private_dir_at(
            proof_dir.as_raw_fd(),
            OsStr::new(&proof.retained_name),
            path,
        )?;
        require_identity(&directory, proof.workspace_identity, path)?;
        ensure_empty(&directory, path)
    }

    fn require_root(
        actual: FileIdentity,
        authorization: &WorkspaceRemovalAuthorization,
        path: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        if actual.device == authorization.root_device()
            && actual.inode == authorization.root_inode()
        {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()))
        }
    }
    fn require_identity(
        file: &File,
        expected: FileIdentity,
        path: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        if identity(file).map_err(|source| io_error(path, source))? == expected {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()))
        }
    }

    fn validate_namespace(directory: &File, path: &Path) -> Result<(), WorkspaceRemovalError> {
        let names = read_names(directory, path)?;
        if names.len() > MAX_NAMESPACE_ENTRIES {
            return Err(WorkspaceRemovalError::TooMany);
        }
        for name in names {
            let text = name.to_str().ok_or(WorkspaceRemovalError::Corrupt)?;
            let transaction = [FINAL_SUFFIX, PENDING_SUFFIX, RETAINED_SUFFIX]
                .iter()
                .find_map(|suffix| text.strip_suffix(suffix))
                .ok_or(WorkspaceRemovalError::Corrupt)?;
            if transaction.len() != 32
                || !transaction
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            {
                return Err(WorkspaceRemovalError::Corrupt);
            }
        }
        Ok(())
    }

    fn file_exists(parent: &File, name: &str, path: &Path) -> Result<bool, WorkspaceRemovalError> {
        match open_private_file_at(parent.as_raw_fd(), OsStr::new(name), &path.join(name)) {
            Ok(_) => Ok(true),
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                Ok(false)
            }
            Err(error) => Err(error),
        }
    }
    fn dir_exists(parent: &File, name: &str, path: &Path) -> Result<bool, WorkspaceRemovalError> {
        match open_private_dir_at(parent.as_raw_fd(), OsStr::new(name), &path.join(name)) {
            Ok(_) => Ok(true),
            Err(WorkspaceRemovalError::Io { source, .. })
                if source.kind() == io::ErrorKind::NotFound =>
            {
                Ok(false)
            }
            Err(error) => Err(error),
        }
    }

    fn acquire_target_lock(parent: &File, path: &Path) -> Result<File, WorkspaceRemovalError> {
        let lock_path = path.join(TARGET_LOCK_FILE);
        let lock = open_private_file_at(
            parent.as_raw_fd(),
            OsStr::new(TARGET_LOCK_FILE),
            &lock_path,
        )?;
        lock_exclusive(&lock, &lock_path)?;
        Ok(lock)
    }

    fn open_root(path: &Path) -> Result<File, WorkspaceRemovalError> {
        if path.as_os_str().is_empty()
            || path
                .components()
                .any(|value| matches!(value, Component::ParentDir | Component::Prefix(_)))
        {
            return Err(WorkspaceRemovalError::InvalidRoot(path.to_path_buf()));
        }
        let anchor = if path.is_absolute() {
            Path::new("/")
        } else {
            Path::new(".")
        };
        let c_anchor =
            CString::new(anchor.as_os_str().as_bytes()).expect("Linux root anchor has no NUL");
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
                    directory = open_dir_at(directory.as_raw_fd(), name, &display)?;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(WorkspaceRemovalError::InvalidRoot(path.to_path_buf()));
                }
            }
        }
        verify_private_dir(&directory, path)?;
        Ok(directory)
    }

    fn create_private_dir_at(
        parent: RawFd,
        name: &OsStr,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let c_name = os_string(name)?;
        if unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) } != 0 {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        open_private_dir_at(parent, name, path)
    }
    fn open_private_dir_at(
        parent: RawFd,
        name: &OsStr,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let file = open_dir_at(parent, name, path)?;
        verify_private_dir(&file, path)?;
        Ok(file)
    }
    fn open_dir_at(
        parent: RawFd,
        name: &OsStr,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(path, source))
    }
    fn create_private_file_at(
        parent: RawFd,
        name: &OsStr,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0o600,
        )
        .map_err(|source| io_error(path, source))?;
        verify_private_file(&file, path)?;
        Ok(file)
    }
    fn open_private_file_at(
        parent: RawFd,
        name: &OsStr,
        path: &Path,
    ) -> Result<File, WorkspaceRemovalError> {
        let file = open_file_at(
            parent,
            name,
            libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(path, source))?;
        verify_private_file(&file, path)?;
        Ok(file)
    }
    fn open_file_at(
        parent: RawFd,
        name: &OsStr,
        flags: i32,
        mode: libc::mode_t,
    ) -> io::Result<File> {
        let name = CString::new(name.as_bytes())
            .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
        let fd = unsafe { libc::openat(parent, name.as_ptr(), flags, mode as libc::c_uint) };
        if fd < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(unsafe { File::from_raw_fd(fd) })
        }
    }

    fn rename_no_replace(
        from_parent: RawFd,
        from: &OsStr,
        to_parent: RawFd,
        to: &OsStr,
        path: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        let from = os_string(from)?;
        let to = os_string(to)?;
        if unsafe {
            libc::renameat2(
                from_parent,
                from.as_ptr(),
                to_parent,
                to.as_ptr(),
                libc::RENAME_NOREPLACE,
            )
        } != 0
        {
            Err(io_error(path, io::Error::last_os_error()))
        } else {
            Ok(())
        }
    }
    fn ensure_absent(
        parent: &File,
        name: &OsStr,
        path: &Path,
    ) -> Result<(), WorkspaceRemovalError> {
        let name = os_string(name)?;
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
            return Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()));
        }
        let error = io::Error::last_os_error();
        if error.kind() == io::ErrorKind::NotFound {
            Ok(())
        } else {
            Err(io_error(path, error))
        }
    }
    fn ensure_empty(directory: &File, path: &Path) -> Result<(), WorkspaceRemovalError> {
        if read_names(directory, path)?.is_empty() {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()))
        }
    }
    fn verify_private_dir(file: &File, path: &Path) -> Result<(), WorkspaceRemovalError> {
        let metadata = file.metadata().map_err(|source| io_error(path, source))?;
        if metadata.is_dir()
            && metadata.uid() == unsafe { libc::geteuid() }
            && metadata.mode() & 0o7777 == 0o700
        {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()))
        }
    }
    fn verify_private_file(file: &File, path: &Path) -> Result<(), WorkspaceRemovalError> {
        let metadata = file.metadata().map_err(|source| io_error(path, source))?;
        if metadata.is_file()
            && metadata.nlink() == 1
            && metadata.uid() == unsafe { libc::geteuid() }
            && metadata.mode() & 0o7777 == 0o600
        {
            Ok(())
        } else {
            Err(WorkspaceRemovalError::RecoveryConflict(path.to_path_buf()))
        }
    }
    fn lock_exclusive(file: &File, path: &Path) -> Result<(), WorkspaceRemovalError> {
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } == 0 {
            Ok(())
        } else {
            Err(io_error(path, io::Error::last_os_error()))
        }
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
        path: &Path,
    ) -> Result<Vec<u8>, WorkspaceRemovalError> {
        let mut bytes = Vec::new();
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(path, source))?;
        if bytes.len() > limit {
            Err(WorkspaceRemovalError::Corrupt)
        } else {
            Ok(bytes)
        }
    }
    fn read_names(directory: &File, path: &Path) -> Result<Vec<OsString>, WorkspaceRemovalError> {
        let duplicate = unsafe { libc::fcntl(directory.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 0) };
        if duplicate < 0 {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let stream = unsafe { libc::fdopendir(duplicate) };
        if stream.is_null() {
            let source = io::Error::last_os_error();
            unsafe { libc::close(duplicate) };
            return Err(io_error(path, source));
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
                    Err(io_error(path, source))
                };
            }
            let name = unsafe { CStr::from_ptr((*entry).d_name.as_ptr()) }.to_bytes();
            if name != b"." && name != b".." {
                names.push(OsString::from_vec(name.to_vec()));
            }
        }
    }
    fn os_string(value: &OsStr) -> Result<CString, WorkspaceRemovalError> {
        CString::new(value.as_bytes()).map_err(|_| WorkspaceRemovalError::Corrupt)
    }
}

#[cfg(not(target_os = "linux"))]
mod platform {
    use super::*;
    pub(super) fn remove(
        _root: &Path,
        _authorization: &WorkspaceRemovalAuthorization,
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
    #[cfg(target_os = "linux")]
    use std::{
        fs,
        os::unix::fs::{MetadataExt, PermissionsExt},
    };

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }
    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-reference".to_vec()).unwrap()
    }
    #[cfg(target_os = "linux")]
    fn fixture(byte: u8) -> (tempfile::TempDir, PathBuf, WorkspaceRemovalAuthorization) {
        let root = tempfile::tempdir().unwrap();
        let root_path = fs::canonicalize(root.path()).unwrap();
        fs::set_permissions(&root_path, fs::Permissions::from_mode(0o700)).unwrap();
        let parent = root_path.join(REPLACEMENT_DIRECTORY);
        fs::create_dir(&parent).unwrap();
        fs::set_permissions(&parent, fs::Permissions::from_mode(0o700)).unwrap();
        let lock = parent.join(TARGET_LOCK_FILE);
        fs::write(&lock, b"").unwrap();
        fs::set_permissions(&lock, fs::Permissions::from_mode(0o600)).unwrap();
        let workspace = parent.join(format!("tx-{}", hex::encode([byte; 16])));
        fs::create_dir(&workspace).unwrap();
        fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700)).unwrap();
        let root_meta = fs::metadata(&root_path).unwrap();
        let workspace_meta = fs::metadata(&workspace).unwrap();
        let authorization = WorkspaceRemovalAuthorization::for_test(
            ReplacementPhase::Complete,
            transaction(byte),
            reference(),
            root_meta.dev(),
            root_meta.ino(),
            workspace.file_name().unwrap().to_str().unwrap().to_owned(),
            workspace_meta.dev(),
            workspace_meta.ino(),
        );
        (root, workspace, authorization)
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn exact_empty_workspace_yields_restart_verifiable_receipt() {
        let (root, workspace, authorization) = fixture(0x21);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let verified = remove_workspace(&root_path, &authorization).unwrap();
        assert!(!workspace.exists());
        assert_eq!(verified.receipt().transaction_id(), transaction(0x21));
        assert!(root_path
            .join(PROOF_DIRECTORY)
            .join(retained_name(transaction(0x21)))
            .is_dir());
        drop(verified);
        verify_workspace_removal(&root_path, &authorization).unwrap();
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn each_flush_or_rename_fault_restarts_to_one_receipt() {
        for boundary in 1..=11 {
            let byte = 0x30 + boundary as u8;
            let (root, workspace, authorization) = fixture(byte);
            let root_path = fs::canonicalize(root.path()).unwrap();
            assert!(matches!(
                remove_workspace_with_fault(&root_path, &authorization, boundary),
                Err(WorkspaceRemovalError::InjectedCrash)
            ));
            let restarted = remove_workspace(&root_path, &authorization).unwrap();
            assert_eq!(restarted.receipt().transaction_id(), transaction(byte));
            assert!(!workspace.exists());
            verify_workspace_removal(&root_path, &authorization).unwrap();
        }
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn unknown_entry_is_preserved_and_blocks_proof() {
        let (root, workspace, authorization) = fixture(0x22);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let unknown = workspace.join("unknown");
        fs::write(&unknown, b"keep").unwrap();
        assert!(matches!(
            remove_workspace(&root_path, &authorization),
            Err(WorkspaceRemovalError::RecoveryConflict(_))
        ));
        assert_eq!(fs::read(&unknown).unwrap(), b"keep");
        assert!(!root_path.join(PROOF_DIRECTORY).exists());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn unknown_entry_after_logical_removal_is_retained_and_blocks_receipt() {
        let (root, workspace, authorization) = fixture(0x26);
        let root_path = fs::canonicalize(root.path()).unwrap();
        assert!(matches!(
            remove_workspace_with_fault(&root_path, &authorization, 5),
            Err(WorkspaceRemovalError::InjectedCrash)
        ));
        assert!(!workspace.exists());
        let retained = root_path
            .join(PROOF_DIRECTORY)
            .join(retained_name(transaction(0x26)));
        let unknown = retained.join("unknown");
        fs::write(&unknown, b"preserve").unwrap();
        assert!(matches!(
            remove_workspace(&root_path, &authorization),
            Err(WorkspaceRemovalError::RecoveryConflict(_))
        ));
        assert_eq!(fs::read(&unknown).unwrap(), b"preserve");
        assert!(!root_path
            .join(PROOF_DIRECTORY)
            .join(final_name(transaction(0x26)))
            .exists());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn root_or_workspace_replacement_fails_closed() {
        let (root, workspace, authorization) = fixture(0x23);
        let root_path = fs::canonicalize(root.path()).unwrap();
        let replacement = workspace.with_extension("replacement");
        fs::rename(&workspace, &replacement).unwrap();
        fs::create_dir(&workspace).unwrap();
        fs::set_permissions(&workspace, fs::Permissions::from_mode(0o700)).unwrap();
        assert!(matches!(
            remove_workspace(&root_path, &authorization),
            Err(WorkspaceRemovalError::RecoveryConflict(_))
        ));
        assert!(workspace.exists());
        assert!(replacement.exists());

        let bad_root = WorkspaceRemovalAuthorization::for_test(
            authorization.terminal(),
            authorization.transaction_id(),
            authorization.reference().clone(),
            authorization.root_device(),
            authorization.root_inode() + 1,
            authorization.workspace_name().to_owned(),
            authorization.workspace_device(),
            authorization.workspace_inode(),
        );
        assert!(matches!(
            remove_workspace(&root_path, &bad_root),
            Err(WorkspaceRemovalError::RecoveryConflict(path)) if path == root_path
        ));
    }

    #[test]
    fn unsupported_platform_fails_without_proof_material() {
        if cfg!(target_os = "linux") {
            return;
        }
        let root = tempfile::tempdir().unwrap();
        let authorization = WorkspaceRemovalAuthorization::for_test(
            ReplacementPhase::Complete,
            transaction(0x25),
            reference(),
            1,
            1,
            format!("tx-{}", hex::encode([0x25; 16])),
            1,
            1,
        );
        assert!(matches!(
            remove_workspace(root.path(), &authorization),
            Err(WorkspaceRemovalError::UnsupportedPlatform)
        ));
        assert!(!root.path().join(PROOF_DIRECTORY).exists());
    }
}
