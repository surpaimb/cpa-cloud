//! Linux Secret Service recovery-key custody for isolated synthetic sessions.
//!
//! The provider is intentionally unreachable from production: construction
//! requires a [`SyntheticSecretServiceApproval`], whose only constructor is
//! test-only. It talks only to the current D-Bus session, never prompts, never
//! unlocks a collection, never uses `replace=true`, and has no file, CLI,
//! environment, database, network, or plaintext fallback. A private
//! caller-approved directory supplies the per-user cross-process reconciliation
//! lock required to make `create(transaction_id)` idempotent.

use super::{
    backup::BackupKey,
    recovery_key::{
        CreatedRecoveryKey, RecoveryKeyCreateOutcome, RecoveryKeyDeleteOutcome,
        RecoveryKeyOpenOutcome, RecoveryKeyProvider, RecoveryKeyReference,
        ReplacementTransactionId,
    },
};
use ring::{
    constant_time::verify_slices_are_equal,
    rand::{SecureRandom, SystemRandom},
};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, HashMap},
    ffi::CString,
    fs::File,
    io::{self, Read, Seek, SeekFrom, Write},
    os::fd::{AsRawFd, FromRawFd, RawFd},
    path::{Path, PathBuf},
    sync::{Mutex, MutexGuard},
    time::Duration,
};
use zeroize::Zeroizing;

const PROVIDER_KIND: &str = "linux-secret-service";
const PROVIDER_VERSION: u32 = 1;
const LOCK_FILE: &str = ".cpa-cloud-secret-service.lock";
const SERVICE_NAME: &str = "org.freedesktop.secrets";
const SERVICE_PATH: &str = "/org/freedesktop/secrets";
const DEFAULT_COLLECTION_PATH: &str = "/org/freedesktop/secrets/aliases/default";
const SERVICE_INTERFACE: &str = "org.freedesktop.Secret.Service";
const COLLECTION_INTERFACE: &str = "org.freedesktop.Secret.Collection";
const ITEM_INTERFACE: &str = "org.freedesktop.Secret.Item";
const ROOT_PATH: &str = "/";
const CONTENT_TYPE: &str = "application/octet-stream";
const LABEL: &str = "CPA Cloud employee configuration recovery key";
const DBUS_TIMEOUT: Duration = Duration::from_secs(5);
const ITEM_ID_BYTES: usize = 16;
const KEY_BYTES: usize = 32;
const CHECKSUM_BYTES: usize = 32;
const LOCK_NAMESPACE_BYTES: usize = 32;
const LOCK_SEED_BYTES: usize = 32;
const LOCK_RECORD_MAGIC: &[u8; 16] = b"CPA-SS-LOCK-V1\0\0";
const LOCK_RECORD_BYTES: usize = 16 + 4 + 8 + 8 + LOCK_SEED_BYTES + CHECKSUM_BYTES;
const REFERENCE_MAGIC: &[u8; 16] = b"CPA-SS-REF-V1\0\0\0";
const ENVELOPE_MAGIC: &[u8; 16] = b"CPA-SS-KEY-V1\0\0\0";
const REFERENCE_CONTEXT: &[u8] = b"cpa-cloud/linux-secret-service/reference/v1\0";
const ENVELOPE_CONTEXT: &[u8] = b"cpa-cloud/linux-secret-service/envelope/v1\0";
const ATTRIBUTES_CONTEXT: &[u8] = b"cpa-cloud/linux-secret-service/attributes/v1\0";
const LOCK_NAMESPACE_CONTEXT: &[u8] = b"cpa-cloud/linux-secret-service/lock-namespace/v1\0";
const LOCK_RECORD_CONTEXT: &[u8] = b"cpa-cloud/linux-secret-service/lock-record/v1\0";
const REFERENCE_BYTES: usize =
    16 + 16 + 4 + LOCK_NAMESPACE_BYTES + ITEM_ID_BYTES + CHECKSUM_BYTES + CHECKSUM_BYTES;
const ENVELOPE_BYTES: usize =
    16 + 16 + 4 + LOCK_NAMESPACE_BYTES + ITEM_ID_BYTES + KEY_BYTES + CHECKSUM_BYTES;

type Attributes = BTreeMap<String, String>;

#[derive(Debug, thiserror::Error)]
pub(crate) enum LinuxSecretServiceProviderError {
    #[error("synthetic Secret Service lock root is not an owner-private directory: {0}")]
    InvalidLockRoot(PathBuf),
    #[error("synthetic Secret Service lock path is unsafe: {0}")]
    UnsafeLock(PathBuf),
    #[error("Secret Service session is unavailable")]
    SessionUnavailable,
    #[error("Secret Service provider setup failed: {0}")]
    Setup(&'static str),
    #[error("Secret Service lock I/O failed for {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
}

/// Opaque proof that an isolated owner-private lock directory was explicitly
/// approved. Production code has no constructor for this value, and tests must
/// distinguish one-time fresh provisioning from normal fail-closed reopen.
pub(crate) struct SyntheticSecretServiceApproval {
    directory: File,
    display: PathBuf,
    uid: u32,
    allow_initialization: bool,
}

#[cfg(test)]
impl SyntheticSecretServiceApproval {
    fn for_test(path: &Path) -> Result<Self, LinuxSecretServiceProviderError> {
        open_approved_directory(path, false)
    }

    fn for_test_fresh(path: &Path) -> Result<Self, LinuxSecretServiceProviderError> {
        open_approved_directory(path, true)
    }
}

pub(crate) struct LinuxSecretServiceRecoveryKeyProvider {
    inner: ContractProvider<DbusBackend>,
}

impl LinuxSecretServiceRecoveryKeyProvider {
    pub(crate) fn for_synthetic_session(
        approval: SyntheticSecretServiceApproval,
    ) -> Result<Self, LinuxSecretServiceProviderError> {
        #[cfg(test)]
        real_fixture::stage("lock-setup", false);
        let lock = ReconciliationLock::open(approval)?;
        #[cfg(test)]
        real_fixture::stage("lock-setup", true);
        let backend = DbusBackend::connect()?;
        Ok(Self {
            inner: ContractProvider::new(backend, lock),
        })
    }
}

impl RecoveryKeyProvider for LinuxSecretServiceRecoveryKeyProvider {
    fn create_is_transaction_idempotent(&self) -> bool {
        true
    }

    fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum BackendError {
    Locked,
    PromptRequired,
    Missing,
    Corrupt,
    Unavailable,
}

#[derive(Clone, Debug)]
struct ServiceItem {
    handle: String,
    attributes: Attributes,
    locked: bool,
}

struct CreateRequest<'a> {
    label: &'a str,
    attributes: &'a Attributes,
    secret: &'a [u8],
    content_type: &'a str,
    replace: bool,
}

trait SecretServiceBackend {
    fn search(&mut self, attributes: &Attributes) -> Result<Vec<ServiceItem>, BackendError>;
    fn create_item(&mut self, request: CreateRequest<'_>) -> Result<String, BackendError>;
    fn get_secret(&mut self, handle: &str) -> Result<Zeroizing<Vec<u8>>, BackendError>;
    fn delete_item(&mut self, handle: &str) -> Result<(), BackendError>;
}

struct ContractProvider<B> {
    backend: B,
    lock: ReconciliationLock,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
}

impl<B: SecretServiceBackend> ContractProvider<B> {
    fn new(backend: B, lock: ReconciliationLock) -> Self {
        let uid = lock.uid;
        let lock_namespace = lock.namespace;
        Self {
            backend,
            lock,
            uid,
            lock_namespace,
        }
    }

    fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
        let lock = &self.lock;
        let backend = &mut self.backend;
        let _guard = match lock.exclusive() {
            Ok(guard) => guard,
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        };
        match find_one(backend, transaction_id, self.uid, self.lock_namespace) {
            Ok(Some(item)) => {
                return create_from_existing(
                    backend,
                    transaction_id,
                    self.uid,
                    self.lock_namespace,
                    item,
                );
            }
            Ok(None) => {}
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                return RecoveryKeyCreateOutcome::Locked;
            }
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        }

        let key = match BackupKey::generate() {
            Ok(key) => key,
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        };
        let mut item_id = [0_u8; ITEM_ID_BYTES];
        if SystemRandom::new().fill(&mut item_id).is_err() {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        let attributes = full_attributes(transaction_id, self.uid, self.lock_namespace, item_id);
        let envelope = Zeroizing::new(encode_envelope(
            transaction_id,
            self.uid,
            self.lock_namespace,
            item_id,
            &key,
        ));
        let request = CreateRequest {
            label: LABEL,
            attributes: &attributes,
            secret: envelope.as_slice(),
            content_type: CONTENT_TYPE,
            replace: false,
        };
        #[cfg(test)]
        if !real_fixture::capture_outgoing(&key, envelope.as_slice()) {
            return RecoveryKeyCreateOutcome::Unavailable;
        }
        match backend.create_item(request) {
            Ok(_) => {}
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                return RecoveryKeyCreateOutcome::Locked;
            }
            Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
        }
        match find_one(backend, transaction_id, self.uid, self.lock_namespace) {
            Ok(Some(item)) => {
                let result = verify_item(
                    backend,
                    transaction_id,
                    self.uid,
                    self.lock_namespace,
                    &item,
                    Some(item_id),
                );
                match result {
                    VerifiedItem::Available {
                        item_id: verified_id,
                        key: verified_key,
                    } if verified_id == item_id
                        && verify_slices_are_equal(verified_key.expose(), key.expose()).is_ok() =>
                    {
                        created(
                            transaction_id,
                            self.uid,
                            self.lock_namespace,
                            item_id,
                            verified_key,
                        )
                    }
                    VerifiedItem::Locked => RecoveryKeyCreateOutcome::Locked,
                    _ => RecoveryKeyCreateOutcome::Unavailable,
                }
            }
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                RecoveryKeyCreateOutcome::Locked
            }
            _ => RecoveryKeyCreateOutcome::Unavailable,
        }
    }

    fn open(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        #[cfg(test)]
        real_fixture::stage("reference", false);
        let parsed = match parse_reference(transaction_id, self.uid, self.lock_namespace, reference)
        {
            ParsedReference::Valid { item_id } => item_id,
            ParsedReference::WrongIdentity => return RecoveryKeyOpenOutcome::WrongIdentity,
            ParsedReference::Corrupt => return RecoveryKeyOpenOutcome::Corrupt,
        };
        #[cfg(test)]
        real_fixture::stage("reference", true);
        let lock = &self.lock;
        let backend = &mut self.backend;
        #[cfg(test)]
        real_fixture::stage("lock-open", false);
        let _guard = match lock.exclusive() {
            Ok(guard) => guard,
            Err(_) => return RecoveryKeyOpenOutcome::Unavailable,
        };
        #[cfg(test)]
        real_fixture::stage("lock-open", true);
        let item = match find_one(backend, transaction_id, self.uid, self.lock_namespace) {
            Ok(Some(item)) => item,
            Ok(None) => return RecoveryKeyOpenOutcome::Missing,
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                return RecoveryKeyOpenOutcome::Locked;
            }
            Err(BackendError::Corrupt) => return RecoveryKeyOpenOutcome::Corrupt,
            Err(_) => return RecoveryKeyOpenOutcome::Unavailable,
        };
        match verify_item(
            backend,
            transaction_id,
            self.uid,
            self.lock_namespace,
            &item,
            Some(parsed),
        ) {
            VerifiedItem::Available { key, .. } => RecoveryKeyOpenOutcome::Available(key),
            VerifiedItem::Locked => RecoveryKeyOpenOutcome::Locked,
            VerifiedItem::WrongIdentity => RecoveryKeyOpenOutcome::WrongIdentity,
            VerifiedItem::Corrupt => RecoveryKeyOpenOutcome::Corrupt,
            VerifiedItem::Unavailable => RecoveryKeyOpenOutcome::Unavailable,
        }
    }

    fn delete(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome {
        let parsed = match parse_reference(transaction_id, self.uid, self.lock_namespace, reference)
        {
            ParsedReference::Valid { item_id } => item_id,
            ParsedReference::WrongIdentity => return RecoveryKeyDeleteOutcome::WrongIdentity,
            ParsedReference::Corrupt => return RecoveryKeyDeleteOutcome::Corrupt,
        };
        let lock = &self.lock;
        let backend = &mut self.backend;
        let _guard = match lock.exclusive() {
            Ok(guard) => guard,
            Err(_) => return RecoveryKeyDeleteOutcome::Unavailable,
        };
        let item = match find_one(backend, transaction_id, self.uid, self.lock_namespace) {
            Ok(Some(item)) => item,
            Ok(None) => return RecoveryKeyDeleteOutcome::Missing,
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                return RecoveryKeyDeleteOutcome::Locked;
            }
            Err(BackendError::Corrupt) => return RecoveryKeyDeleteOutcome::Corrupt,
            Err(_) => return RecoveryKeyDeleteOutcome::Unavailable,
        };
        match verify_item(
            backend,
            transaction_id,
            self.uid,
            self.lock_namespace,
            &item,
            Some(parsed),
        ) {
            VerifiedItem::Available { .. } => {}
            VerifiedItem::Locked => return RecoveryKeyDeleteOutcome::Locked,
            VerifiedItem::WrongIdentity => return RecoveryKeyDeleteOutcome::WrongIdentity,
            VerifiedItem::Corrupt => return RecoveryKeyDeleteOutcome::Corrupt,
            VerifiedItem::Unavailable => return RecoveryKeyDeleteOutcome::Unavailable,
        }
        match backend.delete_item(&item.handle) {
            Ok(()) => {}
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                return RecoveryKeyDeleteOutcome::Locked;
            }
            Err(BackendError::Missing) => {}
            Err(_) => return RecoveryKeyDeleteOutcome::Unavailable,
        }
        match find_one(backend, transaction_id, self.uid, self.lock_namespace) {
            Ok(None) => RecoveryKeyDeleteOutcome::Deleted,
            Ok(Some(_)) => RecoveryKeyDeleteOutcome::WrongIdentity,
            Err(BackendError::Locked | BackendError::PromptRequired) => {
                RecoveryKeyDeleteOutcome::Locked
            }
            Err(BackendError::Corrupt) => RecoveryKeyDeleteOutcome::Corrupt,
            Err(_) => RecoveryKeyDeleteOutcome::Unavailable,
        }
    }
}

fn create_from_existing<B: SecretServiceBackend>(
    backend: &mut B,
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item: ServiceItem,
) -> RecoveryKeyCreateOutcome {
    match verify_item(backend, transaction_id, uid, lock_namespace, &item, None) {
        VerifiedItem::Available { item_id, key } => {
            created(transaction_id, uid, lock_namespace, item_id, key)
        }
        VerifiedItem::Locked => RecoveryKeyCreateOutcome::Locked,
        _ => RecoveryKeyCreateOutcome::Unavailable,
    }
}

fn created(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item_id: [u8; ITEM_ID_BYTES],
    key: BackupKey,
) -> RecoveryKeyCreateOutcome {
    let reference = match RecoveryKeyReference::new(
        PROVIDER_KIND,
        PROVIDER_VERSION,
        encode_reference(transaction_id, uid, lock_namespace, item_id),
    ) {
        Ok(reference) => reference,
        Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
    };
    RecoveryKeyCreateOutcome::Created(CreatedRecoveryKey { reference, key })
}

fn find_one<B: SecretServiceBackend>(
    backend: &mut B,
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
) -> Result<Option<ServiceItem>, BackendError> {
    let items = backend.search(&base_attributes(transaction_id, uid, lock_namespace))?;
    if items.len() > 1 {
        return Err(BackendError::Corrupt);
    }
    let Some(item) = items.into_iter().next() else {
        return Ok(None);
    };
    if item.locked {
        return Err(BackendError::Locked);
    }
    Ok(Some(item))
}

enum VerifiedItem {
    Available {
        item_id: [u8; ITEM_ID_BYTES],
        key: BackupKey,
    },
    Locked,
    WrongIdentity,
    Corrupt,
    Unavailable,
}

fn verify_item<B: SecretServiceBackend>(
    backend: &mut B,
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item: &ServiceItem,
    expected_item_id: Option<[u8; ITEM_ID_BYTES]>,
) -> VerifiedItem {
    #[cfg(test)]
    real_fixture::stage("attributes", false);
    let Some(item_id_text) = item.attributes.get("item-id") else {
        return VerifiedItem::Corrupt;
    };
    let Ok(item_id_bytes) = hex::decode(item_id_text) else {
        return VerifiedItem::Corrupt;
    };
    let Ok(item_id) = <[u8; ITEM_ID_BYTES]>::try_from(item_id_bytes.as_slice()) else {
        return VerifiedItem::Corrupt;
    };
    if expected_item_id.is_some_and(|expected| expected != item_id) {
        return VerifiedItem::WrongIdentity;
    }
    if item.attributes != full_attributes(transaction_id, uid, lock_namespace, item_id) {
        return VerifiedItem::Corrupt;
    }
    #[cfg(test)]
    real_fixture::stage("attributes", true);
    let secret = match backend.get_secret(&item.handle) {
        Ok(secret) => secret,
        Err(BackendError::Locked | BackendError::PromptRequired) => return VerifiedItem::Locked,
        Err(BackendError::Corrupt) => return VerifiedItem::Corrupt,
        Err(_) => return VerifiedItem::Unavailable,
    };
    #[cfg(test)]
    real_fixture::stage("envelope", false);
    match decode_envelope(transaction_id, uid, lock_namespace, item_id, &secret) {
        Envelope::Available(key) => {
            #[cfg(test)]
            real_fixture::stage("envelope", true);
            VerifiedItem::Available { item_id, key }
        }
        Envelope::WrongIdentity => VerifiedItem::WrongIdentity,
        Envelope::Corrupt => VerifiedItem::Corrupt,
    }
}

fn base_attributes(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
) -> Attributes {
    BTreeMap::from([
        ("application".to_owned(), "cpa-cloud".to_owned()),
        (
            "purpose".to_owned(),
            "employee-config-recovery-key".to_owned(),
        ),
        ("format".to_owned(), PROVIDER_VERSION.to_string()),
        (
            "transaction".to_owned(),
            hex::encode(transaction_id.as_bytes()),
        ),
        ("unix-user".to_owned(), uid.to_string()),
        ("lock-namespace".to_owned(), hex::encode(lock_namespace)),
    ])
}

fn full_attributes(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item_id: [u8; ITEM_ID_BYTES],
) -> Attributes {
    let mut attributes = base_attributes(transaction_id, uid, lock_namespace);
    attributes.insert("item-id".to_owned(), hex::encode(item_id));
    attributes
}

fn attributes_digest(attributes: &Attributes) -> [u8; CHECKSUM_BYTES] {
    let mut hasher = Sha256::new();
    hasher.update(ATTRIBUTES_CONTEXT);
    for (name, value) in attributes {
        hasher.update((name.len() as u32).to_be_bytes());
        hasher.update(name.as_bytes());
        hasher.update((value.len() as u32).to_be_bytes());
        hasher.update(value.as_bytes());
    }
    hasher.finalize().into()
}

fn encode_reference(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item_id: [u8; ITEM_ID_BYTES],
) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(REFERENCE_BYTES);
    bytes.extend_from_slice(REFERENCE_MAGIC);
    bytes.extend_from_slice(transaction_id.as_bytes());
    bytes.extend_from_slice(&uid.to_be_bytes());
    bytes.extend_from_slice(&lock_namespace);
    bytes.extend_from_slice(&item_id);
    bytes.extend_from_slice(&attributes_digest(&full_attributes(
        transaction_id,
        uid,
        lock_namespace,
        item_id,
    )));
    let mut hasher = Sha256::new();
    hasher.update(REFERENCE_CONTEXT);
    hasher.update(&bytes);
    bytes.extend_from_slice(&hasher.finalize());
    bytes
}

enum ParsedReference {
    Valid { item_id: [u8; ITEM_ID_BYTES] },
    WrongIdentity,
    Corrupt,
}

fn parse_reference(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    reference: &RecoveryKeyReference,
) -> ParsedReference {
    if reference.provider_kind() != PROVIDER_KIND
        || reference.provider_version() != PROVIDER_VERSION
    {
        return ParsedReference::WrongIdentity;
    }
    let bytes = reference.opaque_reference();
    if bytes.len() != REFERENCE_BYTES || &bytes[..16] != REFERENCE_MAGIC {
        return ParsedReference::Corrupt;
    }
    let checksum_offset = REFERENCE_BYTES - CHECKSUM_BYTES;
    let mut hasher = Sha256::new();
    hasher.update(REFERENCE_CONTEXT);
    hasher.update(&bytes[..checksum_offset]);
    if verify_slices_are_equal(&hasher.finalize(), &bytes[checksum_offset..]).is_err() {
        return ParsedReference::Corrupt;
    }
    let stored_transaction = &bytes[16..32];
    let stored_uid = u32::from_be_bytes(bytes[32..36].try_into().expect("fixed reference"));
    if verify_slices_are_equal(stored_transaction, transaction_id.as_bytes()).is_err()
        || stored_uid != uid
        || verify_slices_are_equal(&bytes[36..68], &lock_namespace).is_err()
    {
        return ParsedReference::WrongIdentity;
    }
    let mut item_id = [0_u8; ITEM_ID_BYTES];
    item_id.copy_from_slice(&bytes[68..84]);
    let expected_attributes = attributes_digest(&full_attributes(
        transaction_id,
        uid,
        lock_namespace,
        item_id,
    ));
    if verify_slices_are_equal(&expected_attributes, &bytes[84..116]).is_err() {
        return ParsedReference::Corrupt;
    }
    ParsedReference::Valid { item_id }
}

fn encode_envelope(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item_id: [u8; ITEM_ID_BYTES],
    key: &BackupKey,
) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(ENVELOPE_BYTES);
    bytes.extend_from_slice(ENVELOPE_MAGIC);
    bytes.extend_from_slice(transaction_id.as_bytes());
    bytes.extend_from_slice(&uid.to_be_bytes());
    bytes.extend_from_slice(&lock_namespace);
    bytes.extend_from_slice(&item_id);
    bytes.extend_from_slice(key.expose());
    let mut hasher = Sha256::new();
    hasher.update(ENVELOPE_CONTEXT);
    hasher.update(&bytes);
    bytes.extend_from_slice(&hasher.finalize());
    bytes
}

enum Envelope {
    Available(BackupKey),
    WrongIdentity,
    Corrupt,
}

fn decode_envelope(
    transaction_id: ReplacementTransactionId,
    uid: u32,
    lock_namespace: [u8; LOCK_NAMESPACE_BYTES],
    item_id: [u8; ITEM_ID_BYTES],
    bytes: &[u8],
) -> Envelope {
    if bytes.len() != ENVELOPE_BYTES || &bytes[..16] != ENVELOPE_MAGIC {
        return Envelope::Corrupt;
    }
    let checksum_offset = ENVELOPE_BYTES - CHECKSUM_BYTES;
    let mut hasher = Sha256::new();
    hasher.update(ENVELOPE_CONTEXT);
    hasher.update(&bytes[..checksum_offset]);
    if verify_slices_are_equal(&hasher.finalize(), &bytes[checksum_offset..]).is_err() {
        return Envelope::Corrupt;
    }
    let stored_uid = u32::from_be_bytes(bytes[32..36].try_into().expect("fixed envelope"));
    if verify_slices_are_equal(&bytes[16..32], transaction_id.as_bytes()).is_err()
        || stored_uid != uid
        || verify_slices_are_equal(&bytes[36..68], &lock_namespace).is_err()
        || verify_slices_are_equal(&bytes[68..84], &item_id).is_err()
    {
        return Envelope::WrongIdentity;
    }
    let mut key = Zeroizing::new([0_u8; KEY_BYTES]);
    key.copy_from_slice(&bytes[84..116]);
    Envelope::Available(BackupKey::from_bytes(key))
}

struct ReconciliationLock {
    directory: File,
    file: File,
    display: PathBuf,
    local: Mutex<()>,
    uid: u32,
    namespace: [u8; LOCK_NAMESPACE_BYTES],
}

struct ReconciliationGuard<'a> {
    file: &'a File,
    _local: MutexGuard<'a, ()>,
}

impl Drop for ReconciliationGuard<'_> {
    fn drop(&mut self) {
        unsafe {
            libc::flock(self.file.as_raw_fd(), libc::LOCK_UN);
        }
    }
}

impl ReconciliationLock {
    fn open(
        approval: SyntheticSecretServiceApproval,
    ) -> Result<Self, LinuxSecretServiceProviderError> {
        let name = CString::new(LOCK_FILE).expect("fixed lock name has no NUL");
        let flags = if approval.allow_initialization {
            libc::O_RDWR | libc::O_CREAT | libc::O_EXCL | libc::O_CLOEXEC | libc::O_NOFOLLOW
        } else {
            libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW
        };
        let fd =
            unsafe { libc::openat(approval.directory.as_raw_fd(), name.as_ptr(), flags, 0o600) };
        if fd < 0 {
            return Err(LinuxSecretServiceProviderError::UnsafeLock(
                approval.display.join(LOCK_FILE),
            ));
        }
        let mut file = unsafe { File::from_raw_fd(fd) };
        verify_private_regular_file(&file, approval.uid, &approval.display.join(LOCK_FILE))?;
        let seed = initialize_lock_namespace(
            &mut file,
            approval.uid,
            approval.allow_initialization,
            &approval.display.join(LOCK_FILE),
        )?;
        let namespace =
            derive_lock_namespace(&file, approval.uid, seed, &approval.display.join(LOCK_FILE))?;
        approval
            .directory
            .sync_all()
            .map_err(|source| LinuxSecretServiceProviderError::Io {
                path: approval.display.clone(),
                source,
            })?;
        Ok(Self {
            directory: approval.directory,
            file,
            display: approval.display,
            local: Mutex::new(()),
            uid: approval.uid,
            namespace,
        })
    }

    fn exclusive(&self) -> Result<ReconciliationGuard<'_>, LinuxSecretServiceProviderError> {
        let local = self
            .local
            .lock()
            .map_err(|_| LinuxSecretServiceProviderError::Setup("local lock poisoned"))?;
        verify_private_directory_file(&self.directory, self.uid, &self.display)?;
        verify_private_regular_file(&self.file, self.uid, &self.display.join(LOCK_FILE))?;
        loop {
            if unsafe { libc::flock(self.file.as_raw_fd(), libc::LOCK_EX) } == 0 {
                let guard = ReconciliationGuard {
                    file: &self.file,
                    _local: local,
                };
                verify_current_lock_entry(
                    &self.directory,
                    &self.file,
                    self.uid,
                    self.namespace,
                    &self.display.join(LOCK_FILE),
                )?;
                return Ok(guard);
            }
            let source = io::Error::last_os_error();
            if source.kind() != io::ErrorKind::Interrupted {
                return Err(LinuxSecretServiceProviderError::Io {
                    path: self.display.join(LOCK_FILE),
                    source,
                });
            }
        }
    }
}

fn verify_current_lock_entry(
    directory: &File,
    held: &File,
    uid: u32,
    expected_namespace: [u8; LOCK_NAMESPACE_BYTES],
    path: &Path,
) -> Result<(), LinuxSecretServiceProviderError> {
    let name = CString::new(LOCK_FILE).expect("fixed lock name has no NUL");
    let fd = unsafe {
        libc::openat(
            directory.as_raw_fd(),
            name.as_ptr(),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
        )
    };
    if fd < 0 {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    let mut current = unsafe { File::from_raw_fd(fd) };
    verify_private_regular_file(&current, uid, path)?;
    let held_stat = stat(held.as_raw_fd(), path)?;
    let current_stat = stat(current.as_raw_fd(), path)?;
    if held_stat.st_dev != current_stat.st_dev || held_stat.st_ino != current_stat.st_ino {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    let seed = read_lock_seed(&mut current, uid, path)?;
    let current_namespace = derive_lock_namespace(&current, uid, seed, path)?;
    if current_namespace != expected_namespace {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    Ok(())
}

fn derive_lock_namespace(
    file: &File,
    uid: u32,
    seed: [u8; LOCK_NAMESPACE_BYTES],
    path: &Path,
) -> Result<[u8; LOCK_NAMESPACE_BYTES], LinuxSecretServiceProviderError> {
    let metadata = stat(file.as_raw_fd(), path)?;
    let mut hasher = Sha256::new();
    hasher.update(LOCK_NAMESPACE_CONTEXT);
    hasher.update(uid.to_be_bytes());
    hasher.update((metadata.st_dev as u64).to_be_bytes());
    hasher.update((metadata.st_ino as u64).to_be_bytes());
    hasher.update(seed);
    Ok(hasher.finalize().into())
}

fn initialize_lock_namespace(
    file: &mut File,
    uid: u32,
    allow_initialization: bool,
    path: &Path,
) -> Result<[u8; LOCK_SEED_BYTES], LinuxSecretServiceProviderError> {
    loop {
        if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX) } == 0 {
            break;
        }
        let source = io::Error::last_os_error();
        if source.kind() != io::ErrorKind::Interrupted {
            return Err(LinuxSecretServiceProviderError::Io {
                path: path.to_path_buf(),
                source,
            });
        }
    }
    let result = (|| {
        let length = file
            .metadata()
            .map_err(|source| LinuxSecretServiceProviderError::Io {
                path: path.to_path_buf(),
                source,
            })?
            .len();
        match length {
            0 if allow_initialization => {
                let mut seed = [0_u8; LOCK_SEED_BYTES];
                SystemRandom::new().fill(&mut seed).map_err(|_| {
                    LinuxSecretServiceProviderError::Setup("lock namespace generation failed")
                })?;
                let record = encode_lock_record(file, uid, seed, path)?;
                file.seek(SeekFrom::Start(0)).map_err(|source| {
                    LinuxSecretServiceProviderError::Io {
                        path: path.to_path_buf(),
                        source,
                    }
                })?;
                file.write_all(&record)
                    .map_err(|source| LinuxSecretServiceProviderError::Io {
                        path: path.to_path_buf(),
                        source,
                    })?;
                file.sync_all()
                    .map_err(|source| LinuxSecretServiceProviderError::Io {
                        path: path.to_path_buf(),
                        source,
                    })?;
            }
            length if length == LOCK_RECORD_BYTES as u64 => {}
            _ => {
                return Err(LinuxSecretServiceProviderError::UnsafeLock(
                    path.to_path_buf(),
                ));
            }
        }
        read_lock_seed(file, uid, path)
    })();
    unsafe {
        libc::flock(file.as_raw_fd(), libc::LOCK_UN);
    }
    result
}

fn encode_lock_record(
    file: &File,
    uid: u32,
    seed: [u8; LOCK_SEED_BYTES],
    path: &Path,
) -> Result<Vec<u8>, LinuxSecretServiceProviderError> {
    let metadata = stat(file.as_raw_fd(), path)?;
    let mut record = Vec::with_capacity(LOCK_RECORD_BYTES);
    record.extend_from_slice(LOCK_RECORD_MAGIC);
    record.extend_from_slice(&uid.to_be_bytes());
    record.extend_from_slice(&(metadata.st_dev as u64).to_be_bytes());
    record.extend_from_slice(&(metadata.st_ino as u64).to_be_bytes());
    record.extend_from_slice(&seed);
    let mut hasher = Sha256::new();
    hasher.update(LOCK_RECORD_CONTEXT);
    hasher.update(&record);
    record.extend_from_slice(&hasher.finalize());
    Ok(record)
}

fn read_lock_seed(
    file: &mut File,
    uid: u32,
    path: &Path,
) -> Result<[u8; LOCK_SEED_BYTES], LinuxSecretServiceProviderError> {
    let metadata = stat(file.as_raw_fd(), path)?;
    if metadata.st_size as u64 != LOCK_RECORD_BYTES as u64 {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    file.seek(SeekFrom::Start(0))
        .map_err(|source| LinuxSecretServiceProviderError::Io {
            path: path.to_path_buf(),
            source,
        })?;
    let mut record = [0_u8; LOCK_RECORD_BYTES];
    file.read_exact(&mut record)
        .map_err(|source| LinuxSecretServiceProviderError::Io {
            path: path.to_path_buf(),
            source,
        })?;
    let checksum_offset = LOCK_RECORD_BYTES - CHECKSUM_BYTES;
    let mut hasher = Sha256::new();
    hasher.update(LOCK_RECORD_CONTEXT);
    hasher.update(&record[..checksum_offset]);
    let stored_uid = u32::from_be_bytes(record[16..20].try_into().expect("fixed lock record"));
    let stored_device = u64::from_be_bytes(record[20..28].try_into().expect("fixed lock record"));
    let stored_inode = u64::from_be_bytes(record[28..36].try_into().expect("fixed lock record"));
    if &record[..16] != LOCK_RECORD_MAGIC
        || stored_uid != uid
        || stored_device != metadata.st_dev as u64
        || stored_inode != metadata.st_ino as u64
        || verify_slices_are_equal(&hasher.finalize(), &record[checksum_offset..]).is_err()
    {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    let mut seed = [0_u8; LOCK_SEED_BYTES];
    seed.copy_from_slice(&record[36..68]);
    Ok(seed)
}

fn open_approved_directory(
    path: &Path,
    allow_initialization: bool,
) -> Result<SyntheticSecretServiceApproval, LinuxSecretServiceProviderError> {
    let c_path = CString::new(path.as_os_str().as_encoded_bytes())
        .map_err(|_| LinuxSecretServiceProviderError::InvalidLockRoot(path.to_path_buf()))?;
    let fd = unsafe {
        libc::open(
            c_path.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC | libc::O_NOFOLLOW,
        )
    };
    if fd < 0 {
        return Err(LinuxSecretServiceProviderError::Io {
            path: path.to_path_buf(),
            source: io::Error::last_os_error(),
        });
    }
    let directory = unsafe { File::from_raw_fd(fd) };
    let uid = unsafe { libc::geteuid() };
    verify_private_directory_file(&directory, uid, path)?;
    Ok(SyntheticSecretServiceApproval {
        directory,
        display: path.to_path_buf(),
        uid,
        allow_initialization,
    })
}

fn stat(fd: RawFd, path: &Path) -> Result<libc::stat, LinuxSecretServiceProviderError> {
    let mut value = std::mem::MaybeUninit::<libc::stat>::uninit();
    if unsafe { libc::fstat(fd, value.as_mut_ptr()) } != 0 {
        return Err(LinuxSecretServiceProviderError::Io {
            path: path.to_path_buf(),
            source: io::Error::last_os_error(),
        });
    }
    Ok(unsafe { value.assume_init() })
}

fn verify_private_directory_file(
    file: &File,
    uid: u32,
    path: &Path,
) -> Result<(), LinuxSecretServiceProviderError> {
    let metadata = stat(file.as_raw_fd(), path)?;
    if metadata.st_uid != uid
        || metadata.st_mode & libc::S_IFMT != libc::S_IFDIR
        || metadata.st_mode & 0o077 != 0
    {
        return Err(LinuxSecretServiceProviderError::InvalidLockRoot(
            path.to_path_buf(),
        ));
    }
    Ok(())
}

fn verify_private_regular_file(
    file: &File,
    uid: u32,
    path: &Path,
) -> Result<(), LinuxSecretServiceProviderError> {
    let metadata = stat(file.as_raw_fd(), path)?;
    if metadata.st_uid != uid
        || metadata.st_mode & libc::S_IFMT != libc::S_IFREG
        || metadata.st_mode & 0o777 != 0o600
        || metadata.st_nlink != 1
    {
        return Err(LinuxSecretServiceProviderError::UnsafeLock(
            path.to_path_buf(),
        ));
    }
    Ok(())
}

struct DbusBackend {
    connection: dbus::blocking::Connection,
    session: dbus::Path<'static>,
}

impl DbusBackend {
    fn connect() -> Result<Self, LinuxSecretServiceProviderError> {
        use dbus::arg::Variant;
        #[cfg(test)]
        real_fixture::stage("bus-connect", false);
        let connection = dbus::blocking::Connection::new_session()
            .map_err(|_| LinuxSecretServiceProviderError::SessionUnavailable)?;
        #[cfg(test)]
        real_fixture::stage("bus-connect", true);
        let proxy = connection.with_proxy(SERVICE_NAME, SERVICE_PATH, DBUS_TIMEOUT);
        let input = Variant(String::new());
        #[cfg(test)]
        real_fixture::stage("session-open", false);
        let (Variant(output), session): (Variant<String>, dbus::Path<'static>) = proxy
            .method_call(SERVICE_INTERFACE, "OpenSession", ("plain", input))
            .map_err(|_| LinuxSecretServiceProviderError::SessionUnavailable)?;
        #[cfg(test)]
        real_fixture::stage("session-open", true);
        #[cfg(test)]
        real_fixture::stage("session-check", false);
        if !valid_plain_session(&output, &session.to_string()) {
            return Err(LinuxSecretServiceProviderError::SessionUnavailable);
        }
        #[cfg(test)]
        real_fixture::stage("session-check", true);
        Ok(Self {
            connection,
            session,
        })
    }

    fn classify(error: &dbus::Error) -> BackendError {
        #[cfg(test)]
        real_fixture::backend_error(error);
        match error.name() {
            Some("org.freedesktop.Secret.Error.IsLocked") => BackendError::Locked,
            Some("org.freedesktop.Secret.Error.NoSuchObject") => BackendError::Missing,
            Some("org.freedesktop.Secret.Error.NoSession") => BackendError::Unavailable,
            _ => BackendError::Unavailable,
        }
    }

    fn exact_attributes(&self, handle: &str) -> Result<Attributes, BackendError> {
        use dbus::blocking::stdintf::org_freedesktop_dbus::Properties;
        #[cfg(test)]
        real_fixture::stage("search-attributes", false);
        let proxy = self
            .connection
            .with_proxy(SERVICE_NAME, handle, DBUS_TIMEOUT);
        let attributes: HashMap<String, String> = proxy
            .get(ITEM_INTERFACE, "Attributes")
            .map_err(|error| Self::classify(&error))?;
        #[cfg(test)]
        real_fixture::stage("search-attributes", true);
        Ok(attributes.into_iter().collect())
    }
}

fn valid_plain_session(output: &str, session_path: &str) -> bool {
    output.is_empty() && session_path != ROOT_PATH
}

impl SecretServiceBackend for DbusBackend {
    fn search(&mut self, attributes: &Attributes) -> Result<Vec<ServiceItem>, BackendError> {
        #[cfg(test)]
        real_fixture::stage("search", false);
        let query: HashMap<&str, &str> = attributes
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()))
            .collect();
        let proxy = self
            .connection
            .with_proxy(SERVICE_NAME, SERVICE_PATH, DBUS_TIMEOUT);
        let (unlocked, locked): (Vec<dbus::Path<'static>>, Vec<dbus::Path<'static>>) = proxy
            .method_call(SERVICE_INTERFACE, "SearchItems", (query,))
            .map_err(|error| Self::classify(&error))?;
        #[cfg(test)]
        real_fixture::stage("search", true);
        let mut items = Vec::with_capacity(unlocked.len() + locked.len());
        for (paths, is_locked) in [(unlocked, false), (locked, true)] {
            for path in paths {
                let handle = path.to_string();
                items.push(ServiceItem {
                    attributes: self.exact_attributes(&handle)?,
                    handle,
                    locked: is_locked,
                });
            }
        }
        Ok(items)
    }

    fn create_item(&mut self, request: CreateRequest<'_>) -> Result<String, BackendError> {
        use dbus::arg::{PropMap, RefArg, Variant};
        let mut properties = PropMap::new();
        properties.insert(
            "org.freedesktop.Secret.Item.Label".to_owned(),
            Variant(Box::new(request.label.to_owned()) as Box<dyn RefArg>),
        );
        let attribute_map: HashMap<String, String> = request
            .attributes
            .iter()
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect();
        properties.insert(
            "org.freedesktop.Secret.Item.Attributes".to_owned(),
            Variant(Box::new(attribute_map) as Box<dyn RefArg>),
        );
        let secret = (
            self.session.clone(),
            Vec::<u8>::new(),
            request.secret.to_vec(),
            request.content_type.to_owned(),
        );
        let proxy = self
            .connection
            .with_proxy(SERVICE_NAME, DEFAULT_COLLECTION_PATH, DBUS_TIMEOUT);
        let (item, prompt): (dbus::Path<'static>, dbus::Path<'static>) = proxy
            .method_call(
                COLLECTION_INTERFACE,
                "CreateItem",
                (properties, secret, request.replace),
            )
            .map_err(|error| Self::classify(&error))?;
        if prompt.to_string() != ROOT_PATH || item.to_string() == ROOT_PATH {
            return Err(BackendError::PromptRequired);
        }
        Ok(item.to_string())
    }

    fn get_secret(&mut self, handle: &str) -> Result<Zeroizing<Vec<u8>>, BackendError> {
        let proxy = self
            .connection
            .with_proxy(SERVICE_NAME, handle, DBUS_TIMEOUT);
        let decoded: Result<((dbus::Path<'static>, Vec<u8>, Vec<u8>, String),), dbus::Error> =
            proxy.method_call(ITEM_INTERFACE, "GetSecret", (self.session.clone(),));
        let ((session, parameters, value, content_type),) = decoded.map_err(|error| {
            #[cfg(test)]
            real_fixture::decoded_result(false);
            Self::classify(&error)
        })?;
        // Own the decoded allocation before any metadata rejection. The guard
        // also stays alive through the caller's envelope and identity checks.
        let value = Zeroizing::new(value);
        #[cfg(test)]
        real_fixture::decoded_result(true);
        #[cfg(test)]
        real_fixture::returned_secret(session == self.session, &parameters, &content_type, &value);
        validate_secret_transfer(session == self.session, &parameters, &content_type, value)
    }

    fn delete_item(&mut self, handle: &str) -> Result<(), BackendError> {
        #[cfg(test)]
        a3_fixture::delete_send();
        let proxy = self
            .connection
            .with_proxy(SERVICE_NAME, handle, DBUS_TIMEOUT);
        let (prompt,): (dbus::Path<'static>,) = proxy
            .method_call(ITEM_INTERFACE, "Delete", ())
            .map_err(|error| Self::classify(&error))?;
        if prompt.to_string() != ROOT_PATH {
            return Err(BackendError::PromptRequired);
        }
        Ok(())
    }
}

fn validate_secret_transfer(
    session_matches: bool,
    parameters: &[u8],
    content_type: &str,
    value: Zeroizing<Vec<u8>>,
) -> Result<Zeroizing<Vec<u8>>, BackendError> {
    // Exact read compatibility only: no normalization, service-brand trust,
    // text decoding or NUL truncation. Writes retain canonical CONTENT_TYPE.
    if !session_matches
        || !parameters.is_empty()
        || !matches!(content_type, CONTENT_TYPE | "text/plain")
    {
        return Err(BackendError::Corrupt);
    }
    Ok(value)
}

#[cfg(test)]
#[path = "linux_secret_service_fixture.rs"]
mod real_fixture;

#[cfg(test)]
#[path = "linux_secret_service_a3_fixture.rs"]
pub(crate) mod a3_fixture;

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        collections::BTreeMap,
        fs,
        os::unix::fs::PermissionsExt,
        process::Command,
        sync::{Arc, Barrier},
        thread,
        time::Instant,
    };

    #[derive(Clone)]
    struct FakeItem {
        attributes: Attributes,
        secret: Vec<u8>,
        locked: bool,
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum Fault {
        SearchUnavailable,
        CreatePrompt,
        CreatePublishedThenUnavailable,
        GetLocked,
        DeletePrompt,
    }

    #[derive(Default)]
    struct FakeState {
        items: BTreeMap<String, FakeItem>,
        next: usize,
        searches: usize,
        creates: usize,
        gets: usize,
        deletes: usize,
        prompts_executed: usize,
        unlocks: usize,
        last_replace: Option<bool>,
        next_fault: Option<Fault>,
        replacement_after_delete: Option<FakeItem>,
        read_content_type: Option<String>,
    }

    #[derive(Clone, Default)]
    struct FakeBackend {
        state: Arc<Mutex<FakeState>>,
    }

    impl FakeBackend {
        fn fault(&self, fault: Fault) {
            self.state.lock().unwrap().next_fault = Some(fault);
        }

        fn take_fault(state: &mut FakeState, expected: Fault) -> bool {
            if state.next_fault == Some(expected) {
                state.next_fault = None;
                true
            } else {
                false
            }
        }
    }

    impl SecretServiceBackend for FakeBackend {
        fn search(&mut self, attributes: &Attributes) -> Result<Vec<ServiceItem>, BackendError> {
            let mut state = self.state.lock().unwrap();
            state.searches += 1;
            if Self::take_fault(&mut state, Fault::SearchUnavailable) {
                return Err(BackendError::Unavailable);
            }
            Ok(state
                .items
                .iter()
                .filter(|(_, item)| {
                    attributes.iter().all(|(name, value)| {
                        item.attributes
                            .get(name)
                            .is_some_and(|actual| actual == value)
                    })
                })
                .map(|(handle, item)| ServiceItem {
                    handle: handle.clone(),
                    attributes: item.attributes.clone(),
                    locked: item.locked,
                })
                .collect())
        }

        fn create_item(&mut self, request: CreateRequest<'_>) -> Result<String, BackendError> {
            let mut state = self.state.lock().unwrap();
            state.creates += 1;
            state.last_replace = Some(request.replace);
            assert_eq!(request.label, LABEL);
            assert_eq!(request.content_type, CONTENT_TYPE);
            if Self::take_fault(&mut state, Fault::CreatePrompt) {
                return Err(BackendError::PromptRequired);
            }
            state.next += 1;
            let handle = format!(
                "/org/freedesktop/secrets/collection/default/item{}",
                state.next
            );
            state.items.insert(
                handle.clone(),
                FakeItem {
                    attributes: request.attributes.clone(),
                    secret: request.secret.to_vec(),
                    locked: false,
                },
            );
            if Self::take_fault(&mut state, Fault::CreatePublishedThenUnavailable) {
                return Err(BackendError::Unavailable);
            }
            Ok(handle)
        }

        fn get_secret(&mut self, handle: &str) -> Result<Zeroizing<Vec<u8>>, BackendError> {
            let mut state = self.state.lock().unwrap();
            state.gets += 1;
            if Self::take_fault(&mut state, Fault::GetLocked) {
                return Err(BackendError::Locked);
            }
            let value = state
                .items
                .get(handle)
                .map(|item| Zeroizing::new(item.secret.clone()))
                .ok_or(BackendError::Missing)?;
            validate_secret_transfer(
                true,
                &[],
                state.read_content_type.as_deref().unwrap_or(CONTENT_TYPE),
                value,
            )
        }

        fn delete_item(&mut self, handle: &str) -> Result<(), BackendError> {
            let mut state = self.state.lock().unwrap();
            state.deletes += 1;
            if Self::take_fault(&mut state, Fault::DeletePrompt) {
                return Err(BackendError::PromptRequired);
            }
            if state.items.remove(handle).is_none() {
                return Err(BackendError::Missing);
            }
            if let Some(replacement) = state.replacement_after_delete.take() {
                state
                    .items
                    .insert(format!("{handle}-replacement"), replacement);
            }
            Ok(())
        }
    }

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; 16]).unwrap()
    }

    fn fixture() -> (tempfile::TempDir, SyntheticSecretServiceApproval) {
        let root = tempfile::tempdir().unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        let fresh = SyntheticSecretServiceApproval::for_test_fresh(root.path()).unwrap();
        drop(ReconciliationLock::open(fresh).unwrap());
        let approval = SyntheticSecretServiceApproval::for_test(root.path()).unwrap();
        (root, approval)
    }

    fn provider(backend: FakeBackend, root: &Path) -> ContractProvider<FakeBackend> {
        let approval = SyntheticSecretServiceApproval::for_test(root).unwrap();
        ContractProvider::new(backend, ReconciliationLock::open(approval).unwrap())
    }

    fn created(outcome: RecoveryKeyCreateOutcome) -> CreatedRecoveryKey {
        match outcome {
            RecoveryKeyCreateOutcome::Created(created) => created,
            other => panic!("expected Created, got {other:?}"),
        }
    }

    #[test]
    fn read_mime_is_exact_and_never_decodes_or_truncates_binary() {
        for mime in [CONTENT_TYPE, "text/plain"] {
            let binary = vec![0, 0xff, 0x80, b'a', 0, b'z'];
            let received =
                validate_secret_transfer(true, &[], mime, Zeroizing::new(binary.clone())).unwrap();
            assert!(received.as_slice() == binary.as_slice());
            for (matches, parameters) in [(false, vec![]), (true, vec![0]), (false, vec![1])] {
                assert!(matches!(
                    validate_secret_transfer(
                        matches,
                        &parameters,
                        mime,
                        Zeroizing::new(binary.clone())
                    ),
                    Err(BackendError::Corrupt)
                ));
            }
        }
        for mime in [
            "",
            "application/json",
            "application/Octet-Stream",
            "Text/Plain",
            "TEXT/PLAIN",
            "text/plain; charset=utf-8",
            "text/plain;charset=utf-8",
            "application/octet-stream; charset=binary",
            " text/plain",
            "text/plain ",
            "text/plain\n",
            "text/plain\0",
            "text/plain,application/octet-stream",
        ] {
            assert!(matches!(
                validate_secret_transfer(
                    true,
                    &[],
                    mime,
                    Zeroizing::new(vec![0x51; ENVELOPE_BYTES])
                ),
                Err(BackendError::Corrupt)
            ));
        }
    }

    fn rechecksum(bytes: &mut [u8], context: &[u8]) {
        let offset = bytes.len() - CHECKSUM_BYTES;
        let mut hasher = Sha256::new();
        hasher.update(context);
        hasher.update(&bytes[..offset]);
        bytes[offset..].copy_from_slice(&hasher.finalize());
    }

    #[test]
    fn both_read_types_preserve_envelope_format_checksum_and_identity_rejection() {
        for mime in [CONTENT_TYPE, "text/plain"] {
            let tx = transaction(0x61);
            let namespace = [0x62; LOCK_NAMESPACE_BYTES];
            let id = [0x63; ITEM_ID_BYTES];
            let key = BackupKey::from_bytes(Zeroizing::new([0; KEY_BYTES]));
            let original = encode_envelope(tx, 1001, namespace, id, &key);
            let transferred =
                |bytes| validate_secret_transfer(true, &[], mime, Zeroizing::new(bytes)).unwrap();
            let accepted = transferred(original.clone());
            match decode_envelope(tx, 1001, namespace, id, &accepted) {
                Envelope::Available(key) => assert!(key.expose().iter().all(|byte| *byte == 0)),
                _ => panic!("expected synthetic envelope acceptance"),
            }
            for length in [0, ENVELOPE_BYTES - 1, ENVELOPE_BYTES + 1] {
                let mut changed = original.clone();
                changed.resize(length, 0);
                assert!(matches!(
                    decode_envelope(tx, 1001, namespace, id, &transferred(changed)),
                    Envelope::Corrupt
                ));
            }
            for offset in [0, ENVELOPE_BYTES - 1] {
                let mut changed = original.clone();
                changed[offset] ^= 1;
                assert!(matches!(
                    decode_envelope(tx, 1001, namespace, id, &transferred(changed)),
                    Envelope::Corrupt
                ));
            }
            // Isolate magic rejection from checksum rejection: removing the
            // magic guard would accept this otherwise valid envelope.
            let mut wrong_magic = original.clone();
            wrong_magic[0] ^= 1;
            rechecksum(&mut wrong_magic, ENVELOPE_CONTEXT);
            assert!(matches!(
                decode_envelope(tx, 1001, namespace, id, &transferred(wrong_magic)),
                Envelope::Corrupt
            ));
            // Recompute a valid checksum to prove it is not a MAC or a substitute
            // for transaction/UID/namespace/item identity comparison.
            for offset in [16, 32, 36, 68] {
                let mut changed = original.clone();
                changed[offset] ^= 1;
                rechecksum(&mut changed, ENVELOPE_CONTEXT);
                assert!(matches!(
                    decode_envelope(tx, 1001, namespace, id, &transferred(changed)),
                    Envelope::WrongIdentity
                ));
            }
        }
    }

    #[test]
    fn both_read_types_preserve_provider_attribute_and_reference_checks() {
        for mime in [CONTENT_TYPE, "text/plain"] {
            let (root, _) = fixture();
            let backend = FakeBackend::default();
            let mut provider = provider(backend.clone(), root.path());
            let tx = transaction(0x64);
            let first = created(provider.create(tx)); // Fake create asserts canonical write MIME.
            backend.state.lock().unwrap().read_content_type = Some(mime.to_owned());
            match provider.open(tx, &first.reference) {
                RecoveryKeyOpenOutcome::Available(key) => {
                    assert!(verify_slices_are_equal(key.expose(), first.key.expose()).is_ok())
                }
                _ => panic!("expected synthetic provider acceptance"),
            }
            backend.state.lock().unwrap().read_content_type =
                Some("text/plain; charset=utf-8".to_owned());
            assert!(matches!(
                provider.open(tx, &first.reference),
                RecoveryKeyOpenOutcome::Corrupt
            ));
            backend.state.lock().unwrap().read_content_type = Some(mime.to_owned());
            let item = backend
                .state
                .lock()
                .unwrap()
                .items
                .values()
                .next()
                .unwrap()
                .clone();
            let service_item = ServiceItem {
                handle: backend
                    .state
                    .lock()
                    .unwrap()
                    .items
                    .keys()
                    .next()
                    .unwrap()
                    .clone(),
                attributes: item.attributes.clone(),
                locked: false,
            };
            let parsed_id: [u8; ITEM_ID_BYTES] =
                hex::decode(item.attributes.get("item-id").unwrap())
                    .unwrap()
                    .try_into()
                    .unwrap();
            for name in item.attributes.keys() {
                let mut altered = service_item.clone();
                altered.attributes.insert(name.clone(), "wrong".to_owned());
                assert!(!matches!(
                    verify_item(
                        &mut backend.clone(),
                        tx,
                        provider.uid,
                        provider.lock_namespace,
                        &altered,
                        Some(parsed_id)
                    ),
                    VerifiedItem::Available { .. }
                ));
                altered = service_item.clone();
                altered.attributes.remove(name);
                assert!(!matches!(
                    verify_item(
                        &mut backend.clone(),
                        tx,
                        provider.uid,
                        provider.lock_namespace,
                        &altered,
                        Some(parsed_id)
                    ),
                    VerifiedItem::Available { .. }
                ));
            }
            let mut extra = service_item.clone();
            extra
                .attributes
                .insert("unexpected".to_owned(), "value".to_owned());
            assert!(matches!(
                verify_item(
                    &mut backend.clone(),
                    tx,
                    provider.uid,
                    provider.lock_namespace,
                    &extra,
                    Some(parsed_id)
                ),
                VerifiedItem::Corrupt
            ));
            let mut wrong_id = parsed_id;
            wrong_id[0] ^= 1;
            assert!(matches!(
                verify_item(
                    &mut backend.clone(),
                    tx,
                    provider.uid,
                    provider.lock_namespace,
                    &service_item,
                    Some(wrong_id)
                ),
                VerifiedItem::WrongIdentity
            ));
            for offset in [0, 16, 32, 36, 68, 84, REFERENCE_BYTES - 1] {
                let mut changed = first.reference.opaque_reference().to_vec();
                changed[offset] ^= 1;
                // Identity and attribute-digest corruption must fail even with
                // a freshly recomputed unkeyed checksum.
                if (16..116).contains(&offset) {
                    rechecksum(&mut changed, REFERENCE_CONTEXT);
                }
                let reference =
                    RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, changed).unwrap();
                assert!(!matches!(
                    provider.open(tx, &reference),
                    RecoveryKeyOpenOutcome::Available(_)
                ));
            }
            for (kind, version) in [
                ("different-provider", PROVIDER_VERSION),
                (PROVIDER_KIND, PROVIDER_VERSION + 1),
            ] {
                let reference = RecoveryKeyReference::new(
                    kind,
                    version,
                    first.reference.opaque_reference().to_vec(),
                )
                .unwrap();
                assert!(matches!(
                    provider.open(tx, &reference),
                    RecoveryKeyOpenOutcome::WrongIdentity
                ));
            }
            // A valid checksum must not mask a missing reference-magic guard.
            // All other identity fields still select the original valid item.
            let mut wrong_magic = first.reference.opaque_reference().to_vec();
            wrong_magic[0] ^= 1;
            rechecksum(&mut wrong_magic, REFERENCE_CONTEXT);
            let reference =
                RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, wrong_magic).unwrap();
            assert!(matches!(
                provider.open(tx, &reference),
                RecoveryKeyOpenOutcome::Corrupt
            ));
            for length in [REFERENCE_BYTES - 1, REFERENCE_BYTES + 1] {
                let mut changed = first.reference.opaque_reference().to_vec();
                changed.resize(length, 0);
                let reference =
                    RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, changed).unwrap();
                assert!(matches!(
                    provider.open(tx, &reference),
                    RecoveryKeyOpenOutcome::Corrupt
                ));
            }
            let state = backend.state.lock().unwrap();
            assert_eq!(state.creates, 1);
            assert_eq!(state.deletes, 0);
        }
    }

    #[test]
    fn create_is_no_replace_idempotent_and_restart_reopens_exact_key() {
        let (root, approval) = fixture();
        let backend = FakeBackend::default();
        let shared = backend.clone();
        let mut first = ContractProvider::new(backend, ReconciliationLock::open(approval).unwrap());
        let tx = transaction(0x11);
        let created_first = created(first.create(tx));
        let key = *created_first.key.expose();
        let created_again = created(first.create(tx));
        assert_eq!(created_again.reference, created_first.reference);
        assert_eq!(created_again.key.expose(), &key);
        {
            let state = shared.state.lock().unwrap();
            assert_eq!(state.items.len(), 1);
            assert_eq!(state.creates, 1);
            assert_eq!(state.last_replace, Some(false));
            let attributes = &state.items.values().next().unwrap().attributes;
            assert_eq!(attributes.len(), 7);
            assert!(!attributes
                .values()
                .any(|value| value.contains(&hex::encode(key))));
        }
        let mut restarted = provider(shared.clone(), root.path());
        match restarted.open(tx, &created_first.reference) {
            RecoveryKeyOpenOutcome::Available(opened) => assert_eq!(opened.expose(), &key),
            other => panic!("expected Available, got {other:?}"),
        }
        assert_eq!(
            restarted.delete(tx, &created_first.reference),
            RecoveryKeyDeleteOutcome::Deleted
        );
        assert!(matches!(
            restarted.open(tx, &created_first.reference),
            RecoveryKeyOpenOutcome::Missing
        ));
    }

    #[test]
    fn create_retry_reconciles_unknown_post_publish_without_replacing() {
        let (_root, approval) = fixture();
        let backend = FakeBackend::default();
        let shared = backend.clone();
        backend.fault(Fault::CreatePublishedThenUnavailable);
        let mut first = ContractProvider::new(backend, ReconciliationLock::open(approval).unwrap());
        let tx = transaction(0x22);
        assert!(matches!(
            first.create(tx),
            RecoveryKeyCreateOutcome::Unavailable
        ));
        let recovered = created(first.create(tx));
        assert!(matches!(
            first.open(tx, &recovered.reference),
            RecoveryKeyOpenOutcome::Available(_)
        ));
        let state = shared.state.lock().unwrap();
        assert_eq!(state.creates, 1);
        assert_eq!(state.items.len(), 1);
    }

    #[test]
    fn unknown_post_publish_never_reprovisions_a_missing_or_empty_lock_record() {
        for truncate in [false, true] {
            let (root, approval) = fixture();
            let backend = FakeBackend::default();
            backend.fault(Fault::CreatePublishedThenUnavailable);
            let mut provider =
                ContractProvider::new(backend.clone(), ReconciliationLock::open(approval).unwrap());
            assert!(matches!(
                provider.create(transaction(if truncate { 0x24 } else { 0x23 })),
                RecoveryKeyCreateOutcome::Unavailable
            ));
            assert_eq!(backend.state.lock().unwrap().creates, 1);
            drop(provider);
            if truncate {
                fs::write(root.path().join(LOCK_FILE), b"").unwrap();
            } else {
                fs::remove_file(root.path().join(LOCK_FILE)).unwrap();
            }
            let restart_approval = SyntheticSecretServiceApproval::for_test(root.path()).unwrap();
            assert!(matches!(
                ReconciliationLock::open(restart_approval),
                Err(LinuxSecretServiceProviderError::UnsafeLock(_))
            ));
            assert_eq!(backend.state.lock().unwrap().creates, 1);
        }
    }

    #[test]
    fn duplicate_matches_fail_closed_without_create_overwrite_or_delete() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        let tx = transaction(0x33);
        let uid = unsafe { libc::geteuid() };
        let mut provider = provider(backend.clone(), root.path());
        let lock_namespace = provider.lock_namespace;
        let key = BackupKey::generate().unwrap();
        let ids = [[1_u8; ITEM_ID_BYTES], [2_u8; ITEM_ID_BYTES]];
        {
            let mut state = backend.state.lock().unwrap();
            for (index, item_id) in ids.into_iter().enumerate() {
                state.items.insert(
                    format!("/item/{index}"),
                    FakeItem {
                        attributes: full_attributes(tx, uid, lock_namespace, item_id),
                        secret: encode_envelope(tx, uid, lock_namespace, item_id, &key),
                        locked: false,
                    },
                );
            }
        }
        let reference = RecoveryKeyReference::new(
            PROVIDER_KIND,
            PROVIDER_VERSION,
            encode_reference(tx, uid, lock_namespace, ids[0]),
        )
        .unwrap();
        assert!(matches!(
            provider.create(tx),
            RecoveryKeyCreateOutcome::Unavailable
        ));
        assert!(matches!(
            provider.open(tx, &reference),
            RecoveryKeyOpenOutcome::Corrupt
        ));
        assert_eq!(
            provider.delete(tx, &reference),
            RecoveryKeyDeleteOutcome::Corrupt
        );
        let state = backend.state.lock().unwrap();
        assert_eq!(state.items.len(), 2);
        assert_eq!(state.creates, 0);
        assert_eq!(state.deletes, 0);
    }

    #[test]
    fn prompts_and_locked_items_never_unlock_or_execute_a_prompt() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        backend.fault(Fault::CreatePrompt);
        let mut provider = provider(backend.clone(), root.path());
        assert!(matches!(
            provider.create(transaction(0x44)),
            RecoveryKeyCreateOutcome::Locked
        ));
        {
            let state = backend.state.lock().unwrap();
            assert_eq!(state.prompts_executed, 0);
            assert_eq!(state.unlocks, 0);
            assert!(state.items.is_empty());
        }

        let tx = transaction(0x45);
        let created_key = created(provider.create(tx));
        backend
            .state
            .lock()
            .unwrap()
            .items
            .values_mut()
            .next()
            .unwrap()
            .locked = true;
        assert!(matches!(
            provider.open(tx, &created_key.reference),
            RecoveryKeyOpenOutcome::Locked
        ));
        assert_eq!(
            provider.delete(tx, &created_key.reference),
            RecoveryKeyDeleteOutcome::Locked
        );
        backend
            .state
            .lock()
            .unwrap()
            .items
            .values_mut()
            .next()
            .unwrap()
            .locked = false;
        backend.fault(Fault::GetLocked);
        assert!(matches!(
            provider.open(tx, &created_key.reference),
            RecoveryKeyOpenOutcome::Locked
        ));
    }

    #[test]
    fn missing_wrong_identity_corrupt_and_unavailable_never_create() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        let mut provider = provider(backend.clone(), root.path());
        let tx = transaction(0x55);
        let other = transaction(0x56);
        let created_key = created(provider.create(tx));
        let creates = backend.state.lock().unwrap().creates;
        assert!(matches!(
            provider.open(other, &created_key.reference),
            RecoveryKeyOpenOutcome::WrongIdentity
        ));
        let corrupt =
            RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, vec![1, 2, 3]).unwrap();
        assert!(matches!(
            provider.open(tx, &corrupt),
            RecoveryKeyOpenOutcome::Corrupt
        ));
        backend.state.lock().unwrap().items.clear();
        assert!(matches!(
            provider.open(tx, &created_key.reference),
            RecoveryKeyOpenOutcome::Missing
        ));
        backend.fault(Fault::SearchUnavailable);
        assert!(matches!(
            provider.open(tx, &created_key.reference),
            RecoveryKeyOpenOutcome::Unavailable
        ));
        assert_eq!(backend.state.lock().unwrap().creates, creates);
    }

    #[test]
    fn corrupt_secret_and_delete_prompt_preserve_the_item() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        let mut provider = provider(backend.clone(), root.path());
        let tx = transaction(0x66);
        let created_key = created(provider.create(tx));
        backend
            .state
            .lock()
            .unwrap()
            .items
            .values_mut()
            .next()
            .unwrap()
            .secret[0] ^= 0xff;
        assert!(matches!(
            provider.open(tx, &created_key.reference),
            RecoveryKeyOpenOutcome::Corrupt
        ));
        assert_eq!(
            provider.delete(tx, &created_key.reference),
            RecoveryKeyDeleteOutcome::Corrupt
        );
        assert_eq!(backend.state.lock().unwrap().deletes, 0);

        let tx2 = transaction(0x67);
        let created2 = created(provider.create(tx2));
        backend.fault(Fault::DeletePrompt);
        assert_eq!(
            provider.delete(tx2, &created2.reference),
            RecoveryKeyDeleteOutcome::Locked
        );
        assert_eq!(backend.state.lock().unwrap().items.len(), 2);
    }

    #[test]
    fn delete_revalidation_preserves_a_raced_replacement_item() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        let mut provider = provider(backend.clone(), root.path());
        let tx = transaction(0x68);
        let created = created(provider.create(tx));
        let uid = unsafe { libc::geteuid() };
        let lock_namespace = provider.lock_namespace;
        let replacement_id = [0xa5; ITEM_ID_BYTES];
        let replacement_key = BackupKey::generate().unwrap();
        backend.state.lock().unwrap().replacement_after_delete = Some(FakeItem {
            attributes: full_attributes(tx, uid, lock_namespace, replacement_id),
            secret: encode_envelope(tx, uid, lock_namespace, replacement_id, &replacement_key),
            locked: false,
        });
        assert_eq!(
            provider.delete(tx, &created.reference),
            RecoveryKeyDeleteOutcome::WrongIdentity
        );
        let state = backend.state.lock().unwrap();
        assert_eq!(state.items.len(), 1);
        assert_eq!(state.deletes, 1);
    }

    #[test]
    fn debug_output_never_contains_reference_or_key_material() {
        let (root, _) = fixture();
        let backend = FakeBackend::default();
        let mut provider = provider(backend, root.path());
        let created = created(provider.create(transaction(0x77)));
        let rendered_reference = format!("{:?}", created.reference);
        let rendered_created = format!("{:?}", created);
        assert!(!rendered_reference.contains(&hex::encode(created.reference.opaque_reference())));
        assert!(!rendered_created.contains(&hex::encode(created.key.expose())));
        assert!(rendered_reference.contains("[REDACTED]"));
        assert!(rendered_created.contains("[REDACTED]"));
    }

    #[test]
    fn lock_root_and_lock_file_must_remain_owner_private() {
        let root = tempfile::tempdir().unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o755)).unwrap();
        assert!(matches!(
            SyntheticSecretServiceApproval::for_test(root.path()),
            Err(LinuxSecretServiceProviderError::InvalidLockRoot(_))
        ));

        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        let approval = SyntheticSecretServiceApproval::for_test_fresh(root.path()).unwrap();
        let lock = ReconciliationLock::open(approval).unwrap();
        fs::set_permissions(
            root.path().join(LOCK_FILE),
            fs::Permissions::from_mode(0o644),
        )
        .unwrap();
        assert!(lock.exclusive().is_err());
    }

    #[test]
    fn lock_namespace_persists_and_distinct_roots_do_not_alias() {
        let (first_root, first_approval) = fixture();
        let first_lock = ReconciliationLock::open(first_approval).unwrap();
        let first_namespace = first_lock.namespace;
        drop(first_lock);

        let reopened_approval =
            SyntheticSecretServiceApproval::for_test(first_root.path()).unwrap();
        let reopened = ReconciliationLock::open(reopened_approval).unwrap();
        assert_eq!(reopened.namespace, first_namespace);

        let (_second_root, second_approval) = fixture();
        let second = ReconciliationLock::open(second_approval).unwrap();
        assert_ne!(second.namespace, first_namespace);

        let copied_root = tempfile::tempdir().unwrap();
        fs::set_permissions(copied_root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        fs::copy(
            first_root.path().join(LOCK_FILE),
            copied_root.path().join(LOCK_FILE),
        )
        .unwrap();
        fs::set_permissions(
            copied_root.path().join(LOCK_FILE),
            fs::Permissions::from_mode(0o600),
        )
        .unwrap();
        let copied_approval = SyntheticSecretServiceApproval::for_test(copied_root.path()).unwrap();
        assert!(matches!(
            ReconciliationLock::open(copied_approval),
            Err(LinuxSecretServiceProviderError::UnsafeLock(_))
        ));

        let mut corrupted = fs::read(first_root.path().join(LOCK_FILE)).unwrap();
        corrupted[40] ^= 0xff;
        fs::write(first_root.path().join(LOCK_FILE), corrupted).unwrap();
        let corrupted_approval =
            SyntheticSecretServiceApproval::for_test(first_root.path()).unwrap();
        assert!(matches!(
            ReconciliationLock::open(corrupted_approval),
            Err(LinuxSecretServiceProviderError::UnsafeLock(_))
        ));
    }

    #[test]
    fn replacing_the_lock_directory_entry_invalidates_the_held_lock() {
        let (root, approval) = fixture();
        let original = ReconciliationLock::open(approval).unwrap();
        let original_namespace = original.namespace;
        fs::rename(
            root.path().join(LOCK_FILE),
            root.path().join("displaced-lock"),
        )
        .unwrap();
        let replacement_approval = SyntheticSecretServiceApproval::for_test(root.path()).unwrap();
        assert!(matches!(
            ReconciliationLock::open(replacement_approval),
            Err(LinuxSecretServiceProviderError::UnsafeLock(_))
        ));
        assert_ne!(original_namespace, [0_u8; LOCK_NAMESPACE_BYTES]);
        assert!(matches!(
            original.exclusive(),
            Err(LinuxSecretServiceProviderError::UnsafeLock(_))
        ));
    }

    #[test]
    fn plain_session_requires_exact_empty_output_and_non_root_path() {
        assert!(valid_plain_session(
            "",
            "/org/freedesktop/secrets/session/fixture"
        ));
        assert!(!valid_plain_session(
            "unexpected",
            "/org/freedesktop/secrets/session/fixture"
        ));
        assert!(!valid_plain_session("", ROOT_PATH));
    }

    struct BlockingBackend {
        inner: FakeBackend,
        entered: Arc<Barrier>,
        release: Arc<Barrier>,
        blocked: bool,
    }

    impl SecretServiceBackend for BlockingBackend {
        fn search(&mut self, attributes: &Attributes) -> Result<Vec<ServiceItem>, BackendError> {
            self.inner.search(attributes)
        }

        fn create_item(&mut self, request: CreateRequest<'_>) -> Result<String, BackendError> {
            if !self.blocked {
                self.blocked = true;
                self.entered.wait();
                self.release.wait();
            }
            self.inner.create_item(request)
        }

        fn get_secret(&mut self, handle: &str) -> Result<Zeroizing<Vec<u8>>, BackendError> {
            self.inner.get_secret(handle)
        }

        fn delete_item(&mut self, handle: &str) -> Result<(), BackendError> {
            self.inner.delete_item(handle)
        }
    }

    #[test]
    fn separate_providers_serialize_reconciliation_at_the_file_lock() {
        let (root, _) = fixture();
        let shared = FakeBackend::default();
        let entered = Arc::new(Barrier::new(2));
        let release = Arc::new(Barrier::new(2));
        let first_backend = BlockingBackend {
            inner: shared.clone(),
            entered: entered.clone(),
            release: release.clone(),
            blocked: false,
        };
        let first_approval = SyntheticSecretServiceApproval::for_test(root.path()).unwrap();
        let mut first = ContractProvider::new(
            first_backend,
            ReconciliationLock::open(first_approval).unwrap(),
        );
        let second_approval = SyntheticSecretServiceApproval::for_test(root.path()).unwrap();
        let mut second = ContractProvider::new(
            shared.clone(),
            ReconciliationLock::open(second_approval).unwrap(),
        );
        let tx = transaction(0x7a);
        let first_thread = thread::spawn(move || first.create(tx));
        entered.wait();
        let second_thread = thread::spawn(move || second.create(tx));
        thread::sleep(Duration::from_millis(100));
        assert_eq!(shared.state.lock().unwrap().searches, 1);
        release.wait();
        let a = created(first_thread.join().unwrap());
        let b = created(second_thread.join().unwrap());
        assert_eq!(a.reference, b.reference);
        assert_eq!(a.key.expose(), b.key.expose());
        let state = shared.state.lock().unwrap();
        assert_eq!(state.creates, 1);
        assert_eq!(state.items.len(), 1);
    }

    #[test]
    fn distinct_lock_roots_are_distinct_persistent_provider_namespaces() {
        let (first_root, first_approval) = fixture();
        let (second_root, second_approval) = fixture();
        let shared = FakeBackend::default();
        let entered = Arc::new(Barrier::new(3));
        let release = Arc::new(Barrier::new(3));
        let first_backend = BlockingBackend {
            inner: shared.clone(),
            entered: entered.clone(),
            release: release.clone(),
            blocked: false,
        };
        let second_backend = BlockingBackend {
            inner: shared.clone(),
            entered: entered.clone(),
            release: release.clone(),
            blocked: false,
        };
        let mut first = ContractProvider::new(
            first_backend,
            ReconciliationLock::open(first_approval).unwrap(),
        );
        let mut second = ContractProvider::new(
            second_backend,
            ReconciliationLock::open(second_approval).unwrap(),
        );
        assert_ne!(first.lock_namespace, second.lock_namespace);
        let tx = transaction(0x7b);
        let first_thread = thread::spawn(move || {
            let outcome = first.create(tx);
            (outcome, first)
        });
        let second_thread = thread::spawn(move || {
            let outcome = second.create(tx);
            (outcome, second)
        });
        entered.wait();
        release.wait();
        let (first_outcome, mut first) = first_thread.join().unwrap();
        let (second_outcome, mut second) = second_thread.join().unwrap();
        let first_created = created(first_outcome);
        let second_created = created(second_outcome);
        assert_ne!(first_created.reference, second_created.reference);
        assert!(matches!(
            first.open(tx, &second_created.reference),
            RecoveryKeyOpenOutcome::WrongIdentity
        ));
        assert!(matches!(
            second.open(tx, &first_created.reference),
            RecoveryKeyOpenOutcome::WrongIdentity
        ));
        let first_retry = created(first.create(tx));
        let second_retry = created(second.create(tx));
        assert_eq!(first_retry.reference, first_created.reference);
        assert_eq!(second_retry.reference, second_created.reference);
        let state = shared.state.lock().unwrap();
        assert_eq!(state.items.len(), 2);
        let namespaces: std::collections::BTreeSet<_> = state
            .items
            .values()
            .map(|item| item.attributes["lock-namespace"].clone())
            .collect();
        assert_eq!(namespaces.len(), 2);
        drop((first_root, second_root));
    }

    #[test]
    fn cross_process_lock_child() {
        let Some(root) = std::env::var_os("CPA_SS_LOCK_CHILD_ROOT") else {
            return;
        };
        let attempted = std::env::var_os("CPA_SS_LOCK_CHILD_ATTEMPTED").unwrap();
        let acquired = std::env::var_os("CPA_SS_LOCK_CHILD_ACQUIRED").unwrap();
        let approval = SyntheticSecretServiceApproval::for_test(Path::new(&root)).unwrap();
        let name = CString::new(LOCK_FILE).unwrap();
        let probe_fd = unsafe {
            libc::openat(
                approval.directory.as_raw_fd(),
                name.as_ptr(),
                libc::O_RDWR | libc::O_CLOEXEC | libc::O_NOFOLLOW,
            )
        };
        assert!(probe_fd >= 0);
        let probe = unsafe { File::from_raw_fd(probe_fd) };
        assert_eq!(
            unsafe { libc::flock(probe.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) },
            -1
        );
        let contention = io::Error::last_os_error().raw_os_error();
        assert!(contention == Some(libc::EWOULDBLOCK) || contention == Some(libc::EAGAIN));
        fs::write(attempted, b"contended").unwrap();
        drop(probe);
        let lock = ReconciliationLock::open(approval).unwrap();
        let _guard = lock.exclusive().unwrap();
        fs::write(acquired, b"acquired").unwrap();
    }

    #[test]
    fn reconciliation_lock_blocks_a_fresh_process() {
        let (root, approval) = fixture();
        let lock = ReconciliationLock::open(approval).unwrap();
        let guard = lock.exclusive().unwrap();
        let attempted = root.path().join("child-attempted");
        let acquired = root.path().join("child-acquired");
        let mut child = Command::new(std::env::current_exe().unwrap())
            .arg("--exact")
            .arg("live::linux_secret_service::tests::cross_process_lock_child")
            .arg("--nocapture")
            .env("CPA_SS_LOCK_CHILD_ROOT", root.path())
            .env("CPA_SS_LOCK_CHILD_ATTEMPTED", &attempted)
            .env("CPA_SS_LOCK_CHILD_ACQUIRED", &acquired)
            .spawn()
            .unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        while !attempted.exists() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(fs::read(&attempted).unwrap(), b"contended");
        assert!(!acquired.exists());
        drop(guard);
        assert!(child.wait().unwrap().success());
        assert_eq!(fs::read(acquired).unwrap(), b"acquired");
    }
}
