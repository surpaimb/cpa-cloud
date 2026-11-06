package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type forbiddenStdin struct{}

func (forbiddenStdin) Read([]byte) (int, error) { panic("offline command must not read stdin") }

func offlineArguments(t *testing.T) []string {
	t.Helper()
	return []string{"systemd-v3-verify", "--input", filepath.Join(t.TempDir(), "input.cpacb"), "--binding-seed", t.TempDir(),
		"--expect-provider", "backup", "--expect-instance", "01234567-89ab-cdef-0123-456789abcdef",
		"--expect-master-id", "master", "--expect-master-version", "1", "--expect-wrapping-id", "wrapping",
		"--expect-wrapping-version", "1", "--expect-source-version", version}
}

func TestOfflineCLIRejectsArgumentsWithoutEchoOrStdin(t *testing.T) {
	for _, tail := range [][]string{{"--key=synthetic-untrusted-value"}, {"--expect-master-version=synthetic-untrusted-value"},
		{"--expect-wrapping-version=01"}, {"--expect-master-version=9007199254740992"}, {"--init"}, {"synthetic-untrusted-value"},
		{"--data-dir", t.TempDir()}, {"--expect-instance", "synthetic-untrusted-value"}, {"--expect-source-version", "different"}} {
		args := append(offlineArguments(t), tail...)
		var out bytes.Buffer
		code, err := runCLI(context.Background(), args, forbiddenStdin{}, &out)
		if code != 2 || err == nil || err.Error() != "OFFLINE_V3 arguments failed" || out.Len() != 0 {
			t.Fatalf("invalid fixed result: %d %v", code, err)
		}
	}
}

func TestOfflineCLIExplicitModeFailsClosedOutsideRecoveryUnit(t *testing.T) {
	var out bytes.Buffer
	code, err := runCLI(context.Background(), offlineArguments(t), forbiddenStdin{}, &out)
	if code != 1 || err == nil || !strings.HasPrefix(err.Error(), "OFFLINE_V3 ") || out.Len() != 0 {
		t.Fatal("offline admission failed open")
	}
}

func TestOfflineCLICannotRestoreIntoRetainedSeed(t *testing.T) {
	args := offlineArguments(t)
	args[0] = "systemd-v3-restore"
	args = append(args, "--data-dir", filepath.Join(args[4], "restored"))
	var out bytes.Buffer
	code, err := runCLI(context.Background(), args, forbiddenStdin{}, &out)
	if code != 2 || err == nil || err.Error() != "OFFLINE_V3 arguments failed" || out.Len() != 0 {
		t.Fatal("seed overlap admitted")
	}
}
