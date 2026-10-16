//! Independent test-only entry point for interop/CONTRACT.md, never a production API.
use super::*;
use std::{fs, os::unix::fs::MetadataExt};

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

fn oracle(path: &Path, length: usize) -> Vec<u8> {
    let info = fs::symlink_metadata(path).expect("oracle-metadata-failed");
    check(info.is_file() && info.uid() == unsafe { libc::geteuid() });
    check(info.mode() & 0o777 == 0o600 && info.nlink() == 1 && info.len() == length as u64);
    fs::read(path).expect("oracle-read-failed")
}

#[test]
#[ignore = "requires the explicitly invoked isolated Linux interop fixture"]
fn volatile_collection_interop() {
    let work =
        PathBuf::from(std::env::var_os("CPA_SS_FIXTURE_ROOT").expect("fixture-root-required"));
    let mode = std::env::var("CPA_SS_FIXTURE_MODE").expect("fixture-mode-required");
    let uid = unsafe { libc::geteuid() };
    check(uid != 0 && work.is_absolute());
    let info = fs::symlink_metadata(&work).expect("fixture-root-metadata");
    check(info.is_dir() && info.uid() == uid && info.mode() & 0o777 == 0o700);
    check(
        fs::read_to_string("/proc/self/cgroup")
            .expect("fixture-cgroup")
            .contains("cpa-ss-preflight.service"),
    );
    check(
        std::env::var("DBUS_SESSION_BUS_ADDRESS").ok()
            == Some(format!("unix:path={}", work.join("runtime/bus").display())),
    );
    check(std::env::var_os("HOME") == Some(work.join("home").into_os_string()));
    let transaction = ReplacementTransactionId::from_bytes([0x71; 16]).expect("fixed-transaction");
    let approval = if mode == "create" {
        SyntheticSecretServiceApproval::for_test_fresh(&work.join("lock"))
    } else {
        SyntheticSecretServiceApproval::for_test(&work.join("lock"))
    }
    .expect("fixture-approval-failed");
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
            _ => {
                // Read-only failure diagnosis. Never retry CreateItem or replace.
                let attributes = base_attributes(transaction, uid, provider.inner.lock_namespace);
                match provider.inner.backend.search(&attributes) {
                    Ok(items) if items.is_empty() => eprintln!("native-create-no-item"),
                    Ok(items) if items.len() == 1 => eprintln!("native-create-one-item"),
                    Ok(_) => eprintln!("native-create-multiple-items"),
                    Err(_) => eprintln!("native-search-failed"),
                }
                panic!("fixture-create-failed");
            }
        };
        // Exclusive fixture oracles, not provider storage or a fallback path.
        for (name, bytes) in [
            ("expected.key", created.key.expose().as_slice()),
            ("reference.bin", created.reference.opaque_reference()),
        ] {
            let mut file = std::fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(work.join(name))
                .expect("oracle-create-failed");
            file.write_all(bytes).expect("oracle-write-failed");
            file.sync_all().expect("oracle-flush-failed");
        }
        let again = match provider.create(transaction) {
            RecoveryKeyCreateOutcome::Created(value) => value,
            _ => panic!("fixture-retry-failed"),
        };
        check(again.reference == created.reference);
        check(verify_slices_are_equal(again.key.expose(), created.key.expose()).is_ok());
    } else {
        let reference = RecoveryKeyReference::new(
            PROVIDER_KIND,
            PROVIDER_VERSION,
            oracle(&work.join("reference.bin"), REFERENCE_BYTES),
        )
        .expect("oracle-reference-failed");
        match mode.as_str() {
            "reopen" => {
                let expected = Zeroizing::new(oracle(&work.join("expected.key"), KEY_BYTES));
                match provider.open(transaction, &reference) {
                    RecoveryKeyOpenOutcome::Available(key) => {
                        check(verify_slices_are_equal(key.expose(), &expected).is_ok());
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
