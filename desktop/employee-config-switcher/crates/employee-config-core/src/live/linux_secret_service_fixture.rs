//! Independent test-only entry point for interop/CONTRACT.md, never a production API.
use super::*;
use std::{cell::RefCell, fs, os::unix::fs::MetadataExt};

thread_local! {
    // Enabled only after the explicit native test has checked its fixture root,
    // UID, cgroup and private bus address. Normal contract tests have no oracle.
    static DIAGNOSTIC_ROOT: RefCell<Option<File>> = const { RefCell::new(None) };
    static REOPEN_TRACE: RefCell<Option<BTreeMap<&'static str, &'static str>>> = const { RefCell::new(None) };
}

const REOPEN_STAGES: &[&str] = &[
    "uid",
    "root-path",
    "root-metadata",
    "root-kind",
    "root-owner",
    "root-mode",
    "cgroup",
    "home",
    "bus-address",
    "diagnostic-root",
    "approval",
    "lock-setup",
    "bus-connect",
    "session-open",
    "session-check",
    "reference-build",
    "reference",
    "lock-open",
    "search",
    "search-attributes",
    "attributes",
    "get-decode",
    "get-session",
    "get-parameters",
    "get-type",
    "get-length",
    "get-envelope",
    "envelope",
    "open-outcome",
    "key-compare",
    "reference-oracle-metadata",
    "reference-oracle-kind",
    "reference-oracle-owner",
    "reference-oracle-mode",
    "reference-oracle-links",
    "reference-oracle-length",
    "reference-oracle-read",
    "key-oracle-metadata",
    "key-oracle-kind",
    "key-oracle-owner",
    "key-oracle-mode",
    "key-oracle-links",
    "key-oracle-length",
    "key-oracle-read",
];

struct ReopenTrace;
impl ReopenTrace {
    fn start(enabled: bool) -> Self {
        REOPEN_TRACE.with(|trace| {
            *trace.borrow_mut() = enabled.then(|| {
                REOPEN_STAGES
                    .iter()
                    .map(|stage| (*stage, "not-reached"))
                    .collect()
            })
        });
        Self
    }
}
impl Drop for ReopenTrace {
    fn drop(&mut self) {
        // Final fixed statuses survive an ordinary assertion unwind. Ignore
        // stderr I/O failure instead of panicking again during unwinding.
        let trace = REOPEN_TRACE.with(|trace| trace.borrow_mut().take());
        if let Some(trace) = trace {
            for (stage, status) in trace {
                let _ = writeln!(io::stderr().lock(), "reopen-{stage}-{status}");
            }
        }
    }
}

pub(super) fn stage(name: &'static str, ok: bool) {
    REOPEN_TRACE.with(|trace| {
        if let Some(trace) = trace.borrow_mut().as_mut() {
            if let Some(status) = trace.get_mut(name) {
                *status = if ok { "ok" } else { "failed" };
            }
        }
    });
}

fn stage_check(name: &'static str, ok: bool) {
    stage(name, ok);
    check(ok);
}

#[test]
fn reopen_trace_defaults_to_not_reached_and_uses_only_fixed_statuses() {
    let trace = ReopenTrace::start(true);
    REOPEN_TRACE.with(|state| {
        let state = state.borrow();
        let state = state.as_ref().unwrap();
        assert_eq!(state.len(), REOPEN_STAGES.len());
        assert!(state.values().all(|value| *value == "not-reached"));
    });
    stage("reference-oracle-mode", false);
    stage("root-path", true);
    stage("not-a-stage", true);
    REOPEN_TRACE.with(|state| {
        let state = state.borrow();
        let state = state.as_ref().unwrap();
        assert_eq!(state["reference-oracle-mode"], "failed");
        assert_eq!(state["root-path"], "ok");
        assert_eq!(state["key-compare"], "not-reached");
        assert_eq!(state.len(), REOPEN_STAGES.len());
    });
    drop(trace);
    REOPEN_TRACE.with(|state| assert!(state.borrow().is_none()));
}

fn enable_diagnostics(work: &Path) {
    stage("diagnostic-root", false);
    let approval =
        open_approved_directory(work, false).unwrap_or_else(|_| panic!("diagnostic-root-failed"));
    DIAGNOSTIC_ROOT.with(|root| *root.borrow_mut() = Some(approval.directory));
    stage("diagnostic-root", true);
}

fn write_sample(directory: &File, name: &str, value: &[u8]) -> io::Result<()> {
    let name = CString::new(name).map_err(|_| io::ErrorKind::InvalidInput)?;
    let fd = unsafe {
        libc::openat(
            directory.as_raw_fd(),
            name.as_ptr(),
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            0o600,
        )
    };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    let mut file = unsafe { File::from_raw_fd(fd) };
    file.write_all(value)?;
    file.sync_all()
}

/// Capture the actual original random Key and envelope before the first secret
/// is sent. Failure prevents sending. Never derive an oracle from a response.
pub(super) fn capture_outgoing(key: &BackupKey, envelope: &[u8]) -> bool {
    DIAGNOSTIC_ROOT.with(|root| {
        let root = root.borrow();
        let Some(directory) = root.as_ref() else {
            return true;
        };
        let result = (|| {
            if envelope.len() != ENVELOPE_BYTES {
                return Err(io::ErrorKind::InvalidData.into());
            }
            write_sample(directory, "sent.key", key.expose())?;
            write_sample(directory, "sent.envelope", envelope)?;
            directory.sync_all()
        })();
        if result.is_err() {
            eprintln!("pre-send-oracle-failed");
        }
        result.is_ok()
    })
}

fn sent_envelope(directory: &File) -> Option<Zeroizing<Vec<u8>>> {
    let name = CString::new("sent.envelope").ok()?;
    let fd = unsafe {
        libc::openat(
            directory.as_raw_fd(),
            name.as_ptr(),
            libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
        )
    };
    if fd < 0 {
        return None;
    }
    let mut file = unsafe { File::from_raw_fd(fd) };
    let info = file.metadata().ok()?;
    if !info.is_file()
        || info.uid() != unsafe { libc::geteuid() }
        || info.mode() & 0o777 != 0o600
        || info.nlink() != 1
        || info.len() != ENVELOPE_BYTES as u64
    {
        return None;
    }
    let mut bytes = Zeroizing::new(vec![0; ENVELOPE_BYTES]);
    file.read_exact(&mut bytes).ok()?;
    Some(bytes)
}

fn metadata_categories(
    session_matches: bool,
    parameters: &[u8],
    content_type: &str,
    value: &[u8],
) -> [&'static str; 4] {
    [
        if session_matches {
            "native-session-match"
        } else {
            "native-session-mismatch"
        },
        if parameters.is_empty() {
            "native-parameters-empty"
        } else {
            "native-parameters-nonempty"
        },
        match content_type {
            "application/octet-stream" => "native-type-octet-stream",
            "text/plain" => "native-type-text-plain",
            "" => "native-type-empty",
            _ => "native-type-other",
        },
        match value.len().cmp(&ENVELOPE_BYTES) {
            std::cmp::Ordering::Equal => "native-length-exact",
            std::cmp::Ordering::Less => "native-length-short",
            std::cmp::Ordering::Greater => "native-length-long",
        },
    ]
}

fn envelope_category(value: &[u8], original: Option<&[u8]>) -> &'static str {
    let Some(original) = original.filter(|bytes| bytes.len() == ENVELOPE_BYTES) else {
        return "native-envelope-not-checked";
    };
    if value.len() != ENVELOPE_BYTES || &value[..16] != ENVELOPE_MAGIC {
        return "native-envelope-format";
    }
    let mut hasher = Sha256::new();
    hasher.update(ENVELOPE_CONTEXT);
    hasher.update(&value[..116]);
    if verify_slices_are_equal(&hasher.finalize(), &value[116..]).is_err() {
        return "native-envelope-checksum";
    }
    // Identity includes the original Key bytes, not merely public identity fields.
    if verify_slices_are_equal(&value[16..116], &original[16..116]).is_err() {
        return "native-envelope-identity";
    }
    "native-envelope-valid"
}

#[test]
fn diagnostic_metadata_categories_are_fixed() {
    assert_eq!(
        metadata_categories(true, &[], CONTENT_TYPE, &[0; ENVELOPE_BYTES]),
        [
            "native-session-match",
            "native-parameters-empty",
            "native-type-octet-stream",
            "native-length-exact"
        ]
    );
    assert_eq!(
        metadata_categories(false, &[1], "text/plain", &[]),
        [
            "native-session-mismatch",
            "native-parameters-nonempty",
            "native-type-text-plain",
            "native-length-short"
        ]
    );
    assert_eq!(
        metadata_categories(true, &[], "", &[0; ENVELOPE_BYTES + 1])[2..],
        ["native-type-empty", "native-length-long"]
    );
    assert_eq!(
        metadata_categories(true, &[], "arbitrary-private-value", &[])[2],
        "native-type-other"
    );
}

#[test]
fn diagnostic_envelope_categories_use_original_send_not_response_oracle() {
    fn checksum(bytes: &mut [u8]) {
        let mut hasher = Sha256::new();
        hasher.update(ENVELOPE_CONTEXT);
        hasher.update(&bytes[..116]);
        bytes[116..].copy_from_slice(&hasher.finalize());
    }
    let mut original = [0x51; ENVELOPE_BYTES];
    original[..16].copy_from_slice(ENVELOPE_MAGIC);
    checksum(&mut original);
    assert_eq!(
        envelope_category(&original, None),
        "native-envelope-not-checked"
    );
    assert_eq!(
        envelope_category(&original, Some(&[])),
        "native-envelope-not-checked"
    );
    assert_eq!(
        envelope_category(&original, Some(&original)),
        "native-envelope-valid"
    );
    assert_eq!(
        envelope_category(&[], Some(&original)),
        "native-envelope-format"
    );
    let mut altered = original;
    altered[0] ^= 1;
    assert_eq!(
        envelope_category(&altered, Some(&original)),
        "native-envelope-format"
    );
    altered = original;
    altered[116] ^= 1;
    assert_eq!(
        envelope_category(&altered, Some(&original)),
        "native-envelope-checksum"
    );
    for offset in [16, 32, 36, 68, 84] {
        altered = original;
        altered[offset] ^= 1;
        checksum(&mut altered);
        assert_eq!(
            envelope_category(&altered, Some(&original)),
            "native-envelope-identity"
        );
    }
}

pub(super) fn decoded_result(ok: bool) {
    stage("get-decode", ok);
    DIAGNOSTIC_ROOT.with(|root| {
        if root.borrow().is_some() {
            eprintln!(
                "{}",
                if ok {
                    "native-decode-ok"
                } else {
                    "native-decode-failed"
                }
            );
            if !ok {
                eprintln!("native-envelope-not-checked");
            }
        }
    });
}

/// Called once on the original typed response before the unchanged rejection.
/// No service query, response mutation, raw output or acceptance decision here.
pub(super) fn returned_secret(
    session_matches: bool,
    parameters: &[u8],
    content_type: &str,
    value: &[u8],
) {
    DIAGNOSTIC_ROOT.with(|root| {
        let root = root.borrow();
        let Some(directory) = root.as_ref() else {
            return;
        };
        for category in metadata_categories(session_matches, parameters, content_type, value) {
            eprintln!("{category}");
        }
        stage("get-session", session_matches);
        stage("get-parameters", parameters.is_empty());
        stage(
            "get-type",
            matches!(content_type, CONTENT_TYPE | "text/plain"),
        );
        stage("get-length", value.len() == ENVELOPE_BYTES);
        let original = sent_envelope(directory);
        let category = envelope_category(value, original.as_deref().map(|bytes| bytes.as_slice()));
        stage("get-envelope", category == "native-envelope-valid");
        eprintln!("{}", category);
    });
}

pub(super) fn backend_error(error: &dbus::Error) {
    // Never print the daemon's error message or arbitrary error name.
    let label = match error.name() {
        Some("org.freedesktop.DBus.Error.AccessDenied") => "native-access-denied",
        Some("org.freedesktop.DBus.Error.UnknownMethod") => "native-unknown-method",
        Some("org.freedesktop.DBus.Error.UnknownObject") => "native-unknown-object",
        Some("org.freedesktop.DBus.Error.InvalidArgs") => "native-invalid-args",
        Some("org.freedesktop.Secret.Error.IsLocked") => "native-locked",
        Some("org.freedesktop.Secret.Error.NoSuchObject") => "native-missing",
        Some("org.freedesktop.Secret.Error.NoSession") => "native-no-session",
        _ => "native-other-error",
    };
    eprintln!("{label}");
}

fn check(value: bool) {
    // Do not include compared values, errors, paths, keys or envelopes in panics.
    assert!(value, "fixture-check-failed");
}

#[derive(Clone, Copy)]
enum OracleRole {
    Reference,
    Key,
}

impl OracleRole {
    fn label(self) -> &'static str {
        match self {
            Self::Reference => "reference",
            Self::Key => "key",
        }
    }
    fn filename(self) -> &'static str {
        match self {
            Self::Reference => "reference.bin",
            Self::Key => "expected.key",
        }
    }
}

const ORACLE_WRITE_STAGES: [&str; 8] = [
    "directory",
    "create",
    "prewrite",
    "write",
    "sync",
    "postwrite",
    "directory-sync",
    "handoff",
];
struct OracleWriteTrace {
    role: OracleRole,
    states: [&'static str; 8],
}
impl OracleWriteTrace {
    fn new(role: OracleRole) -> Self {
        Self {
            role,
            states: ["not-reached"; 8],
        }
    }
    fn check(&mut self, index: usize, ok: bool) -> Result<(), ()> {
        self.states[index] = if ok { "ok" } else { "failed" };
        if ok {
            Ok(())
        } else {
            Err(())
        }
    }
}
impl Drop for OracleWriteTrace {
    fn drop(&mut self) {
        for (phase, status) in ORACLE_WRITE_STAGES.iter().zip(self.states) {
            let _ = writeln!(
                io::stderr().lock(),
                "oracle-write-{}-{phase}-{status}",
                self.role.label()
            );
        }
    }
}

fn private_oracle_metadata(
    mode: u32,
    uid: u32,
    links: u64,
    length: u64,
    expected_uid: u32,
    expected_length: u64,
) -> bool {
    mode & libc::S_IFMT == libc::S_IFREG
        && mode & 0o7777 == 0o600
        && uid == expected_uid
        && links == 1
        && length == expected_length
}

fn checked_oracle_metadata(file: &File, uid: u32, length: u64) -> Option<fs::Metadata> {
    file.metadata().ok().filter(|info| {
        private_oracle_metadata(
            info.mode(),
            info.uid(),
            info.nlink(),
            info.len(),
            uid,
            length,
        )
    })
}

fn write_handoff_oracle(
    directory: &File,
    trace: &mut OracleWriteTrace,
    bytes: &[u8],
) -> Result<(), ()> {
    let uid = unsafe { libc::geteuid() };
    let safe_directory = directory
        .metadata()
        .is_ok_and(|info| info.is_dir() && info.uid() == uid && info.mode() & 0o7777 == 0o700);
    trace.check(0, safe_directory)?;
    let name = CString::new(trace.role.filename()).map_err(|_| ())?;
    // Explicit creation mode, exclusive/no-follow, anchored to the approved fd.
    // Never open an existing file for writing or repair it with chmod.
    let fd = unsafe {
        libc::openat(
            directory.as_raw_fd(),
            name.as_ptr(),
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            0o600,
        )
    };
    trace.check(1, fd >= 0)?;
    let mut file = unsafe { File::from_raw_fd(fd) };
    let before = checked_oracle_metadata(&file, uid, 0);
    trace.check(2, before.is_some())?;
    let before = before.ok_or(())?;
    // No Key/reference byte may be written before all prewrite predicates pass.
    trace.check(3, file.write_all(bytes).is_ok())?;
    trace.check(4, file.sync_all().is_ok())?;
    let after = checked_oracle_metadata(&file, uid, bytes.len() as u64);
    trace.check(
        5,
        after.is_some_and(|info| info.dev() == before.dev() && info.ino() == before.ino()),
    )?;
    trace.check(6, directory.sync_all().is_ok())?;
    // Metadata-only handoff check; no reopen/read of the secret or pathname output.
    let mut entry = std::mem::MaybeUninit::<libc::stat>::uninit();
    let found = unsafe {
        libc::fstatat(
            directory.as_raw_fd(),
            name.as_ptr(),
            entry.as_mut_ptr(),
            libc::AT_SYMLINK_NOFOLLOW,
        )
    } == 0;
    let matches = if found {
        let entry = unsafe { entry.assume_init() };
        entry.st_dev == before.dev()
            && entry.st_ino == before.ino()
            && private_oracle_metadata(
                entry.st_mode,
                entry.st_uid,
                entry.st_nlink,
                entry.st_size as u64,
                uid,
                bytes.len() as u64,
            )
    } else {
        false
    };
    trace.check(7, matches)
}

fn write_handoff_oracles(created: &CreatedRecoveryKey) -> Result<(), ()> {
    // Both roles are initialized before any attempt, so a failed first role
    // leaves the unattempted second role explicitly not-reached.
    let mut traces = [
        OracleWriteTrace::new(OracleRole::Key),
        OracleWriteTrace::new(OracleRole::Reference),
    ];
    DIAGNOSTIC_ROOT.with(|root| {
        let root = root.borrow();
        let directory = root.as_ref().ok_or(())?;
        for (trace, bytes) in traces.iter_mut().zip([
            created.key.expose().as_slice(),
            created.reference.opaque_reference(),
        ]) {
            write_handoff_oracle(directory, trace, bytes)?;
        }
        Ok(())
    })
}

#[test]
fn handoff_metadata_rejects_each_unsafe_prewrite_property() {
    let mode = libc::S_IFREG | 0o600;
    assert!(private_oracle_metadata(mode, 1001, 1, 0, 1001, 0));
    for changed in [
        libc::S_IFDIR | 0o600,
        libc::S_IFLNK | 0o600,
        libc::S_IFREG | 0o644,
        libc::S_IFREG | 0o400,
        libc::S_IFREG | 0o4600,
    ] {
        assert!(!private_oracle_metadata(changed, 1001, 1, 0, 1001, 0));
    }
    assert!(!private_oracle_metadata(mode, 1002, 1, 0, 1001, 0));
    assert!(!private_oracle_metadata(mode, 1001, 2, 0, 1001, 0));
    assert!(!private_oracle_metadata(mode, 1001, 1, 1, 1001, 0));
    assert!(private_oracle_metadata(mode, 1001, 1, 32, 1001, 32));
    assert!(!private_oracle_metadata(mode, 1001, 1, 31, 1001, 32));
    let mut trace = OracleWriteTrace::new(OracleRole::Key);
    assert!(trace.check(2, false).is_err());
    assert_eq!(trace.states[2], "failed");
    assert!(trace.states[3..]
        .iter()
        .all(|status| *status == "not-reached"));
}

#[test]
fn handoff_writer_creates_private_files_and_refuses_existing_or_symlink_entries() {
    use std::os::unix::fs::{symlink, PermissionsExt};
    for role in [OracleRole::Key, OracleRole::Reference] {
        let root = tempfile::tempdir().unwrap();
        fs::set_permissions(root.path(), fs::Permissions::from_mode(0o700)).unwrap();
        let directory = File::open(root.path()).unwrap();
        let public_sample = [0x51; 32]; // Public dummy bytes, not a generated Key.
        let mut trace = OracleWriteTrace::new(role);
        assert!(write_handoff_oracle(&directory, &mut trace, &public_sample).is_ok());
        assert!(trace.states.iter().all(|status| *status == "ok"));
        let info = fs::symlink_metadata(root.path().join(role.filename())).unwrap();
        assert!(private_oracle_metadata(
            info.mode(),
            info.uid(),
            info.nlink(),
            info.len(),
            unsafe { libc::geteuid() },
            32
        ));
        let mut duplicate = OracleWriteTrace::new(role);
        assert!(write_handoff_oracle(&directory, &mut duplicate, b"must-not-write").is_err());
        assert_eq!(duplicate.states[1], "failed");
        assert!(duplicate.states[2..]
            .iter()
            .all(|status| *status == "not-reached"));
        assert_eq!(
            fs::symlink_metadata(root.path().join(role.filename()))
                .unwrap()
                .len(),
            32
        );

        let other = tempfile::tempdir().unwrap();
        fs::set_permissions(other.path(), fs::Permissions::from_mode(0o700)).unwrap();
        symlink(
            root.path().join(role.filename()),
            other.path().join(role.filename()),
        )
        .unwrap();
        let mut alias = OracleWriteTrace::new(role);
        assert!(write_handoff_oracle(
            &File::open(other.path()).unwrap(),
            &mut alias,
            b"must-not-write"
        )
        .is_err());
        assert_eq!(alias.states[1], "failed");
        assert!(alias.states[2..]
            .iter()
            .all(|status| *status == "not-reached"));
        assert_eq!(
            fs::symlink_metadata(root.path().join(role.filename()))
                .unwrap()
                .len(),
            32
        );
    }
}

fn oracle(path: &Path, length: usize, role: OracleRole) -> Vec<u8> {
    let stages = match role {
        OracleRole::Reference => [
            "reference-oracle-metadata",
            "reference-oracle-kind",
            "reference-oracle-owner",
            "reference-oracle-mode",
            "reference-oracle-links",
            "reference-oracle-length",
            "reference-oracle-read",
        ],
        OracleRole::Key => [
            "key-oracle-metadata",
            "key-oracle-kind",
            "key-oracle-owner",
            "key-oracle-mode",
            "key-oracle-links",
            "key-oracle-length",
            "key-oracle-read",
        ],
    };
    let metadata = fs::symlink_metadata(path);
    stage(stages[0], metadata.is_ok());
    let info = metadata.unwrap_or_else(|_| panic!("oracle-metadata-failed"));
    let owner_matches = info.uid() == unsafe { libc::geteuid() };
    for (name, ok) in stages[1..6].iter().zip([
        info.is_file(),
        owner_matches,
        info.mode() & 0o777 == 0o600,
        info.nlink() == 1,
        info.len() == length as u64,
    ]) {
        stage(name, ok);
    }
    check(info.is_file() && owner_matches);
    check(info.mode() & 0o777 == 0o600 && info.nlink() == 1 && info.len() == length as u64);
    let result = fs::read(path);
    stage(stages[6], result.is_ok());
    result.unwrap_or_else(|_| panic!("oracle-read-failed"))
}

#[test]
#[ignore = "requires the explicitly invoked isolated Linux interop fixture"]
fn volatile_collection_interop() {
    let mode = std::env::var("CPA_SS_FIXTURE_MODE").expect("fixture-mode-required");
    let _trace = ReopenTrace::start(mode == "reopen");
    stage("root-path", false);
    let work =
        PathBuf::from(std::env::var_os("CPA_SS_FIXTURE_ROOT").expect("fixture-root-required"));
    let uid = unsafe { libc::geteuid() };
    stage_check("uid", uid != 0);
    stage_check("root-path", work.is_absolute());
    let metadata = fs::symlink_metadata(&work);
    stage("root-metadata", metadata.is_ok());
    let info = metadata.unwrap_or_else(|_| panic!("fixture-root-metadata"));
    stage("root-kind", info.is_dir());
    stage("root-owner", info.uid() == uid);
    stage("root-mode", info.mode() & 0o777 == 0o700);
    check(info.is_dir() && info.uid() == uid && info.mode() & 0o777 == 0o700);
    stage("cgroup", false);
    stage_check(
        "cgroup",
        fs::read_to_string("/proc/self/cgroup")
            .expect("fixture-cgroup")
            .contains("cpa-ss-preflight.service"),
    );
    stage_check(
        "bus-address",
        std::env::var("DBUS_SESSION_BUS_ADDRESS").ok()
            == Some(format!("unix:path={}", work.join("runtime/bus").display())),
    );
    stage_check(
        "home",
        std::env::var_os("HOME") == Some(work.join("home").into_os_string()),
    );
    enable_diagnostics(&work);
    let transaction = ReplacementTransactionId::from_bytes([0x71; 16]).expect("fixed-transaction");
    stage("approval", false);
    let approval = if mode == "create" {
        SyntheticSecretServiceApproval::for_test_fresh(&work.join("lock"))
    } else {
        SyntheticSecretServiceApproval::for_test(&work.join("lock"))
    }
    .expect("fixture-approval-failed");
    stage("approval", true);
    let candidate = LinuxSecretServiceRecoveryKeyProvider::for_synthetic_session(approval);
    if mode == "no-service" {
        check(matches!(
            candidate,
            Err(LinuxSecretServiceProviderError::SessionUnavailable)
        ));
        println!("CPA_FIXTURE_OK");
        return;
    }
    let mut provider = candidate.expect("fixture-provider-failed");
    if mode == "create" {
        let created = match provider.create(transaction) {
            RecoveryKeyCreateOutcome::Created(value) => value,
            _ => panic!("fixture-create-failed"),
        };
        write_handoff_oracles(&created).unwrap_or_else(|_| panic!("oracle-create-failed"));
        let again = match provider.create(transaction) {
            RecoveryKeyCreateOutcome::Created(value) => value,
            _ => panic!("fixture-retry-failed"),
        };
        check(again.reference == created.reference);
        check(verify_slices_are_equal(again.key.expose(), created.key.expose()).is_ok());
    } else {
        let reference_bytes = oracle(
            &work.join("reference.bin"),
            REFERENCE_BYTES,
            OracleRole::Reference,
        );
        stage("reference-build", false);
        let reference = RecoveryKeyReference::new(PROVIDER_KIND, PROVIDER_VERSION, reference_bytes)
            .expect("oracle-reference-failed");
        stage("reference-build", true);
        match mode.as_str() {
            "reopen" => {
                let expected = Zeroizing::new(oracle(
                    &work.join("expected.key"),
                    KEY_BYTES,
                    OracleRole::Key,
                ));
                let outcome = provider.open(transaction, &reference);
                stage(
                    "open-outcome",
                    matches!(&outcome, RecoveryKeyOpenOutcome::Available(_)),
                );
                match outcome {
                    RecoveryKeyOpenOutcome::Available(key) => {
                        stage_check(
                            "key-compare",
                            verify_slices_are_equal(key.expose(), &expected).is_ok(),
                        );
                    }
                    _ => panic!("fixture-reopen-failed"),
                }
            }
            "wrong-transaction" => {
                let wrong =
                    ReplacementTransactionId::from_bytes([0x72; 16]).expect("fixed-transaction");
                check(matches!(
                    provider.open(wrong, &reference),
                    RecoveryKeyOpenOutcome::WrongIdentity
                ));
            }
            "wrong-namespace" => {
                let approval =
                    SyntheticSecretServiceApproval::for_test_fresh(&work.join("other-lock"))
                        .expect("fixture-other-approval-failed");
                let mut other =
                    LinuxSecretServiceRecoveryKeyProvider::for_synthetic_session(approval)
                        .expect("fixture-other-provider-failed");
                check(matches!(
                    other.open(transaction, &reference),
                    RecoveryKeyOpenOutcome::WrongIdentity
                ));
            }
            "missing-item" => {
                let missing =
                    ReplacementTransactionId::from_bytes([0x73; 16]).expect("fixed-transaction");
                let reference = RecoveryKeyReference::new(
                    PROVIDER_KIND,
                    PROVIDER_VERSION,
                    encode_reference(missing, uid, provider.inner.lock_namespace, [0x74; 16]),
                )
                .expect("fixture-missing-reference-failed");
                check(matches!(
                    provider.open(missing, &reference),
                    RecoveryKeyOpenOutcome::Missing
                ));
            }
            _ => panic!("fixture-mode-invalid"),
        }
    }
    println!("CPA_FIXTURE_OK");
}
