//! Current-user Windows DPAPI custody for synthetic replacement roots.
//!
//! This module is deliberately not wired to the Tauri apply command. On
//! Windows it stores only a DPAPI-protected envelope in a private provider
//! directory under a caller-supplied synthetic root. Creation writes and
//! flushes a private stage before a same-directory, no-replace Windows rename;
//! an interrupted unpublished stage is removable on an exact create retry,
//! while an already published value is authenticated and returned
//! idempotently. Linux and macOS return `UnsupportedPlatform` before touching
//! the filesystem. There is no
//! environment-variable, command-line, database, network, or plaintext-file
//! fallback.

use super::{
    backup::BackupKey,
    recovery_key::{
        CreatedRecoveryKey, RecoveryKeyCreateOutcome, RecoveryKeyDeleteOutcome,
        RecoveryKeyOpenOutcome, RecoveryKeyProvider, RecoveryKeyReference,
        ReplacementTransactionId,
    },
};
use sha2::{Digest, Sha256};
use std::io;
use std::path::{Path, PathBuf};

const PROVIDER_KIND: &str = "windows-dpapi-user";
const PROVIDER_VERSION: u32 = 1;
const STORE_DIRECTORY: &str = ".cpa-cloud-employee-recovery-keys";
const LOCK_FILE: &str = ".lock";
const BLOB_SUFFIX: &str = ".dpapi";
const STAGE_SUFFIX: &str = ".dpapi.pending";
const MAX_PROTECTED_BYTES: usize = 16 * 1024;
const REFERENCE_BYTES: usize = 32;
const ENVELOPE_MAGIC: &[u8; 16] = b"CPA-DPAPI-KEY-V1";
const ENVELOPE_BYTES: usize = 16 + 16 + 32 + 32;
const ENTROPY_CONTEXT: &[u8] = b"cpa-cloud/employee-config/recovery-key/v1";

#[derive(Debug, thiserror::Error)]
pub enum WindowsDpapiProviderError {
    #[error("Windows current-user DPAPI recovery-key custody is unsupported on this platform")]
    UnsupportedPlatform,
    #[error("synthetic recovery-key root is not a real directory: {0}")]
    InvalidRoot(PathBuf),
    #[error("recovery-key path contains a symbolic link or Windows reparse point: {0}")]
    LinkedPath(PathBuf),
    #[error("recovery-key path is not an owner-private object: {0}")]
    UnsafePermissions(PathBuf),
    #[error("recovery-key provider namespace is locked: {0}")]
    Locked(PathBuf),
    #[error("recovery-key provider namespace is corrupt: {0}")]
    Corrupt(PathBuf),
    #[error("recovery-key provider is unavailable for: {0}")]
    Unavailable(PathBuf),
    #[error("recovery-key filesystem operation failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
}

/// A synthetic-root provider whose protected values are bound to the current
/// Windows logon identity by DPAPI. Construction is explicitly unsupported on
/// every other platform and never creates fallback key material.
pub struct WindowsDpapiRecoveryKeyProvider {
    #[cfg(windows)]
    store: windows_impl::StableStore,
}

impl WindowsDpapiRecoveryKeyProvider {
    pub fn for_synthetic_root(root: &Path) -> Result<Self, WindowsDpapiProviderError> {
        #[cfg(windows)]
        {
            return windows_impl::open_store(root).map(|store| Self { store });
        }

        #[cfg(not(windows))]
        {
            let _ = root;
            Err(WindowsDpapiProviderError::UnsupportedPlatform)
        }
    }
}

impl RecoveryKeyProvider for WindowsDpapiRecoveryKeyProvider {
    fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
        #[cfg(windows)]
        {
            return windows_impl::create(&self.store, transaction_id);
        }
        #[cfg(not(windows))]
        {
            let _ = transaction_id;
            RecoveryKeyCreateOutcome::Unavailable
        }
    }

    fn open(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        #[cfg(windows)]
        {
            return windows_impl::open(&self.store, transaction_id, reference);
        }
        #[cfg(not(windows))]
        {
            let _ = (transaction_id, reference);
            RecoveryKeyOpenOutcome::Unavailable
        }
    }

    fn delete(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome {
        #[cfg(windows)]
        {
            return windows_impl::delete(&self.store, transaction_id, reference);
        }
        #[cfg(not(windows))]
        {
            let _ = (transaction_id, reference);
            RecoveryKeyDeleteOutcome::Unavailable
        }
    }
}

fn reference_for(transaction_id: ReplacementTransactionId, protected: &[u8]) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(b"cpa-cloud/windows-dpapi-reference/v1\0");
    hasher.update(transaction_id.as_bytes());
    hasher.update(protected);
    hasher.finalize().into()
}

fn valid_reference(reference: &RecoveryKeyReference) -> bool {
    reference.provider_kind() == PROVIDER_KIND
        && reference.provider_version() == PROVIDER_VERSION
        && reference.opaque_reference().len() == REFERENCE_BYTES
}

#[cfg(windows)]
#[allow(deprecated)]
mod windows_impl {
    use super::*;
    use ring::constant_time::verify_slices_are_equal;
    use std::fs::{self, File, OpenOptions};
    use std::io::{Read, Write};
    use std::mem::{offset_of, size_of};
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
    use std::os::windows::io::{AsRawHandle, FromRawHandle};
    use std::ptr::{null, null_mut};
    use std::sync::{Mutex, MutexGuard};
    use windows_sys::Win32::Foundation::{
        GetLastError, LocalFree, ERROR_ACCESS_DENIED, ERROR_FILE_NOT_FOUND,
        ERROR_INSUFFICIENT_BUFFER, ERROR_INVALID_DATA, ERROR_INVALID_PARAMETER,
        ERROR_LOCK_VIOLATION, ERROR_PASSWORD_RESTRICTION, ERROR_PATH_NOT_FOUND,
        ERROR_SHARING_VIOLATION, ERROR_SUCCESS, GENERIC_READ, GENERIC_WRITE, INVALID_HANDLE_VALUE,
    };
    use windows_sys::Win32::Security::Authorization::{
        ConvertSecurityDescriptorToStringSecurityDescriptorW,
        ConvertStringSecurityDescriptorToSecurityDescriptorW, SetNamedSecurityInfoW,
        SDDL_REVISION_1, SE_FILE_OBJECT,
    };
    use windows_sys::Win32::Security::Cryptography::{
        CryptProtectData, CryptUnprotectData, CRYPTPROTECT_UI_FORBIDDEN, CRYPT_INTEGER_BLOB,
    };
    use windows_sys::Win32::Security::{
        EqualSid, GetFileSecurityW, GetSecurityDescriptorOwner, GetTokenInformation,
        SetFileSecurityW, TokenUser, DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION,
        PROTECTED_DACL_SECURITY_INFORMATION, TOKEN_QUERY, TOKEN_USER,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, FileRenameInfoEx, GetFileInformationByHandle, SetFileInformationByHandle,
        BY_HANDLE_FILE_INFORMATION, DELETE, FILE_ATTRIBUTE_REPARSE_POINT,
        FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_READ_ATTRIBUTES,
        FILE_RENAME_INFO, FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
    };
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};
    use zeroize::{Zeroize, Zeroizing};

    static PROCESS_PROVIDER_LOCK: Mutex<()> = Mutex::new(());

    pub(super) struct StableStore {
        path: PathBuf,
        _root: File,
        _directory: File,
    }

    struct ProviderGuard {
        _process: MutexGuard<'static, ()>,
        _file: File,
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum CreateInterruption {
        AfterStageCreate,
        AfterStageWrite,
        AfterStageFlush,
        AfterPublish,
        AfterDirectoryFlush,
    }

    struct TokenHandle(windows_sys::Win32::Foundation::HANDLE);

    impl Drop for TokenHandle {
        fn drop(&mut self) {
            unsafe { windows_sys::Win32::Foundation::CloseHandle(self.0) };
        }
    }

    struct DpapiOutput(CRYPT_INTEGER_BLOB);

    impl DpapiOutput {
        fn new() -> Self {
            Self(CRYPT_INTEGER_BLOB {
                cbData: 0,
                pbData: null_mut(),
            })
        }

        fn as_bytes(&self) -> Result<&[u8], ()> {
            let len = usize::try_from(self.0.cbData).map_err(|_| ())?;
            if len == 0 || self.0.pbData.is_null() {
                return Err(());
            }
            Ok(unsafe { std::slice::from_raw_parts(self.0.pbData, len) })
        }
    }

    impl Drop for DpapiOutput {
        fn drop(&mut self) {
            if !self.0.pbData.is_null() {
                if let Ok(len) = usize::try_from(self.0.cbData) {
                    unsafe { std::slice::from_raw_parts_mut(self.0.pbData, len) }.zeroize();
                }
                unsafe { LocalFree(self.0.pbData.cast()) };
                self.0.pbData = null_mut();
                self.0.cbData = 0;
            }
        }
    }

    pub(super) fn open_store(root: &Path) -> Result<StableStore, WindowsDpapiProviderError> {
        if !root.is_absolute() {
            return Err(WindowsDpapiProviderError::InvalidRoot(root.to_path_buf()));
        }
        let root_metadata = fs::symlink_metadata(root).map_err(|source| io_error(root, source))?;
        if !root_metadata.is_dir() {
            return Err(WindowsDpapiProviderError::InvalidRoot(root.to_path_buf()));
        }
        ensure_not_reparse(root, &root_metadata)?;
        let root_handle = open_stable_directory(root)?;

        let store_path = root.join(STORE_DIRECTORY);
        let created = match fs::create_dir(&store_path) {
            Ok(()) => true,
            Err(source) if source.kind() == io::ErrorKind::AlreadyExists => false,
            Err(source) => return Err(io_error(&store_path, source)),
        };
        let store_metadata =
            fs::symlink_metadata(&store_path).map_err(|source| io_error(&store_path, source))?;
        if !store_metadata.is_dir() {
            return Err(WindowsDpapiProviderError::InvalidRoot(store_path));
        }
        ensure_not_reparse(&store_path, &store_metadata)?;
        let store_handle = open_stable_directory(&store_path)?;
        if created {
            enforce_private_permissions(&store_path)?;
        } else {
            verify_private_permissions(&store_path)?;
        }

        let store = StableStore {
            path: store_path,
            _root: root_handle,
            _directory: store_handle,
        };
        let _guard = acquire_provider_lock(&store)
            .map_err(|error| provider_error_for(error, &store.path))?;
        sync_store_directory(&store).map_err(|error| provider_error_for(error, &store.path))?;
        Ok(store)
    }

    pub(super) fn create(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
    ) -> RecoveryKeyCreateOutcome {
        create_with_interruption(store, transaction_id, None)
    }

    fn create_with_interruption(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
        interruption: Option<CreateInterruption>,
    ) -> RecoveryKeyCreateOutcome {
        let _guard = match acquire_provider_lock(store) {
            Ok(guard) => guard,
            Err(error) => return create_outcome_for(error),
        };
        match reconcile_create_state(store, transaction_id) {
            Ok(Some(created)) => return RecoveryKeyCreateOutcome::Created(created),
            Ok(None) => {}
            Err(error) => return create_outcome_for(error),
        }

        let key = match BackupKey::generate() {
            Ok(key) => key,
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        };
        let protected = match protect(transaction_id, &key) {
            Ok(protected) => protected,
            Err(error) => return create_outcome_for(error),
        };
        if protected.is_empty() || protected.len() > MAX_PROTECTED_BYTES {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        let reference_bytes = reference_for(transaction_id, &protected);
        let reference = match RecoveryKeyReference::new(
            PROVIDER_KIND,
            PROVIDER_VERSION,
            reference_bytes.to_vec(),
        ) {
            Ok(reference) => reference,
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        };

        let stage_path = stage_path(store, transaction_id);
        let mut file = match create_private_file(&stage_path) {
            Ok(file) => file,
            Err(error) => return create_outcome_for(error),
        };
        if interruption == Some(CreateInterruption::AfterStageCreate) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if file.write_all(&protected).is_err() {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if interruption == Some(CreateInterruption::AfterStageWrite) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if file.sync_all().is_err() {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if interruption == Some(CreateInterruption::AfterStageFlush) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        let staged_identity = match file_identity(&file, &stage_path) {
            Ok(identity) => identity,
            Err(error) => return create_outcome_for(error),
        };
        if let Err(error) = publish_stage_no_replace(store, &file, transaction_id) {
            return create_outcome_for(error);
        }
        if interruption == Some(CreateInterruption::AfterPublish) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if let Err(error) = verify_published_stage(store, &file, transaction_id, staged_identity) {
            return create_outcome_for(error);
        }
        if sync_store_directory(store).is_err() {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        if interruption == Some(CreateInterruption::AfterDirectoryFlush) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        drop(file);

        match open_locked(store, transaction_id, &reference) {
            RecoveryKeyOpenOutcome::Available(opened)
                if verify_slices_are_equal(opened.expose(), key.expose()).is_ok() =>
            {
                RecoveryKeyCreateOutcome::Created(CreatedRecoveryKey { reference, key })
            }
            _ => RecoveryKeyCreateOutcome::Unavailable,
        }
    }

    pub(super) fn open(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        if !valid_reference(reference) {
            return RecoveryKeyOpenOutcome::Corrupt;
        }
        let _guard = match acquire_provider_lock(store) {
            Ok(guard) => guard,
            Err(error) => return open_outcome_for(error),
        };
        open_locked(store, transaction_id, reference)
    }

    pub(super) fn delete(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome {
        if !valid_reference(reference) {
            return RecoveryKeyDeleteOutcome::Corrupt;
        }
        let _guard = match acquire_provider_lock(store) {
            Ok(guard) => guard,
            Err(error) => return delete_outcome_for(error),
        };
        match open_locked(store, transaction_id, reference) {
            RecoveryKeyOpenOutcome::Available(_) => {}
            RecoveryKeyOpenOutcome::Missing => {
                return match sync_store_directory(store) {
                    Ok(()) => RecoveryKeyDeleteOutcome::Missing,
                    Err(error) => delete_outcome_for(error),
                }
            }
            RecoveryKeyOpenOutcome::Locked => return RecoveryKeyDeleteOutcome::Locked,
            RecoveryKeyOpenOutcome::WrongIdentity => {
                return RecoveryKeyDeleteOutcome::WrongIdentity
            }
            RecoveryKeyOpenOutcome::Corrupt => return RecoveryKeyDeleteOutcome::Corrupt,
            RecoveryKeyOpenOutcome::Unavailable => return RecoveryKeyDeleteOutcome::Unavailable,
        }
        let path = blob_path(store, transaction_id);
        match fs::remove_file(&path) {
            Ok(()) => {}
            Err(source) if source.kind() == io::ErrorKind::NotFound => {
                return RecoveryKeyDeleteOutcome::Missing
            }
            Err(source) => return delete_outcome_for(NativeError::Io(io_error(&path, source))),
        }
        match sync_store_directory(store) {
            Ok(()) => RecoveryKeyDeleteOutcome::Deleted,
            Err(error) => delete_outcome_for(error),
        }
    }

    fn open_locked(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        if let Err(error) = verify_store_identity(store) {
            return open_outcome_for(error);
        }
        match path_exists(&stage_path(store, transaction_id)) {
            Ok(true) => return RecoveryKeyOpenOutcome::Corrupt,
            Ok(false) => {}
            Err(error) => return open_outcome_for(error),
        }
        let path = blob_path(store, transaction_id);
        let protected = match read_protected_file(&path) {
            Ok(protected) => protected,
            Err(error) => return open_outcome_for(error),
        };
        let expected = reference_for(transaction_id, &protected);
        if verify_slices_are_equal(&expected, reference.opaque_reference()).is_err() {
            return RecoveryKeyOpenOutcome::Corrupt;
        }
        match unprotect(transaction_id, &protected) {
            Ok(key) => RecoveryKeyOpenOutcome::Available(key),
            Err(error) => open_outcome_for(error),
        }
    }

    fn protect(
        transaction_id: ReplacementTransactionId,
        key: &BackupKey,
    ) -> Result<Vec<u8>, NativeError> {
        let mut envelope = Zeroizing::new(Vec::with_capacity(ENVELOPE_BYTES));
        envelope.extend_from_slice(ENVELOPE_MAGIC);
        envelope.extend_from_slice(transaction_id.as_bytes());
        envelope.extend_from_slice(key.expose());
        let checksum = Sha256::digest(&envelope);
        envelope.extend_from_slice(&checksum);

        let mut entropy = entropy(transaction_id);
        let mut input = blob_from_mut(&mut envelope)?;
        let mut entropy_blob = blob_from_mut(&mut entropy)?;
        let mut output = DpapiOutput::new();
        let ok = unsafe {
            CryptProtectData(
                &mut input,
                null(),
                &mut entropy_blob,
                null(),
                null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output.0,
            )
        };
        if ok == 0 {
            return Err(NativeError::Dpapi(unsafe { GetLastError() }));
        }
        let protected = output.as_bytes().map_err(|_| NativeError::Corrupt)?;
        Ok(protected.to_vec())
    }

    fn unprotect(
        transaction_id: ReplacementTransactionId,
        protected: &[u8],
    ) -> Result<BackupKey, NativeError> {
        let mut protected = protected.to_vec();
        let mut entropy = entropy(transaction_id);
        let mut input = blob_from_mut(&mut protected)?;
        let mut entropy_blob = blob_from_mut(&mut entropy)?;
        let mut output = DpapiOutput::new();
        let ok = unsafe {
            CryptUnprotectData(
                &mut input,
                null_mut(),
                &mut entropy_blob,
                null(),
                null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output.0,
            )
        };
        if ok == 0 {
            return Err(NativeError::Dpapi(unsafe { GetLastError() }));
        }
        let plaintext = output.as_bytes().map_err(|_| NativeError::Corrupt)?;
        if plaintext.len() != ENVELOPE_BYTES
            || verify_slices_are_equal(&plaintext[..16], ENVELOPE_MAGIC).is_err()
            || verify_slices_are_equal(&plaintext[16..32], transaction_id.as_bytes()).is_err()
            || verify_slices_are_equal(&Sha256::digest(&plaintext[..64]), &plaintext[64..]).is_err()
        {
            return Err(NativeError::Corrupt);
        }
        let mut key = Zeroizing::new([0u8; 32]);
        key.copy_from_slice(&plaintext[32..64]);
        Ok(BackupKey::from_bytes(key))
    }

    fn entropy(transaction_id: ReplacementTransactionId) -> Zeroizing<Vec<u8>> {
        let mut bytes = Zeroizing::new(Vec::with_capacity(
            ENTROPY_CONTEXT.len() + transaction_id.as_bytes().len(),
        ));
        bytes.extend_from_slice(ENTROPY_CONTEXT);
        bytes.extend_from_slice(transaction_id.as_bytes());
        bytes
    }

    fn blob_from_mut(bytes: &mut [u8]) -> Result<CRYPT_INTEGER_BLOB, NativeError> {
        Ok(CRYPT_INTEGER_BLOB {
            cbData: u32::try_from(bytes.len()).map_err(|_| NativeError::Corrupt)?,
            pbData: bytes.as_mut_ptr(),
        })
    }

    fn blob_path(store: &StableStore, transaction_id: ReplacementTransactionId) -> PathBuf {
        store.path.join(blob_file_name(transaction_id))
    }

    fn blob_file_name(transaction_id: ReplacementTransactionId) -> String {
        format!("{}{}", hex::encode(transaction_id.as_bytes()), BLOB_SUFFIX)
    }

    fn stage_path(store: &StableStore, transaction_id: ReplacementTransactionId) -> PathBuf {
        store.path.join(format!(
            "{}{}",
            hex::encode(transaction_id.as_bytes()),
            STAGE_SUFFIX
        ))
    }

    fn path_exists(path: &Path) -> Result<bool, NativeError> {
        match fs::symlink_metadata(path) {
            Ok(_) => Ok(true),
            Err(source) if source.kind() == io::ErrorKind::NotFound => Ok(false),
            Err(source) => Err(NativeError::Io(io_error(path, source))),
        }
    }

    fn read_protected_file(path: &Path) -> Result<Vec<u8>, NativeError> {
        let mut file = open_private_file(path)?;
        let metadata = file
            .metadata()
            .map_err(|source| NativeError::Io(io_error(path, source)))?;
        if !metadata.is_file()
            || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
            || metadata.len() == 0
            || metadata.len() > MAX_PROTECTED_BYTES as u64
        {
            return Err(NativeError::Corrupt);
        }
        let mut protected = Vec::with_capacity(metadata.len() as usize);
        file.read_to_end(&mut protected)
            .map_err(|source| NativeError::Io(io_error(path, source)))?;
        if protected.len() != metadata.len() as usize {
            return Err(NativeError::Unavailable);
        }
        Ok(protected)
    }

    fn reconcile_create_state(
        store: &StableStore,
        transaction_id: ReplacementTransactionId,
    ) -> Result<Option<CreatedRecoveryKey>, NativeError> {
        verify_store_identity(store)?;
        let final_path = blob_path(store, transaction_id);
        let stage_path = stage_path(store, transaction_id);
        let final_exists = path_exists(&final_path)?;
        let stage_exists = path_exists(&stage_path)?;

        match (final_exists, stage_exists) {
            (true, true) => Err(NativeError::Corrupt),
            (true, false) => {
                let protected = read_protected_file(&final_path)?;
                let reference = RecoveryKeyReference::new(
                    PROVIDER_KIND,
                    PROVIDER_VERSION,
                    reference_for(transaction_id, &protected).to_vec(),
                )
                .map_err(|_| NativeError::Corrupt)?;
                let key = unprotect(transaction_id, &protected)?;
                Ok(Some(CreatedRecoveryKey { reference, key }))
            }
            (false, true) => {
                let file = open_private_file(&stage_path)?;
                let metadata = file
                    .metadata()
                    .map_err(|source| NativeError::Io(io_error(&stage_path, source)))?;
                if !metadata.is_file()
                    || metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
                    || metadata.len() > MAX_PROTECTED_BYTES as u64
                {
                    return Err(NativeError::Corrupt);
                }
                drop(file);
                fs::remove_file(&stage_path)
                    .map_err(|source| NativeError::Io(io_error(&stage_path, source)))?;
                sync_store_directory(store)?;
                Ok(None)
            }
            (false, false) => Ok(None),
        }
    }

    fn publish_stage_no_replace(
        store: &StableStore,
        stage: &File,
        transaction_id: ReplacementTransactionId,
    ) -> Result<(), NativeError> {
        let final_path = blob_path(store, transaction_id);
        let encoded: Vec<u16> = blob_file_name(transaction_id).encode_utf16().collect();
        let file_name_bytes = encoded
            .len()
            .checked_mul(size_of::<u16>())
            .ok_or(NativeError::Corrupt)?;
        let buffer_bytes = offset_of!(FILE_RENAME_INFO, FileName)
            .checked_add(file_name_bytes)
            .ok_or(NativeError::Corrupt)?;
        let storage_words = buffer_bytes
            .checked_add(size_of::<usize>() - 1)
            .ok_or(NativeError::Corrupt)?
            / size_of::<usize>();
        if encoded.is_empty() || storage_words == 0 {
            return Err(NativeError::Corrupt);
        }
        let mut storage = vec![0usize; storage_words];
        let info = storage.as_mut_ptr().cast::<FILE_RENAME_INFO>();
        unsafe {
            (*info).Anonymous.Flags = 0;
            (*info).RootDirectory = null_mut();
            (*info).FileNameLength =
                u32::try_from(file_name_bytes).map_err(|_| NativeError::Corrupt)?;
            std::ptr::copy_nonoverlapping(
                encoded.as_ptr(),
                (*info).FileName.as_mut_ptr(),
                encoded.len(),
            );
        }
        let ok = unsafe {
            SetFileInformationByHandle(
                stage.as_raw_handle(),
                FileRenameInfoEx,
                info.cast(),
                u32::try_from(buffer_bytes).map_err(|_| NativeError::Corrupt)?,
            )
        };
        if ok == 0 {
            return Err(NativeError::Io(io_error(
                &final_path,
                io::Error::last_os_error(),
            )));
        }
        Ok(())
    }

    fn verify_published_stage(
        store: &StableStore,
        stage: &File,
        transaction_id: ReplacementTransactionId,
        staged_identity: (u32, u64),
    ) -> Result<(), NativeError> {
        if path_exists(&stage_path(store, transaction_id))? {
            return Err(NativeError::Corrupt);
        }
        let final_path = blob_path(store, transaction_id);
        let final_file = open_private_file(&final_path)?;
        if file_identity(stage, &final_path)? != staged_identity
            || file_identity(&final_file, &final_path)? != staged_identity
        {
            return Err(NativeError::Corrupt);
        }
        verify_store_identity(store)?;
        Ok(())
    }

    fn file_identity(file: &File, path: &Path) -> Result<(u32, u64), NativeError> {
        let mut information = BY_HANDLE_FILE_INFORMATION::default();
        if unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut information) } == 0 {
            return Err(NativeError::Io(io_error(
                path,
                io::Error::last_os_error(),
            )));
        }
        Ok((
            information.dwVolumeSerialNumber,
            (u64::from(information.nFileIndexHigh) << 32) | u64::from(information.nFileIndexLow),
        ))
    }

    fn verify_store_identity(store: &StableStore) -> Result<(), NativeError> {
        let named = open_stable_directory(&store.path).map_err(NativeError::Io)?;
        if file_identity(&named, &store.path)? != file_identity(&store._directory, &store.path)? {
            return Err(NativeError::Corrupt);
        }
        verify_private_permissions(&store.path).map_err(NativeError::Io)
    }

    fn acquire_provider_lock(store: &StableStore) -> Result<ProviderGuard, NativeError> {
        let process = PROCESS_PROVIDER_LOCK
            .lock()
            .map_err(|_| NativeError::Unavailable)?;
        let path = store.path.join(LOCK_FILE);
        let existed = if let Ok(metadata) = fs::symlink_metadata(&path) {
            ensure_not_reparse_native(&path, &metadata)?;
            if !metadata.is_file() {
                return Err(NativeError::Corrupt);
            }
            true
        } else {
            false
        };
        let file = OpenOptions::new()
            .create(true)
            .read(true)
            .write(true)
            .share_mode(0)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(&path)
            .map_err(|source| NativeError::Io(io_error(&path, source)))?;
        if existed {
            verify_private_permissions(&path).map_err(NativeError::Io)?;
        } else {
            enforce_private_permissions(&path).map_err(NativeError::Io)?;
        }
        let metadata = file
            .metadata()
            .map_err(|source| NativeError::Io(io_error(&path, source)))?;
        ensure_not_reparse_native(&path, &metadata)?;
        Ok(ProviderGuard {
            _process: process,
            _file: file,
        })
    }

    fn create_private_file(path: &Path) -> Result<File, NativeError> {
        let file = OpenOptions::new()
            .create_new(true)
            .read(true)
            .write(true)
            .access_mode(GENERIC_READ | GENERIC_WRITE | DELETE)
            .share_mode(FILE_SHARE_READ)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
            .map_err(|source| NativeError::Io(io_error(path, source)))?;
        enforce_private_permissions(path).map_err(NativeError::Io)?;
        Ok(file)
    }

    fn open_private_file(path: &Path) -> Result<File, NativeError> {
        let file = OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ)
            .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
            .map_err(|source| NativeError::Io(io_error(path, source)))?;
        verify_private_permissions(path).map_err(NativeError::Io)?;
        Ok(file)
    }

    fn open_stable_directory(path: &Path) -> Result<File, WindowsDpapiProviderError> {
        let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        let handle = unsafe {
            CreateFileW(
                wide.as_ptr(),
                FILE_READ_ATTRIBUTES,
                FILE_SHARE_READ | FILE_SHARE_WRITE,
                null(),
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
                null_mut(),
            )
        };
        if handle == INVALID_HANDLE_VALUE {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let file = unsafe { File::from_raw_handle(handle) };
        let metadata = file.metadata().map_err(|source| io_error(path, source))?;
        ensure_not_reparse(path, &metadata)?;
        Ok(file)
    }

    fn sync_store_directory(store: &StableStore) -> Result<(), NativeError> {
        let path = &store.path;
        let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        let handle = unsafe {
            CreateFileW(
                wide.as_ptr(),
                GENERIC_WRITE | FILE_READ_ATTRIBUTES,
                FILE_SHARE_READ | FILE_SHARE_WRITE,
                null(),
                OPEN_EXISTING,
                FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT,
                null_mut(),
            )
        };
        if handle == INVALID_HANDLE_VALUE {
            return Err(NativeError::Io(io_error(path, io::Error::last_os_error())));
        }
        let directory = unsafe { File::from_raw_handle(handle) };
        let metadata = directory
            .metadata()
            .map_err(|source| NativeError::Io(io_error(path, source)))?;
        ensure_not_reparse_native(path, &metadata)?;
        if file_identity(&directory, path)? != file_identity(&store._directory, path)? {
            return Err(NativeError::Corrupt);
        }
        verify_private_permissions(path).map_err(NativeError::Io)?;
        directory
            .sync_all()
            .map_err(|source| NativeError::Io(io_error(path, source)))
    }

    fn enforce_private_permissions(path: &Path) -> Result<(), WindowsDpapiProviderError> {
        let token_info = current_user_token_info(path)?;
        let token_user =
            unsafe { std::ptr::read_unaligned(token_info.as_ptr().cast::<TOKEN_USER>()) };
        if token_user.User.Sid.is_null() {
            return Err(WindowsDpapiProviderError::UnsafePermissions(
                path.to_path_buf(),
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
        if unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                sddl.as_ptr(),
                SDDL_REVISION_1,
                &mut descriptor,
                null_mut(),
            )
        } == 0
        {
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
            return Err(io_error(path, io::Error::last_os_error()));
        }
        verify_private_permissions(path)
    }

    fn verify_private_permissions(path: &Path) -> Result<(), WindowsDpapiProviderError> {
        let wide: Vec<u16> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        let requested = DACL_SECURITY_INFORMATION | OWNER_SECURITY_INFORMATION;
        let mut needed = 0u32;
        unsafe { GetFileSecurityW(wide.as_ptr(), requested, null_mut(), 0, &mut needed) };
        if needed == 0 || unsafe { GetLastError() } != ERROR_INSUFFICIENT_BUFFER {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let mut descriptor = vec![0u8; needed as usize];
        if unsafe {
            GetFileSecurityW(
                wide.as_ptr(),
                requested,
                descriptor.as_mut_ptr().cast(),
                needed,
                &mut needed,
            )
        } == 0
        {
            return Err(io_error(path, io::Error::last_os_error()));
        }

        let mut owner = null_mut();
        let mut owner_defaulted = 0;
        if unsafe {
            GetSecurityDescriptorOwner(
                descriptor.as_mut_ptr().cast(),
                &mut owner,
                &mut owner_defaulted,
            )
        } == 0
            || owner.is_null()
        {
            return Err(WindowsDpapiProviderError::UnsafePermissions(
                path.to_path_buf(),
            ));
        }
        let token_info = current_user_token_info(path)?;
        let token_user =
            unsafe { std::ptr::read_unaligned(token_info.as_ptr().cast::<TOKEN_USER>()) };
        if token_user.User.Sid.is_null() || unsafe { EqualSid(owner, token_user.User.Sid) } == 0 {
            return Err(WindowsDpapiProviderError::UnsafePermissions(
                path.to_path_buf(),
            ));
        }

        let mut rendered = null_mut();
        let mut rendered_len = 0u32;
        if unsafe {
            ConvertSecurityDescriptorToStringSecurityDescriptorW(
                descriptor.as_mut_ptr().cast(),
                SDDL_REVISION_1,
                DACL_SECURITY_INFORMATION,
                &mut rendered,
                &mut rendered_len,
            )
        } == 0
        {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let actual = unsafe {
            String::from_utf16_lossy(std::slice::from_raw_parts(rendered, rendered_len as usize))
        };
        unsafe { LocalFree(rendered.cast()) };
        if actual.trim_end_matches('\0') != "D:P(A;;FA;;;OW)" {
            return Err(WindowsDpapiProviderError::UnsafePermissions(
                path.to_path_buf(),
            ));
        }
        Ok(())
    }

    fn current_user_token_info(path: &Path) -> Result<Vec<u8>, WindowsDpapiProviderError> {
        let mut token = null_mut();
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let token = TokenHandle(token);
        let mut needed = 0u32;
        unsafe { GetTokenInformation(token.0, TokenUser, null_mut(), 0, &mut needed) };
        if needed == 0 || unsafe { GetLastError() } != ERROR_INSUFFICIENT_BUFFER {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        let mut info = vec![0u8; needed as usize];
        if unsafe {
            GetTokenInformation(
                token.0,
                TokenUser,
                info.as_mut_ptr().cast(),
                needed,
                &mut needed,
            )
        } == 0
        {
            return Err(io_error(path, io::Error::last_os_error()));
        }
        Ok(info)
    }

    fn ensure_not_reparse(
        path: &Path,
        metadata: &fs::Metadata,
    ) -> Result<(), WindowsDpapiProviderError> {
        if metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
            Err(WindowsDpapiProviderError::LinkedPath(path.to_path_buf()))
        } else {
            Ok(())
        }
    }

    fn ensure_not_reparse_native(path: &Path, metadata: &fs::Metadata) -> Result<(), NativeError> {
        ensure_not_reparse(path, metadata).map_err(NativeError::Io)
    }

    fn io_error(path: &Path, source: io::Error) -> WindowsDpapiProviderError {
        WindowsDpapiProviderError::Io {
            path: path.to_path_buf(),
            source,
        }
    }

    enum NativeError {
        Dpapi(u32),
        Io(WindowsDpapiProviderError),
        Corrupt,
        Unavailable,
    }

    fn native_code(error: &NativeError) -> Option<u32> {
        match error {
            NativeError::Dpapi(code) => Some(*code),
            NativeError::Io(WindowsDpapiProviderError::Io { source, .. }) => {
                source.raw_os_error().map(|code| code as u32)
            }
            _ => None,
        }
    }

    fn provider_error_for(error: NativeError, path: &Path) -> WindowsDpapiProviderError {
        if is_locked(&error) {
            WindowsDpapiProviderError::Locked(path.to_path_buf())
        } else if is_corrupt(&error) {
            WindowsDpapiProviderError::Corrupt(path.to_path_buf())
        } else {
            match error {
                NativeError::Io(error) => error,
                NativeError::Dpapi(_) | NativeError::Unavailable => {
                    WindowsDpapiProviderError::Unavailable(path.to_path_buf())
                }
                NativeError::Corrupt => WindowsDpapiProviderError::Corrupt(path.to_path_buf()),
            }
        }
    }

    fn is_missing(error: &NativeError) -> bool {
        matches!(
            native_code(error),
            Some(ERROR_FILE_NOT_FOUND | ERROR_PATH_NOT_FOUND)
        )
    }

    fn is_locked(error: &NativeError) -> bool {
        matches!(
            native_code(error),
            Some(ERROR_SHARING_VIOLATION | ERROR_LOCK_VIOLATION | ERROR_PASSWORD_RESTRICTION)
        )
    }

    fn is_wrong_identity(error: &NativeError) -> bool {
        matches!(native_code(error), Some(ERROR_ACCESS_DENIED))
            || matches!(
                error,
                NativeError::Io(WindowsDpapiProviderError::UnsafePermissions(_))
            )
    }

    fn is_corrupt(error: &NativeError) -> bool {
        matches!(error, NativeError::Corrupt)
            || matches!(
                error,
                NativeError::Io(WindowsDpapiProviderError::LinkedPath(_))
            )
            || matches!(
                native_code(error),
                Some(ERROR_INVALID_DATA | ERROR_INVALID_PARAMETER)
            )
    }

    fn create_outcome_for(error: NativeError) -> RecoveryKeyCreateOutcome {
        if is_locked(&error) {
            RecoveryKeyCreateOutcome::Locked
        } else {
            RecoveryKeyCreateOutcome::Unavailable
        }
    }

    fn open_outcome_for(error: NativeError) -> RecoveryKeyOpenOutcome {
        if is_missing(&error) {
            RecoveryKeyOpenOutcome::Missing
        } else if is_locked(&error) {
            RecoveryKeyOpenOutcome::Locked
        } else if is_wrong_identity(&error) {
            RecoveryKeyOpenOutcome::WrongIdentity
        } else if is_corrupt(&error) {
            RecoveryKeyOpenOutcome::Corrupt
        } else {
            RecoveryKeyOpenOutcome::Unavailable
        }
    }

    fn delete_outcome_for(error: NativeError) -> RecoveryKeyDeleteOutcome {
        if is_missing(&error) {
            RecoveryKeyDeleteOutcome::Missing
        } else if is_locked(&error) {
            RecoveryKeyDeleteOutcome::Locked
        } else if is_wrong_identity(&error) {
            RecoveryKeyDeleteOutcome::WrongIdentity
        } else if is_corrupt(&error) {
            RecoveryKeyDeleteOutcome::Corrupt
        } else {
            RecoveryKeyDeleteOutcome::Unavailable
        }
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        fn transaction(byte: u8) -> ReplacementTransactionId {
            ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
        }

        fn created(outcome: RecoveryKeyCreateOutcome) -> (RecoveryKeyReference, BackupKey) {
            match outcome {
                RecoveryKeyCreateOutcome::Created(created) => (created.reference, created.key),
                other => panic!("unexpected create outcome: {other:?}"),
            }
        }

        #[test]
        fn current_user_dpapi_reopens_after_provider_restart_and_deletes_idempotently() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x31);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, original) = created(provider.create(transaction_id));
            drop(provider);

            let mut restarted =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let opened = match restarted.open(transaction_id, &reference) {
                RecoveryKeyOpenOutcome::Available(key) => key,
                other => panic!("unexpected open outcome: {other:?}"),
            };
            assert!(verify_slices_are_equal(original.expose(), opened.expose()).is_ok());
            assert_eq!(
                restarted.delete(transaction_id, &reference),
                RecoveryKeyDeleteOutcome::Deleted
            );
            assert!(matches!(
                restarted.open(transaction_id, &reference),
                RecoveryKeyOpenOutcome::Missing
            ));
            assert_eq!(
                restarted.delete(transaction_id, &reference),
                RecoveryKeyDeleteOutcome::Missing
            );
        }

        #[test]
        fn protected_blob_and_reference_do_not_contain_the_plaintext_key() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x32);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, key) = created(provider.create(transaction_id));
            let blob = fs::read(blob_path(&provider.store, transaction_id)).unwrap();
            assert!(!blob.windows(32).any(|window| window == key.expose()));
            assert!(!reference
                .opaque_reference()
                .windows(32)
                .any(|window| window == key.expose()));
            assert_eq!(
                format!("{reference:?}"),
                "RecoveryKeyReference { provider_kind: \"windows-dpapi-user\", provider_version: 1, opaque_reference: \"[REDACTED]\" }"
            );
        }

        #[test]
        fn tampered_blob_and_reference_fail_closed_without_replacement() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x33);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, _) = created(provider.create(transaction_id));
            let path = blob_path(&provider.store, transaction_id);
            let mut blob = fs::read(&path).unwrap();
            blob[0] ^= 0x80;
            fs::write(&path, &blob).unwrap();

            assert!(matches!(
                provider.open(transaction_id, &reference),
                RecoveryKeyOpenOutcome::Corrupt
            ));
            assert!(matches!(
                provider.create(transaction_id),
                RecoveryKeyCreateOutcome::Unavailable
            ));
            assert_eq!(fs::read(&path).unwrap(), blob);

            let wrong_reference = RecoveryKeyReference::new(
                PROVIDER_KIND,
                PROVIDER_VERSION,
                vec![0x44; REFERENCE_BYTES],
            )
            .unwrap();
            assert!(matches!(
                provider.open(transaction_id, &wrong_reference),
                RecoveryKeyOpenOutcome::Corrupt
            ));
        }

        #[test]
        fn wrong_transaction_never_opens_or_creates_a_key() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x34);
            let wrong_transaction = transaction(0x35);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, _) = created(provider.create(transaction_id));
            assert!(matches!(
                provider.open(wrong_transaction, &reference),
                RecoveryKeyOpenOutcome::Missing
            ));
            assert!(!blob_path(&provider.store, wrong_transaction).exists());
        }

        #[test]
        fn concurrent_duplicate_create_is_idempotent_and_publishes_exactly_one_blob() {
            use std::sync::{Arc, Barrier};
            use std::thread;

            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x36);
            let provider_a =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let provider_b =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let barrier = Arc::new(Barrier::new(2));

            let spawn = |mut provider: WindowsDpapiRecoveryKeyProvider, barrier: Arc<Barrier>| {
                thread::spawn(move || {
                    barrier.wait();
                    match provider.create(transaction_id) {
                        RecoveryKeyCreateOutcome::Created(created) => created,
                        other => panic!("unexpected create outcome: {other:?}"),
                    }
                })
            };
            let first = spawn(provider_a, Arc::clone(&barrier));
            let second = spawn(provider_b, barrier);
            let first_created = first.join().unwrap();
            let second_created = second.join().unwrap();
            assert_eq!(first_created.reference, second_created.reference);
            assert!(verify_slices_are_equal(
                first_created.key.expose(),
                second_created.key.expose()
            )
            .is_ok());

            let files = fs::read_dir(root.path().join(STORE_DIRECTORY))
                .unwrap()
                .filter_map(Result::ok)
                .filter(|entry| entry.file_name().to_string_lossy().ends_with(BLOB_SUFFIX))
                .count();
            assert_eq!(files, 1);
        }

        #[test]
        fn every_create_interruption_recovers_without_a_partial_final_blob() {
            let boundaries = [
                CreateInterruption::AfterStageCreate,
                CreateInterruption::AfterStageWrite,
                CreateInterruption::AfterStageFlush,
                CreateInterruption::AfterPublish,
                CreateInterruption::AfterDirectoryFlush,
            ];

            for (index, boundary) in boundaries.into_iter().enumerate() {
                let root = tempfile::tempdir().unwrap();
                let transaction_id = transaction(0x40 + index as u8);
                let provider =
                    WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
                assert!(matches!(
                    create_with_interruption(&provider.store, transaction_id, Some(boundary)),
                    RecoveryKeyCreateOutcome::Unavailable
                ));
                drop(provider);

                let mut restarted =
                    WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
                let (reference, key) = created(restarted.create(transaction_id));
                assert!(!stage_path(&restarted.store, transaction_id).exists());
                let final_blob = fs::read(blob_path(&restarted.store, transaction_id)).unwrap();
                assert!(!final_blob.is_empty());
                assert!(final_blob.len() <= MAX_PROTECTED_BYTES);

                let reopened = match restarted.open(transaction_id, &reference) {
                    RecoveryKeyOpenOutcome::Available(key) => key,
                    other => panic!("unexpected open after {boundary:?}: {other:?}"),
                };
                assert!(verify_slices_are_equal(key.expose(), reopened.expose()).is_ok());
                assert_eq!(
                    restarted.delete(transaction_id, &reference),
                    RecoveryKeyDeleteOutcome::Deleted
                );
                assert_eq!(
                    restarted.delete(transaction_id, &reference),
                    RecoveryKeyDeleteOutcome::Missing
                );
            }
        }

        #[test]
        fn no_replace_publication_preserves_an_existing_final_blob() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x50);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (_reference, _) = created(provider.create(transaction_id));
            let final_path = blob_path(&provider.store, transaction_id);
            let original = fs::read(&final_path).unwrap();

            let stage_path = stage_path(&provider.store, transaction_id);
            let mut stage = match create_private_file(&stage_path) {
                Ok(stage) => stage,
                Err(_) => panic!("synthetic stage creation failed"),
            };
            stage.write_all(b"independent-stage-sentinel").unwrap();
            stage.sync_all().unwrap();
            assert!(publish_stage_no_replace(&provider.store, &stage, transaction_id).is_err());
            assert_eq!(fs::read(&final_path).unwrap(), original);
            assert_eq!(
                fs::read(&stage_path).unwrap(),
                b"independent-stage-sentinel"
            );
        }

        #[test]
        fn simultaneous_final_and_stage_state_blocks_open_create_and_delete() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x51);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, _) = created(provider.create(transaction_id));
            let stage_path = stage_path(&provider.store, transaction_id);
            let mut stage = match create_private_file(&stage_path) {
                Ok(stage) => stage,
                Err(_) => panic!("synthetic stage creation failed"),
            };
            stage.write_all(b"ambiguous-stage").unwrap();
            stage.sync_all().unwrap();
            drop(stage);

            assert!(matches!(
                provider.open(transaction_id, &reference),
                RecoveryKeyOpenOutcome::Corrupt
            ));
            assert!(matches!(
                provider.create(transaction_id),
                RecoveryKeyCreateOutcome::Unavailable
            ));
            assert_eq!(
                provider.delete(transaction_id, &reference),
                RecoveryKeyDeleteOutcome::Corrupt
            );
            assert!(blob_path(&provider.store, transaction_id).exists());
            assert!(stage_path.exists());
        }

        #[test]
        fn contended_provider_lock_is_classified_as_locked() {
            let root = tempfile::tempdir().unwrap();
            let transaction_id = transaction(0x37);
            let mut provider =
                WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()).unwrap();
            let (reference, _) = created(provider.create(transaction_id));
            let lock = OpenOptions::new()
                .read(true)
                .write(true)
                .share_mode(0)
                .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
                .open(provider.store.path.join(LOCK_FILE))
                .unwrap();

            assert!(matches!(
                provider.open(transaction_id, &reference),
                RecoveryKeyOpenOutcome::Locked
            ));
            drop(lock);
            assert!(matches!(
                provider.open(transaction_id, &reference),
                RecoveryKeyOpenOutcome::Available(_)
            ));
        }
    }
}

#[cfg(all(test, not(windows)))]
mod unsupported_tests {
    use super::*;

    #[test]
    fn unsupported_platform_refuses_before_namespace_creation() {
        let root = tempfile::tempdir().unwrap();
        assert!(matches!(
            WindowsDpapiRecoveryKeyProvider::for_synthetic_root(root.path()),
            Err(WindowsDpapiProviderError::UnsupportedPlatform)
        ));
        assert!(!root.path().join(STORE_DIRECTORY).exists());
    }
}
