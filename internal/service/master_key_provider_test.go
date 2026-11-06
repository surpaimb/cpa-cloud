package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseSystemdMasterKeyCredentialIsStrict(t *testing.T) {
	material := testMasterKeyMaterial(0x31)
	encoded := testSystemdCredentialJSON(t, material)
	parsed, err := parseSystemdMasterKeyCredential(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ProviderKind != material.ProviderKind || parsed.InstanceID != material.InstanceID || parsed.KeyID != material.KeyID || parsed.KeyVersion != material.KeyVersion || parsed.Key != material.Key {
		t.Fatalf("parsed material mismatch: %+v", parsed)
	}
	clearBytes(parsed.Key[:])

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown field", func(value map[string]any) { value["unexpected"] = true }},
		{"wrong provider", func(value map[string]any) { value["provider_kind"] = "preview-file-v1" }},
		{"invalid instance", func(value map[string]any) { value["instance_id"] = "not-a-uuid" }},
		{"invalid key id", func(value map[string]any) { value["key_id"] = "bad/key" }},
		{"zero key version", func(value map[string]any) { value["key_version"] = float64(0) }},
		{"oversized key version", func(value map[string]any) { value["key_version"] = float64(maxJSONSafeInteger + 1) }},
		{"short key", func(value map[string]any) {
			value["master_key"] = base64.RawStdEncoding.EncodeToString(make([]byte, 31))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyObject := make(map[string]any, len(object)+1)
			for key, value := range object {
				copyObject[key] = value
			}
			test.mutate(copyObject)
			candidate, err := json.Marshal(copyObject)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseSystemdMasterKeyCredential(candidate); !errors.Is(err, errMasterKeyProviderRejected) {
				t.Fatalf("credential accepted: %v", err)
			}
		})
	}
	if _, err := parseSystemdMasterKeyCredential(append(encoded, []byte("{}")...)); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("trailing JSON accepted: %v", err)
	}
}

func TestMasterKeyBindingAuthenticatesExactMetadataAndKey(t *testing.T) {
	material := testMasterKeyMaterial(0x42)
	binding, err := createMasterKeyBinding(&material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(binding), base64.RawStdEncoding.EncodeToString(material.Key[:])) {
		t.Fatal("binding contains plaintext master key")
	}
	if err := verifyMasterKeyBinding(binding, &material); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*masterKeyMaterial)
	}{
		{"wrong key", func(value *masterKeyMaterial) { value.Key[0] ^= 0xff }},
		{"wrong instance", func(value *masterKeyMaterial) { value.InstanceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" }},
		{"wrong key id", func(value *masterKeyMaterial) { value.KeyID = "different-key" }},
		{"wrong version", func(value *masterKeyMaterial) { value.KeyVersion++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := material
			test.mutate(&candidate)
			if err := verifyMasterKeyBinding(binding, &candidate); !errors.Is(err, errMasterKeyProviderRejected) {
				t.Fatalf("mismatch accepted: %v", err)
			}
		})
	}

	tampered := append([]byte(nil), binding...)
	tampered[len(tampered)/2] ^= 1
	if err := verifyMasterKeyBinding(tampered, &material); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("tampered binding accepted: %v", err)
	}
}

func TestPreviewMasterKeyProviderRemainsTheDefault(t *testing.T) {
	if got := normalizeMasterKeyProvider(""); got != masterKeyProviderPreviewFileV1 {
		t.Fatalf("default provider=%q", got)
	}
	if err := validateMasterKeyProviderKind("unknown"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func testMasterKeyMaterial(fill byte) masterKeyMaterial {
	material := masterKeyMaterial{
		ProviderKind: masterKeyProviderLinuxSystemdCredentialV1,
		InstanceID:   "01234567-89ab-cdef-0123-456789abcdef",
		KeyID:        "installation-root",
		KeyVersion:   1,
	}
	for index := range material.Key {
		material.Key[index] = fill
	}
	return material
}

func testSystemdCredentialJSON(t *testing.T, material masterKeyMaterial) []byte {
	t.Helper()
	encoded, err := json.Marshal(systemdMasterKeyCredential{
		FormatVersion: 1,
		ProviderKind:  material.ProviderKind,
		InstanceID:    material.InstanceID,
		KeyID:         material.KeyID,
		KeyVersion:    material.KeyVersion,
		MasterKey:     base64.RawStdEncoding.EncodeToString(material.Key[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
