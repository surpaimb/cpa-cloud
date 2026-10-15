//! Pure recovery-key custody interface for the replacement transaction.
//!
//! No operating-system provider is implemented here. In particular, this
//! module does not read environment variables or files and never turns an
//! `open` failure into key creation. Platform providers remain separately
//! gated before any real replacement transaction can use this interface.

use super::{
    backup::BackupKey, completion_tombstone::CompletionTombstoneRemovalReceipt,
    replacement::ReplacementPhase, workspace_removal::WorkspaceRemovalReceipt,
};

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
/// `create` is used only before the first encrypted backup. A provider that
/// returns `true` from `create_is_transaction_idempotent` must return the exact
/// same key and reference for every successful retry of one transaction ID; it
/// must never replace a value already published for that ID. Recovery calls
/// `open` with the exact durable reference, and terminal cleanup calls `delete`
/// only after the workspace has been durably removed.
pub trait RecoveryKeyProvider {
    fn create_is_transaction_idempotent(&self) -> bool {
        false
    }

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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RecoveryKeyCleanupPhase {
    TerminalRecorded,
    CompletionTombstoneDurable,
    WorkspaceRemovalDurable,
    KeyDeletionConfirmed,
    Complete,
}

#[derive(Debug, Clone, Copy, thiserror::Error, PartialEq, Eq)]
pub enum RecoveryKeyDeleteBlocked {
    #[error("recovery-key provider is locked")]
    Locked,
    #[error("recovery key belongs to a different identity")]
    WrongIdentity,
    #[error("recovery-key metadata or protected value is corrupt")]
    Corrupt,
    #[error("recovery-key provider is unavailable")]
    Unavailable,
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum RecoveryKeyCleanupError {
    #[error("key cleanup requires a durable Complete or RolledBack decision")]
    InvalidTerminal,
    #[error("key cleanup requires one canonical transaction workspace name")]
    InvalidWorkspace,
    #[error("key-cleanup event is not valid in phase {0:?}")]
    InvalidTransition(RecoveryKeyCleanupPhase),
    #[error("key deletion is blocked: {0}")]
    DeleteBlocked(RecoveryKeyDeleteBlocked),
}

pub(crate) fn valid_workspace_name(name: &str) -> bool {
    let parts: Vec<_> = name.split('-').collect();
    parts.len() == 4
        && parts[0] == "tx"
        && parts[1]
            .parse::<u32>()
            .is_ok_and(|value| value.to_string() == parts[1])
        && u128::from_str_radix(parts[2], 16).is_ok_and(|value| format!("{value:x}") == parts[2])
        && u64::from_str_radix(parts[3], 16).is_ok_and(|value| format!("{value:x}") == parts[3])
}

/// Proof that the provider has confirmed deletion for one exact transaction
/// and recovery-key reference.
///
/// This value can only be obtained from a cleanup sequence in
/// `KeyDeletionConfirmed`; it authorizes the separate tombstone store to move
/// that exact non-secret record to its inactive durable proof state. It is
/// intentionally not serializable.
pub struct CompletionTombstoneRemovalAuthorization {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
}

impl CompletionTombstoneRemovalAuthorization {
    pub(crate) fn terminal(&self) -> ReplacementPhase {
        self.terminal
    }

    pub(crate) fn transaction_id(&self) -> ReplacementTransactionId {
        self.transaction_id
    }

    pub(crate) fn reference(&self) -> &RecoveryKeyReference {
        &self.reference
    }
}

impl std::fmt::Debug for CompletionTombstoneRemovalAuthorization {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CompletionTombstoneRemovalAuthorization")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .finish()
    }
}

/// Authorization to verify, without mutating, the inactive completion proof
/// for a cleanup sequence whose `Complete` event is already durable.
pub(crate) struct CompletionTombstoneProofAuthorization {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
}

impl CompletionTombstoneProofAuthorization {
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
    pub(crate) fn for_test(
        terminal: ReplacementPhase,
        transaction_id: ReplacementTransactionId,
        reference: RecoveryKeyReference,
    ) -> Self {
        Self {
            terminal,
            transaction_id,
            reference,
        }
    }
}

impl std::fmt::Debug for CompletionTombstoneProofAuthorization {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CompletionTombstoneProofAuthorization")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .finish()
    }
}

/// Proof that the completion tombstone is durable and that one exact
/// transaction workspace may now be removed. It is intentionally not
/// serializable and carries no key material.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct WorkspaceRemovalTarget {
    workspace_name: String,
    device: u64,
    inode: u64,
}

impl WorkspaceRemovalTarget {
    pub(crate) fn from_durable(
        _evidence: super::cleanup_journal::DurablePhaseEvidence,
        workspace_name: String,
        device: u64,
        inode: u64,
    ) -> Result<Self, RecoveryKeyCleanupError> {
        if !valid_workspace_name(&workspace_name) {
            return Err(RecoveryKeyCleanupError::InvalidWorkspace);
        }
        Ok(Self {
            workspace_name,
            device,
            inode,
        })
    }

    pub(crate) fn workspace_name(&self) -> &str {
        &self.workspace_name
    }

    pub(crate) fn device(&self) -> u64 {
        self.device
    }

    pub(crate) fn inode(&self) -> u64 {
        self.inode
    }

    #[cfg(test)]
    pub(crate) fn for_test(workspace_name: &str, device: u64, inode: u64) -> Self {
        if !valid_workspace_name(workspace_name) {
            panic!("test workspace name must be canonical");
        }
        Self {
            workspace_name: workspace_name.to_owned(),
            device,
            inode,
        }
    }
}

/// Verified precondition for a future durable artifact-retirement protocol.
///
/// The value is issued only after the cleanup journal is durably `Complete`
/// and the tombstone store has revalidated the exact inactive completion proof.
/// It authorizes no unlink by itself; the crash-safe cross-namespace retirement
/// protocol remains a separate implementation slice.
pub(crate) struct CleanupArtifactRetirementAuthorization {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    target: WorkspaceRemovalTarget,
}

impl CleanupArtifactRetirementAuthorization {
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
        self.target.workspace_name()
    }

    pub(crate) fn workspace_device(&self) -> u64 {
        self.target.device()
    }

    pub(crate) fn workspace_inode(&self) -> u64 {
        self.target.inode()
    }
}

impl std::fmt::Debug for CleanupArtifactRetirementAuthorization {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CleanupArtifactRetirementAuthorization")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .field("target", &self.target)
            .finish()
    }
}

pub struct WorkspaceRemovalAuthorization {
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    target: WorkspaceRemovalTarget,
}

impl WorkspaceRemovalAuthorization {
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
        self.target.workspace_name()
    }

    pub(crate) fn workspace_device(&self) -> u64 {
        self.target.device()
    }

    pub(crate) fn workspace_inode(&self) -> u64 {
        self.target.inode()
    }
}

impl std::fmt::Debug for WorkspaceRemovalAuthorization {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("WorkspaceRemovalAuthorization")
            .field("terminal", &self.terminal)
            .field("transaction_id", &self.transaction_id)
            .field("reference", &self.reference)
            .field("target", &self.target)
            .finish()
    }
}

/// Provider-agnostic ordering gate for terminal recovery-key cleanup.
///
/// This pure type does not itself serialize a tombstone, remove a workspace or
/// flush a directory. Its workspace transition accepts only the receipt issued
/// by the synthetic-root removal store after exact deletion and durability
/// checks. It retains the exact transaction/reference pair and invokes provider
/// deletion only after both a durable terminal decision and verified durable
/// workspace removal. Its final
/// transition requires the exact receipt returned by the tombstone store after
/// logical removal to an inactive proof and directory/root flush.
#[derive(Debug)]
pub struct RecoveryKeyCleanupSequence {
    phase: RecoveryKeyCleanupPhase,
    terminal: ReplacementPhase,
    transaction_id: ReplacementTransactionId,
    reference: RecoveryKeyReference,
    workspace_target: WorkspaceRemovalTarget,
}

impl RecoveryKeyCleanupSequence {
    pub(crate) fn after_durable_terminal(
        terminal: ReplacementPhase,
        transaction_id: ReplacementTransactionId,
        reference: RecoveryKeyReference,
        workspace_target: WorkspaceRemovalTarget,
    ) -> Result<Self, RecoveryKeyCleanupError> {
        if !matches!(
            terminal,
            ReplacementPhase::Complete | ReplacementPhase::RolledBack
        ) {
            return Err(RecoveryKeyCleanupError::InvalidTerminal);
        }
        Ok(Self {
            phase: RecoveryKeyCleanupPhase::TerminalRecorded,
            terminal,
            transaction_id,
            reference,
            workspace_target,
        })
    }

    pub fn phase(&self) -> RecoveryKeyCleanupPhase {
        self.phase
    }

    pub fn terminal(&self) -> ReplacementPhase {
        self.terminal
    }

    pub(crate) fn transaction_id(&self) -> ReplacementTransactionId {
        self.transaction_id
    }

    pub(crate) fn reference(&self) -> &RecoveryKeyReference {
        &self.reference
    }

    pub(crate) fn workspace_name(&self) -> &str {
        self.workspace_target.workspace_name()
    }

    pub(crate) fn workspace_target(&self) -> &WorkspaceRemovalTarget {
        &self.workspace_target
    }

    pub(crate) fn from_durable_phase(
        _evidence: super::cleanup_journal::DurablePhaseEvidence,
        phase: RecoveryKeyCleanupPhase,
        terminal: ReplacementPhase,
        transaction_id: ReplacementTransactionId,
        reference: RecoveryKeyReference,
        workspace_target: WorkspaceRemovalTarget,
    ) -> Result<Self, RecoveryKeyCleanupError> {
        if !matches!(
            terminal,
            ReplacementPhase::Complete | ReplacementPhase::RolledBack
        ) {
            return Err(RecoveryKeyCleanupError::InvalidTerminal);
        }
        Ok(Self {
            phase,
            terminal,
            transaction_id,
            reference,
            workspace_target,
        })
    }

    pub fn record_completion_tombstone_durable(&mut self) -> Result<(), RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::TerminalRecorded)?;
        self.phase = RecoveryKeyCleanupPhase::CompletionTombstoneDurable;
        Ok(())
    }

    pub fn authorize_workspace_removal(
        &self,
    ) -> Result<WorkspaceRemovalAuthorization, RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::CompletionTombstoneDurable)?;
        Ok(self.workspace_removal_authorization())
    }

    pub(crate) fn workspace_removal_authorization(&self) -> WorkspaceRemovalAuthorization {
        WorkspaceRemovalAuthorization {
            terminal: self.terminal,
            transaction_id: self.transaction_id,
            reference: self.reference.clone(),
            target: self.workspace_target.clone(),
        }
    }

    pub(crate) fn record_workspace_removal_durable(
        &mut self,
        receipt: &WorkspaceRemovalReceipt,
    ) -> Result<(), RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::CompletionTombstoneDurable)?;
        if receipt.terminal() != self.terminal
            || receipt.transaction_id() != self.transaction_id
            || receipt.reference() != &self.reference
            || receipt.workspace_name() != self.workspace_target.workspace_name()
            || receipt.workspace_device() != self.workspace_target.device()
            || receipt.workspace_inode() != self.workspace_target.inode()
        {
            return Err(RecoveryKeyCleanupError::InvalidTransition(self.phase));
        }
        self.phase = RecoveryKeyCleanupPhase::WorkspaceRemovalDurable;
        Ok(())
    }

    pub fn delete_key<P: RecoveryKeyProvider + ?Sized>(
        &mut self,
        provider: &mut P,
    ) -> Result<(), RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::WorkspaceRemovalDurable)?;
        match provider.delete(self.transaction_id, &self.reference) {
            RecoveryKeyDeleteOutcome::Deleted | RecoveryKeyDeleteOutcome::Missing => {
                self.phase = RecoveryKeyCleanupPhase::KeyDeletionConfirmed;
                Ok(())
            }
            RecoveryKeyDeleteOutcome::Locked => Err(RecoveryKeyCleanupError::DeleteBlocked(
                RecoveryKeyDeleteBlocked::Locked,
            )),
            RecoveryKeyDeleteOutcome::WrongIdentity => Err(RecoveryKeyCleanupError::DeleteBlocked(
                RecoveryKeyDeleteBlocked::WrongIdentity,
            )),
            RecoveryKeyDeleteOutcome::Corrupt => Err(RecoveryKeyCleanupError::DeleteBlocked(
                RecoveryKeyDeleteBlocked::Corrupt,
            )),
            RecoveryKeyDeleteOutcome::Unavailable => Err(RecoveryKeyCleanupError::DeleteBlocked(
                RecoveryKeyDeleteBlocked::Unavailable,
            )),
        }
    }

    pub fn authorize_completion_tombstone_removal(
        &self,
    ) -> Result<CompletionTombstoneRemovalAuthorization, RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::KeyDeletionConfirmed)?;
        Ok(CompletionTombstoneRemovalAuthorization {
            terminal: self.terminal,
            transaction_id: self.transaction_id,
            reference: self.reference.clone(),
        })
    }

    pub fn record_completion_tombstone_removal_durable(
        &mut self,
        receipt: CompletionTombstoneRemovalReceipt,
    ) -> Result<(), RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::KeyDeletionConfirmed)?;
        if receipt.terminal() != self.terminal
            || receipt.transaction_id() != self.transaction_id
            || receipt.reference() != &self.reference
        {
            return Err(RecoveryKeyCleanupError::InvalidTransition(self.phase));
        }
        self.phase = RecoveryKeyCleanupPhase::Complete;
        Ok(())
    }

    pub(crate) fn authorize_completion_tombstone_proof_verification(
        &self,
    ) -> Result<CompletionTombstoneProofAuthorization, RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::Complete)?;
        Ok(CompletionTombstoneProofAuthorization {
            terminal: self.terminal,
            transaction_id: self.transaction_id,
            reference: self.reference.clone(),
        })
    }

    pub(crate) fn authorize_artifact_retirement(
        &self,
        _evidence: super::cleanup_journal::RetirementEvidence,
        receipt: &CompletionTombstoneRemovalReceipt,
    ) -> Result<CleanupArtifactRetirementAuthorization, RecoveryKeyCleanupError> {
        self.require_phase(RecoveryKeyCleanupPhase::Complete)?;
        if receipt.terminal() != self.terminal
            || receipt.transaction_id() != self.transaction_id
            || receipt.reference() != &self.reference
        {
            return Err(RecoveryKeyCleanupError::InvalidTransition(self.phase));
        }
        Ok(CleanupArtifactRetirementAuthorization {
            terminal: self.terminal,
            transaction_id: self.transaction_id,
            reference: self.reference.clone(),
            target: self.workspace_target.clone(),
        })
    }

    fn require_phase(&self, phase: RecoveryKeyCleanupPhase) -> Result<(), RecoveryKeyCleanupError> {
        if self.phase == phase {
            Ok(())
        } else {
            Err(RecoveryKeyCleanupError::InvalidTransition(self.phase))
        }
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
        next_delete: Option<RecoveryKeyDeleteOutcome>,
    }

    impl FakeProvider {
        fn opening(outcome: RecoveryKeyOpenOutcome) -> Self {
            Self {
                create_calls: 0,
                open_calls: 0,
                delete_calls: 0,
                next_open: Some(outcome),
                next_delete: Some(RecoveryKeyDeleteOutcome::Unavailable),
            }
        }

        fn deleting(outcome: RecoveryKeyDeleteOutcome) -> Self {
            Self {
                create_calls: 0,
                open_calls: 0,
                delete_calls: 0,
                next_open: Some(RecoveryKeyOpenOutcome::Unavailable),
                next_delete: Some(outcome),
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
            self.next_delete.take().unwrap()
        }
    }

    fn transaction_id() -> ReplacementTransactionId {
        ReplacementTransactionId::from_bytes([0x11; TRANSACTION_ID_BYTES]).unwrap()
    }

    fn reference() -> RecoveryKeyReference {
        RecoveryKeyReference::new("test-provider", 1, b"opaque-sentinel".to_vec()).unwrap()
    }

    fn cleanup_ready_for_key_deletion() -> RecoveryKeyCleanupSequence {
        let mut cleanup = RecoveryKeyCleanupSequence::after_durable_terminal(
            ReplacementPhase::RolledBack,
            transaction_id(),
            reference(),
            WorkspaceRemovalTarget::for_test("tx-1-1-0", 1, 1),
        )
        .unwrap();
        cleanup.record_completion_tombstone_durable().unwrap();
        let authorization = cleanup.authorize_workspace_removal().unwrap();
        let receipt = WorkspaceRemovalReceipt::for_test(&authorization);
        cleanup.record_workspace_removal_durable(&receipt).unwrap();
        cleanup
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
            BackupKey::from_bytes(zeroize::Zeroizing::new([0x7a; 32])),
        ));
        let key = open_existing_recovery_key(&mut provider, transaction_id(), &reference())
            .expect("available key should be returned");
        assert_eq!(format!("{key:?}"), "BackupKey([REDACTED])");

        let opened = RecoveryKeyOpenOutcome::Available(BackupKey::from_bytes(
            zeroize::Zeroizing::new([0x7b; 32]),
        ));
        assert_eq!(format!("{opened:?}"), "Available([REDACTED])");

        let created = CreatedRecoveryKey {
            reference: reference(),
            key: BackupKey::from_bytes(zeroize::Zeroizing::new([0x7c; 32])),
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

    #[test]
    fn cleanup_never_deletes_a_key_before_tombstone_and_workspace_durability() {
        assert!(matches!(
            RecoveryKeyCleanupSequence::after_durable_terminal(
                ReplacementPhase::CommitPrepared,
                transaction_id(),
                reference(),
                WorkspaceRemovalTarget::for_test("tx-1-1-0", 1, 1),
            ),
            Err(RecoveryKeyCleanupError::InvalidTerminal)
        ));

        let mut cleanup = RecoveryKeyCleanupSequence::after_durable_terminal(
            ReplacementPhase::Complete,
            transaction_id(),
            reference(),
            WorkspaceRemovalTarget::for_test("tx-1-1-0", 1, 1),
        )
        .unwrap();
        let mut provider = FakeProvider::deleting(RecoveryKeyDeleteOutcome::Deleted);

        assert!(matches!(
            cleanup.authorize_completion_tombstone_removal(),
            Err(RecoveryKeyCleanupError::InvalidTransition(
                RecoveryKeyCleanupPhase::TerminalRecorded
            ))
        ));
        assert!(matches!(
            cleanup.delete_key(&mut provider),
            Err(RecoveryKeyCleanupError::InvalidTransition(
                RecoveryKeyCleanupPhase::TerminalRecorded
            ))
        ));
        assert_eq!(provider.delete_calls, 0);

        cleanup.record_completion_tombstone_durable().unwrap();
        assert!(matches!(
            cleanup.delete_key(&mut provider),
            Err(RecoveryKeyCleanupError::InvalidTransition(
                RecoveryKeyCleanupPhase::CompletionTombstoneDurable
            ))
        ));
        assert_eq!(provider.delete_calls, 0);

        let workspace_authorization = cleanup.authorize_workspace_removal().unwrap();
        let workspace_receipt = WorkspaceRemovalReceipt::for_test(&workspace_authorization);
        cleanup
            .record_workspace_removal_durable(&workspace_receipt)
            .unwrap();
        cleanup.delete_key(&mut provider).unwrap();
        assert_eq!(provider.delete_calls, 1);
        assert_eq!(
            cleanup.phase(),
            RecoveryKeyCleanupPhase::KeyDeletionConfirmed
        );
        assert!(matches!(
            cleanup.authorize_completion_tombstone_proof_verification(),
            Err(RecoveryKeyCleanupError::InvalidTransition(
                RecoveryKeyCleanupPhase::KeyDeletionConfirmed
            ))
        ));
        let authorization = cleanup.authorize_completion_tombstone_removal().unwrap();
        assert_eq!(authorization.terminal(), ReplacementPhase::Complete);
        assert_eq!(authorization.transaction_id(), transaction_id());
        assert_eq!(authorization.reference(), &reference());
        assert!(!format!("{authorization:?}").contains("opaque-sentinel"));
        assert_eq!(
            cleanup.phase(),
            RecoveryKeyCleanupPhase::KeyDeletionConfirmed
        );
    }

    #[test]
    fn blocked_key_deletion_preserves_the_tombstone_for_retry() {
        let mut cleanup = cleanup_ready_for_key_deletion();
        let mut provider = FakeProvider::deleting(RecoveryKeyDeleteOutcome::Locked);

        assert_eq!(
            cleanup.delete_key(&mut provider).unwrap_err(),
            RecoveryKeyCleanupError::DeleteBlocked(RecoveryKeyDeleteBlocked::Locked)
        );
        assert_eq!(
            cleanup.phase(),
            RecoveryKeyCleanupPhase::WorkspaceRemovalDurable
        );
        assert!(matches!(
            cleanup.authorize_completion_tombstone_removal(),
            Err(RecoveryKeyCleanupError::InvalidTransition(
                RecoveryKeyCleanupPhase::WorkspaceRemovalDurable
            ))
        ));

        provider.next_delete = Some(RecoveryKeyDeleteOutcome::Missing);
        cleanup.delete_key(&mut provider).unwrap();
        cleanup.authorize_completion_tombstone_removal().unwrap();
        assert_eq!(
            cleanup.phase(),
            RecoveryKeyCleanupPhase::KeyDeletionConfirmed
        );
        assert_eq!(provider.delete_calls, 2);
    }

    #[test]
    fn every_blocking_delete_outcome_keeps_cleanup_retryable() {
        let cases = [
            (
                RecoveryKeyDeleteOutcome::Locked,
                RecoveryKeyDeleteBlocked::Locked,
            ),
            (
                RecoveryKeyDeleteOutcome::WrongIdentity,
                RecoveryKeyDeleteBlocked::WrongIdentity,
            ),
            (
                RecoveryKeyDeleteOutcome::Corrupt,
                RecoveryKeyDeleteBlocked::Corrupt,
            ),
            (
                RecoveryKeyDeleteOutcome::Unavailable,
                RecoveryKeyDeleteBlocked::Unavailable,
            ),
        ];

        for (outcome, expected) in cases {
            let mut cleanup = cleanup_ready_for_key_deletion();
            let mut provider = FakeProvider::deleting(outcome);
            assert_eq!(
                cleanup.delete_key(&mut provider).unwrap_err(),
                RecoveryKeyCleanupError::DeleteBlocked(expected)
            );
            assert_eq!(
                cleanup.phase(),
                RecoveryKeyCleanupPhase::WorkspaceRemovalDurable
            );
            assert_eq!(provider.delete_calls, 1);
        }
    }
}
