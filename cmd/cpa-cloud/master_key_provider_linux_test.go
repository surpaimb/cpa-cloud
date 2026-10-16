//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cpacloud.local/server/internal/service"
)

func TestLinuxSystemdCredentialFailurePrecedesListenerAndWorkerStartup(t *testing.T) {
	credentialDirectory := t.TempDir()
	if err := os.Chmod(credentialDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(credentialDirectory, "cpa-cloud-master-key")
	key := bytes.Repeat([]byte{0x91}, 32)
	writeCLISystemdCredential(t, credentialPath, key)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := service.Config{DataDir: dataDir, MasterKeyProvider: "linux-systemd-credential-v1"}
	if err := service.InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	before := snapshotPreinitTestDirectory(t, dataDir)
	key[0] ^= 0xff
	writeCLISystemdCredential(t, credentialPath, key)
	t.Setenv("CPA_CLOUD_MASTER_KEY", base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x91}, 32)))
	listen := preinitTestFreeAddress(t)
	var stdout bytes.Buffer
	code, err := runCLI([]string{"--data-dir", dataDir, "--listen", listen, "--master-key-provider", "linux-systemd-credential-v1", "--scheduled-tests-enabled", "--automated-backups-enabled"}, strings.NewReader(""), &stdout)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "master key provider rejected") || stdout.Len() != 0 {
		t.Fatalf("startup result code=%d err=%v stdout=%q", code, err, stdout.String())
	}
	if after := snapshotPreinitTestDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected startup mutated data directory\nbefore=%v\nafter=%v", before, after)
	}
	listener, listenErr := net.Listen("tcp", listen)
	if listenErr != nil {
		t.Fatalf("rejected startup left listener at %s: %v", listen, listenErr)
	}
	listener.Close()
}

func writeCLISystemdCredential(t *testing.T, path string, key []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{
		"format_version": 1,
		"provider_kind":  "linux-systemd-credential-v1",
		"instance_id":    "01234567-89ab-cdef-0123-456789abcdef",
		"key_id":         "installation-root",
		"key_version":    1,
		"master_key":     base64.RawStdEncoding.EncodeToString(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}
