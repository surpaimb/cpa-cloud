package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpacloud.local/server/internal/keyprovider"
	"cpacloud.local/server/internal/recoverymaterial"
)

func TestParseRecoveryVersions(t *testing.T) {
	versions, err := parseRecoveryVersions("1,2,9007199254740991")
	if err != nil || len(versions) != 3 || versions[2] != 9007199254740991 {
		t.Fatalf("versions=%v err=%v", versions, err)
	}
	for _, value := range []string{"", "0", "1,1", "2,1", "1, 2", "1,", "9007199254740992", "-1"} {
		if _, err := parseRecoveryVersions(value); err == nil {
			t.Fatalf("accepted invalid versions %q", value)
		}
	}
}

func TestRunCLIKeyVerifyReadsPasswordOnlyFromStdin(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "material.cpakr")
	password := []byte("recovery-secret-sentinel")
	bundle := &recoverymaterial.Bundle{
		Reference:       recoverymaterial.Reference{EnvelopeVersion: recoverymaterial.FormatVersion, ProviderID: "provider_one", ProviderKind: keyprovider.KindWindowsDPAPIUser, Versions: []uint64{1}},
		InstanceBinding: [32]byte{1},
		Entries:         []recoverymaterial.Entry{{Version: 1, Key: [32]byte{1}}},
	}
	encoded, err := recoverymaterial.Seal(password, bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer wipe(encoded)
	if err := os.WriteFile(input, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code, err := runCLI(context.Background(), []string{"key-verify", "--input", input}, strings.NewReader(string(password)+"\n"), &output)
	if code != 0 || err != nil || !strings.Contains(output.String(), "provider provider_one with 1 explicit versions") {
		t.Fatalf("code=%d err=%v output=%q", code, err, output.String())
	}
	if strings.Contains(output.String(), string(password)) {
		t.Fatal("recovery password was printed")
	}

	output.Reset()
	code, err = runCLI(context.Background(), []string{"key-verify", "--input", input, "--password", string(password)}, strings.NewReader("unused\n"), &output)
	if code != 2 || err == nil {
		t.Fatalf("password argv flag accepted: code=%d err=%v", code, err)
	}
}

func TestRecoveryCommandsValidateFlagsBeforeReadingStdin(t *testing.T) {
	reader := &countingReader{}
	for _, args := range [][]string{{"key-export"}, {"key-verify"}, {"key-import"}} {
		code, err := runCLI(context.Background(), args, reader, &bytes.Buffer{})
		if code != 2 || err == nil {
			t.Fatalf("args=%v code=%d err=%v", args, code, err)
		}
	}
	if reader.reads != 0 {
		t.Fatalf("stdin read %d times before flag validation", reader.reads)
	}
}

type countingReader struct{ reads int }

func (r *countingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, nil
}
