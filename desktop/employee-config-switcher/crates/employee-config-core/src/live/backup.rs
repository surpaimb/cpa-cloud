//! Durable encrypted rollback material for a bounded synthetic-root slice.
//!
//! This module deliberately does not publish or replace a configuration file.
//! On Unix it anchors at `/` or `.`, opens every normal root-path component
//! relative to a held directory descriptor with no-follow semantics, and then
//! does the same for the target, separate backup directory, workspace,
//! ciphertext, and manifest. Parent traversal is rejected. Windows remains
//! explicitly unsupported until the equivalent handle-relative implementation
//! exists. The Tauri apply command remains fail-closed.

use ring::{
    aead::{self, Aad, LessSafeKey, Nonce, UnboundKey},
    rand::{SecureRandom, SystemRandom},
};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
#[cfg(unix)]
use std::fs::File;
use std::io;
#[cfg(unix)]
use std::io::{Read, Write};
use std::path::{Component, Path, PathBuf};
use zeroize::Zeroizing;

// Keep rollback material outside the create-only engine namespace. That engine
// treats every child of its transaction directory as one of its own journals.
const BACKUP_DIRECTORY: &str = ".cpa-cloud-employee-config-backups";
const BACKUP_FILE: &str = "rollback.aes256gcm";
const MANIFEST_FILE: &str = "rollback.json";
const FORMAT_VERSION: u32 = 1;
const MAX_TARGET_BYTES: usize = 4 * 1024 * 1024;
const MAX_MANIFEST_BYTES: usize = 16 * 1024;
const NONCE_BYTES: usize = 12;
const KEY_BYTES: usize = 32;

/// Caller-supplied recovery key. A future live caller must obtain this from
/// platform key management; this synthetic slice never persists it.
pub struct BackupKey(Zeroizing<[u8; KEY_BYTES]>);

impl BackupKey {
    pub fn from_bytes(bytes: [u8; KEY_BYTES]) -> Self {
        Self(Zeroizing::new(bytes))
    }

    pub fn generate() -> Result<Self, BackupError> {
        let mut bytes = [0_u8; KEY_BYTES];
        SystemRandom::new()
            .fill(&mut bytes)
            .map_err(|_| BackupError::Cryptography)?;
        Ok(Self::from_bytes(bytes))
    }

    fn expose(&self) -> &[u8; KEY_BYTES] {
        &self.0
    }
}

impl std::fmt::Debug for BackupKey {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("BackupKey([REDACTED])")
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BackupReceipt {
    pub workspace: String,
    pub target: PathBuf,
    pub original_digest: String,
}

#[derive(Debug, thiserror::Error)]
pub enum BackupError {
    #[error("encrypted rollback material is not supported on this platform")]
    UnsupportedPlatform,
    #[error("backup target must be one non-empty relative path component: {0}")]
    InvalidTarget(PathBuf),
    #[error("backup root must not be empty or contain parent traversal: {0}")]
    InvalidRoot(PathBuf),
    #[error("backup workspace identifier is invalid")]
    InvalidWorkspace,
    #[error("target is too large for the bounded backup slice: {0}")]
    TooLarge(PathBuf),
    #[error("target or root identity changed; recovery refuses to continue: {0}")]
    RecoveryConflict(PathBuf),
    #[error("encrypted rollback manifest is corrupt")]
    CorruptManifest,
    #[error("encrypted rollback material failed authentication")]
    Authentication,
    #[error("cryptographic operation failed")]
    Cryptography,
    #[error("filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
struct FileIdentity {
    volume: u64,
    file: u64,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct BackupManifest {
    version: u32,
    target: String,
    root_identity: FileIdentity,
    target_identity: FileIdentity,
    original_digest: String,
    original_len: usize,
    nonce: String,
    ciphertext_digest: String,
}

pub fn prepare_encrypted_backup(
    root: &Path,
    target: &Path,
    key: &BackupKey,
) -> Result<BackupReceipt, BackupError> {
    validate_target(target)?;
    platform::prepare(root, target, key)
}

/// Reload encrypted rollback material using only durable state plus the
/// caller-supplied recovery key. The target must still have the identity and
/// digest captured before any future publication step.
pub fn load_encrypted_backup(
    root: &Path,
    workspace: &str,
    key: &BackupKey,
) -> Result<Zeroizing<Vec<u8>>, BackupError> {
    validate_workspace(workspace)?;
    platform::load(root, workspace, key)
}

fn validate_target(target: &Path) -> Result<&std::ffi::OsStr, BackupError> {
    let mut components = target.components();
    let Some(Component::Normal(name)) = components.next() else {
        return Err(BackupError::InvalidTarget(target.to_path_buf()));
    };
    if components.next().is_some() || name.is_empty() {
        return Err(BackupError::InvalidTarget(target.to_path_buf()));
    }
    Ok(name)
}

fn validate_workspace(workspace: &str) -> Result<(), BackupError> {
    let valid = workspace.len() == 39
        && workspace.starts_with("backup-")
        && workspace[7..].bytes().all(|byte| byte.is_ascii_hexdigit());
    if valid {
        Ok(())
    } else {
        Err(BackupError::InvalidWorkspace)
    }
}

fn digest(bytes: &[u8]) -> String {
    hex::encode(Sha256::digest(bytes))
}

fn associated_data(manifest: &BackupManifest) -> Vec<u8> {
    format!(
        "cpa-cloud-employee-config-backup-v{}\n{}\n{}:{}\n{}:{}\n{}\n{}",
        manifest.version,
        manifest.target,
        manifest.root_identity.volume,
        manifest.root_identity.file,
        manifest.target_identity.volume,
        manifest.target_identity.file,
        manifest.original_digest,
        manifest.original_len,
    )
    .into_bytes()
}

fn encryption_key(key: &BackupKey) -> Result<LessSafeKey, BackupError> {
    UnboundKey::new(&aead::AES_256_GCM, key.expose())
        .map(LessSafeKey::new)
        .map_err(|_| BackupError::Cryptography)
}

fn encrypt(
    plaintext: Zeroizing<Vec<u8>>,
    manifest: &BackupManifest,
    nonce_bytes: [u8; NONCE_BYTES],
    key: &BackupKey,
) -> Result<Zeroizing<Vec<u8>>, BackupError> {
    let mut ciphertext = plaintext;
    encryption_key(key)?
        .seal_in_place_append_tag(
            Nonce::assume_unique_for_key(nonce_bytes),
            Aad::from(associated_data(manifest)),
            &mut *ciphertext,
        )
        .map_err(|_| BackupError::Cryptography)?;
    Ok(ciphertext)
}

fn decrypt(
    ciphertext: Zeroizing<Vec<u8>>,
    manifest: &BackupManifest,
    key: &BackupKey,
) -> Result<Zeroizing<Vec<u8>>, BackupError> {
    let nonce_vec = hex::decode(&manifest.nonce).map_err(|_| BackupError::CorruptManifest)?;
    let nonce_bytes: [u8; NONCE_BYTES] = nonce_vec
        .try_into()
        .map_err(|_| BackupError::CorruptManifest)?;
    let mut plaintext = ciphertext;
    let plain_len = encryption_key(key)?
        .open_in_place(
            Nonce::assume_unique_for_key(nonce_bytes),
            Aad::from(associated_data(manifest)),
            &mut plaintext,
        )
        .map_err(|_| BackupError::Authentication)?
        .len();
    plaintext.truncate(plain_len);
    if plaintext.len() != manifest.original_len || digest(&plaintext) != manifest.original_digest {
        return Err(BackupError::Authentication);
    }
    Ok(plaintext)
}

fn io_error(path: impl Into<PathBuf>, source: io::Error) -> BackupError {
    BackupError::Io {
        path: path.into(),
        source,
    }
}

#[cfg(unix)]
mod platform {
    use super::*;
    use std::ffi::{CString, OsStr};
    use std::os::fd::{AsRawFd, FromRawFd, RawFd};
    use std::os::unix::ffi::OsStrExt;
    use std::os::unix::fs::{MetadataExt, PermissionsExt};

    pub(super) fn prepare(
        root: &Path,
        target: &Path,
        key: &BackupKey,
    ) -> Result<BackupReceipt, BackupError> {
        let target_name = validate_target(target)?;
        let target_text = target_name
            .to_str()
            .ok_or_else(|| BackupError::InvalidTarget(target.to_path_buf()))?;
        let root_dir = open_root(root)?;
        let root_identity = identity(&root_dir).map_err(|source| io_error(root, source))?;
        let mut target_file = open_file_at(
            root_dir.as_raw_fd(),
            target_name,
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(root.join(target), source))?;
        let metadata = target_file
            .metadata()
            .map_err(|source| io_error(root.join(target), source))?;
        if !metadata.is_file()
            || metadata.nlink() != 1
            || metadata.uid() != unsafe { libc::geteuid() }
        {
            return Err(BackupError::RecoveryConflict(root.join(target)));
        }
        let target_identity =
            identity(&target_file).map_err(|source| io_error(root.join(target), source))?;
        let plaintext = read_bounded(&mut target_file, MAX_TARGET_BYTES, root.join(target))?;
        if identity(&target_file).map_err(|source| io_error(root.join(target), source))?
            != target_identity
        {
            return Err(BackupError::RecoveryConflict(root.join(target)));
        }

        let transaction_dir = open_or_create_private_dir(
            root_dir.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            root.join(BACKUP_DIRECTORY),
        )?;
        // Persist a freshly created backup-directory entry in the synthetic
        // root before reporting any child workspace as durable.
        root_dir
            .sync_all()
            .map_err(|source| io_error(root, source))?;
        let (workspace, workspace_dir) =
            create_workspace(transaction_dir.as_raw_fd(), root.join(BACKUP_DIRECTORY))?;

        let mut nonce_bytes = [0_u8; NONCE_BYTES];
        SystemRandom::new()
            .fill(&mut nonce_bytes)
            .map_err(|_| BackupError::Cryptography)?;
        let mut manifest = BackupManifest {
            version: FORMAT_VERSION,
            target: target_text.to_owned(),
            root_identity,
            target_identity,
            original_digest: digest(&plaintext),
            original_len: plaintext.len(),
            nonce: hex::encode(nonce_bytes),
            ciphertext_digest: String::new(),
        };
        let ciphertext = encrypt(plaintext, &manifest, nonce_bytes, key)?;
        manifest.ciphertext_digest = digest(&ciphertext);

        let mut backup_file = create_private_file_at(
            workspace_dir.as_raw_fd(),
            OsStr::new(BACKUP_FILE),
            root.join(BACKUP_DIRECTORY)
                .join(&workspace)
                .join(BACKUP_FILE),
        )?;
        backup_file
            .write_all(&ciphertext)
            .map_err(|source| io_error(root.join(BACKUP_FILE), source))?;
        backup_file
            .sync_all()
            .map_err(|source| io_error(root.join(BACKUP_FILE), source))?;
        workspace_dir
            .sync_all()
            .map_err(|source| io_error(root.join(&workspace), source))?;

        let manifest_bytes =
            serde_json::to_vec(&manifest).map_err(|_| BackupError::CorruptManifest)?;
        let mut manifest_file = create_private_file_at(
            workspace_dir.as_raw_fd(),
            OsStr::new(MANIFEST_FILE),
            root.join(BACKUP_DIRECTORY)
                .join(&workspace)
                .join(MANIFEST_FILE),
        )?;
        manifest_file
            .write_all(&manifest_bytes)
            .map_err(|source| io_error(root.join(MANIFEST_FILE), source))?;
        manifest_file
            .sync_all()
            .map_err(|source| io_error(root.join(MANIFEST_FILE), source))?;
        workspace_dir
            .sync_all()
            .map_err(|source| io_error(root.join(&workspace), source))?;
        transaction_dir
            .sync_all()
            .map_err(|source| io_error(root.join(BACKUP_DIRECTORY), source))?;

        Ok(BackupReceipt {
            workspace,
            target: target.to_path_buf(),
            original_digest: manifest.original_digest,
        })
    }

    pub(super) fn load(
        root: &Path,
        workspace: &str,
        key: &BackupKey,
    ) -> Result<Zeroizing<Vec<u8>>, BackupError> {
        validate_workspace(workspace)?;
        let root_dir = open_root(root)?;
        let transaction_dir = open_private_dir_at(
            root_dir.as_raw_fd(),
            OsStr::new(BACKUP_DIRECTORY),
            root.join(BACKUP_DIRECTORY),
        )?;
        let workspace_dir = open_private_dir_at(
            transaction_dir.as_raw_fd(),
            OsStr::new(workspace),
            root.join(BACKUP_DIRECTORY).join(workspace),
        )?;
        let manifest_path = root
            .join(BACKUP_DIRECTORY)
            .join(workspace)
            .join(MANIFEST_FILE);
        let mut manifest_file = open_file_at(
            workspace_dir.as_raw_fd(),
            OsStr::new(MANIFEST_FILE),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(&manifest_path, source))?;
        let manifest_bytes = read_bounded(&mut manifest_file, MAX_MANIFEST_BYTES, &manifest_path)?;
        let manifest: BackupManifest =
            serde_json::from_slice(&manifest_bytes).map_err(|_| BackupError::CorruptManifest)?;
        if manifest.version != FORMAT_VERSION
            || manifest.original_len > MAX_TARGET_BYTES
            || manifest.original_digest.len() != 64
            || manifest.ciphertext_digest.len() != 64
        {
            return Err(BackupError::CorruptManifest);
        }
        if identity(&root_dir).map_err(|source| io_error(root, source))? != manifest.root_identity {
            return Err(BackupError::RecoveryConflict(root.to_path_buf()));
        }
        let target_path = PathBuf::from(&manifest.target);
        let target_name = validate_target(&target_path)?;
        let mut target_file = open_file_at(
            root_dir.as_raw_fd(),
            target_name,
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(root.join(&target_path), source))?;
        if identity(&target_file).map_err(|source| io_error(root.join(&target_path), source))?
            != manifest.target_identity
        {
            return Err(BackupError::RecoveryConflict(root.join(target_path)));
        }
        let target_bytes =
            read_bounded(&mut target_file, MAX_TARGET_BYTES, root.join(&target_path))?;
        if digest(&target_bytes) != manifest.original_digest {
            return Err(BackupError::RecoveryConflict(root.join(target_path)));
        }

        let backup_path = root
            .join(BACKUP_DIRECTORY)
            .join(workspace)
            .join(BACKUP_FILE);
        let mut backup_file = open_file_at(
            workspace_dir.as_raw_fd(),
            OsStr::new(BACKUP_FILE),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(&backup_path, source))?;
        let ciphertext = read_bounded(
            &mut backup_file,
            MAX_TARGET_BYTES + aead::MAX_TAG_LEN,
            &backup_path,
        )?;
        if digest(&ciphertext) != manifest.ciphertext_digest {
            return Err(BackupError::Authentication);
        }
        decrypt(ciphertext, &manifest, key)
    }

    fn open_root(path: &Path) -> Result<File, BackupError> {
        if path.as_os_str().is_empty() {
            return Err(BackupError::InvalidRoot(path.to_path_buf()));
        }

        // Anchor once at a stable process namespace entry, then make every
        // caller-controlled component the final component of an `openat` call.
        // `O_NOFOLLOW` therefore applies to every ancestor rather than only to
        // the caller-supplied root's final component.
        let anchor = if path.is_absolute() {
            Path::new("/")
        } else {
            Path::new(".")
        };
        let c_anchor = CString::new(anchor.as_os_str().as_bytes())
            .expect("Unix root anchors have no NUL");
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
                    directory = open_file_at(
                        directory.as_raw_fd(),
                        name,
                        libc::O_RDONLY
                            | libc::O_DIRECTORY
                            | libc::O_CLOEXEC
                            | libc::O_NOFOLLOW,
                        0,
                    )
                    .map_err(|source| io_error(&display, source))?;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(BackupError::InvalidRoot(path.to_path_buf()));
                }
            }
        }

        let metadata = directory
            .metadata()
            .map_err(|source| io_error(path, source))?;
        if !metadata.is_dir() {
            return Err(BackupError::RecoveryConflict(path.to_path_buf()));
        }
        Ok(directory)
    }

    fn open_or_create_private_dir(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, BackupError> {
        let c_name = os_c_string(name)?;
        let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
        if result != 0 {
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::AlreadyExists {
                return Err(io_error(&display, error));
            }
        }
        let dir = open_private_dir_at(parent, name, display.clone())?;
        let metadata = dir
            .metadata()
            .map_err(|source| io_error(&display, source))?;
        if metadata.uid() != unsafe { libc::geteuid() }
            || metadata.permissions().mode() & 0o077 != 0
        {
            return Err(BackupError::RecoveryConflict(display));
        }
        Ok(dir)
    }

    fn open_private_dir_at(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, BackupError> {
        let dir = open_file_at(
            parent,
            name,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0,
        )
        .map_err(|source| io_error(&display, source))?;
        let metadata = dir
            .metadata()
            .map_err(|source| io_error(&display, source))?;
        if !metadata.is_dir() {
            return Err(BackupError::RecoveryConflict(display));
        }
        Ok(dir)
    }

    fn create_workspace(parent: RawFd, display: PathBuf) -> Result<(String, File), BackupError> {
        for _ in 0..16 {
            let mut random = [0_u8; 16];
            SystemRandom::new()
                .fill(&mut random)
                .map_err(|_| BackupError::Cryptography)?;
            let name = format!("backup-{}", hex::encode(random));
            let c_name = CString::new(name.as_bytes()).expect("generated name has no NUL");
            let result = unsafe { libc::mkdirat(parent, c_name.as_ptr(), 0o700) };
            if result == 0 {
                let dir = open_private_dir_at(parent, OsStr::new(&name), display.join(&name))?;
                return Ok((name, dir));
            }
            let error = io::Error::last_os_error();
            if error.kind() != io::ErrorKind::AlreadyExists {
                return Err(io_error(&display, error));
            }
        }
        Err(io_error(
            display,
            io::Error::new(io::ErrorKind::AlreadyExists, "workspace name collision"),
        ))
    }

    fn create_private_file_at(
        parent: RawFd,
        name: &OsStr,
        display: PathBuf,
    ) -> Result<File, BackupError> {
        open_file_at(
            parent,
            name,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            0o600,
        )
        .map_err(|source| io_error(display, source))
    }

    fn open_file_at(
        parent: RawFd,
        name: &OsStr,
        flags: i32,
        mode: libc::mode_t,
    ) -> io::Result<File> {
        let c_name = os_c_string(name).map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
        let fd = unsafe { libc::openat(parent, c_name.as_ptr(), flags, mode as libc::c_uint) };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(unsafe { File::from_raw_fd(fd) })
    }

    fn os_c_string(name: &OsStr) -> Result<CString, BackupError> {
        CString::new(name.as_bytes()).map_err(|_| BackupError::InvalidTarget(PathBuf::from(name)))
    }

    fn identity(file: &File) -> io::Result<FileIdentity> {
        let metadata = file.metadata()?;
        Ok(FileIdentity {
            volume: metadata.dev(),
            file: metadata.ino(),
        })
    }

    fn read_bounded(
        file: &mut File,
        limit: usize,
        display: impl Into<PathBuf>,
    ) -> Result<Zeroizing<Vec<u8>>, BackupError> {
        let display = display.into();
        let mut bytes = Zeroizing::new(Vec::new());
        file.take((limit + 1) as u64)
            .read_to_end(&mut bytes)
            .map_err(|source| io_error(&display, source))?;
        if bytes.len() > limit {
            return Err(BackupError::TooLarge(display));
        }
        Ok(bytes)
    }
}

#[cfg(not(unix))]
mod platform {
    use super::*;

    pub(super) fn prepare(
        _root: &Path,
        _target: &Path,
        _key: &BackupKey,
    ) -> Result<BackupReceipt, BackupError> {
        Err(BackupError::UnsupportedPlatform)
    }

    pub(super) fn load(
        _root: &Path,
        _workspace: &str,
        _key: &BackupKey,
    ) -> Result<Zeroizing<Vec<u8>>, BackupError> {
        Err(BackupError::UnsupportedPlatform)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn fixed_key(byte: u8) -> BackupKey {
        BackupKey::from_bytes([byte; KEY_BYTES])
    }

    #[test]
    fn key_debug_is_redacted() {
        let key = fixed_key(0x42);
        let rendered = format!("{key:?}");
        assert_eq!(rendered, "BackupKey([REDACTED])");
        assert!(!rendered.contains("42"));
    }

    #[test]
    fn rejects_non_leaf_targets() {
        let root = tempfile::tempdir().unwrap();
        let error =
            prepare_encrypted_backup(root.path(), Path::new("nested/config.json"), &fixed_key(1))
                .expect_err("nested paths remain outside this bounded slice");
        assert!(matches!(error, BackupError::InvalidTarget(_)));
    }

    #[cfg(unix)]
    #[test]
    fn durable_backup_reloads_without_persisting_plaintext_or_key() {
        use std::os::unix::fs::PermissionsExt;

        let root = tempfile::tempdir().unwrap();
        let key_sentinel = b"old-employee-key-sentinel";
        let original = b"old-employee-key-sentinel\nuser-setting=true\n";
        fs::write(root.path().join("config.json"), original).unwrap();
        let key = fixed_key(0x5a);

        let receipt =
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &key).unwrap();
        let workspace = root.path().join(BACKUP_DIRECTORY).join(&receipt.workspace);
        let backup = fs::read(workspace.join(BACKUP_FILE)).unwrap();
        let manifest = fs::read(workspace.join(MANIFEST_FILE)).unwrap();
        assert!(!backup
            .windows(key_sentinel.len())
            .any(|window| window == key_sentinel));
        assert!(!manifest
            .windows(key_sentinel.len())
            .any(|window| window == key_sentinel));
        assert!(!backup
            .windows(KEY_BYTES)
            .any(|window| window == [0x5a; KEY_BYTES]));
        assert!(!manifest
            .windows(KEY_BYTES)
            .any(|window| window == [0x5a; KEY_BYTES]));
        assert_eq!(
            fs::metadata(&workspace).unwrap().permissions().mode() & 0o777,
            0o700
        );
        assert_eq!(
            fs::metadata(workspace.join(BACKUP_FILE))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );

        let reloaded = load_encrypted_backup(root.path(), &receipt.workspace, &key).unwrap();
        assert_eq!(&*reloaded, original);
    }

    #[cfg(unix)]
    #[test]
    fn backup_namespace_coexists_with_create_only_recovery_after_restart() {
        use crate::live::engine::{apply_create_only, recover_create_only, CreateFilePlan};

        let root = tempfile::tempdir().unwrap();
        let original = b"old-employee-key-sentinel\n";
        fs::write(root.path().join("config.json"), original).unwrap();
        let receipt =
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &fixed_key(0x33))
                .unwrap();
        let workspace = receipt.workspace.clone();
        drop(receipt);

        recover_create_only(root.path()).unwrap();
        apply_create_only(
            root.path(),
            vec![CreateFilePlan::new("created.json", b"new-file".to_vec())],
        )
        .unwrap();
        recover_create_only(root.path()).unwrap();

        let reloaded = load_encrypted_backup(root.path(), &workspace, &fixed_key(0x33)).unwrap();
        assert_eq!(&*reloaded, original);
        assert_eq!(
            fs::read(root.path().join("created.json")).unwrap(),
            b"new-file"
        );
    }

    #[cfg(unix)]
    #[test]
    fn wrong_key_and_ciphertext_tampering_fail_authentication() {
        let root = tempfile::tempdir().unwrap();
        fs::write(root.path().join("config.json"), b"old-secret").unwrap();
        let receipt =
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &fixed_key(1)).unwrap();
        assert!(matches!(
            load_encrypted_backup(root.path(), &receipt.workspace, &fixed_key(2)),
            Err(BackupError::Authentication)
        ));

        let backup_path = root
            .path()
            .join(BACKUP_DIRECTORY)
            .join(&receipt.workspace)
            .join(BACKUP_FILE);
        let mut bytes = fs::read(&backup_path).unwrap();
        bytes[0] ^= 0x80;
        fs::write(&backup_path, bytes).unwrap();
        assert!(matches!(
            load_encrypted_backup(root.path(), &receipt.workspace, &fixed_key(1)),
            Err(BackupError::Authentication)
        ));
    }

    #[cfg(unix)]
    #[test]
    fn target_replacement_is_a_recovery_conflict() {
        let root = tempfile::tempdir().unwrap();
        let target = root.path().join("config.json");
        fs::write(&target, b"old-secret").unwrap();
        let key = fixed_key(3);
        let receipt =
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &key).unwrap();
        fs::rename(&target, root.path().join("moved.json")).unwrap();
        fs::write(&target, b"external-edit").unwrap();
        assert!(matches!(
            load_encrypted_backup(root.path(), &receipt.workspace, &key),
            Err(BackupError::RecoveryConflict(_))
        ));
        assert_eq!(fs::read(&target).unwrap(), b"external-edit");
    }

    #[cfg(unix)]
    #[test]
    fn symlink_target_is_refused() {
        use std::os::unix::fs::symlink;

        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::NamedTempFile::new().unwrap();
        symlink(outside.path(), root.path().join("config.json")).unwrap();
        assert!(
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &fixed_key(4)).is_err()
        );
    }

    #[cfg(unix)]
    #[test]
    fn symlinked_root_ancestor_is_refused() {
        use std::os::unix::fs::symlink;

        let parent = tempfile::tempdir().unwrap();
        let real_parent = parent.path().join("real-parent");
        let real_root = real_parent.join("root");
        fs::create_dir_all(&real_root).unwrap();
        fs::write(real_root.join("config.json"), b"old-secret").unwrap();
        symlink(&real_parent, parent.path().join("linked-parent")).unwrap();

        let linked_root = parent.path().join("linked-parent").join("root");
        assert!(prepare_encrypted_backup(
            &linked_root,
            Path::new("config.json"),
            &fixed_key(6)
        )
        .is_err());
        assert!(!real_root.join(BACKUP_DIRECTORY).exists());
    }

    #[cfg(unix)]
    #[test]
    fn root_parent_traversal_is_refused() {
        let root = tempfile::tempdir().unwrap();
        fs::create_dir(root.path().join("child")).unwrap();
        fs::write(root.path().join("config.json"), b"old-secret").unwrap();

        let traversing_root = root.path().join("child").join("..");
        assert!(matches!(
            prepare_encrypted_backup(
                &traversing_root,
                Path::new("config.json"),
                &fixed_key(7)
            ),
            Err(BackupError::InvalidRoot(_))
        ));
        assert!(!root.path().join(BACKUP_DIRECTORY).exists());
    }

    #[cfg(windows)]
    #[test]
    fn windows_fails_closed_until_handle_relative_support_exists() {
        let root = tempfile::tempdir().unwrap();
        fs::write(root.path().join("config.json"), b"old-secret").unwrap();
        assert!(matches!(
            prepare_encrypted_backup(root.path(), Path::new("config.json"), &fixed_key(5)),
            Err(BackupError::UnsupportedPlatform)
        ));
    }
}
