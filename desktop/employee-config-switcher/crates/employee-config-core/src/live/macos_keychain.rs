//! Fail-closed macOS Keychain boundary and independently testable SecItem contract.
//!
//! The application does not yet have a fixed macOS signing team, bundle
//! entitlement, or dedicated Keychain access group. Consequently the public
//! provider constructor deliberately refuses to create a native provider. This
//! matters because an unauthorized Keychain query can look the same as a
//! missing item. Returning `Missing` before verifying the process entitlement
//! would therefore make recovery unsafe.
//!
//! The test-only contract below fixes the intended native request shape and
//! state machine without claiming Data Protection Keychain validation. It is
//! not a file-Keychain fallback and never touches a real Keychain. Real
//! `SecItem` wiring remains gated on a signed test bundle and native lifecycle
//! evidence. The Tauri apply command remains fail-closed.

use super::recovery_key::{
    RecoveryKeyCreateOutcome, RecoveryKeyDeleteOutcome, RecoveryKeyOpenOutcome,
    RecoveryKeyProvider, RecoveryKeyReference, ReplacementTransactionId,
};

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum MacosKeychainProviderError {
    #[error("macOS Data Protection Keychain custody is unsupported on this platform")]
    UnsupportedPlatform,
    #[error("macOS signing and Keychain access-group identity are not verified")]
    NativeIdentityUnverified,
}

/// Production remains unavailable until signing and access-group identity can
/// be verified before any Keychain query.
pub struct MacosKeychainRecoveryKeyProvider {
    _sealed: (),
}

impl MacosKeychainRecoveryKeyProvider {
    pub fn current_user() -> Result<Self, MacosKeychainProviderError> {
        #[cfg(target_os = "macos")]
        {
            Err(MacosKeychainProviderError::NativeIdentityUnverified)
        }

        #[cfg(not(target_os = "macos"))]
        {
            Err(MacosKeychainProviderError::UnsupportedPlatform)
        }
    }
}

impl RecoveryKeyProvider for MacosKeychainRecoveryKeyProvider {
    fn create(&mut self, _transaction_id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
        RecoveryKeyCreateOutcome::Unavailable
    }

    fn open(
        &mut self,
        _transaction_id: ReplacementTransactionId,
        _reference: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        RecoveryKeyOpenOutcome::Unavailable
    }

    fn delete(
        &mut self,
        _transaction_id: ReplacementTransactionId,
        _reference: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome {
        RecoveryKeyDeleteOutcome::Unavailable
    }
}

#[cfg(test)]
mod contract_tests {
    use super::*;
    use crate::live::{
        backup::BackupKey,
        recovery_key::CreatedRecoveryKey,
    };
    use ring::{
        constant_time::verify_slices_are_equal,
        rand::{SecureRandom, SystemRandom},
    };
    use sha2::{Digest, Sha256};
    use std::collections::BTreeMap;
    use zeroize::Zeroizing;

    const PROVIDER_KIND: &str = "macos-keychain-user";
    const PROVIDER_VERSION: u32 = 1;
    const SERVICE: &str = "com.surpaimb.cpa-cloud.employee-config.recovery-key.v1";
    const ACCESS_GROUP: &str = "TESTTEAM.com.surpaimb.cpa-cloud.employee-recovery-key.v1";
    const MAGIC_BYTES: usize = 16;
    const TRANSACTION_BYTES: usize = 16;
    const ITEM_ID_BYTES: usize = 16;
    const COMMITMENT_BYTES: usize = 32;
    const KEY_BYTES: usize = 32;
    const CHECKSUM_BYTES: usize = 32;
    const MAX_PERSISTENT_REFERENCE_BYTES: usize = 1024;
    const ENVELOPE_MAGIC: &[u8; MAGIC_BYTES] = b"CPA-MAC-KEY--V1!";
    const REFERENCE_MAGIC: &[u8; MAGIC_BYTES] = b"CPA-MAC-REF--V1!";
    const ENVELOPE_CONTEXT: &[u8] = b"cpa-cloud/macos-keychain-envelope/v1\0";
    const REFERENCE_CONTEXT: &[u8] = b"cpa-cloud/macos-keychain-reference/v1\0";
    const ACCESS_GROUP_CONTEXT: &[u8] = b"cpa-cloud/macos-keychain-access-group/v1\0";
    const ENVELOPE_BYTES: usize = MAGIC_BYTES
        + TRANSACTION_BYTES
        + ITEM_ID_BYTES
        + COMMITMENT_BYTES
        + COMMITMENT_BYTES
        + KEY_BYTES
        + CHECKSUM_BYTES;
    const REFERENCE_FIXED_BYTES: usize = MAGIC_BYTES
        + TRANSACTION_BYTES
        + ITEM_ID_BYTES
        + COMMITMENT_BYTES
        + COMMITMENT_BYTES
        + 2
        + CHECKSUM_BYTES;

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum Accessibility {
        WhenUnlockedThisDeviceOnly,
    }

    #[derive(Clone, Debug, PartialEq, Eq)]
    struct SecItemPolicy {
        data_protection_keychain: bool,
        synchronizable: bool,
        access_group: String,
        accessibility: Accessibility,
        authentication_context_interaction_not_allowed: bool,
    }

    impl SecItemPolicy {
        fn fixed() -> Self {
            Self {
                data_protection_keychain: true,
                synchronizable: false,
                access_group: ACCESS_GROUP.to_owned(),
                accessibility: Accessibility::WhenUnlockedThisDeviceOnly,
                authentication_context_interaction_not_allowed: true,
            }
        }
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum MatchLimit {
        All,
    }

    #[derive(Clone, Debug, PartialEq, Eq)]
    struct PrimaryQuery {
        policy: SecItemPolicy,
        service: String,
        account: String,
        match_limit: MatchLimit,
        return_attributes: bool,
        return_data: bool,
        return_persistent_reference: bool,
    }

    #[derive(Clone, PartialEq, Eq)]
    struct ExactQuery {
        policy: SecItemPolicy,
        persistent_reference: Vec<u8>,
        return_attributes: bool,
        return_data: bool,
        return_persistent_reference: bool,
    }

    impl std::fmt::Debug for ExactQuery {
        fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter
                .debug_struct("ExactQuery")
                .field("policy", &self.policy)
                .field("persistent_reference", &"[REDACTED]")
                .field("return_attributes", &self.return_attributes)
                .field("return_data", &self.return_data)
                .field(
                    "return_persistent_reference",
                    &self.return_persistent_reference,
                )
                .finish()
        }
    }

    #[derive(Clone)]
    struct SecItemRecord {
        policy: SecItemPolicy,
        service: String,
        account: String,
        generic_metadata: Vec<u8>,
        value_data: Zeroizing<Vec<u8>>,
        persistent_reference: Vec<u8>,
    }

    impl std::fmt::Debug for SecItemRecord {
        fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter
                .debug_struct("SecItemRecord")
                .field("policy", &self.policy)
                .field("service", &self.service)
                .field("account", &self.account)
                .field("generic_metadata", &"[REDACTED]")
                .field("value_data", &"[REDACTED]")
                .field("persistent_reference", &"[REDACTED]")
                .finish()
        }
    }

    #[derive(Clone)]
    struct AddRequest {
        record: SecItemRecord,
        return_persistent_reference: bool,
    }

    impl std::fmt::Debug for AddRequest {
        fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter
                .debug_struct("AddRequest")
                .field("record", &self.record)
                .field(
                    "return_persistent_reference",
                    &self.return_persistent_reference,
                )
                .finish()
        }
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum SecItemStatus {
        DuplicateItem,
        ItemNotFound,
        InteractionNotAllowed,
        InteractionRequired,
        NotLoggedIn,
        AuthFailed,
        Decode,
        MissingEntitlement,
        Internal,
    }

    trait SecItemBackend {
        fn add(&mut self, request: AddRequest) -> Result<Vec<u8>, SecItemStatus>;
        fn read_exact(&mut self, query: &ExactQuery) -> Result<SecItemRecord, SecItemStatus>;
        fn read_primary(
            &mut self,
            query: &PrimaryQuery,
        ) -> Result<Vec<SecItemRecord>, SecItemStatus>;
        fn delete_exact(&mut self, query: &ExactQuery) -> Result<(), SecItemStatus>;
    }

    struct VerifiedIdentity {
        current_user_uuid_commitment: [u8; COMMITMENT_BYTES],
        access_group_commitment: [u8; COMMITMENT_BYTES],
    }

    struct ContractProvider<B> {
        backend: B,
        identity: VerifiedIdentity,
    }

    enum ParsedReference {
        Valid {
            item_id: [u8; ITEM_ID_BYTES],
            persistent_reference: Vec<u8>,
        },
        WrongIdentity,
        Corrupt,
    }

    enum VerifiedRecord {
        Available {
            key: BackupKey,
            reference: RecoveryKeyReference,
        },
        Locked,
        Missing,
        WrongIdentity,
        Corrupt,
        Unavailable,
    }

    impl<B: SecItemBackend> ContractProvider<B> {
        fn for_verified_test_identity(backend: B, user_uuid: [u8; 16]) -> Self {
            let mut user_hasher = Sha256::new();
            user_hasher.update(b"cpa-cloud/macos-current-user/v1\0");
            user_hasher.update(user_uuid);
            Self {
                backend,
                identity: VerifiedIdentity {
                    current_user_uuid_commitment: user_hasher.finalize().into(),
                    access_group_commitment: access_group_commitment(),
                },
            }
        }

        fn create(
            &mut self,
            transaction_id: ReplacementTransactionId,
        ) -> RecoveryKeyCreateOutcome {
            let Ok(key) = BackupKey::generate() else {
                return RecoveryKeyCreateOutcome::Unavailable;
            };
            let mut item_id = [0_u8; ITEM_ID_BYTES];
            if SystemRandom::new().fill(&mut item_id).is_err() {
                return RecoveryKeyCreateOutcome::Unavailable;
            }
            let envelope = envelope(transaction_id, item_id, &self.identity, &key);
            let record = SecItemRecord {
                policy: SecItemPolicy::fixed(),
                service: SERVICE.to_owned(),
                account: account(transaction_id),
                generic_metadata: generic_metadata(transaction_id, item_id, &self.identity),
                value_data: envelope,
                persistent_reference: Vec::new(),
            };
            let request = AddRequest {
                record,
                return_persistent_reference: true,
            };

            let persistent_reference = match self.backend.add(request) {
                Ok(reference) => reference,
                Err(SecItemStatus::DuplicateItem) => {
                    return self.reopen_duplicate(transaction_id);
                }
                Err(status) if locked_status(status) => return RecoveryKeyCreateOutcome::Locked,
                Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
            };
            let query = exact_query(persistent_reference);
            match self.read_and_verify(transaction_id, &query, Some(item_id)) {
                VerifiedRecord::Available { key, reference } => {
                    RecoveryKeyCreateOutcome::Created(CreatedRecoveryKey { key, reference })
                }
                VerifiedRecord::Locked => RecoveryKeyCreateOutcome::Locked,
                _ => RecoveryKeyCreateOutcome::Unavailable,
            }
        }

        fn open(
            &mut self,
            transaction_id: ReplacementTransactionId,
            reference: &RecoveryKeyReference,
        ) -> RecoveryKeyOpenOutcome {
            let (item_id, persistent_reference) =
                match parse_reference(transaction_id, &self.identity, reference) {
                    ParsedReference::Valid {
                        item_id,
                        persistent_reference,
                    } => (item_id, persistent_reference),
                    ParsedReference::WrongIdentity => {
                        return RecoveryKeyOpenOutcome::WrongIdentity;
                    }
                    ParsedReference::Corrupt => return RecoveryKeyOpenOutcome::Corrupt,
                };
            let query = exact_query(persistent_reference);
            match self.read_and_verify(transaction_id, &query, Some(item_id)) {
                VerifiedRecord::Available { key, .. } => RecoveryKeyOpenOutcome::Available(key),
                VerifiedRecord::Locked => RecoveryKeyOpenOutcome::Locked,
                VerifiedRecord::Missing => RecoveryKeyOpenOutcome::Missing,
                VerifiedRecord::WrongIdentity => RecoveryKeyOpenOutcome::WrongIdentity,
                VerifiedRecord::Corrupt => RecoveryKeyOpenOutcome::Corrupt,
                VerifiedRecord::Unavailable => RecoveryKeyOpenOutcome::Unavailable,
            }
        }

        fn delete(
            &mut self,
            transaction_id: ReplacementTransactionId,
            reference: &RecoveryKeyReference,
        ) -> RecoveryKeyDeleteOutcome {
            let (item_id, persistent_reference) =
                match parse_reference(transaction_id, &self.identity, reference) {
                    ParsedReference::Valid {
                        item_id,
                        persistent_reference,
                    } => (item_id, persistent_reference),
                    ParsedReference::WrongIdentity => {
                        return RecoveryKeyDeleteOutcome::WrongIdentity;
                    }
                    ParsedReference::Corrupt => return RecoveryKeyDeleteOutcome::Corrupt,
                };
            let query = exact_query(persistent_reference);
            match self.read_and_verify(transaction_id, &query, Some(item_id)) {
                VerifiedRecord::Available { .. } => {}
                VerifiedRecord::Locked => return RecoveryKeyDeleteOutcome::Locked,
                VerifiedRecord::Missing => return RecoveryKeyDeleteOutcome::Missing,
                VerifiedRecord::WrongIdentity => return RecoveryKeyDeleteOutcome::WrongIdentity,
                VerifiedRecord::Corrupt => return RecoveryKeyDeleteOutcome::Corrupt,
                VerifiedRecord::Unavailable => return RecoveryKeyDeleteOutcome::Unavailable,
            }
            match self.backend.delete_exact(&query) {
                Ok(()) | Err(SecItemStatus::ItemNotFound) => {}
                Err(status) if locked_status(status) => {
                    return RecoveryKeyDeleteOutcome::Locked;
                }
                Err(SecItemStatus::Decode) => return RecoveryKeyDeleteOutcome::Corrupt,
                Err(_) => return RecoveryKeyDeleteOutcome::Unavailable,
            }
            match self.backend.read_exact(&query) {
                Err(SecItemStatus::ItemNotFound) => {}
                Err(status) if locked_status(status) => {
                    return RecoveryKeyDeleteOutcome::Locked;
                }
                Err(_) => return RecoveryKeyDeleteOutcome::Unavailable,
                Ok(_) => return RecoveryKeyDeleteOutcome::Unavailable,
            }
            match self.backend.read_primary(&primary_query(transaction_id)) {
                Ok(items) if items.is_empty() => RecoveryKeyDeleteOutcome::Deleted,
                Ok(_) => RecoveryKeyDeleteOutcome::WrongIdentity,
                Err(SecItemStatus::ItemNotFound) => RecoveryKeyDeleteOutcome::Deleted,
                Err(status) if locked_status(status) => RecoveryKeyDeleteOutcome::Locked,
                Err(_) => RecoveryKeyDeleteOutcome::Unavailable,
            }
        }

        fn reopen_duplicate(
            &mut self,
            transaction_id: ReplacementTransactionId,
        ) -> RecoveryKeyCreateOutcome {
            let items = match self.backend.read_primary(&primary_query(transaction_id)) {
                Ok(items) => items,
                Err(status) if locked_status(status) => return RecoveryKeyCreateOutcome::Locked,
                Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
            };
            if items.len() != 1 {
                return RecoveryKeyCreateOutcome::Unavailable;
            }
            let item_id = match item_id_from_metadata(transaction_id, &self.identity, &items[0]) {
                Ok(item_id) => item_id,
                Err(_) => return RecoveryKeyCreateOutcome::Unavailable,
            };
            let query = exact_query(items[0].persistent_reference.clone());
            match self.read_and_verify(transaction_id, &query, Some(item_id)) {
                VerifiedRecord::Available { key, reference } => {
                    RecoveryKeyCreateOutcome::Created(CreatedRecoveryKey { key, reference })
                }
                VerifiedRecord::Locked => RecoveryKeyCreateOutcome::Locked,
                _ => RecoveryKeyCreateOutcome::Unavailable,
            }
        }

        fn read_and_verify(
            &mut self,
            transaction_id: ReplacementTransactionId,
            exact: &ExactQuery,
            expected_item_id: Option<[u8; ITEM_ID_BYTES]>,
        ) -> VerifiedRecord {
            let record = match self.backend.read_exact(exact) {
                Ok(record) => record,
                Err(SecItemStatus::ItemNotFound) => {
                    return match self.backend.read_primary(&primary_query(transaction_id)) {
                        Ok(items) if items.is_empty() => VerifiedRecord::Missing,
                        Err(SecItemStatus::ItemNotFound) => VerifiedRecord::Missing,
                        Ok(_) => VerifiedRecord::WrongIdentity,
                        Err(status) if locked_status(status) => VerifiedRecord::Locked,
                        Err(_) => VerifiedRecord::Unavailable,
                    };
                }
                Err(status) if locked_status(status) => return VerifiedRecord::Locked,
                Err(SecItemStatus::Decode) => return VerifiedRecord::Corrupt,
                Err(_) => return VerifiedRecord::Unavailable,
            };
            if record.persistent_reference != exact.persistent_reference {
                return VerifiedRecord::WrongIdentity;
            }
            let item_id = match item_id_from_metadata(transaction_id, &self.identity, &record) {
                Ok(item_id) => item_id,
                Err(outcome) => return outcome,
            };
            if expected_item_id.is_some_and(|expected| expected != item_id) {
                return VerifiedRecord::WrongIdentity;
            }
            let key = match key_from_envelope(transaction_id, item_id, &self.identity, &record) {
                Ok(key) => key,
                Err(outcome) => return outcome,
            };
            let primary = match self.backend.read_primary(&primary_query(transaction_id)) {
                Ok(items) => items,
                Err(status) if locked_status(status) => return VerifiedRecord::Locked,
                Err(_) => return VerifiedRecord::Unavailable,
            };
            if primary.len() != 1 || primary[0].persistent_reference != record.persistent_reference {
                return VerifiedRecord::WrongIdentity;
            }
            let reference = match make_reference(
                transaction_id,
                item_id,
                &self.identity,
                &record.persistent_reference,
            ) {
                Ok(reference) => reference,
                Err(_) => return VerifiedRecord::Corrupt,
            };
            VerifiedRecord::Available { key, reference }
        }

        fn into_backend(self) -> B {
            self.backend
        }
    }

    fn account(transaction_id: ReplacementTransactionId) -> String {
        hex::encode(transaction_id.as_bytes())
    }

    fn access_group_commitment() -> [u8; COMMITMENT_BYTES] {
        let mut hasher = Sha256::new();
        hasher.update(ACCESS_GROUP_CONTEXT);
        hasher.update(ACCESS_GROUP.as_bytes());
        hasher.finalize().into()
    }

    fn primary_query(transaction_id: ReplacementTransactionId) -> PrimaryQuery {
        PrimaryQuery {
            policy: SecItemPolicy::fixed(),
            service: SERVICE.to_owned(),
            account: account(transaction_id),
            match_limit: MatchLimit::All,
            return_attributes: true,
            return_data: true,
            return_persistent_reference: true,
        }
    }

    fn exact_query(persistent_reference: Vec<u8>) -> ExactQuery {
        ExactQuery {
            policy: SecItemPolicy::fixed(),
            persistent_reference,
            return_attributes: true,
            return_data: true,
            return_persistent_reference: true,
        }
    }

    fn generic_metadata(
        transaction_id: ReplacementTransactionId,
        item_id: [u8; ITEM_ID_BYTES],
        identity: &VerifiedIdentity,
    ) -> Vec<u8> {
        let mut bytes = Vec::with_capacity(4 + TRANSACTION_BYTES + ITEM_ID_BYTES + COMMITMENT_BYTES);
        bytes.extend_from_slice(&PROVIDER_VERSION.to_be_bytes());
        bytes.extend_from_slice(transaction_id.as_bytes());
        bytes.extend_from_slice(&item_id);
        bytes.extend_from_slice(&identity.current_user_uuid_commitment);
        bytes
    }

    fn envelope(
        transaction_id: ReplacementTransactionId,
        item_id: [u8; ITEM_ID_BYTES],
        identity: &VerifiedIdentity,
        key: &BackupKey,
    ) -> Zeroizing<Vec<u8>> {
        let mut bytes = Zeroizing::new(Vec::with_capacity(ENVELOPE_BYTES));
        bytes.extend_from_slice(ENVELOPE_MAGIC);
        bytes.extend_from_slice(transaction_id.as_bytes());
        bytes.extend_from_slice(&item_id);
        bytes.extend_from_slice(&identity.current_user_uuid_commitment);
        bytes.extend_from_slice(&identity.access_group_commitment);
        bytes.extend_from_slice(key.expose());
        let mut hasher = Sha256::new();
        hasher.update(ENVELOPE_CONTEXT);
        hasher.update(&*bytes);
        bytes.extend_from_slice(&hasher.finalize());
        bytes
    }

    fn make_reference(
        transaction_id: ReplacementTransactionId,
        item_id: [u8; ITEM_ID_BYTES],
        identity: &VerifiedIdentity,
        persistent_reference: &[u8],
    ) -> Result<RecoveryKeyReference, ()> {
        if persistent_reference.is_empty()
            || persistent_reference.len() > MAX_PERSISTENT_REFERENCE_BYTES
        {
            return Err(());
        }
        let length = u16::try_from(persistent_reference.len()).map_err(|_| ())?;
        let mut bytes = Vec::with_capacity(REFERENCE_FIXED_BYTES + persistent_reference.len());
        bytes.extend_from_slice(REFERENCE_MAGIC);
        bytes.extend_from_slice(transaction_id.as_bytes());
        bytes.extend_from_slice(&item_id);
        bytes.extend_from_slice(&identity.current_user_uuid_commitment);
        bytes.extend_from_slice(&identity.access_group_commitment);
        bytes.extend_from_slice(&length.to_be_bytes());
        bytes.extend_from_slice(persistent_reference);
        let mut hasher = Sha256::new();
        hasher.update(REFERENCE_CONTEXT);
        hasher.update(&bytes);
        bytes.extend_from_slice(&hasher.finalize());
        RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, bytes).map_err(|_| ())
    }

    fn parse_reference(
        transaction_id: ReplacementTransactionId,
        identity: &VerifiedIdentity,
        reference: &RecoveryKeyReference,
    ) -> ParsedReference {
        if reference.provider_kind() != PROVIDER_KIND
            || reference.provider_version() != PROVIDER_VERSION
        {
            return ParsedReference::WrongIdentity;
        }
        let bytes = reference.opaque_reference();
        if bytes.len() < REFERENCE_FIXED_BYTES || &bytes[..MAGIC_BYTES] != REFERENCE_MAGIC {
            return ParsedReference::Corrupt;
        }
        let checksum_offset = bytes.len() - CHECKSUM_BYTES;
        let mut hasher = Sha256::new();
        hasher.update(REFERENCE_CONTEXT);
        hasher.update(&bytes[..checksum_offset]);
        if verify_slices_are_equal(&hasher.finalize(), &bytes[checksum_offset..]).is_err() {
            return ParsedReference::Corrupt;
        }
        let mut offset = MAGIC_BYTES;
        let stored_transaction = &bytes[offset..offset + TRANSACTION_BYTES];
        offset += TRANSACTION_BYTES;
        let mut item_id = [0_u8; ITEM_ID_BYTES];
        item_id.copy_from_slice(&bytes[offset..offset + ITEM_ID_BYTES]);
        offset += ITEM_ID_BYTES;
        let stored_user = &bytes[offset..offset + COMMITMENT_BYTES];
        offset += COMMITMENT_BYTES;
        let stored_access_group = &bytes[offset..offset + COMMITMENT_BYTES];
        offset += COMMITMENT_BYTES;
        if verify_slices_are_equal(stored_transaction, transaction_id.as_bytes()).is_err()
            || verify_slices_are_equal(stored_user, &identity.current_user_uuid_commitment).is_err()
            || verify_slices_are_equal(stored_access_group, &identity.access_group_commitment)
                .is_err()
        {
            return ParsedReference::WrongIdentity;
        }
        let length = u16::from_be_bytes([bytes[offset], bytes[offset + 1]]) as usize;
        offset += 2;
        if length == 0
            || length > MAX_PERSISTENT_REFERENCE_BYTES
            || offset + length != checksum_offset
        {
            return ParsedReference::Corrupt;
        }
        ParsedReference::Valid {
            item_id,
            persistent_reference: bytes[offset..offset + length].to_vec(),
        }
    }

    fn item_id_from_metadata(
        transaction_id: ReplacementTransactionId,
        identity: &VerifiedIdentity,
        record: &SecItemRecord,
    ) -> Result<[u8; ITEM_ID_BYTES], VerifiedRecord> {
        if record.policy != SecItemPolicy::fixed()
            || record.service != SERVICE
            || record.account != account(transaction_id)
        {
            return Err(VerifiedRecord::WrongIdentity);
        }
        let expected_len = 4 + TRANSACTION_BYTES + ITEM_ID_BYTES + COMMITMENT_BYTES;
        if record.generic_metadata.len() != expected_len {
            return Err(VerifiedRecord::Corrupt);
        }
        let mut offset = 0;
        let version = u32::from_be_bytes(
            record.generic_metadata[offset..offset + 4]
                .try_into()
                .map_err(|_| VerifiedRecord::Corrupt)?,
        );
        offset += 4;
        if version != PROVIDER_VERSION {
            return Err(VerifiedRecord::Corrupt);
        }
        let stored_transaction = &record.generic_metadata[offset..offset + TRANSACTION_BYTES];
        offset += TRANSACTION_BYTES;
        let mut item_id = [0_u8; ITEM_ID_BYTES];
        item_id.copy_from_slice(&record.generic_metadata[offset..offset + ITEM_ID_BYTES]);
        offset += ITEM_ID_BYTES;
        let stored_user = &record.generic_metadata[offset..offset + COMMITMENT_BYTES];
        if verify_slices_are_equal(stored_transaction, transaction_id.as_bytes()).is_err()
            || verify_slices_are_equal(stored_user, &identity.current_user_uuid_commitment).is_err()
        {
            return Err(VerifiedRecord::WrongIdentity);
        }
        Ok(item_id)
    }

    fn key_from_envelope(
        transaction_id: ReplacementTransactionId,
        item_id: [u8; ITEM_ID_BYTES],
        identity: &VerifiedIdentity,
        record: &SecItemRecord,
    ) -> Result<BackupKey, VerifiedRecord> {
        let bytes = &record.value_data;
        if bytes.len() != ENVELOPE_BYTES || &bytes[..MAGIC_BYTES] != ENVELOPE_MAGIC {
            return Err(VerifiedRecord::Corrupt);
        }
        let checksum_offset = ENVELOPE_BYTES - CHECKSUM_BYTES;
        let mut hasher = Sha256::new();
        hasher.update(ENVELOPE_CONTEXT);
        hasher.update(&bytes[..checksum_offset]);
        if verify_slices_are_equal(&hasher.finalize(), &bytes[checksum_offset..]).is_err() {
            return Err(VerifiedRecord::Corrupt);
        }
        let mut offset = MAGIC_BYTES;
        let stored_transaction = &bytes[offset..offset + TRANSACTION_BYTES];
        offset += TRANSACTION_BYTES;
        let stored_item = &bytes[offset..offset + ITEM_ID_BYTES];
        offset += ITEM_ID_BYTES;
        let stored_user = &bytes[offset..offset + COMMITMENT_BYTES];
        offset += COMMITMENT_BYTES;
        let stored_access_group = &bytes[offset..offset + COMMITMENT_BYTES];
        offset += COMMITMENT_BYTES;
        if verify_slices_are_equal(stored_transaction, transaction_id.as_bytes()).is_err()
            || verify_slices_are_equal(stored_item, &item_id).is_err()
            || verify_slices_are_equal(stored_user, &identity.current_user_uuid_commitment).is_err()
            || verify_slices_are_equal(stored_access_group, &identity.access_group_commitment)
                .is_err()
        {
            return Err(VerifiedRecord::WrongIdentity);
        }
        let mut key = Zeroizing::new([0_u8; KEY_BYTES]);
        key.copy_from_slice(&bytes[offset..offset + KEY_BYTES]);
        Ok(BackupKey::from_bytes(key))
    }

    fn locked_status(status: SecItemStatus) -> bool {
        matches!(
            status,
            SecItemStatus::InteractionNotAllowed
                | SecItemStatus::InteractionRequired
                | SecItemStatus::NotLoggedIn
        )
    }

    #[derive(Default)]
    struct FakeBackend {
        records: BTreeMap<Vec<u8>, SecItemRecord>,
        next_reference: u64,
        exact_reads: usize,
        primary_reads: usize,
        adds: usize,
        deletes: usize,
        next_error: Option<SecItemStatus>,
        replacement_after_delete: Option<SecItemRecord>,
        last_add: Option<AddRequest>,
        last_exact: Option<ExactQuery>,
        last_primary: Option<PrimaryQuery>,
    }

    impl FakeBackend {
        fn take_error(&mut self) -> Result<(), SecItemStatus> {
            self.next_error.take().map_or(Ok(()), Err)
        }

        fn matching_primary(&self, query: &PrimaryQuery) -> Vec<SecItemRecord> {
            self.records
                .values()
                .filter(|record| {
                    record.policy == query.policy
                        && record.service == query.service
                        && record.account == query.account
                })
                .cloned()
                .collect()
        }
    }

    impl SecItemBackend for FakeBackend {
        fn add(&mut self, mut request: AddRequest) -> Result<Vec<u8>, SecItemStatus> {
            self.adds += 1;
            self.last_add = Some(request.clone());
            self.take_error()?;
            if !self.matching_primary(&primary_query_from_record(&request.record)).is_empty() {
                return Err(SecItemStatus::DuplicateItem);
            }
            self.next_reference += 1;
            let reference = format!("persistent-ref-{}", self.next_reference).into_bytes();
            request.record.persistent_reference = reference.clone();
            self.records.insert(reference.clone(), request.record);
            Ok(reference)
        }

        fn read_exact(&mut self, query: &ExactQuery) -> Result<SecItemRecord, SecItemStatus> {
            self.exact_reads += 1;
            self.last_exact = Some(query.clone());
            self.take_error()?;
            self.records
                .get(&query.persistent_reference)
                .cloned()
                .ok_or(SecItemStatus::ItemNotFound)
        }

        fn read_primary(
            &mut self,
            query: &PrimaryQuery,
        ) -> Result<Vec<SecItemRecord>, SecItemStatus> {
            self.primary_reads += 1;
            self.last_primary = Some(query.clone());
            self.take_error()?;
            Ok(self.matching_primary(query))
        }

        fn delete_exact(&mut self, query: &ExactQuery) -> Result<(), SecItemStatus> {
            self.deletes += 1;
            self.last_exact = Some(query.clone());
            self.take_error()?;
            if self.records.remove(&query.persistent_reference).is_none() {
                return Err(SecItemStatus::ItemNotFound);
            }
            if let Some(replacement) = self.replacement_after_delete.take() {
                self.records
                    .insert(replacement.persistent_reference.clone(), replacement);
            }
            Ok(())
        }
    }

    fn primary_query_from_record(record: &SecItemRecord) -> PrimaryQuery {
        PrimaryQuery {
            policy: record.policy.clone(),
            service: record.service.clone(),
            account: record.account.clone(),
            match_limit: MatchLimit::All,
            return_attributes: true,
            return_data: true,
            return_persistent_reference: true,
        }
    }

    fn transaction(byte: u8) -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([byte; TRANSACTION_BYTES]).unwrap()
    }

    fn user(byte: u8) -> [u8; 16] {
        [byte; 16]
    }

    fn created(outcome: RecoveryKeyCreateOutcome) -> CreatedRecoveryKey {
        match outcome {
            RecoveryKeyCreateOutcome::Created(created) => created,
            other => panic!("expected Created, got {other:?}"),
        }
    }

    #[test]
    fn production_constructor_stays_fail_closed_without_native_identity_gate() {
        #[cfg(target_os = "macos")]
        assert!(matches!(
            MacosKeychainRecoveryKeyProvider::current_user(),
            Err(MacosKeychainProviderError::NativeIdentityUnverified)
        ));
        #[cfg(not(target_os = "macos"))]
        assert!(matches!(
            MacosKeychainRecoveryKeyProvider::current_user(),
            Err(MacosKeychainProviderError::UnsupportedPlatform)
        ));
    }

    #[test]
    fn every_request_has_the_fixed_data_protection_policy() {
        let tx = transaction(0x11);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(1));
        let created = created(provider.create(tx));
        assert!(matches!(provider.open(tx, &created.reference), RecoveryKeyOpenOutcome::Available(_)));

        let backend = &provider.backend;
        let add = backend.last_add.as_ref().unwrap();
        assert_eq!(add.record.policy, SecItemPolicy::fixed());
        assert!(add.return_persistent_reference);
        let exact = backend.last_exact.as_ref().unwrap();
        assert_eq!(exact.policy, SecItemPolicy::fixed());
        assert!(exact.return_attributes && exact.return_data && exact.return_persistent_reference);
        let primary = backend.last_primary.as_ref().unwrap();
        assert_eq!(primary.policy, SecItemPolicy::fixed());
        assert_eq!(primary.match_limit, MatchLimit::All);
        assert!(primary.return_attributes && primary.return_data && primary.return_persistent_reference);
    }

    #[test]
    fn create_is_idempotent_and_restart_reopens_the_same_key() {
        let tx = transaction(0x22);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(2));
        let first = created(provider.create(tx));
        let expected_key = *first.key.expose();
        let second = created(provider.create(tx));
        assert_eq!(second.reference, first.reference);
        assert_eq!(second.key.expose(), &expected_key);
        assert_eq!(provider.backend.records.len(), 1);

        let backend = provider.into_backend();
        let mut restarted = ContractProvider::for_verified_test_identity(backend, user(2));
        match restarted.open(tx, &first.reference) {
            RecoveryKeyOpenOutcome::Available(key) => assert_eq!(key.expose(), &expected_key),
            other => panic!("expected Available, got {other:?}"),
        }
        assert_eq!(
            restarted.delete(tx, &first.reference),
            RecoveryKeyDeleteOutcome::Deleted
        );
        assert!(matches!(
            restarted.open(tx, &first.reference),
            RecoveryKeyOpenOutcome::Missing
        ));
    }

    #[test]
    fn wrong_user_and_transaction_are_rejected_before_any_backend_query() {
        let tx = transaction(0x33);
        let mut owner = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(3));
        let created = created(owner.create(tx));
        let backend = owner.into_backend();
        let mut wrong_user = ContractProvider::for_verified_test_identity(backend, user(4));
        let before = (wrong_user.backend.exact_reads, wrong_user.backend.primary_reads);
        assert!(matches!(
            wrong_user.open(tx, &created.reference),
            RecoveryKeyOpenOutcome::WrongIdentity
        ));
        assert_eq!(
            (wrong_user.backend.exact_reads, wrong_user.backend.primary_reads),
            before
        );
        assert_eq!(
            wrong_user.delete(transaction(0x34), &created.reference),
            RecoveryKeyDeleteOutcome::WrongIdentity
        );
        assert_eq!(wrong_user.backend.deletes, 0);
    }

    #[test]
    fn corrupt_reference_and_value_never_fall_back_to_creation() {
        let tx = transaction(0x44);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(5));
        let created = created(provider.create(tx));
        let adds = provider.backend.adds;
        let corrupt = RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, vec![1, 2, 3]).unwrap();
        assert!(matches!(provider.open(tx, &corrupt), RecoveryKeyOpenOutcome::Corrupt));
        assert_eq!(provider.backend.adds, adds);

        let record = provider.backend.records.values_mut().next().unwrap();
        record.value_data[0] ^= 0xff;
        assert!(matches!(provider.open(tx, &created.reference), RecoveryKeyOpenOutcome::Corrupt));
        assert_eq!(provider.backend.adds, adds);
    }

    #[test]
    fn only_narrow_statuses_map_to_locked() {
        assert!(locked_status(SecItemStatus::InteractionNotAllowed));
        assert!(locked_status(SecItemStatus::InteractionRequired));
        assert!(locked_status(SecItemStatus::NotLoggedIn));
        assert!(!locked_status(SecItemStatus::AuthFailed));
        assert!(!locked_status(SecItemStatus::MissingEntitlement));
        assert!(!locked_status(SecItemStatus::Internal));

        for status in [
            SecItemStatus::InteractionNotAllowed,
            SecItemStatus::InteractionRequired,
            SecItemStatus::NotLoggedIn,
        ] {
            let mut backend = FakeBackend::default();
            backend.next_error = Some(status);
            let mut provider = ContractProvider::for_verified_test_identity(backend, user(6));
            assert!(matches!(provider.create(transaction(0x55)), RecoveryKeyCreateOutcome::Locked));
        }
        for status in [
            SecItemStatus::AuthFailed,
            SecItemStatus::MissingEntitlement,
            SecItemStatus::Internal,
        ] {
            let mut backend = FakeBackend::default();
            backend.next_error = Some(status);
            let mut provider = ContractProvider::for_verified_test_identity(backend, user(7));
            assert!(matches!(provider.create(transaction(0x56)), RecoveryKeyCreateOutcome::Unavailable));
        }
    }

    #[test]
    fn delete_uses_exact_reference_and_preserves_a_replacement_item() {
        let tx = transaction(0x66);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(8));
        let created = created(provider.create(tx));
        let original = provider.backend.records.values().next().unwrap().clone();
        let mut replacement = original.clone();
        replacement.persistent_reference = b"replacement-persistent-ref".to_vec();
        replacement.value_data[0] ^= 0x01;
        provider.backend.replacement_after_delete = Some(replacement.clone());

        assert_eq!(
            provider.delete(tx, &created.reference),
            RecoveryKeyDeleteOutcome::WrongIdentity
        );
        assert_eq!(provider.backend.deletes, 1);
        assert!(provider
            .backend
            .records
            .contains_key(&replacement.persistent_reference));
    }

    #[test]
    fn debug_output_redacts_keychain_material() {
        let tx = transaction(0x77);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(9));
        let created = created(provider.create(tx));
        let rendered_reference = format!("{:?}", created.reference);
        let rendered_add = format!("{:?}", provider.backend.last_add.as_ref().unwrap());
        let rendered_exact = format!("{:?}", provider.backend.last_exact.as_ref().unwrap());
        assert!(!rendered_reference.contains("persistent-ref"));
        assert!(!rendered_add.contains("persistent-ref"));
        assert!(!rendered_exact.contains("persistent-ref"));
        assert!(!rendered_add.contains(&hex::encode(created.key.expose())));
        assert!(rendered_add.contains("[REDACTED]"));
    }

    #[test]
    fn decode_status_maps_to_corrupt_after_reference_identity_is_verified() {
        let tx = transaction(0x78);
        let mut provider = ContractProvider::for_verified_test_identity(FakeBackend::default(), user(10));
        let created = created(provider.create(tx));
        provider.backend.next_error = Some(SecItemStatus::Decode);
        assert!(matches!(
            provider.open(tx, &created.reference),
            RecoveryKeyOpenOutcome::Corrupt
        ));
    }
}
