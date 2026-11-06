package main

// Independently authored from docs/backup-cli-diagnostic-redaction-contract.md.
// Only public synthetic inputs are used. Never print args, causes or captures.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/recoverymaterial"
)

const diagnosticPublicCanary = "BCLI_PUBLIC_" + "DIA_73F2B961C4E80A5D"
const diagnosticChildMarker = "CPA_BACKUP_DIAGNOSTIC_MAIN_HELPER"
const diagnosticPublicPassword = "public-synthetic-backup-password\n"

type diagnosticInput struct {
	reader io.Reader
	reads  int
}

func (r *diagnosticInput) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

type diagnosticCapture struct {
	stdout string
	stderr string
	exit   int
}

// The child calls the actual main (including its logger and os.Exit), not a
// replacement entry point. It is a Go test subprocess, not a release binary.
func TestBackupCLIDiagnosticMainHelper(t *testing.T) {
	if os.Getenv(diagnosticChildMarker) != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cpa-cloud-backup"}, os.Args[index+1:]...)
			main()
			os.Exit(126)
		}
	}
	os.Exit(125)
}

func diagnosticMain(t *testing.T, args []string, password string) diagnosticCapture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	childArgs := append([]string{"-test.run=^TestBackupCLIDiagnosticMainHelper$", "--"}, args...)
	child := exec.CommandContext(ctx, os.Args[0], childArgs...)
	// Remove any inherited helper marker before adding the owned one.
	for _, entry := range os.Environ() {
		if !strings.EqualFold(strings.SplitN(entry, "=", 2)[0], diagnosticChildMarker) {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, diagnosticChildMarker+"=1")
	child.Stdin = strings.NewReader(password)
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	err := child.Run()
	if ctx.Err() != nil {
		t.Fatal("BCLI_CHILD_TIMEOUT")
	}
	exit := 0
	if err != nil {
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			t.Fatal("BCLI_CHILD_START_UNKNOWN")
		}
		exit = exited.ExitCode()
	}
	if exit != 0 && exit != 1 && exit != 2 {
		t.Fatal("BCLI_CHILD_EXIT_UNKNOWN")
	}
	return diagnosticCapture{stdout.String(), stderr.String(), exit}
}

func diagnosticEcho(value string, args []string) bool {
	tokens := []string{diagnosticPublicCanary}
	for _, arg := range args {
		if strings.Contains(arg, diagnosticPublicCanary) {
			tokens = append(tokens, arg)
		}
	}
	for _, token := range tokens {
		data := []byte(token)
		for _, encoded := range []string{
			token, strconv.Quote(token), url.QueryEscape(token), url.PathEscape(token),
			hex.EncodeToString(data), strings.ToUpper(hex.EncodeToString(data)),
			base64.StdEncoding.EncodeToString(data), base64.RawStdEncoding.EncodeToString(data),
			base64.URLEncoding.EncodeToString(data), base64.RawURLEncoding.EncodeToString(data),
		} {
			if strings.Contains(value, encoded) {
				return true
			}
		}
	}
	return false
}

func diagnosticOperationArgs(command, root string) []string {
	missing := filepath.Join(root, diagnosticPublicCanary)
	switch command {
	case "create":
		return []string{command, "--data-dir", missing, "--output", filepath.Join(root, "backup.cpacb")}
	case "verify":
		return []string{command, "--input", missing}
	case "restore":
		return []string{command, "--input", missing, "--data-dir", filepath.Join(root, "restored")}
	case "key-export":
		return []string{command, "--provider-store", missing, "--data-dir", filepath.Join(root, "source"), "--provider-id", "bkp_public_diag", "--versions", "1", "--output", filepath.Join(root, "material.cpakr")}
	case "key-verify":
		return []string{command, "--input", missing}
	case "key-import":
		return []string{command, "--input", missing, "--backup", filepath.Join(root, "package.cpacb"), "--target-root", filepath.Join(root, "recovered")}
	}
	return nil
}

func diagnosticEmptyRoot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("BCLI_OWNED_ROOT_CHANGED_OR_UNREADABLE")
	}
}

// This same oracle is frozen before the old run and retained after the change.
// Only a complete old joint witness is the approved expected red.
func TestBackupCLIDiagnosticCanaryJointOracle(t *testing.T) {
	root := t.TempDir()
	type vector struct {
		args     []string
		command  string
		kind     string
		expected int
		pathEcho bool
	}
	vectors := []vector{{[]string{diagnosticPublicCanary}, "unknown", "unknown", 2, false}}
	commands := []string{"create", "verify", "restore", "key-export", "key-verify", "key-import"}
	for _, command := range commands {
		for _, token := range []string{"--" + diagnosticPublicCanary, "---" + diagnosticPublicCanary, "--=" + diagnosticPublicCanary} {
			vectors = append(vectors, vector{[]string{command, token}, command, "parser", 2, false})
		}
		vectors = append(vectors, vector{[]string{command, "--password=" + diagnosticPublicCanary}, command, "counterexample", 2, false})
		pathEcho := runtime.GOOS == "windows" || (command != "key-export" && command != "key-import")
		vectors = append(vectors, vector{diagnosticOperationArgs(command, root), command, "operation", 1, pathEcho})
	}
	if len(vectors) != 31 {
		t.Fatal("BCLI_VECTOR_MANIFEST_MISMATCH")
	}
	oldJoint, newJoint := true, true
	for _, item := range vectors {
		stdin := &diagnosticInput{reader: strings.NewReader(diagnosticPublicPassword)}
		var stdout bytes.Buffer
		exit, err := runCLI(context.Background(), item.args, stdin, &stdout)
		if err == nil || exit != item.expected {
			t.Fatal("BCLI_DIRECT_EXIT_OR_ERROR_MISMATCH")
		}
		if (item.kind == "operation") != (stdin.reads > 0) {
			t.Fatal("BCLI_STDIN_PRECEDENCE_MISMATCH")
		}
		child := diagnosticMain(t, item.args, diagnosticPublicPassword)
		if child.exit != item.expected || child.stdout != stdout.String() || !strings.Contains(child.stderr, "cpa-cloud-backup stopped") {
			t.Fatal("BCLI_MAIN_CHANNEL_OR_EXIT_MISMATCH")
		}
		directEcho := diagnosticEcho(err.Error(), item.args)
		stdoutEcho := diagnosticEcho(stdout.String(), item.args)
		stderrEcho := diagnosticEcho(child.stderr, item.args)
		var pathError *os.PathError
		hasPath := errors.As(err, &pathError)
		switch item.kind {
		case "unknown":
			oldJoint = oldJoint && directEcho && stderrEcho && !stdoutEcho && !hasPath
		case "parser":
			oldJoint = oldJoint && directEcho && stdoutEcho && stderrEcho && !hasPath
		case "counterexample":
			// Ordinary --password=VALUE does not prove the VALUE was echoed.
			oldJoint = oldJoint && !directEcho && !stdoutEcho && !stderrEcho && !hasPath
		case "operation":
			if item.pathEcho {
				oldJoint = oldJoint && directEcho && stderrEcho && hasPath && !stdoutEcho
			} else {
				oldJoint = oldJoint && !directEcho && !stdoutEcho && !stderrEcho
			}
		}
		phase := "arguments"
		if item.kind == "operation" {
			phase = "operation"
		}
		fixed := "BACKUP_CLI " + item.command + " " + phase + " failed"
		newJoint = newJoint && !directEcho && !stdoutEcho && !stderrEcho && !hasPath && err.Error() == fixed && strings.Contains(child.stderr, fixed)
		switch item.kind {
		case "parser", "counterexample":
			newJoint = newJoint && stdout.String() == fixed+"\n"
		case "operation":
			newJoint = newJoint && stdout.Len() == 0
		case "unknown":
			var usage bytes.Buffer
			writeUsage(&usage)
			newJoint = newJoint && stdout.String() == usage.String()
		}
		diagnosticEmptyRoot(t, root)
	}
	if oldJoint && !newJoint {
		t.Log("BCLI_OLD_JOINT_WITNESS=true BCLI_PASSWORD_VALUE_COUNTEREXAMPLES_CLEAR=true BCLI_PRECEDENCE_AND_CLEANUP=true")
		t.Fatal("BCLI_OLD_DIAGNOSTICS_ECHO_PUBLIC_CANARY")
	}
	if !newJoint || oldJoint {
		t.Fatal("BCLI_JOINT_ORACLE_UNEXPECTED_OR_PARTIAL")
	}
	t.Log("BCLI_FIXED_DIRECT_AND_MAIN=true BCLI_REGISTERED_CANARY_ENCODINGS_CLEAR=true BCLI_PRECEDENCE_AND_CLEANUP=true")
}

func diagnosticExpectedHelp(command string) string {
	// Frozen registration descriptions/defaults, independently compared bytewise.
	var items []string
	switch command {
	case "create":
		items = []string{"data-dir|source CPA Cloud data directory", "output|new backup package file"}
	case "verify":
		items = []string{"input|backup package file"}
	case "restore":
		items = []string{"data-dir|new restore data directory", "input|backup package file"}
	case "key-export":
		items = []string{"data-dir|source CPA Cloud data directory", "output|new encrypted recovery material file", "provider-id|backup key provider identifier", "provider-store|source Windows DPAPI provider store", "versions|strictly increasing comma-separated key versions"}
	case "key-verify":
		items = []string{"input|encrypted recovery material file"}
	case "key-import":
		items = []string{"backup|automated backup v2 package", "input|encrypted recovery material file", "target-root|new recovery root"}
	}
	result := "Usage of cpa-cloud-backup " + command + ":\n"
	for _, item := range items {
		pair := strings.SplitN(item, "|", 2)
		result += "  -" + pair[0] + " string\n    \t" + pair[1] + "\n"
	}
	return result
}

func TestBackupCLIDiagnosticInputPrecedenceAndHelp(t *testing.T) {
	root := t.TempDir()
	checks := 0
	check := func(args []string, password, command, phase string, wantExit int, wantStdout string, wantRead bool) {
		t.Helper()
		checks++
		stdin := &diagnosticInput{reader: strings.NewReader(password)}
		var stdout bytes.Buffer
		exit, err := runCLI(context.Background(), args, stdin, &stdout)
		child := diagnosticMain(t, args, password)
		if exit != wantExit || child.exit != wantExit || stdout.String() != wantStdout || child.stdout != wantStdout || (stdin.reads > 0) != wantRead {
			t.Fatal("BCLI_CONTROL_OUTPUT_EXIT_OR_PRECEDENCE")
		}
		if wantExit == 0 {
			if err != nil || child.stderr != "" {
				t.Fatal("BCLI_HELP_NOT_SUCCESS_OR_STDERR_NOT_EMPTY")
			}
		} else {
			fixed := "BACKUP_CLI " + command + " " + phase + " failed"
			if err == nil || err.Error() != fixed || !strings.Contains(child.stderr, fixed) || !strings.Contains(child.stderr, "cpa-cloud-backup stopped") || errors.Unwrap(err) != nil {
				t.Fatal("BCLI_CONTROL_FIXED_ERROR_MISMATCH")
			}
		}
		if diagnosticEcho(stdout.String()+child.stdout+child.stderr, args) || (err != nil && diagnosticEcho(err.Error(), args)) {
			t.Fatal("BCLI_CONTROL_CAPTURE_NOT_CLEAR")
		}
		diagnosticEmptyRoot(t, root)
	}
	var topUsage bytes.Buffer
	writeUsage(&topUsage)
	check(nil, diagnosticPublicPassword, "none", "arguments", 2, topUsage.String(), false)
	for _, arg := range []string{"-h", "--help"} {
		check([]string{arg}, diagnosticPublicPassword, "unknown", "arguments", 2, topUsage.String(), false)
	}
	for _, command := range []string{"create", "verify", "restore", "key-export", "key-verify", "key-import"} {
		valid := diagnosticOperationArgs(command, root)
		for _, help := range []string{"-h", "--help"} {
			check([]string{command, help}, diagnosticPublicPassword, command, "", 0, diagnosticExpectedHelp(command), false)
			withValues := append(append([]string(nil), valid...), help)
			check(withValues, diagnosticPublicPassword, command, "", 0, diagnosticExpectedHelp(command), false)
		}
		check([]string{command}, diagnosticPublicPassword, command, "arguments", 2, "", false)
		fixedArguments := "BACKUP_CLI " + command + " arguments failed\n"
		check([]string{command, valid[1]}, diagnosticPublicPassword, command, "arguments", 2, fixedArguments, false)
		extra := append(append([]string(nil), valid...), diagnosticPublicCanary)
		check(extra, diagnosticPublicPassword, command, "arguments", 2, "", false)
		for index := 1; index < len(valid); index += 2 {
			withoutRequired := append(append([]string(nil), valid[:index]...), valid[index+2:]...)
			check(withoutRequired, diagnosticPublicPassword, command, "arguments", 2, "", false)
		}
		for _, password := range []string{"", "one\ntwo\n", strings.Repeat("p", 1025)} {
			check(valid, password, command, "password", 1, "", true)
		}
	}
	for _, value := range []string{"0", "1,1", "2,1", "1, 2", "1,", "9007199254740992", "-1", diagnosticPublicCanary} {
		args := diagnosticOperationArgs("key-export", root)
		for index := 1; index < len(args); index += 2 {
			if args[index] == "--versions" {
				args[index+1] = value
			}
		}
		check(args, diagnosticPublicPassword, "key-export", "versions", 2, "", false)
	}
	// 3 top-level, 24 help, 18 basic invalid-argument, 14 omitted required
	// items, 18 password and 8 versions controls. No extra Go subtest names.
	if checks != 85 {
		t.Fatal("BCLI_CONTROL_MANIFEST_MISMATCH")
	}
	t.Log("BCLI_HELP_BYTES_AND_MAIN_EXIT=true BCLI_INPUT_PRECEDENCE=true BCLI_PASSWORD_AND_VERSIONS_FIXED=true")
}

type diagnosticOpaqueError struct{}

func (diagnosticOpaqueError) Error() string { panic("BCLI_CAUSE_FORMATTED_UNEXPECTEDLY") }

type diagnosticFailingInput struct{}

func (diagnosticFailingInput) Read([]byte) (int, error) { return 0, diagnosticOpaqueError{} }

func TestBackupCLIDiagnosticFixedErrorMapping(t *testing.T) {
	commands := []string{"none", "unknown", "create", "verify", "restore", "key-export", "key-verify", "key-import"}
	for _, command := range commands {
		for _, phase := range []string{"arguments", "versions", "password", "operation"} {
			err := legacyFailure(command, phase)
			if err.Error() != "BACKUP_CLI "+command+" "+phase+" failed" || errors.Unwrap(err) != nil {
				t.Fatal("BCLI_ALLOWLIST_MAPPING_MISMATCH")
			}
		}
	}
	for _, pair := range [][2]string{{diagnosticPublicCanary, "arguments"}, {"verify", diagnosticPublicCanary}, {diagnosticPublicCanary, diagnosticPublicCanary}} {
		if legacyFailure(pair[0], pair[1]).Error() != "BACKUP_CLI unknown operation failed" {
			t.Fatal("BCLI_UNKNOWN_CLASSIFICATION_NOT_FIXED")
		}
	}
	pathCause := &os.PathError{Op: diagnosticPublicCanary, Path: diagnosticPublicCanary, Err: errors.New(diagnosticPublicCanary)}
	causes := []error{diagnosticOpaqueError{}, pathCause, errors.New(diagnosticPublicCanary), errors.Join(diagnosticOpaqueError{}, pathCause), context.Canceled, context.DeadlineExceeded}
	for _, command := range commands {
		for _, cause := range causes {
			err := legacyOperationFailure(command, cause)
			var pathError *os.PathError
			if err.Error() != "BACKUP_CLI "+command+" operation failed" || errors.Unwrap(err) != nil || errors.As(err, &pathError) || errors.Is(err, cause) {
				t.Fatal("BCLI_RAW_CAUSE_RETAINED_OR_FORMATTED")
			}
		}
	}
	for _, cause := range causes {
		err := keyImportFailure(cause)
		if err.Error() != "BACKUP_CLI key-import operation failed" || errors.Unwrap(err) != nil {
			t.Fatal("BCLI_IMPORT_RAW_CAUSE_RETAINED")
		}
	}
	for _, cause := range []error{recoverymaterial.ErrPublishStateUncertain, errors.Join(diagnosticOpaqueError{}, recoverymaterial.ErrPublishStateUncertain, pathCause)} {
		err := keyImportFailure(cause)
		want := "BACKUP_CLI key-import publish_state_uncertain failed: recovery import publish state is uncertain; inspect the target without retrying or overwriting"
		var pathError *os.PathError
		if err.Error() != want || !errors.Is(err, recoverymaterial.ErrPublishStateUncertain) || errors.Unwrap(err) != recoverymaterial.ErrPublishStateUncertain || errors.As(err, &pathError) || diagnosticEcho(err.Error(), nil) {
			t.Fatal("BCLI_UNCERTAIN_PUBLICATION_HINT_OR_CLASSIFICATION_LOST")
		}
	}
	root := t.TempDir()
	for _, command := range []string{"create", "verify", "restore", "key-export", "key-verify", "key-import"} {
		var stdout bytes.Buffer
		exit, err := runCLI(context.Background(), diagnosticOperationArgs(command, root), diagnosticFailingInput{}, &stdout)
		if exit != 1 || err == nil || err.Error() != "BACKUP_CLI "+command+" password failed" || stdout.Len() != 0 || errors.Unwrap(err) != nil {
			t.Fatal("BCLI_STDIN_CAUSE_NOT_FIXED")
		}
		diagnosticEmptyRoot(t, root)
	}
	t.Log("BCLI_NO_RAW_CAUSE_FORMAT_OR_CHAIN=true BCLI_UNCERTAIN_INSPECT_NO_RETRY_HINT=true BCLI_PURE_CLASSIFIER_NOT_IO_INJECTION=true")
}
