//! Independently authored, explicitly ignored A3 driver. Never a production API.
use super::*;
use crate::live::{
    cleanup_journal::{
        create_durable_cleanup_journal, load_durable_cleanup_journals, DurableCleanupJournal,
    },
    completion_tombstone::{
        load_completion_tombstones, persist_completion_tombstone, remove_completion_tombstone,
    },
    linux_replacement::{replace_existing_synthetic, SyntheticRootApproval, SyntheticValidation},
    recovery_key::RecoveryKeyCleanupPhase as Phase,
};
use std::{
    cell::Cell,
    fs,
    fs::OpenOptions,
    os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt},
};

// A3_JOURNAL_DIAGNOSTICS_START
// Independently authored bounded Result-only diagnostics, 2026-10-19.
// This entire driver is cfg(test). Never format an error or inspect the FS.
// https://doc.rust-lang.org/std/io/enum.ErrorKind.html
// https://doc.rust-lang.org/std/result/enum.Result.html#method.expect
use crate::live::cleanup_journal::CleanupJournalError;

fn journal_io_label(kind: io::ErrorKind) -> &'static str {
    match kind {
        io::ErrorKind::NotFound => "a3-journal-io-not-found",
        io::ErrorKind::PermissionDenied => "a3-journal-io-permission-denied",
        io::ErrorKind::AlreadyExists => "a3-journal-io-already-exists",
        io::ErrorKind::WouldBlock => "a3-journal-io-would-block",
        io::ErrorKind::NotADirectory => "a3-journal-io-not-a-directory",
        io::ErrorKind::IsADirectory => "a3-journal-io-is-a-directory",
        io::ErrorKind::DirectoryNotEmpty => "a3-journal-io-directory-not-empty",
        io::ErrorKind::ReadOnlyFilesystem => "a3-journal-io-read-only-filesystem",
        io::ErrorKind::InvalidInput => "a3-journal-io-invalid-input",
        io::ErrorKind::InvalidData => "a3-journal-io-invalid-data",
        io::ErrorKind::TimedOut => "a3-journal-io-timed-out",
        io::ErrorKind::WriteZero => "a3-journal-io-write-zero",
        io::ErrorKind::StorageFull => "a3-journal-io-storage-full",
        io::ErrorKind::QuotaExceeded => "a3-journal-io-quota-exceeded",
        io::ErrorKind::FileTooLarge => "a3-journal-io-file-too-large",
        io::ErrorKind::ResourceBusy => "a3-journal-io-resource-busy",
        io::ErrorKind::Deadlock => "a3-journal-io-deadlock",
        io::ErrorKind::CrossesDevices => "a3-journal-io-crosses-devices",
        io::ErrorKind::TooManyLinks => "a3-journal-io-too-many-links",
        io::ErrorKind::Interrupted => "a3-journal-io-interrupted",
        io::ErrorKind::Unsupported => "a3-journal-io-unsupported",
        io::ErrorKind::UnexpectedEof => "a3-journal-io-unexpected-eof",
        io::ErrorKind::OutOfMemory => "a3-journal-io-out-of-memory",
        io::ErrorKind::Other => "a3-journal-io-other",
        _ => "a3-journal-io-unlisted",
    }
}

fn journal_error_labels(error: &CleanupJournalError) -> (&'static str, Option<&'static str>) {
    let family = match error {
        CleanupJournalError::UnsupportedPlatform => "a3-journal-family-unsupported-platform",
        CleanupJournalError::InvalidRoot(_) => "a3-journal-family-invalid-root",
        CleanupJournalError::InvalidMetadata => "a3-journal-family-invalid-metadata",
        CleanupJournalError::Corrupt => "a3-journal-family-corrupt",
        CleanupJournalError::IncompleteWrite(_) => "a3-journal-family-incomplete-write",
        CleanupJournalError::Missing(_) => "a3-journal-family-missing",
        CleanupJournalError::AlreadyExists(_) => "a3-journal-family-already-exists",
        CleanupJournalError::TooMany => "a3-journal-family-too-many",
        CleanupJournalError::RecoveryConflict(_) => "a3-journal-family-recovery-conflict",
        CleanupJournalError::Cleanup(_) => "a3-journal-family-cleanup",
        CleanupJournalError::WorkspaceRemoval(_) => "a3-journal-family-workspace-removal",
        CleanupJournalError::WorkspaceRetirement(_) => "a3-journal-family-workspace-retirement",
        CleanupJournalError::CompletionTombstone(_) => "a3-journal-family-completion-tombstone",
        CleanupJournalError::Io { source, .. } => {
            return (
                "a3-journal-family-io",
                Some(journal_io_label(source.kind())),
            );
        }
    };
    (family, None)
}

fn a3_journal_result<T>(result: Result<T, CleanupJournalError>) -> T {
    match result {
        Ok(value) => value,
        Err(error) => {
            let (family, kind) = journal_error_labels(&error);
            eprintln!("{family}");
            if let Some(kind) = kind {
                eprintln!("{kind}");
            }
            // Preserve failure, without expect's Debug of the original Err.
            panic!("a3-journal-create");
        }
    }
}

#[cfg(test)]
mod journal_diagnostic_tests {
    use super::*;

    struct NoFormat;
    impl std::fmt::Debug for NoFormat {
        fn fmt(&self, _: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            panic!("PUBLIC_ERROR_DEBUG_MUST_NOT_RUN");
        }
    }
    impl std::fmt::Display for NoFormat {
        fn fmt(&self, _: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            panic!("PUBLIC_ERROR_DISPLAY_MUST_NOT_RUN");
        }
    }
    impl std::error::Error for NoFormat {}

    #[test]
    fn fixed_journal_error_families_ignore_all_payloads() {
        let path = PathBuf::from("PUBLIC_DO_NOT_PRINT_PATH");
        let cases = vec![
            (CleanupJournalError::UnsupportedPlatform, "a3-journal-family-unsupported-platform"),
            (CleanupJournalError::InvalidRoot(path.clone()), "a3-journal-family-invalid-root"),
            (CleanupJournalError::InvalidMetadata, "a3-journal-family-invalid-metadata"),
            (CleanupJournalError::Corrupt, "a3-journal-family-corrupt"),
            (CleanupJournalError::IncompleteWrite(path.clone()), "a3-journal-family-incomplete-write"),
            (CleanupJournalError::Missing(tx(0x91)), "a3-journal-family-missing"),
            (CleanupJournalError::AlreadyExists(tx(0x91)), "a3-journal-family-already-exists"),
            (CleanupJournalError::TooMany, "a3-journal-family-too-many"),
            (CleanupJournalError::RecoveryConflict(path.clone()), "a3-journal-family-recovery-conflict"),
            (CleanupJournalError::Cleanup(crate::live::recovery_key::RecoveryKeyCleanupError::InvalidTerminal), "a3-journal-family-cleanup"),
            (CleanupJournalError::WorkspaceRemoval(crate::live::workspace_removal::WorkspaceRemovalError::UnsupportedPlatform), "a3-journal-family-workspace-removal"),
            (CleanupJournalError::WorkspaceRetirement(crate::live::workspace_retirement::WorkspaceRetirementError::UnsupportedPlatform), "a3-journal-family-workspace-retirement"),
            (CleanupJournalError::CompletionTombstone(crate::live::completion_tombstone::CompletionTombstoneError::UnsupportedPlatform), "a3-journal-family-completion-tombstone"),
            (CleanupJournalError::Io { path, source: io::Error::new(io::ErrorKind::PermissionDenied, NoFormat) }, "a3-journal-family-io"),
        ];
        for (error, expected) in cases {
            let labels = journal_error_labels(&error);
            assert_eq!(labels.0, expected);
            assert_eq!(labels.1.is_some(), expected == "a3-journal-family-io");
        }
    }

    #[test]
    fn fixed_journal_io_whitelist_and_fallback_never_format_error() {
        let cases = [
            (io::ErrorKind::NotFound, "a3-journal-io-not-found"),
            (
                io::ErrorKind::PermissionDenied,
                "a3-journal-io-permission-denied",
            ),
            (io::ErrorKind::AlreadyExists, "a3-journal-io-already-exists"),
            (io::ErrorKind::WouldBlock, "a3-journal-io-would-block"),
            (
                io::ErrorKind::NotADirectory,
                "a3-journal-io-not-a-directory",
            ),
            (io::ErrorKind::IsADirectory, "a3-journal-io-is-a-directory"),
            (
                io::ErrorKind::DirectoryNotEmpty,
                "a3-journal-io-directory-not-empty",
            ),
            (
                io::ErrorKind::ReadOnlyFilesystem,
                "a3-journal-io-read-only-filesystem",
            ),
            (io::ErrorKind::InvalidInput, "a3-journal-io-invalid-input"),
            (io::ErrorKind::InvalidData, "a3-journal-io-invalid-data"),
            (io::ErrorKind::TimedOut, "a3-journal-io-timed-out"),
            (io::ErrorKind::WriteZero, "a3-journal-io-write-zero"),
            (io::ErrorKind::StorageFull, "a3-journal-io-storage-full"),
            (io::ErrorKind::QuotaExceeded, "a3-journal-io-quota-exceeded"),
            (io::ErrorKind::FileTooLarge, "a3-journal-io-file-too-large"),
            (io::ErrorKind::ResourceBusy, "a3-journal-io-resource-busy"),
            (io::ErrorKind::Deadlock, "a3-journal-io-deadlock"),
            (
                io::ErrorKind::CrossesDevices,
                "a3-journal-io-crosses-devices",
            ),
            (io::ErrorKind::TooManyLinks, "a3-journal-io-too-many-links"),
            (io::ErrorKind::Interrupted, "a3-journal-io-interrupted"),
            (io::ErrorKind::Unsupported, "a3-journal-io-unsupported"),
            (io::ErrorKind::UnexpectedEof, "a3-journal-io-unexpected-eof"),
            (io::ErrorKind::OutOfMemory, "a3-journal-io-out-of-memory"),
            (io::ErrorKind::Other, "a3-journal-io-other"),
            (io::ErrorKind::ConnectionRefused, "a3-journal-io-unlisted"),
        ];
        for (kind, expected) in cases {
            let error = CleanupJournalError::Io {
                path: PathBuf::from("PUBLIC_DO_NOT_PRINT_PATH"),
                source: io::Error::new(kind, NoFormat),
            };
            assert_eq!(
                journal_error_labels(&error),
                ("a3-journal-family-io", Some(expected))
            );
        }
    }

    #[test]
    fn same_result_preserves_success_and_panics_with_only_fixed_payload() {
        struct NotDebug(u8);
        let value = Box::new(NotDebug(7));
        let address = &*value as *const NotDebug;
        let value = a3_journal_result(Ok(value));
        assert!(std::ptr::eq(address, &*value));
        assert_eq!(value.0, 7);
        let calls = Cell::new(0);
        let result: Result<(), CleanupJournalError> = (|| {
            calls.set(calls.get() + 1);
            Err(CleanupJournalError::Io {
                path: PathBuf::from("PUBLIC_DO_NOT_PRINT_PATH"),
                source: io::Error::new(io::ErrorKind::PermissionDenied, NoFormat),
            })
        })();
        let outcome =
            std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| a3_journal_result(result)));
        match outcome {
            Err(payload) => assert_eq!(payload.downcast_ref::<&str>(), Some(&"a3-journal-create")),
            Ok(()) => panic!("PUBLIC_EXPECTED_FIXED_FAILURE"),
        }
        assert_eq!(calls.get(), 1);
    }
}
// A3_JOURNAL_DIAGNOSTICS_END

thread_local! {
    static ENABLED: Cell<bool> = const { Cell::new(false) };
    static DELETE_SENDS: Cell<usize> = const { Cell::new(0) };
}

pub(crate) fn delete_send() {
    if ENABLED.with(Cell::get) {
        DELETE_SENDS.with(|v| v.set(v.get() + 1));
        println!("A3_BACKEND_DELETE_SEND");
    }
}

pub(crate) fn checkpoint(name: &'static str) {
    if ENABLED.with(Cell::get) && std::env::var("CPA_A3_CRASH").ok().as_deref() == Some(name) {
        println!("A3_BOUNDARY {name}");
        io::stdout().flush().expect("a3-boundary-flush");
        // The parent retains this pipe without sending a byte, verifies our
        // exact owned PID/executable/cgroup, then SIGKILLs us at this boundary.
        let mut byte = [0u8];
        io::stdin()
            .read_exact(&mut byte)
            .expect("a3-boundary-unexpected-release");
        panic!("a3-boundary-unexpected-release");
    }
}

fn private_dir(path: &Path) {
    let m = fs::symlink_metadata(path).expect("a3-directory-metadata");
    assert!(
        m.is_dir()
            && m.uid() == unsafe { libc::geteuid() }
            && m.gid() == unsafe { libc::getegid() }
            && m.mode() & 0o7777 == 0o700,
        "a3-directory-invalid"
    );
}

fn write_private(path: &Path, bytes: &[u8]) {
    let parent = path.parent().expect("a3-private-parent");
    private_dir(parent);
    let mut f = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .expect("a3-private-create");
    let before = f.metadata().expect("a3-private-metadata");
    assert!(
        before.is_file()
            && before.uid() == unsafe { libc::geteuid() }
            && before.gid() == unsafe { libc::getegid() }
            && before.mode() & 0o7777 == 0o600
            && before.nlink() == 1
            && before.len() == 0,
        "a3-private-prewrite"
    );
    f.write_all(bytes).expect("a3-private-write");
    f.sync_all().expect("a3-private-sync");
    let after = f.metadata().expect("a3-private-metadata");
    assert!(
        after.dev() == before.dev()
            && after.ino() == before.ino()
            && after.nlink() == 1
            && after.mode() == before.mode()
            && after.uid() == before.uid()
            && after.gid() == before.gid()
            && after.len() == bytes.len() as u64,
        "a3-private-postwrite"
    );
    File::open(parent)
        .expect("a3-private-parent")
        .sync_all()
        .expect("a3-private-parent-sync");
    let named = fs::symlink_metadata(path).expect("a3-private-named");
    assert!(
        named.is_file()
            && named.dev() == after.dev()
            && named.ino() == after.ino()
            && named.mode() == after.mode()
            && named.uid() == after.uid()
            && named.gid() == after.gid()
            && named.nlink() == 1
            && named.len() == after.len(),
        "a3-private-named-changed"
    );
}

fn read_private(path: &Path, length: usize) -> Zeroizing<Vec<u8>> {
    private_dir(path.parent().expect("a3-private-parent"));
    let mut f = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .expect("a3-private-read");
    let m = f.metadata().expect("a3-private-metadata");
    assert!(
        m.is_file()
            && m.uid() == unsafe { libc::geteuid() }
            && m.gid() == unsafe { libc::getegid() }
            && m.mode() & 0o7777 == 0o600
            && m.nlink() == 1
            && m.len() == length as u64,
        "a3-private-invalid"
    );
    let mut v = Zeroizing::new(vec![0; length]);
    f.read_exact(&mut v).expect("a3-private-read");
    let mut end = [0];
    assert_eq!(
        f.read(&mut end).expect("a3-private-read"),
        0,
        "a3-private-long"
    );
    let after = f.metadata().expect("a3-private-metadata");
    assert!(
        m.dev() == after.dev()
            && m.ino() == after.ino()
            && m.len() == after.len()
            && after.nlink() == 1
            && after.mode() == m.mode(),
        "a3-private-changed"
    );
    v
}

fn reference(role: &Path) -> RecoveryKeyReference {
    RecoveryKeyReference::new(
        PROVIDER_KIND,
        PROVIDER_VERSION,
        read_private(&role.join("reference.bin"), REFERENCE_BYTES).to_vec(),
    )
    .expect("a3-reference")
}

fn tx(byte: u8) -> ReplacementTransactionId {
    ReplacementTransactionId::from_bytes([byte; 16]).expect("a3-transaction")
}

struct ObservedProvider {
    actual: LinuxSecretServiceRecoveryKeyProvider,
    role: PathBuf,
    allow_create: bool,
}

impl RecoveryKeyProvider for ObservedProvider {
    fn create_is_transaction_idempotent(&self) -> bool {
        self.actual.create_is_transaction_idempotent()
    }
    fn create(&mut self, id: ReplacementTransactionId) -> RecoveryKeyCreateOutcome {
        assert!(self.allow_create, "a3-replay-create-forbidden");
        let result = self.actual.create(id);
        if let RecoveryKeyCreateOutcome::Created(created) = &result {
            write_private(
                &self.role.join("reference.bin"),
                created.reference.opaque_reference(),
            );
            write_private(&self.role.join("expected.key"), created.key.expose());
        }
        result
    }
    fn open(
        &mut self,
        id: ReplacementTransactionId,
        r: &RecoveryKeyReference,
    ) -> RecoveryKeyOpenOutcome {
        self.actual.open(id, r)
    }
    fn delete(
        &mut self,
        id: ReplacementTransactionId,
        r: &RecoveryKeyReference,
    ) -> RecoveryKeyDeleteOutcome {
        let outcome = self.actual.delete(id, r);
        match outcome {
            RecoveryKeyDeleteOutcome::Deleted => println!("A3_DELETE_OUTCOME_DELETED"),
            RecoveryKeyDeleteOutcome::Missing => println!("A3_DELETE_OUTCOME_MISSING"),
            RecoveryKeyDeleteOutcome::WrongIdentity => println!("A3_DELETE_OUTCOME_WRONG_IDENTITY"),
            RecoveryKeyDeleteOutcome::Corrupt => println!("A3_DELETE_OUTCOME_CORRUPT"),
            _ => println!("A3_DELETE_OUTCOME_BLOCKED"),
        }
        outcome
    }
}

fn provider(work: &Path, role: &Path, fresh: bool) -> ObservedProvider {
    real_fixture::enable_diagnostics(role);
    let approval = if fresh {
        SyntheticSecretServiceApproval::for_test_fresh(&work.join("lock"))
    } else {
        SyntheticSecretServiceApproval::for_test(&work.join("lock"))
    }
    .expect("a3-lock-approval");
    ObservedProvider {
        actual: LinuxSecretServiceRecoveryKeyProvider::for_synthetic_session(approval)
            .expect("a3-provider"),
        role: role.to_path_buf(),
        allow_create: true,
    }
}

fn count(expected: usize) {
    assert_eq!(
        DELETE_SENDS.with(Cell::get),
        expected,
        "a3-delete-send-count"
    );
    println!(
        "{}",
        if expected == 0 {
            "A3_DELETE_SEND_ZERO"
        } else {
            "A3_DELETE_SEND_ONE"
        }
    );
}

fn early(cleanup: &mut DurableCleanupJournal, p: &mut ObservedProvider) {
    assert!(cleanup.delete_key(p).is_err(), "a3-early-delete-accepted");
    assert_eq!(DELETE_SENDS.with(Cell::get), 0, "a3-early-delete-sent");
    assert!(
        cleanup.authorize_completion_tombstone_removal().is_err(),
        "a3-early-tombstone-authorized"
    );
    println!("A3_EARLY_DELETE_REFUSED");
}

fn load(root: &Path) -> DurableCleanupJournal {
    let mut values = load_durable_cleanup_journals(root).expect("a3-journal-load");
    assert_eq!(values.len(), 1, "a3-journal-cardinality");
    values.remove(0)
}

fn tamper(root: &Path, cleanup: &mut DurableCleanupJournal, p: &mut ObservedProvider, case: &str) {
    let id = hex::encode(cleanup.transaction_id().as_bytes());
    let directory = root.join(".cpa-cloud-employee-config-cleanup");
    let proof = root
        .join(crate::live::workspace_removal::PROOF_DIRECTORY)
        .join(crate::live::workspace_removal::final_name(
            cleanup.transaction_id(),
        ));
    match case {
        "pending" => write_private(&directory.join(format!("{id}.3.pending")), b"incomplete"),
        "gap" => fs::rename(
            directory.join(format!("{id}.1.json")),
            root.join("retained-gap.json"),
        )
        .expect("a3-gap"),
        "proof-inode" => {
            let bytes = fs::read(&proof).expect("a3-proof-read");
            fs::rename(&proof, root.join("retained-proof.json")).expect("a3-proof-retain");
            write_private(&proof, &bytes);
        }
        "proof-hardlink" => {
            fs::hard_link(&proof, root.join("retained-proof-link.json")).expect("a3-proof-link")
        }
        "workspace-reappeared" => {
            let path = root
                .join(".cpa-cloud-employee-config-replacements")
                .join(format!("tx-{id}"));
            fs::create_dir(&path).expect("a3-workspace-create");
            fs::set_permissions(path, fs::Permissions::from_mode(0o700))
                .expect("a3-workspace-mode");
        }
        _ => panic!("a3-tamper-case"),
    }
    // Reload from disk; a pure reload error is also an evidence-preserving stop.
    if let Ok(mut values) = load_durable_cleanup_journals(root) {
        assert_eq!(values.len(), 1, "a3-journal-cardinality");
        let mut restarted = values.remove(0);
        assert!(
            restarted.delete_key(p).is_err(),
            "a3-tampered-delete-accepted"
        );
        assert_eq!(
            restarted.phase(),
            Phase::WorkspaceRemovalDurable,
            "a3-tampered-phase"
        );
        assert!(
            restarted.authorize_completion_tombstone_removal().is_err(),
            "a3-tampered-tombstone"
        );
    }
    count(0);
    let tombstones = load_completion_tombstones(root).expect("a3-blocked-tombstone-load");
    assert_eq!(tombstones.len(), 1, "a3-blocked-tombstone-cardinality");
    assert_eq!(
        tombstones[0].transaction_id(),
        cleanup.transaction_id(),
        "a3-blocked-tombstone-identity"
    );
    assert_eq!(
        tombstones[0].reference(),
        cleanup.reference(),
        "a3-blocked-tombstone-reference"
    );
    println!("A3_TAMPER_PRESERVED");
}

fn finish(root: &Path, mut cleanup: DurableCleanupJournal, p: &mut ObservedProvider, case: &str) {
    if cleanup.phase() == Phase::TerminalRecorded {
        early(&mut cleanup, p);
        let r = persist_completion_tombstone(
            root,
            cleanup.terminal(),
            cleanup.transaction_id(),
            cleanup.reference(),
        )
        .expect("a3-tombstone-persist");
        cleanup
            .record_completion_tombstone_durable(&r)
            .expect("a3-tombstone-record");
        checkpoint("tombstone");
    }
    if cleanup.phase() == Phase::CompletionTombstoneDurable {
        early(&mut cleanup, p);
        let retired = cleanup
            .retire_workspace_contents_durable()
            .expect("a3-retire");
        early(&mut cleanup, p);
        checkpoint("contents-retired");
        cleanup
            .remove_workspace_durable(&retired)
            .expect("a3-workspace-remove");
        checkpoint("workspace-removed");
    }
    if cleanup.phase() == Phase::WorkspaceRemovalDurable {
        if let Some(case) = case.strip_prefix("tamper-") {
            tamper(root, &mut cleanup, p, case);
            return;
        }
        cleanup.delete_key(p).expect("a3-key-delete");
        checkpoint("key-confirmed");
    }
    if cleanup.phase() == Phase::KeyDeletionConfirmed {
        let authorization = cleanup
            .authorize_completion_tombstone_removal()
            .expect("a3-remove-authorization");
        let receipt =
            remove_completion_tombstone(root, &authorization).expect("a3-tombstone-remove");
        checkpoint("inactive-proof");
        cleanup
            .record_completion_tombstone_removal_durable(receipt)
            .expect("a3-completion-record");
        checkpoint("complete");
    }
    assert_eq!(cleanup.phase(), Phase::Complete, "a3-not-complete");
    cleanup
        .verify_artifact_retirement_preconditions()
        .expect("a3-completion-proof");
    assert!(
        matches!(
            p.open(cleanup.transaction_id(), cleanup.reference()),
            RecoveryKeyOpenOutcome::Missing
        ),
        "a3-deleted-key-open"
    );
    assert!(load(root).phase() == Phase::Complete, "a3-durable-complete");
    println!("A3_DURABLE_COMPLETE");
}

#[test]
#[ignore = "requires explicitly invoked disposable A3 Linux Secret Service fixture"]
fn isolated_item_delete_and_cleanup() {
    ENABLED.with(|v| v.set(true));
    let work = PathBuf::from(std::env::var_os("CPA_SS_FIXTURE_ROOT").expect("a3-root-required"));
    private_dir(&work);
    assert!(
        work.is_absolute() && unsafe { libc::geteuid() } != 0,
        "a3-root-identity"
    );
    assert!(
        fs::read_to_string("/proc/self/cgroup")
            .expect("a3-cgroup")
            .contains("cpa-ss-preflight.service"),
        "a3-cgroup"
    );
    assert_eq!(
        std::env::var_os("HOME"),
        Some(work.join("home").into_os_string()),
        "a3-home"
    );
    assert_eq!(
        std::env::var("DBUS_SESSION_BUS_ADDRESS").ok(),
        Some(format!("unix:path={}", work.join("runtime/bus").display())),
        "a3-bus"
    );
    let mode = std::env::var("CPA_A3_MODE").expect("a3-mode");
    let case = std::env::var("CPA_A3_CASE").expect("a3-case");
    if mode == "duplicate-delete" {
        let mut p = provider(&work, &work, false);
        assert_eq!(
            p.delete(tx(0x71), &reference(&work)),
            RecoveryKeyDeleteOutcome::Corrupt,
            "a3-duplicate-delete"
        );
        count(0);
    } else {
        let role = work.join(if mode == "neighbor-create" {
            "neighbor"
        } else {
            "target"
        });
        private_dir(&role);
        let mut p = provider(&work, &role, mode == "neighbor-create");
        if mode == "neighbor-create" || mode == "exact-create" {
            assert!(
                matches!(
                    p.create(tx(if mode == "neighbor-create" {
                        0x90
                    } else {
                        0x91
                    })),
                    RecoveryKeyCreateOutcome::Created(_)
                ),
                "a3-create"
            );
            count(0);
        } else if mode == "exact-negatives" {
            let r = reference(&role);
            assert_eq!(
                p.delete(tx(0x92), &r),
                RecoveryKeyDeleteOutcome::WrongIdentity,
                "a3-wrong-identity"
            );
            let mut corrupt = r.opaque_reference().to_vec();
            corrupt[REFERENCE_BYTES - 1] ^= 1;
            let corrupt = RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, corrupt)
                .expect("a3-corrupt-reference");
            assert_eq!(
                p.delete(tx(0x91), &corrupt),
                RecoveryKeyDeleteOutcome::Corrupt,
                "a3-corrupt-outcome"
            );
            count(0);
        } else if mode == "exact-delete" || mode == "exact-missing" {
            assert_eq!(
                p.delete(tx(0x91), &reference(&role)),
                if mode == "exact-delete" {
                    RecoveryKeyDeleteOutcome::Deleted
                } else {
                    RecoveryKeyDeleteOutcome::Missing
                },
                "a3-exact-outcome"
            );
            count(usize::from(mode == "exact-delete"));
        } else if mode == "durable-start" {
            let root = work.join("transaction");
            private_dir(&root);
            let mut old = Zeroizing::new([0u8; 32]);
            let mut desired = Zeroizing::new([0u8; 32]);
            SystemRandom::new().fill(&mut old[..]).expect("a3-random");
            SystemRandom::new()
                .fill(&mut desired[..])
                .expect("a3-random");
            let old = Zeroizing::new(hex::encode(&old[..]).into_bytes());
            let desired = Zeroizing::new(hex::encode(&desired[..]).into_bytes());
            // Register and flush both actual synthetic credentials BEFORE any
            // target/desired-stage write. These are not response-derived oracles.
            write_private(&role.join("old.sample"), &old);
            write_private(&role.join("new.sample"), &desired);
            write_private(&root.join("config.json"), &old);
            let approval = SyntheticRootApproval::for_a3_test(&root);
            let validation = if case.starts_with("rollback-") {
                SyntheticValidation::Reject
            } else {
                SyntheticValidation::Accept
            };
            let receipt = replace_existing_synthetic(
                &approval,
                Path::new("config.json"),
                &desired,
                tx(0x91),
                &mut p,
                validation,
            )
            .expect("a3-real-replacement");
            println!("A3_SNAPSHOT_READY");
            io::stdout().flush().expect("a3-ready-flush");
            let mut release = [0u8];
            io::stdin()
                .read_exact(&mut release)
                .expect("a3-ready-release");
            assert_eq!(release, [b'c'], "a3-ready-release");
            let mut cleanup = a3_journal_result(create_durable_cleanup_journal(
                &root,
                receipt.into_terminal_handoff(),
            ));
            checkpoint("terminal");
            if case == "cross-root" || case == "cross-reference" {
                let other = work.join("other-transaction");
                let (receipt_root, id, r) = if case == "cross-root" {
                    (&other, tx(0x91), cleanup.reference().clone())
                } else {
                    (&root, tx(0x90), reference(&work.join("neighbor")))
                };
                let receipt =
                    persist_completion_tombstone(receipt_root, cleanup.terminal(), id, &r)
                        .expect("a3-cross-tombstone");
                assert!(
                    cleanup
                        .record_completion_tombstone_durable(&receipt)
                        .is_err(),
                    "a3-cross-bound-accepted"
                );
                assert_eq!(cleanup.phase(), Phase::TerminalRecorded, "a3-cross-phase");
                early(&mut cleanup, &mut p);
                count(0);
                println!("A3_CROSS_BOUND_REFUSED");
            } else {
                finish(&root, cleanup, &mut p, &case);
            }
        } else if mode == "durable-resume" {
            p.allow_create = false;
            let root = work.join("transaction");
            finish(&root, load(&root), &mut p, &case);
            let sends = DELETE_SENDS.with(Cell::get);
            assert!(sends <= 1, "a3-replay-extra-delete");
            count(sends);
        } else {
            panic!("a3-mode-invalid");
        }
    }
    println!("A3_DRIVER_OK");
}
