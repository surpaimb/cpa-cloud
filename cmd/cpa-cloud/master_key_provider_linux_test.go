//go:build linux

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxSystemdCredentialFailurePrecedesListenerAndWorkerStartup(t *testing.T) {
	credentialDirectory := t.TempDir()
	if err := os.Chmod(credentialDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	listen := preinitTestFreeAddress(t)
	var stdout bytes.Buffer
	code, err := runCLI([]string{"--data-dir", dataDir, "--listen", listen, "--master-key-provider", "linux-systemd-credential-v1", "--scheduled-tests-enabled", "--automated-backups-enabled"}, strings.NewReader(""), &stdout)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "master key provider rejected") || stdout.Len() != 0 {
		t.Fatalf("startup result code=%d err=%v stdout=%q", code, err, stdout.String())
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected startup created data directory: %v", statErr)
	}
	listener, listenErr := net.Listen("tcp", listen)
	if listenErr != nil {
		t.Fatalf("rejected startup left listener at %s: %v", listen, listenErr)
	}
	listener.Close()
}
