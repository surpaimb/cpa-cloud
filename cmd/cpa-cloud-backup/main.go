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
		return 2, errors.New("a subcommand is required")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("cpa-cloud-backup create", flag.ContinueOnError)
		flags.SetOutput(stdout)
		var dataDir, output string
		flags.StringVar(&dataDir, "data-dir", "", "source CPA Cloud data directory")
		flags.StringVar(&output, "output", "", "new backup package file")
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0, nil
			}
			return 2, err
		}
		if flags.NArg() != 0 || dataDir == "" || output == "" {
			return 2, errors.New("create requires --data-dir and --output")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, err
		}
		defer wipe(password)
		info, err := backup.Create(ctx, dataDir, output, password, version)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "Created authenticated backup format v%d from CPA Cloud %s with %d files.\n", info.FormatVersion, info.SourceVersion, len(info.Files))
		return 0, nil
	case "verify":
		flags := flag.NewFlagSet("cpa-cloud-backup verify", flag.ContinueOnError)
		flags.SetOutput(stdout)
		var input string
		flags.StringVar(&input, "input", "", "backup package file")
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0, nil
			}
			return 2, err
		}
		if flags.NArg() != 0 || input == "" {
			return 2, errors.New("verify requires --input")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, err
		}
		defer wipe(password)
		info, err := backup.Verify(ctx, input, password)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "Verified authenticated backup format v%d from CPA Cloud %s (%s); future-version restore compatibility is not implied.\n", info.FormatVersion, info.SourceVersion, info.CreatedAt)
		return 0, nil
	case "restore":
		flags := flag.NewFlagSet("cpa-cloud-backup restore", flag.ContinueOnError)
		flags.SetOutput(stdout)
		var input, dataDir string
		flags.StringVar(&input, "input", "", "backup package file")
		flags.StringVar(&dataDir, "data-dir", "", "new restore data directory")
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0, nil
			}
			return 2, err
		}
		if flags.NArg() != 0 || input == "" || dataDir == "" {
			return 2, errors.New("restore requires --input and --data-dir")
		}
		password, err := readPassword(stdin)
		if err != nil {
			return 1, err
		}
		defer wipe(password)
		info, err := backup.Restore(ctx, input, dataDir, password)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "Restored authenticated backup format v%d into a new data directory.\n", info.FormatVersion)
		return 0, nil
	default:
		writeUsage(stdout)
		return 2, fmt.Errorf("unknown subcommand %q", args[0])
	}
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
	fmt.Fprintln(w, "Usage: cpa-cloud-backup <create|verify|restore> [options]")
	fmt.Fprintln(w, "The backup password is read only from stdin.")
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
