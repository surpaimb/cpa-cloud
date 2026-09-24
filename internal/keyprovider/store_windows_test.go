//go:build windows

package keyprovider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsDPAPIUserVersionLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-store")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if ready, reason := store.Ready(); !ready || reason != ReasonReady {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}

	prepared1, err := store.PrepareVersion(context.Background(), "bkp_synthetic", 1)
	if err != nil {
		t.Fatal(err)
	}
	material1, err := store.Resolve(context.Background(), "bkp_synthetic", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer material1.Destroy()
	protected1, err := os.ReadFile(prepared1.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(protected1, material1.Key[:]) {
		t.Fatal("protected store contains the plaintext wrapping key")
	}
	prepared1.Commit()

	if _, err := store.PrepareVersion(context.Background(), "bkp_synthetic", 1); err == nil {
		t.Fatal("duplicate provider version overwrote an existing version")
	}
	prepared2, err := store.PrepareVersion(context.Background(), "bkp_synthetic", 2)
	if err != nil {
		t.Fatal(err)
	}
	material2, err := store.Resolve(context.Background(), "bkp_synthetic", 2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(material1.Key[:], material2.Key[:]) {
		material2.Destroy()
		t.Fatal("rotation reused wrapping key material")
	}
	material2.Destroy()
	if err := prepared2.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(prepared2.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left prepared version: %v", err)
	}
	oldMaterial, err := store.Resolve(context.Background(), "bkp_synthetic", 1)
	if err != nil {
		t.Fatalf("rollback damaged old version: %v", err)
	}
	oldMaterial.Destroy()

	prepared2, err = store.PrepareVersion(context.Background(), "bkp_synthetic", 2)
	if err != nil {
		t.Fatal(err)
	}
	prepared2.Commit()
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		material, err := reopened.Resolve(context.Background(), "bkp_synthetic", version)
		if err != nil {
			t.Fatalf("resolve version %d: %v", version, err)
		}
		material.Destroy()
	}
}

func TestWindowsDPAPIBindingCorruptionCancellationAndBounds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-store")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareVersion(context.Background(), "bkp_one", 1)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()

	encoded, err := os.ReadFile(prepared.path)
	if err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(root, versionFilename("bkp_two", 1))
	if err := os.WriteFile(otherPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "bkp_two", 1); err == nil {
		t.Fatal("DPAPI file was not bound to provider identity")
	}

	encoded[len(encoded)-1] ^= 1
	if err := os.WriteFile(prepared.path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), "bkp_one", 1); err == nil {
		t.Fatal("damaged DPAPI ciphertext was accepted")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PrepareVersion(cancelled, "bkp_cancelled", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prepare error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, versionFilename("bkp_cancelled", 1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled prepare published a file: %v", err)
	}
	for _, testCase := range []struct {
		name       string
		providerID string
		version    uint64
	}{
		{"path-separator", "../escape", 1},
		{"uppercase-collision", "bkp_A", 1},
		{"zero-version", "bkp_valid", 0},
		{"js-unsafe-version", "bkp_valid", MaxPublicVersion + 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := store.PrepareVersion(context.Background(), testCase.providerID, testCase.version); err == nil {
				t.Fatal("invalid provider reference was accepted")
			}
		})
	}
}

func TestWindowsDPAPIRollbackRefusesChangedPreparedFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-store")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareVersion(context.Background(), "bkp_rollback", 1)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(prepared.path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(original)
	changed[len(changed)-1] ^= 1
	if err := os.WriteFile(prepared.path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(); err == nil {
		t.Fatal("rollback deleted or accepted a changed prepared file")
	}
	if _, err := os.Lstat(prepared.path); err != nil {
		t.Fatalf("changed prepared file was removed: %v", err)
	}
	if err := os.WriteFile(prepared.path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("rollback exact prepared file: %v", err)
	}
	if _, err := os.Lstat(prepared.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact prepared file remains: %v", err)
	}
}

func TestWindowsDPAPIRejectsNetworkAndDeviceStores(t *testing.T) {
	for _, path := range []string{
		`\\server\share\cpa-cloud-keys`,
		`\\?\C:\cpa-cloud-keys`,
		`\\.\C:\cpa-cloud-keys`,
	} {
		if _, err := Open(path); err == nil {
			t.Fatalf("unsafe provider store path was accepted: %q", path)
		}
	}
}

func TestWindowsDPAPIStoreUsesProtectedDACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-store")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareVersion(context.Background(), "bkp_acl", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Rollback()
	for _, path := range []string{root, prepared.path} {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := descriptor.Control()
		if err != nil {
			t.Fatal(err)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("DACL remains inheritable for %s", path)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if dacl == nil || dacl.AceCount < 3 {
			t.Fatalf("unexpected DACL entry count for %s: %#v", path, dacl)
		}
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
		if err != nil {
			t.Fatal(err)
		}
		admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{"user": false, "system": false, "admins": false}
		for index := uint32(0); index < uint32(dacl.AceCount); index++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, index, &ace); err != nil {
				t.Fatal(err)
			}
			fullAccess := ace.Mask == windows.ACCESS_MASK(0x1f01ff) || ace.Mask == windows.GENERIC_ALL
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || !fullAccess {
				t.Fatalf("unexpected ACE for %s: %#v", path, ace)
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			switch {
			case user.User.Sid.Equals(sid):
				found["user"] = true
			case system.Equals(sid):
				found["system"] = true
			case admins.Equals(sid):
				found["admins"] = true
			default:
				t.Fatalf("unexpected trustee ACE for %s: %s", path, sid.String())
			}
		}
		for trustee, present := range found {
			if !present {
				t.Fatalf("missing %s ACE for %s", trustee, path)
			}
		}
	}
}
