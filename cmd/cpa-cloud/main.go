package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"cpacloud.local/server/internal/service"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("cpa-cloud stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	defaultDataDir, err := os.UserConfigDir()
	if err != nil {
		defaultDataDir = "."
	}
	defaultDataDir = filepath.Join(defaultDataDir, "cpa-cloud")
	var cfg service.Config
	var initialize bool
	flag.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "persistent data directory")
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:8787", "HTTP(S) listen address")
	flag.StringVar(&cfg.WebDir, "web-dir", "", "directory containing the web console build")
	flag.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file")
	flag.StringVar(&cfg.TLSKey, "tls-key", "", "TLS private key file")
	flag.BoolVar(&cfg.AllowLoopbackUpstream, "allow-loopback-upstream", false, "allow loopback upstream endpoints for local development tests")
	flag.BoolVar(&initialize, "init", false, "initialize the data directory using an administrator password from stdin, then exit")
	flag.Parse()
	cfg.Version = version
	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	cfg.DataDir = absDataDir
	if initialize {
		if err := service.Initialize(context.Background(), cfg.DataDir, os.Stdin); err != nil {
			return fmt.Errorf("initialize: %w", err)
		}
		fmt.Fprintln(os.Stdout, "Initialized administrator admin.")
		return nil
	}
	if err := service.ValidateListenConfig(cfg); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := service.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer app.Close()
	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	slog.Info("cpa-cloud starting", "listen", cfg.Listen, "scheme", scheme, "version", cfg.Version)
	return app.Serve(ctx)
}
