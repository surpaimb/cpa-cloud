// Independently authored tests for docs/adr/0004-portable-backup-recovery-material.md.
package recoverymaterial

import (
	"bytes"
	"errors"
	"testing"
)

func testBundle() *Bundle {
	bundle := &Bundle{
		Reference: Reference{EnvelopeVersion: FormatVersion, ProviderID: "bkp_recovery", ProviderKind: windowsDPAPIUserKind, Versions: []uint64{1, 3}},
		Entries:   []Entry{{Version: 1}, {Version: 3}},
	}
	for index := range bundle.InstanceBinding {
		bundle.InstanceBinding[index] = byte(80 + index)
	}
	for entryIndex := range bundle.Entries {
		for index := range bundle.Entries[entryIndex].Key {
			bundle.Entries[entryIndex].Key[index] = byte(1 + entryIndex*32 + index)
		}
	}
	return bundle
}

func TestEnvelopeRoundTripAndRandomizedCiphertext(t *testing.T) {
	password := []byte("a strong recovery password")
	first, err := Seal(password, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(password, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("recovery envelopes reused salt or nonce")
	}
	opened, err := Open(password, first)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Destroy()
	want := testBundle()
	if opened.Reference.ProviderID != want.Reference.ProviderID || opened.Reference.ProviderKind != want.Reference.ProviderKind || !equalVersions(opened.Reference.Versions, want.Reference.Versions) || opened.InstanceBinding != want.InstanceBinding || opened.Entries[0].Key != want.Entries[0].Key || opened.Entries[1].Key != want.Entries[1].Key {
		t.Fatalf("round trip mismatch: %+v", opened.Reference)
	}
}

func TestEnvelopeRejectsWrongPasswordTamperTruncationAndTrailingData(t *testing.T) {
	encoded, err := Seal([]byte("correct password"), testBundle())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"header":     mutate(encoded, 20),
		"provider":   mutate(encoded, fixedHeaderSize),
		"version":    mutate(encoded, fixedHeaderSize+len("bkp_recovery")+len(windowsDPAPIUserKind)),
		"ciphertext": mutate(encoded, len(encoded)-1),
		"truncated":  bytes.Clone(encoded[:len(encoded)-1]),
		"trailing":   append(bytes.Clone(encoded), 0),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Open([]byte("correct password"), candidate); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := Open([]byte("wrong password"), encoded); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong password error=%v", err)
	}
}

func TestEnvelopeRejectsInvalidVersionsBeforeSeal(t *testing.T) {
	for name, versions := range map[string][]uint64{"duplicate": {1, 1}, "unordered": {2, 1}, "zero": {0}, "too-large": {maxPublicVersion + 1}} {
		t.Run(name, func(t *testing.T) {
			bundle := testBundle()
			bundle.Reference.Versions = versions
			bundle.Entries = make([]Entry, len(versions))
			for index, version := range versions {
				bundle.Entries[index].Version = version
				bundle.Entries[index].Key[0] = 1
			}
			if _, err := Seal([]byte("password"), bundle); err == nil {
				t.Fatal("invalid versions were accepted")
			}
		})
	}
}

func TestEnvelopeRejectsZeroInstanceBindingBeforeSealAndAfterDecode(t *testing.T) {
	bundle := testBundle()
	bundle.InstanceBinding = [32]byte{}
	if _, err := Seal([]byte("password"), bundle); err == nil {
		t.Fatal("zero instance binding was sealed")
	}
	if _, err := decodePayload(bundle.Reference, encodePayload(bundle)); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("decoded zero instance binding error=%v", err)
	}
}

func TestEnvelopeRejectsNonDefaultParametersBeforeKDF(t *testing.T) {
	encoded, err := Seal([]byte("password"), testBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{16, 20, 22, 24, 32} {
		candidate := mutate(encoded, offset)
		called := false
		_, err := openWithDeriver([]byte("password"), candidate, func([]byte, []byte) ([]byte, error) {
			called = true
			return nil, errors.New("unexpected")
		})
		if err == nil || called {
			t.Fatalf("offset %d reached KDF: called=%v err=%v", offset, called, err)
		}
	}
}

func TestBundleDestroyWipesPlaintextKeys(t *testing.T) {
	bundle := testBundle()
	bundle.Destroy()
	if bundle.InstanceBinding != [32]byte{} || bundle.Entries[0].Key != [32]byte{} || bundle.Entries[1].Key != [32]byte{} {
		t.Fatal("bundle plaintext was not wiped")
	}
}

func mutate(input []byte, offset int) []byte {
	result := bytes.Clone(input)
	result[offset] ^= 0x40
	return result
}

func equalVersions(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
