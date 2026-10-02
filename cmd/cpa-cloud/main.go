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

type repeatableStringFlag []string

func (values *repeatableStringFlag) String() string {
	if values == nil {
		return ""
	}
	return fmt.Sprint([]string(*values))
}

func (values *repeatableStringFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

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
	flags.Var((*repeatableStringFlag)(&cfg.TrustedProxyCIDRs), "trusted-proxy-cidr", "trusted reverse-proxy address or CIDR; repeat for multiple ranges")
	flags.StringVar(&cfg.InstanceID, "instance-id", "", "public UUID identifying this service process in /healthz")
	flags.BoolVar(&cfg.AllowLoopbackUpstream, "allow-loopback-upstream", false, "allow loopback upstream endpoints for local development tests")
	flags.BoolVar(&cfg.AccountRecoveryEnabled, "allow-account-recovery", false, "permit administrator-enabled background generation recovery probes (may consume upstream usage)")
	flags.BoolVar(&cfg.ScheduledTestsEnabled, "scheduled-tests-enabled", false, "run saved local credential and model catalog test plans in the background")
	flags.BoolVar(&cfg.ChannelMonitorsEnabled, "channel-monitors-enabled", false, "run saved channel-bound credential and catalog monitor plans in the background")
	flags.BoolVar(&cfg.AutomatedBackupsEnabled, "automated-backups-enabled", false, "run configured encrypted backup plans in the background")
	flags.StringVar(&cfg.AutomatedBackupsOutputDir, "automated-backups-output-dir", "", "encrypted backup package directory (default: a sibling of the data directory)")
	flags.StringVar(&cfg.BackupKeyProviderStoreDir, "backup-key-provider-store-dir", "", "host-protected backup key directory (default: a separate sibling of the data directory)")
	flags.BoolVar(&cfg.ExperimentalCodexMembership, "experimental-codex-membership", false, "enable experimental Codex membership credential import and routing")
	flags.BoolVar(&cfg.EmployeeSelfServiceEnabled, "employee-self-service-enabled", false, "enable the development-preview employee self-service page and API")
	flags.BoolVar(&cfg.EmployeeSelfWalletBalanceEnabled, "employee-self-wallet-balance-enabled", false, "allow enrolled employees to read their own employee-owned wallet balance (requires employee self service)")
	flags.BoolVar(&cfg.EmployeeSelfRedemptionEnabled, "employee-self-redemption-enabled", false, "allow enrolled employees to redeem an issued code into their own direct wallet (requires self service and wallet balance)")
	flags.BoolVar(&cfg.EmployeeSelfWalletActivityEnabled, "employee-self-wallet-activity-enabled", false, "allow enrolled employees to read recent changes to their own employee-owned wallet (requires self service and wallet balance)")
	flags.BoolVar(&cfg.EmployeeSelfSubscriptionStatusEnabled, "employee-self-subscription-status-enabled", false, "allow enrolled employees to read minimal status of their own existing subscriptions (requires self service)")
	flags.BoolVar(&cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled, "employee-self-subscription-purchase-snapshot-enabled", false, "allow enrolled employees to read a verified local wallet purchase snapshot for one own subscription (requires self service, subscription status, and wallet balance)")
	flags.BoolVar(&cfg.EmployeeSelfSubscriptionRenewalLinksEnabled, "employee-self-subscription-renewal-links-enabled", false, "allow enrolled employees to read verified predecessor/successor IDs for one own subscription (requires self service and subscription status)")
	flags.BoolVar(&cfg.EmployeeSelfSubscriptionRenewalEnabled, "employee-self-subscription-renewal-enabled", false, "allow enrolled employees to renew one expired own monthly subscription from an existing wallet (requires self service, subscription status, and wallet balance)")
	flags.BoolVar(&cfg.EmployeeSelfSubscriptionCancelEnabled, "employee-self-subscription-cancel-enabled", false, "allow enrolled employees to cancel their own active subscriptions (requires self service and subscription status)")
	// Independently authored for docs/employee-self-one-shot-disarm-contract.md.
	flags.BoolVar(&cfg.EmployeeSelfOneShotRenewalDisarmEnabled, "employee-self-one-shot-renewal-disarm-enabled", false, "allow enrolled employees to read and disarm their own one-shot renewal (requires self service and subscription status)")
	flags.BoolVar(&cfg.EmployeeSelfPlanCatalogEnabled, "employee-self-plan-catalog-enabled", false, "allow enrolled employees to read the current minimal enabled plan catalog (requires self service)")
	flags.BoolVar(&cfg.EmployeeSelfPlanPurchaseEnabled, "employee-self-plan-purchase-enabled", false, "allow enrolled employees to buy a one-time plan with their existing wallet (requires self service, wallet balance, and plan catalog)")
	flags.BoolVar(&cfg.ResponsesStatefulResources, "responses-stateful-resources", false, "enable encrypted employee-owned Responses resources (development preview)")
	flags.BoolVar(&cfg.ResponsesBackgroundTasks, "responses-background-tasks", false, "enable durable background Responses tasks; requires --responses-stateful-resources")
	flags.StringVar(&cfg.CodexOAuthClientID, "codex-oauth-client-id", "", "registered OAuth client ID for the experimental Codex membership lifecycle")
	flags.StringVar(&cfg.CodexOAuthRedirectURI, "codex-oauth-redirect-uri", "", "registered OAuth callback URI ending in /admin/api/v1/codex/oauth/callback")
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
	if cfg.ResponsesBackgroundTasks && !cfg.ResponsesStatefulResources {
		return 1, errors.New("--responses-background-tasks requires --responses-stateful-resources")
	}
	if cfg.EmployeeSelfWalletBalanceEnabled && !cfg.EmployeeSelfServiceEnabled {
		return 1, errors.New("--employee-self-wallet-balance-enabled requires --employee-self-service-enabled")
	}
	if cfg.EmployeeSelfRedemptionEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return 1, errors.New("--employee-self-redemption-enabled requires --employee-self-service-enabled and --employee-self-wallet-balance-enabled")
	}
	if cfg.EmployeeSelfWalletActivityEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return 1, errors.New("--employee-self-wallet-activity-enabled requires --employee-self-service-enabled and --employee-self-wallet-balance-enabled")
	}
	if cfg.EmployeeSelfSubscriptionStatusEnabled && !cfg.EmployeeSelfServiceEnabled {
		return 1, errors.New("--employee-self-subscription-status-enabled requires --employee-self-service-enabled")
	}
	if cfg.EmployeeSelfSubscriptionRenewalLinksEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return 1, errors.New("--employee-self-subscription-renewal-links-enabled requires --employee-self-service-enabled and --employee-self-subscription-status-enabled")
	}
	if cfg.EmployeeSelfSubscriptionCancelEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return 1, errors.New("--employee-self-subscription-cancel-enabled requires --employee-self-service-enabled and --employee-self-subscription-status-enabled")
	}
	if cfg.EmployeeSelfOneShotRenewalDisarmEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return 1, errors.New("--employee-self-one-shot-renewal-disarm-enabled requires --employee-self-service-enabled and --employee-self-subscription-status-enabled")
	}
	if cfg.EmployeeSelfPlanCatalogEnabled && !cfg.EmployeeSelfServiceEnabled {
		return 1, errors.New("--employee-self-plan-catalog-enabled requires --employee-self-service-enabled")
	}
	if cfg.EmployeeSelfPlanPurchaseEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled || !cfg.EmployeeSelfPlanCatalogEnabled) {
		return 1, errors.New("--employee-self-plan-purchase-enabled requires --employee-self-service-enabled, --employee-self-wallet-balance-enabled, and --employee-self-plan-catalog-enabled")
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
	if err := service.ValidateCodexOAuthConfig(cfg); err != nil {
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
