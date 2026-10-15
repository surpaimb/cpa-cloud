//! Pure recovery-key custody interface for the replacement transaction.
//!
//! No operating-system provider is implemented here. In particular, this
//! module does not read environment variables or files and never turns an
//! `open` failure into key creation. Platform providers remain separately
//! gated before any real replacement transaction can use this interface.

use super::backup::BackupKey;

const TRANSACTION_ID_BYTES: usize = 16;
const MAX_PROVIDER_KIND_BYTES: usize = 64;
const MAX_OPAQUE_REFERENCE_BYTES: usize = 4 * 1024;

#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct ReplacementTransactionId([u8; TRANSACTION_ID_BYTES]);

impl ReplacementTransactionId {
    pub fn from_bytes(bytes: [u8; TRANSACTION_ID_BYTES]) -> Result<Self, RecoveryKeyMetadataError> {
        if bytes.iter().all(|byte| *byte == 0) {
            return Err(RecoveryKeyMetadataError::InvalidTransactionId);
        }
        Ok(Self(bytes))
    }

    pub fn as_bytes(&self) -> &[u8; TRANSACTION_ID_BYTES] {
        &self.0
    }
}

impl std::fmt::Debug for ReplacementTransactionId {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_tuple("ReplacementTransactionId")
            .field(&hex::encode(self.0))
            .finish()
    }
}

#[derive(Clone, PartialEq, Eq)]
pub struct RecoveryKeyReference {
    provider_kind: String,
    provider_version: u32,
    opaque_reference: Vec<u8>,
}

impl RecoveryKeyReference {
    pub fn new(
        provider_kind: impl Into<String>,
        provider_version: u32,
        opaque_reference: Vec<u8>,
    ) -> Result<Self, RecoveryKeyMetadataError> {
        let provider_kind = provider_kind.into();
        let kind_bytes = provider_kind.as_bytes();
        let valid_kind = !provider_kind.is_empty()
            && provider_kind.len() <= MAX_PROVIDER_KIND_BYTES
            && kind_bytes.first().is_some_and(u8::is_ascii_alphanumeric)
            && kind_bytes.last().is_some_and(u8::is_ascii_alphanumeric)
            && provider_kind
                .bytes()
                .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-');
        if !valid_kind || provider_version == 0 {
            return Err(RecoveryKeyMetadataError::InvalidProvider);
        }
        if opaque_reference.is_empty() || opaque_reference.len() > MAX_OPAQUE_REFERENCE_BYTES {
            return Err(RecoveryKeyMetadataError::InvalidOpaqueReference);
        }
        Ok(Self {
            provider_kind,
            provider_version,
            opaque_reference,
        })
    }

    pub fn provider_kind(&self) -> &str {
        &self.provider_kind
    }

    pub fn provider_version(&self) -> u32 {
        self.provider_version
    }

    /// Opaque provider state intended only for the selected provider.
    ///
    /// It is defined as non-secret by the replacement contract but remains
    /// redacted from `Debug` so receipts and diagnostics cannot expose it.
    pub fn opaque_reference(&self) -> &[u8] {
        &self.opaque_reference
    }
}

impl std::fmt::Debug for RecoveryKeyReference {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("RecoveryKeyReference")
            .field("provider_kind", &self.provider_kind)
            .field("provider_version", &self.provider_version)
            .field("opaque_reference", &"[REDACTED]")
            .finish()
    }
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum RecoveryKeyMetadataError {
    #[error("replacement transaction identifier is invalid")]
    InvalidTransactionId,
    #[error("recovery-key provider kind or version is invalid")]
    InvalidProvider,
    #[error("recovery-key opaque reference is invalid")]
    InvalidOpaqueReference,
}

pub struct CreatedRecoveryKey {
    pub reference: RecoveryKeyReference,
    pub key: BackupKey,
}

impl std::fmt::Debug for CreatedRecoveryKey {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CreatedRecoveryKey")
            .field("reference", &self.reference)
            .field("key", &"[REDACTED]")
            .finish()
    }
}

#[derive(Debug)]
pub enum RecoveryKeyCreateOutcome {
    Created(CreatedRecoveryKey),
    Locked,
    Unavailable,
}

pub enum RecoveryKeyOpenOutcome {
    Available(BackupKey),
    Locked,
    Missing,
    WrongIdentity,
    Corrupt,
    Unavailable,
}

impl std::fmt::Debug for RecoveryKeyOpenOutcome {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Available(_) => formatter.write_str("Available([REDACTED])"),
            Self::Locked => formatter.write_str("Locked"),
            Self::Missing => formatter.write_str("Missing"),
            Self::WrongIdentity => formatter.write_str("WrongIdentity"),
            Self::Corrupt => formatter.write_str("Corrupt"),
            Self::Unavailable => formatter.write_str("Unavailable"),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RecoveryKeyDeleteOutcome {
    Deleted,
    Missing,
    Locked,
    WrongIdentity,
    Corrupt,
    Unavailable,
}

/// Platform implementations must never generate a key from `open`.
///
/// `create` is used only before the first encrypted backup. Recovery calls
/// `open` with the exact durable reference, and terminal cleanup calls `delete`
/// only after the workspace has been durably removed.
pub trait RecoveryKeyProvider {
    fn create(&mut self, transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome;

    fn open(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome;

    fn delete(
        &mut self,
        transaction_id: ReplacementTransactionId,
        reference: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome;
}

#[derive(Debug, Clone, Copy, thiserror::Error, PartialEq, Eq)]
pub enum RecoveryKeyBlocked {
    #[error("recovery-key provider is locked")]
    Locked,
    #[error("recovery key is missing")]
    Missing,
    #[error("recovery key belongs to a different identity")]
    WrongIdentity,
    #[error("recovery-key metadata or protected value is corrupt")]
    Corrupt,
    #[error("recovery-key provider is unavailable")]
    Unavailable,
}

/// Opens exactly one existing recovery key and maps every non-available
/// outcome to a blocking error. This function has no key-creation path.
pub fn open_existing_recovery_key<P: RecoveryKeyProvider + ?Sized>(
    provider: &mut P,
    transaction_id: ReplacementTransactionId,
    reference: &RecoveryKeyReference,
) -> Result<BackupKey, RecoveryKeyBlocked> {
    match provider.open(transaction_id, reference) {
        RecoveryKeyOpenOutcome::Available(key) => Ok(key),
        RecoveryKeyOpenOutcome::Locked => Err(RecoveryKeyBlocked::Locked),
        RecoveryKeyOpenOutcome::Missing => Err(RecoveryKeyBlocked::Missing),
        RecoveryKeyOpenOutcome::WrongIdentity => Err(RecoveryKeyBlocked::WrongIdentity),
        RecoveryKeyOpenOutcome::Corrupt => Err(RecoveryKeyBlocked::Corrupt),
        RecoveryKeyOpenOutcome::Unavailable => Err(RecoveryKeyBlocked::Unavailable),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct FakeProvider {
        create_calls: usize,
        open_calls: usize,
        delete_calls: usize,
        next_open: Option<RecoveryKeyOpenOutcome>,
    }

    impl FakeProvider {
        fn opening(outcome: RecoveryKeyOpenOutcome) -> Self {
            Self {
                create_calls: 0,
                open_calls: 0,
                delete_calls: 0,
                next_open: Some(outcome),
            }
        }
    }

    impl RecoveryKeyProvider for FakeProvider {
        fn create(
            &mut self,
            _transaction_id: ReplacementTransactionId,
        ) -> RecoveryKeyCreateOutcome {
            self.create_calls += 1;
            RecoveryKeyCreateOutcome::Unavailable
        }

        fn open(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            self.open_calls += 1;
            self.next_open.take().unwrap()
        }

        fn delete(
            &mut self,
            _transaction_id: ReplacementTransactionId,
            _reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            self.delete_calls += 1;
            RecoveryKeyDeleteOutcome::Unavailable
        }
    }

    fn transaction_id() -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([0x11; TRANSACTION_ID_BYTES]).unwrap()
    }

    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-sentinel".to_vec()).unwrap()
    }

    #[test]
    fn open_existing_key_never_falls_back_to_creation_or_deletion() {
        let mut provider = FakeProvider::opening(RecoveryKeyOpenOutcome::Missing);
        let error = open_existing_recovery_key(&mut provider, transaction_id(), &reference())
            .expect_err("missing key must block recovery");

        assert_eq!(error, RecoveryKeyBlocked::Missing);
        assert_eq!(provider.open_calls, 1);
        assert_eq!(provider.create_calls, 0);
        assert_eq!(provider.delete_calls, 0);
    }

    #[test]
    fn every_non_available_open_outcome_is_a_distinct_blocker() {
        let cases = [
            (RecoveryKeyOpenOutcome::Locked, RecoveryKeyBlocked::Locked),
            (RecoveryKeyOpenOutcome::Missing, RecoveryKeyBlocked::Missing),
            (
                RecoveryKeyOpenOutcome::WrongIdentity,
                RecoveryKeyBlocked::WrongIdentity,
            ),
            (RecoveryKeyOpenOutcome::Corrupt, RecoveryKeyBlocked::Corrupt),
            (
                RecoveryKeyOpenOutcome::Unavailable,
                RecoveryKeyBlocked::Unavailable,
            ),
        ];

        for (outcome, expected) in cases {
            let mut provider = FakeProvider::opening(outcome);
            assert_eq!(
                open_existing_recovery_key(&mut provider, transaction_id(), &reference())
                    .unwrap_err(),
                expected
            );
            assert_eq!(provider.open_calls, 1);
            assert_eq!(provider.create_calls, 0);
        }
    }

    #[test]
    fn available_key_and_debug_output_remain_redacted() {
        let mut provider = FakeProvider::opening(RecoveryKeyOpenOutcome::Available(
            BackupKey::from_bytes([0x7a; 32]),
        ));
        let key = open_existing_recovery_key(&mut provider, transaction_id(), &reference())
            .expect("available key should be returned");
        assert_eq!(format!("{key:?}"), "BackupKey([REDACTED])");

        let opened = RecoveryKeyOpenOutcome::Available(BackupKey::from_bytes([0x7b; 32]));
        assert_eq!(format!("{opened:?}"), "Available([REDACTED])");

        let created = CreatedRecoveryKey {
            reference: reference(),
            key: BackupKey::from_bytes([0x7c; 32]),
        };
        let rendered = format!("{created:?}");
        assert!(rendered.contains("test-provider"));
        assert!(!rendered.contains("opaque-sentinel"));
        assert!(!rendered.contains("7c"));
    }

    #[test]
    fn provider_metadata_is_bounded_and_debug_redacts_opaque_state() {
        assert!(matches!(
            ReplacementTransactionId::from_bytes([0; TRANSACTION_ID_BYTES]),
            Err(RecoveryKeyMetadataError::InvalidTransactionId)
        ));
        assert!(matches!(
            RecoveryKeyReference::new("DPAPI", 1, vec![1]),
            Err(RecoveryKeyMetadataError::InvalidProvider)
        ));
        assert!(matches!(
            RecoveryKeyReference::new("-test-provider", 1, vec![1]),
            Err(RecoveryKeyMetadataError::InvalidProvider)
        ));
        assert!(matches!(
            RecoveryKeyReference::new("windows-dpapi-user", 0, vec![1]),
            Err(RecoveryKeyMetadataError::InvalidProvider)
        ));
        assert!(matches!(
            RecoveryKeyReference::new("windows-dpapi-user", 1, Vec::new()),
            Err(RecoveryKeyMetadataError::InvalidOpaqueReference)
        ));
        assert!(matches!(
            RecoveryKeyReference::new(
                "windows-dpapi-user",
                1,
                vec![0; MAX_OPAQUE_REFERENCE_BYTES + 1]
            ),
            Err(RecoveryKeyMetadataError::InvalidOpaqueReference)
        ));

        let reference = reference();
        let rendered = format!("{reference:?}");
        assert!(rendered.contains("test-provider"));
        assert!(!rendered.contains("opaque-sentinel"));
        assert_eq!(reference.opaque_reference(), b"opaque-sentinel");
    }
}
