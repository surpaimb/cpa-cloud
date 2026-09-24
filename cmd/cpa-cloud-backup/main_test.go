package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"cpacloud.local/server/internal/service"
)

func TestReadPassword(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		fail              bool
	}{
		{"line feed", "correct horse\n", "correct horse", false},
		{"crlf", "correct horse\r\n", "correct horse", false},
		{"spaces retained", "  correct horse  \n", "  correct horse  ", false},
		{"empty", "\n", "", true},
		{"multiple lines", "one\ntwo\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readPassword(strings.NewReader(tc.input))
			if tc.fail && err == nil {
				t.Fatal("expected error")
			}
			if !tc.fail && (err != nil || string(got) != tc.want) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestRunCLIRequiresSubcommandWithoutReadingPassword(t *testing.T) {
	var out bytes.Buffer
	code, err := runCLI(context.Background(), nil, strings.NewReader("secret\n"), &out)
	if code != 2 || err == nil || !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}
}

func TestRunCLICreateVerifyRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := service.Initialize(ctx, source, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(root, "backup.cpacb")
	passwordInput := func() *strings.Reader { return strings.NewReader("synthetic-package-password\n") }
	for _, step := range []struct {
		name string
		args []string
		want string
	}{
		{"create", []string{"create", "--data-dir", source, "--output", packagePath}, "Created authenticated backup"},
		{"verify", []string{"verify", "--input", packagePath}, "Verified authenticated backup"},
	} {
		t.Run(step.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := runCLI(ctx, step.args, passwordInput(), &out)
			if code != 0 || err != nil || !strings.Contains(out.String(), step.want) {
				t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
			}
		})
	}
	restored := filepath.Join(root, "restored")
	var out bytes.Buffer
	code, err := runCLI(ctx, []string{"restore", "--input", packagePath, "--data-dir", restored}, passwordInput(), &out)
	if code != 0 || err != nil || !strings.Contains(out.String(), "Restored authenticated backup") {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}
	initialized, err := service.CheckInitialized(ctx, restored)
	if err != nil || !initialized {
		t.Fatalf("restored initialized=%v err=%v", initialized, err)
	}
}
