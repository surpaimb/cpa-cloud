package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cpacloud.local/server/internal/backup"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exitCode, err := runCLI(ctx, os.Args[1:], os.Stdin, os.Stdout)
	if err != nil {
		slog.Error("cpa-cloud-backup stopped", "error", err)
	}
	os.Exit(exitCode)
}

func runCLI(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	if len(args) == 0 {
		writeUsage(stdout)
		return 2, legacyFailure("none", "arguments")
	}
	switch args[0] {
	case "systemd-v3-verify", "systemd-v3-restore":
		return runSystemdV3(ctx, args[0], args[1:], stdout)
	case "key-export":
		return runKeyExport(ctx, args[1:], stdin, stdout)
	case "key-verify":
		return runKeyVerify(ctx, args[1:], stdin, stdout)
	case "key-import":
		return runKeyImport(ctx, args[1:], stdin, stdout)
	case "create":
		flags := flag.NewFlagSet("cpa-cloud-backup create", flag.ContinueOnError)
		var dataDir, output string
		flags.StringVar(&dataDir, "data-dir", "", "source CPA Cloud data directory")
		flags.StringVar(&output, "output", "", "new backup package file")
		if help, err := parseLegacyFlags(flags, "create", args[1:], stdout); help {
			return 0, nil
		} else if err != nil {
			return 2, err
		}
		if flags.NArg() != 0 || dataDir == "" || output == "" {
			return 2, legacyFailure("create", "arguments")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, legacyFailure("create", "password")
		}
		defer wipe(password)
		info, err := backup.Create(ctx, dataDir, output, password, version)
		if err != nil {
			return 1, legacyOperationFailure("create", err)
		}
		fmt.Fprintf(stdout, "Created authenticated backup format v%d from CPA Cloud %s with %d files.\n", info.FormatVersion, info.SourceVersion, len(info.Files))
		return 0, nil
	case "verify":
		flags := flag.NewFlagSet("cpa-cloud-backup verify", flag.ContinueOnError)
		var input string
		flags.StringVar(&input, "input", "", "backup package file")
		if help, err := parseLegacyFlags(flags, "verify", args[1:], stdout); help {
			return 0, nil
		} else if err != nil {
			return 2, err
		}
		if flags.NArg() != 0 || input == "" {
			return 2, legacyFailure("verify", "arguments")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, legacyFailure("verify", "password")
		}
		defer wipe(password)
		info, err := backup.Verify(ctx, input, password)
		if err != nil {
			return 1, legacyOperationFailure("verify", err)
		}
		fmt.Fprintf(stdout, "Verified authenticated backup format v%d from CPA Cloud %s (%s); future-version restore compatibility is not implied.\n", info.FormatVersion, info.SourceVersion, info.CreatedAt)
		return 0, nil
	case "restore":
		flags := flag.NewFlagSet("cpa-cloud-backup restore", flag.ContinueOnError)
		var input, dataDir string
		flags.StringVar(&input, "input", "", "backup package file")
		flags.StringVar(&dataDir, "data-dir", "", "new restore data directory")
		if help, err := parseLegacyFlags(flags, "restore", args[1:], stdout); help {
			return 0, nil
		} else if err != nil {
			return 2, err
		}
		if flags.NArg() != 0 || input == "" || dataDir == "" {
			return 2, legacyFailure("restore", "arguments")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, legacyFailure("restore", "password")
		}
		defer wipe(password)
		info, err := backup.Restore(ctx, input, dataDir, password)
		if err != nil {
			return 1, legacyOperationFailure("restore", err)
		}
		fmt.Fprintf(stdout, "Restored authenticated backup format v%d into a new data directory.\n", info.FormatVersion)
		return 0, nil
	default:
		writeUsage(stdout)
		return 2, legacyFailure("unknown", "arguments")
	}
}

// Independently authored from docs/backup-cli-diagnostic-redaction-contract.md.
// Raw parser diagnostics must never reach a caller's output. Help is generated
// again from static registrations, not from a captured diagnostic or argv.
func parseLegacyFlags(flags *flag.FlagSet, command string, args []string, stdout io.Writer) (bool, error) {
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			flags.SetOutput(stdout)
			flags.Usage()
			return true, nil
		}
		fixed := legacyFailure(command, "arguments")
		fmt.Fprintln(stdout, fixed)
		return false, fixed
	}
	return false, nil
}

func legacyFailure(command, phase string) error {
	switch command {
	case "none", "unknown", "create", "verify", "restore", "key-export", "key-verify", "key-import":
	default:
		command, phase = "unknown", "operation"
	}
	switch phase {
	case "arguments", "versions", "password", "operation":
	default:
		command, phase = "unknown", "operation"
	}
	return errors.New("BACKUP_CLI " + command + " " + phase + " failed")
}

// Deliberately neither format nor retain the cause, including its unwrap chain.
func legacyOperationFailure(command string, _ error) error {
	return legacyFailure(command, "operation")
}

func readPassword(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 1026))
	if err != nil {
		return nil, errors.New("read backup password from stdin")
	}
	if len(data) > 1025 {
		wipe(data)
		return nil, errors.New("backup password exceeds 1024 bytes")
	}
	data = bytesTrimOneLineEnding(data)
	if len(data) == 0 {
		return nil, errors.New("backup password is required on stdin")
	}
	if len(data) > 1024 {
		wipe(data)
		return nil, errors.New("backup password exceeds 1024 bytes")
	}
	for _, value := range data {
		if value == '\r' || value == '\n' {
			wipe(data)
			return nil, errors.New("backup password must be a single line")
		}
	}
	return data, nil
}

func bytesTrimOneLineEnding(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) > 0 && data[len(data)-1] == '\r' {
		data = data[:len(data)-1]
	}
	return data
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: cpa-cloud-backup <create|verify|restore|key-export|key-verify|key-import> [options]")
	fmt.Fprintln(w, "The backup or recovery password is read only from stdin.")
	fmt.Fprintln(w, "Explicit Linux offline recovery: systemd-v3-verify | systemd-v3-restore (fixed recovery unit; no stdin).")
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
