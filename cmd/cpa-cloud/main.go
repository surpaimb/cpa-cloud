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
	"path/filepath"
	"syscall"

	"cpacloud.local/server/internal/service"
)

var version = "dev"

func main() {
	exitCode, err := runCLI(os.Args[1:], os.Stdin, os.Stdout)
	if err != nil {
		slog.Error("cpa-cloud stopped", "error", err)
	}
	os.Exit(exitCode)
}

func runCLI(args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	defaultDataDir, err := os.UserConfigDir()
	if err != nil {
		defaultDataDir = "."
	}
	defaultDataDir = filepath.Join(defaultDataDir, "cpa-cloud")
	var cfg service.Config
	var initialize bool
	var checkInitialized bool
	var shutdownOnStdinEOF bool
	flags := flag.NewFlagSet("cpa-cloud", flag.ContinueOnError)
	flags.SetOutput(stdout)
	flags.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "persistent data directory")
	flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:8787", "HTTP(S) listen address")
	flags.StringVar(&cfg.WebDir, "web-dir", "", "directory containing the web console build")
	flags.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file")
	flags.StringVar(&cfg.TLSKey, "tls-key", "", "TLS private key file")
	flags.StringVar(&cfg.InstanceID, "instance-id", "", "public UUID identifying this service process in /healthz")
	flags.BoolVar(&cfg.AllowLoopbackUpstream, "allow-loopback-upstream", false, "allow loopback upstream endpoints for local development tests")
	flags.BoolVar(&cfg.ExperimentalCodexMembership, "experimental-codex-membership", false, "enable experimental Codex membership credential import and routing")
	flags.BoolVar(&initialize, "init", false, "initialize the data directory using an administrator password from stdin, then exit")
	flags.BoolVar(&checkInitialized, "check-initialized", false, "check initialization without modifying the data directory; exits 0 if initialized or 3 if not")
	flags.BoolVar(&shutdownOnStdinEOF, "shutdown-on-stdin-eof", false, "gracefully stop the running service when stdin reaches EOF")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 1, err
	}
	if flags.NArg() != 0 {
		return 1, errors.New("unexpected positional arguments")
	}
	selectedModes := 0
	if initialize {
		selectedModes++
	}
	if checkInitialized {
		selectedModes++
	}
	if shutdownOnStdinEOF {
		selectedModes++
	}
	if selectedModes > 1 {
		return 1, errors.New("--init, --check-initialized, and --shutdown-on-stdin-eof are mutually exclusive")
	}
	if cfg.InstanceID != "" && !validUUID(cfg.InstanceID) {
		return 1, errors.New("--instance-id must be a UUID in 8-4-4-4-12 hexadecimal format")
	}
	if cfg.InstanceID != "" && (initialize || checkInitialized) {
		return 1, errors.New("--instance-id is only valid when running the service")
	}
	cfg.Version = version
	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return 1, fmt.Errorf("resolve data directory: %w", err)
	}
	cfg.DataDir = absDataDir
	if checkInitialized {
		initialized, err := service.CheckInitialized(context.Background(), cfg.DataDir)
		if err != nil {
			return 1, fmt.Errorf("check initialization: %w", err)
		}
		if !initialized {
			return 3, nil
		}
		return 0, nil
	}
	if initialize {
		if err := service.Initialize(context.Background(), cfg.DataDir, stdin); err != nil {
			return 1, fmt.Errorf("initialize: %w", err)
		}
		fmt.Fprintln(stdout, "Initialized administrator admin.")
		return 0, nil
	}
	if err := service.ValidateListenConfig(cfg); err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := service.Open(ctx, cfg)
	if err != nil {
		return 1, fmt.Errorf("open service: %w", err)
	}
	defer app.Close()
	if shutdownOnStdinEOF {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			stop()
		}()
	}
	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	slog.Info("cpa-cloud starting", "listen", cfg.Listen, "scheme", scheme, "version", cfg.Version)
	if err := app.Serve(ctx); err != nil {
		return 1, err
	}
	return 0, nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}
