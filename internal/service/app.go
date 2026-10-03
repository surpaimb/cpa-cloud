package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/egress"
	"cpacloud.local/server/internal/governance"
	"cpacloud.local/server/internal/keypolicy"
)

const (
	adminMaxBody       = 1 << 20
	codexImportMaxBody = 8 << 20
	modelMaxBody       = 4 << 20
)

type App struct {
	cfg                            Config
	store                          *store
	secrets                        *secrets
	outboundProxies                *outboundProxyStore
	proxyClients                   *egress.ClientCache
	proxyTests                     *outboundProxyTestCoordinator
	http                           *http.Client
	codex                          codexExecutor
	responses                      codexResponsesExecutor
	oauthHTTP                      *http.Client
	admission                      sync.RWMutex
	refresh                        *codexRefreshCoordinator
	accountPool                    *accountPoolRuntime
	healthTests                    *upstreamHealthCoordinator
	scheduledTests                 *scheduledTestCoordinator
	channelMonitors                *channelMonitorCoordinator
	systemProbes                   *accounting.SystemProbeLedger
	recovery                       *accountRecoveryCoordinator
	backupAutomation               *backupAutomationCoordinator
	usage                          *usageLedgerCoordinator
	accountGroupAllocationRequired bool
	responseResources              *responseResourceCoordinator
	backgroundResponses            *backgroundResponseWorker
	subscriptionExpiry             *subscriptionExpiryWorker
	subscriptionOneShot            *subscriptionOneShotWorker
	governance                     *requestGovernance
	governancePolicies             *governanceManagementStore
	budget                         *governance.Budget
	budgetCommit                   func(string, *sql.Tx) error
	usageRequests                  sync.Map
	trustedProxies                 keypolicy.TrustedProxySet
	loginMu                        sync.Mutex
	logins                         map[string]*loginAttempt
	selfLoginMu                    sync.Mutex
	selfLogins                     map[string]*loginAttempt
	// Independently authored for docs/employee-self-password-change-contract.md.
	// SQL-boundary hooks exercise uncertain commits and mid-hash revocation.
	// Production leaves both nil.
	selfPasswordBeforeTx func()
	selfPasswordCommit   func(*sql.Tx) error
	// Test-only boundary after final session validation, before classification output.
	selfClassificationBeforeWrite    func()
	selfRedemptionHistoryBeforeWrite func()
	// Independently authored for docs/employee-self-admin-adjustment-history-contract.md.
	selfAdminAdjustmentBeforeWrite func()
	selfAdminAdjustmentNonce       func([]byte) (int, error)
	// Independently authored for docs/employee-self-key-revocation-contract.md.
	// Test-only SQL boundary hooks; production leaves both nil.
	selfKeyRevokeBeforeTx func()
	selfKeyRevokeCommit   func(*sql.Tx) error
	// Independently authored for docs/employee-self-key-issuance-contract.md.
	// Test-only boundaries; production leaves both nil.
	selfKeyIssueBeforeTx func()
	selfKeyIssueCommit   func(*sql.Tx) error
	// Independently authored for docs/employee-self-signout-others-contract.md.
	// Test-only transaction boundary hooks; production leaves both nil.
	selfSignOutOthersBeforeTx func()
	selfSignOutOthersCommit   func(*sql.Tx) error
	// Independently authored for docs/employee-self-plan-purchase-contract.md.
	// Test-only transaction, clock, and commit boundaries; production leaves nil.
	selfPlanPurchaseNow          func() time.Time
	selfPlanPurchaseBeforeTx     func()
	selfPlanPurchaseBeforeCommit func(*sql.Tx)
	selfPlanPurchaseCommit       func(*sql.Tx) error
	selfPlanQuoteCommit          func(*sql.Tx) error
	// Independently authored for docs/employee-self-subscription-cancel-contract.md.
	// SQL and clock boundaries are test-only; production leaves them nil.
	selfSubscriptionCancelNow          func() time.Time
	selfSubscriptionCancelBeforeTx     func()
	selfSubscriptionCancelBeforeCommit func(*sql.Tx)
	selfSubscriptionCancelCommit       func(*sql.Tx) error
	// Independently authored for docs/employee-self-one-shot-disarm-contract.md.
	// Test-only transaction and commit boundaries; production leaves nil.
	selfOneShotDisarmBeforeTx     func()
	selfOneShotDisarmBeforeCommit func(*sql.Tx)
	selfOneShotDisarmCommit       func(*sql.Tx) error
	selfOneShotReadCommit         func(*sql.Tx) error
	// Independently authored for docs/employee-self-subscription-purchase-snapshot-contract.md.
	// Test-only read commit boundary; production leaves nil.
	selfPurchaseSnapshotCommit func(*sql.Tx) error
	// Independently authored for docs/employee-self-subscription-renewal-links-contract.md.
	// Test-only read commit boundary; production leaves nil.
	selfRenewalLinksCommit func(*sql.Tx) error
	// Independently authored for docs/employee-self-monthly-renewal-contract.md.
	// Test-only clocks and commit boundaries; production leaves these nil.
	selfMonthlyRenewalNow          func() time.Time
	selfMonthlyRenewalBeforeTx     func()
	selfMonthlyRenewalBeforeCommit func(*sql.Tx)
	selfMonthlyRenewalQuoteCommit  func(*sql.Tx) error
	selfMonthlyRenewalCommit       func(*sql.Tx) error
	// Independently authored for docs/employee-self-redemption-contract.md.
	// Test-only SQL boundaries; production leaves both nil.
	selfRedemptionBeforeTx func()
	selfRedemptionCommit   func(*sql.Tx) error
	selfRedemptionNow      func() time.Time
	// Independently authored for docs/employee-self-key-token-summary-contract.md.
	// Test-only snapshot and commit boundaries; production leaves both nil.
	selfKeyTokenSummaryAfterOwnership func()
	selfKeyTokenSummaryCommit         func(*sql.Tx) error
	// Independently authored for docs/employee-self-key-request-history-contract.md.
	// Test-only snapshot and commit boundaries; production leaves both nil.
	selfKeyRequestAfterOwnership func()
	selfKeyRequestCommit         func(*sql.Tx) error
	// Test-only synchronization for the final self cost session check.
	// Independently authored for docs/employee-self-upstream-estimated-cost-summary-contract.md.
	selfEstimatedCostBeforeFinal func()
	catalogMu                    sync.Mutex
	catalogs                     map[string]codexCatalogCacheEntry
	codexCatalog                 codexCatalogLister
	// Lifecycle integration hooks are SQL-only before commit and non-blocking
	// after commit. Scheduled-test integration wires these without changing the
	// account lock -> admission lock -> transaction ordering.
	archiveUpstreamTxHook func(context.Context, *sql.Tx, string, string, string) error
	upstreamArchivedHook  func(string)
}

type loginAttempt struct {
	failures    int
	blockedTill time.Time
	lastSeen    time.Time
}

func Open(ctx context.Context, cfg Config) (*App, error) {
	if cfg.EmployeeSelfUpstreamEstimatedCostSummaryEnabled && !cfg.EmployeeSelfServiceEnabled {
		return nil, errors.New("employee self upstream estimated cost summary requires employee self service")
	}
	if cfg.EmployeeSelfWalletBalanceEnabled && !cfg.EmployeeSelfServiceEnabled {
		return nil, errors.New("employee self wallet balance requires employee self service")
	}
	if cfg.EmployeeSelfRedemptionEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return nil, errors.New("employee self redemption requires employee self service and wallet balance")
	}
	if cfg.EmployeeSelfWalletActivityEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return nil, errors.New("employee self wallet activity requires employee self service and wallet balance")
	}
	if cfg.EmployeeSelfWalletEntryClassificationEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled || !cfg.EmployeeSelfWalletActivityEnabled) {
		return nil, errors.New("employee self wallet entry classification requires employee self service, wallet balance, and wallet activity")
	}
	if cfg.EmployeeSelfRedemptionCreditHistoryEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled || !cfg.EmployeeSelfWalletActivityEnabled || !cfg.EmployeeSelfWalletEntryClassificationEnabled) {
		return nil, errors.New("employee self redemption credit history requires employee self service, wallet balance, wallet activity, and entry classification")
	}
	if cfg.EmployeeSelfAdminAdjustmentHistoryEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled || !cfg.EmployeeSelfWalletActivityEnabled || !cfg.EmployeeSelfWalletEntryClassificationEnabled) {
		return nil, errors.New("employee self admin adjustment history requires employee self service, wallet balance, wallet activity, and entry classification")
	}
	if cfg.EmployeeSelfSubscriptionStatusEnabled && !cfg.EmployeeSelfServiceEnabled {
		return nil, errors.New("employee self subscription status requires employee self service")
	}
	if cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return nil, errors.New("employee self subscription purchase snapshot requires employee self service, subscription status, and wallet balance")
	}
	if cfg.EmployeeSelfSubscriptionRenewalLinksEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return nil, errors.New("employee self subscription renewal links requires employee self service and subscription status")
	}
	if cfg.EmployeeSelfSubscriptionRenewalEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled || !cfg.EmployeeSelfWalletBalanceEnabled) {
		return nil, errors.New("employee self subscription renewal requires employee self service, subscription status, and wallet balance")
	}
	if cfg.EmployeeSelfSubscriptionCancelEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return nil, errors.New("employee self subscription cancel requires employee self service and subscription status")
	}
	if cfg.EmployeeSelfOneShotRenewalDisarmEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfSubscriptionStatusEnabled) {
		return nil, errors.New("employee self one-shot renewal disarm requires employee self service and subscription status")
	}
	if cfg.EmployeeSelfPlanCatalogEnabled && !cfg.EmployeeSelfServiceEnabled {
		return nil, errors.New("employee self plan catalog requires employee self service")
	}
	if cfg.EmployeeSelfPlanPurchaseEnabled && (!cfg.EmployeeSelfServiceEnabled || !cfg.EmployeeSelfWalletBalanceEnabled || !cfg.EmployeeSelfPlanCatalogEnabled) {
		return nil, errors.New("employee self plan purchase requires employee self service, wallet balance, and plan catalog")
	}
	trustedProxies, err := keypolicy.NewTrustedProxySet(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}
	if cfg.ResponsesBackgroundTasks && !cfg.ResponsesStatefulResources {
		return nil, errors.New("background Responses require stateful Responses resources")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if err := ValidateCodexOAuthConfig(cfg); err != nil {
		return nil, err
	}
	sec, err := loadSecrets(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if err := keypolicy.Migrate(ctx, s.db); err != nil {
		s.close()
		return nil, fmt.Errorf("migrate access key policies: %w", err)
	}
	if err := ensureInitialized(ctx, s.db); err != nil {
		s.close()
		return nil, err
	}
	if err := s.migrateSelfService(ctx); err != nil {
		s.close()
		return nil, fmt.Errorf("migrate employee self service: %w", err)
	}
	client := newUpstreamClient(cfg.AllowLoopbackUpstream)
	app := &App{
		cfg: cfg, store: s, secrets: sec, http: client,
		oauthHTTP: newCodexOAuthHTTPClient(), codex: newProductionCodexExecutor(), responses: newProductionCodexResponsesExecutor(),
		logins: make(map[string]*loginAttempt), selfLogins: make(map[string]*loginAttempt), trustedProxies: trustedProxies,
	}
	opened := false
	defer func() {
		if !opened {
			app.Close()
		}
	}()
	app.outboundProxies = newOutboundProxyStore(s.db, sec, cfg.AllowLoopbackUpstream)
	if err := app.outboundProxies.Migrate(ctx); err != nil {
		return nil, err
	}
	app.proxyClients, err = egress.NewClientCache(64)
	if err != nil {
		return nil, err
	}
	app.refresh = newCodexRefreshCoordinator(app)
	prices := accounting.NewPriceCatalog(s.db)
	if err := prices.Migrate(ctx); err != nil {
		return nil, errUsageLedgerUnavailable
	}
	if err := app.initializeSystemProbeAccounting(ctx); err != nil {
		return nil, err
	}
	app.usage = newUsageLedgerCoordinator(s.db)
	app.usage.priceLookupTx = prices.CurrentTx
	if err := app.usage.ledger.Migrate(ctx); err != nil {
		return nil, err
	}
	if err := migrateAccountingV2(ctx, s.db); err != nil {
		return nil, errUsageLedgerUnavailable
	}
	if err := migrateAccountGroupAllocation(ctx, s.db); err != nil {
		return nil, fmt.Errorf("migrate account group cost allocation: %w", err)
	}
	app.accountGroupAllocationRequired = true
	app.usage.accountGroupAllocationRequired = true
	app.usage.ledger = accounting.NewRequiredAllocationLedger(s.db)
	if err := migrateBackupAutomation(ctx, s.db); err != nil {
		return nil, fmt.Errorf("migrate backup automation: %w", err)
	}
	if err := migrateResponseResources(ctx, s.db); err != nil {
		return nil, err
	}
	app.responseResources, err = newResponseResourceCoordinator(app, app.usage.ledger)
	if err != nil {
		return nil, err
	}
	if err := app.responseResources.Recover(ctx); err != nil {
		return nil, err
	}
	if err := app.initializeGovernance(ctx); err != nil {
		return nil, err
	}
	if err := migrateGeneralBudgets(ctx, s.db); err != nil {
		return nil, fmt.Errorf("migrate general budgets: %w", err)
	}
	trimExpiredSessions(ctx, s.db)
	if err := recoverCodexOAuthSessions(ctx, s.db); err != nil {
		return nil, err
	}
	app.accountPool, err = newAccountPoolRuntime(app)
	if err != nil {
		return nil, err
	}
	app.healthTests, err = newUpstreamHealthCoordinator(app)
	if err != nil {
		return nil, err
	}
	app.scheduledTests, err = newScheduledTestCoordinator(ctx, app)
	if err != nil {
		return nil, err
	}
	app.channelMonitors, err = newChannelMonitorCoordinator(ctx, app)
	if err != nil {
		return nil, err
	}
	app.archiveUpstreamTxHook = func(ctx context.Context, tx *sql.Tx, upstreamID, adminID, archivedAt string) error {
		when, err := parseTime(archivedAt)
		if err != nil {
			return err
		}
		if _, err = archiveScheduledTestsForUpstreamTx(ctx, tx, upstreamID, adminID, when); err != nil {
			return err
		}
		return archiveChannelMonitorsForUpstreamTx(ctx, tx, upstreamID, adminID, when)
	}
	app.upstreamArchivedHook = func(upstreamID string) {
		app.scheduledTests.cancelUpstream(upstreamID)
		app.channelMonitors.cancelUpstream(upstreamID)
	}
	if err := app.refresh.Start(); err != nil {
		return nil, err
	}
	app.recovery = newAccountRecoveryCoordinator(app)
	if err := app.recovery.Start(ctx); err != nil {
		return nil, err
	}
	app.proxyTests, err = newOutboundProxyTestCoordinator(app)
	if err != nil {
		return nil, err
	}
	if !cfg.backupAutomationRehearsal {
		backupOutputDir, backupKeyStoreDir := resolveBackupAutomationPaths(cfg)
		app.backupAutomation, err = newBackupAutomationCoordinator(ctx, app, BackupAutomationConfig{
			Enabled:                cfg.AutomatedBackupsEnabled,
			DataDir:                cfg.DataDir,
			OutputRoot:             backupOutputDir,
			ProviderStoreRoot:      backupKeyStoreDir,
			SourceVersion:          cfg.Version,
			PrepareRehearsalConfig: prepareBackupRehearsalConfig,
		})
		if err != nil {
			return nil, err
		}
	}
	if cfg.ResponsesBackgroundTasks {
		app.backgroundResponses = newBackgroundResponseWorker(app)
		app.backgroundResponses.Start()
	}
	if app.backupAutomation != nil {
		if err := app.backupAutomation.Start(); err != nil {
			return nil, err
		}
	}
	if err := app.scheduledTests.Start(); err != nil {
		return nil, err
	}
	if err := app.channelMonitors.Start(); err != nil {
		return nil, err
	}
	app.subscriptionExpiry = newSubscriptionExpiryWorker(app)
	app.subscriptionOneShot = newSubscriptionOneShotWorker(app)
	opened = true
	return app, nil
}

func (a *App) initializeGovernance(ctx context.Context) error {
	core, err := governance.New(a.store.db, governance.Config{LeaseTTL: 2 * time.Minute})
	if err != nil {
		return err
	}
	policies, err := newGovernanceManagementStore(a.store.db, core)
	if err != nil {
		return err
	}
	budget := governance.NewBudget(a.store.db)
	if err := a.migrateGovernanceBudget(ctx, core, policies, budget); err != nil {
		return err
	}
	a.budget = budget
	a.usage.budget = budget
	if err := a.recoverRequestLedgers(ctx, core); err != nil {
		return err
	}
	runtime, err := newRequestGovernanceRuntime(a, core, policies)
	if err != nil {
		return err
	}
	a.governancePolicies, a.governance = policies, runtime
	a.usage.governance = core
	return nil
}

func (a *App) Close() error {
	if a.subscriptionOneShot != nil {
		a.subscriptionOneShot.Close()
	}
	if a.subscriptionExpiry != nil {
		a.subscriptionExpiry.Close()
	}
	if a.backupAutomation != nil {
		a.backupAutomation.Close()
	}
	if a.backgroundResponses != nil {
		a.backgroundResponses.Close()
	}
	if a.scheduledTests != nil {
		a.scheduledTests.Close()
	}
	if a.channelMonitors != nil {
		a.channelMonitors.Close()
	}
	if a.governance != nil {
		a.governance.Close()
	}
	if a.proxyTests != nil {
		a.proxyTests.Close()
	}
	if a.recovery != nil {
		a.recovery.Close()
	}
	if a.healthTests != nil {
		a.healthTests.Close()
	}
	if a.accountPool != nil {
		a.accountPool.Close()
	}
	if a.refresh != nil {
		a.refresh.Close()
	}
	if a.proxyClients != nil {
		a.proxyClients.Close()
	}
	return a.store.close()
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.registerAccountPoolHandlers(mux)
	a.registerOutboundProxyHandlers(mux)
	a.proxyTests.Register(mux)
	if a.governancePolicies != nil {
		a.governancePolicies.Register(a, mux)
	}
	a.registerGovernanceObservationHandlers(mux)
	a.registerPricingHandlers(mux)
	a.registerUsageHandlers(mux)
	a.registerAccountingV2Handlers(mux)
	a.registerSystemProbeHandlers(mux)
	a.registerAccountRecoveryHandlers(mux)
	a.registerScheduledTestHandlers(mux)
	a.registerChannelMonitorHandlers(mux)
	a.registerAdminAuditHandlers(mux)
	registerBackupAutomationHandlers(a, a.backupAutomation, mux)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /admin/api/v1/sessions", a.login)
	mux.HandleFunc("DELETE /admin/api/v1/sessions", a.requireAdmin(a.logout, true))
	mux.HandleFunc("GET /admin/api/v1/session", a.requireAdmin(a.sessionInfo, false))
	mux.HandleFunc("GET /admin/api/v1/employees", a.requireAdmin(a.listEmployees, false))
	if a.cfg.EmployeeSelfServiceEnabled {
		mux.HandleFunc("POST /admin/api/v1/employees/{id}/self-enrollment", a.requireAdmin(a.issueSelfEnrollment, true))
		mux.HandleFunc("POST /admin/api/v1/employees/{id}/self-key-slot", a.requireAdmin(a.reserveSelfKeySlot, true))
		mux.HandleFunc("POST /admin/api/v1/keys/{id}/self-key-slot/arm", a.requireAdmin(a.armSelfKeySlot, true))
		mux.HandleFunc("POST /admin/api/v1/keys/{id}/self-key-slot/cancel", a.requireAdmin(a.cancelSelfKeySlot, true))
		a.registerSelfHandlers(mux)
	} else {
		mux.HandleFunc("/admin/api/v1/employees/{id}/self-enrollment", http.NotFound)
		mux.HandleFunc("/admin/api/v1/employees/{id}/self-key-slot", http.NotFound)
		mux.HandleFunc("/admin/api/v1/keys/{id}/self-key-slot/arm", http.NotFound)
		mux.HandleFunc("/admin/api/v1/keys/{id}/self-key-slot/cancel", http.NotFound)
	}
	mux.HandleFunc("POST /admin/api/v1/employees", a.requireAdmin(a.createEmployee, true))
	mux.HandleFunc("PATCH /admin/api/v1/employees/{id}", a.requireAdmin(a.updateEmployee, true))
	mux.HandleFunc("PUT /admin/api/v1/employees/{id}/model-policy", a.requireAdmin(a.updateModelPolicy, true))
	mux.HandleFunc("GET /admin/api/v1/employees/{id}/keys", a.requireAdmin(a.listKeys, false))
	mux.HandleFunc("POST /admin/api/v1/employees/{id}/keys", a.requireAdmin(a.createKey, true))
	mux.HandleFunc("GET /admin/api/v1/keys/{id}/policy", a.requireAdmin(a.getKeyPolicy, false))
	mux.HandleFunc("PUT /admin/api/v1/keys/{id}/policy", a.requireAdmin(a.putKeyPolicy, true))
	mux.HandleFunc("POST /admin/api/v1/keys/{id}/revoke", a.requireAdmin(a.revokeKey, true))
	mux.HandleFunc("GET /admin/api/v1/upstreams", a.requireAdmin(a.listUpstreams, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams", a.requireAdmin(a.createUpstream, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/batch-import", a.requireAdmin(a.batchImportUpstreams, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/codex-import", a.requireAdmin(a.importCodexUpstream, true))
	mux.HandleFunc("PATCH /admin/api/v1/upstreams/{id}", a.requireAdmin(a.updateUpstream, true))
	mux.HandleFunc("DELETE /admin/api/v1/upstreams/{id}", a.requireAdmin(a.archiveUpstream, true))
	mux.HandleFunc("PUT /admin/api/v1/upstreams/{id}/codex-auth", a.requireAdmin(a.replaceCodexCredential, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/codex-oauth-sessions", a.requireAdmin(a.createCodexOAuthSession, true))
	mux.HandleFunc("GET /admin/api/v1/upstreams/codex-oauth-sessions/{id}", a.requireAdmin(a.getCodexOAuthSession, false))
	mux.HandleFunc("GET /admin/api/v1/codex/oauth/callback", a.requireAdmin(a.completeCodexOAuth, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/codex-refresh", a.requireAdmin(a.refreshCodexCredential, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/discover-models", a.requireAdmin(a.discoverUpstreamModels, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/tests", a.requireAdmin(a.runUpstreamTest, true))
	mux.HandleFunc("GET /admin/api/v1/upstreams/{id}/tests/{operation_id}", a.requireAdmin(a.getUpstreamTest, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/cooldown/clear", a.requireAdmin(a.clearUpstreamCooldown, true))
	mux.HandleFunc("GET /admin/api/v1/models", a.requireAdmin(a.listAdminModels, false))
	mux.HandleFunc("POST /admin/api/v1/models", a.requireAdmin(a.createModel, true))
	mux.HandleFunc("PATCH /admin/api/v1/models/{id}", a.requireAdmin(a.updateModel, true))
	mux.HandleFunc("DELETE /admin/api/v1/models/{id}", a.requireAdmin(a.archiveModel, true))
	mux.HandleFunc("GET /admin/api/v1/system/status", a.requireAdmin(a.systemStatus, false))
	mux.HandleFunc("GET /v1/models", a.listModels)
	mux.HandleFunc("POST /v1/chat/completions", a.chatCompletions)
	mux.HandleFunc("POST /v1/embeddings", a.embeddings)
	mux.HandleFunc("POST /v1/responses", a.responsesAPI)
	mux.HandleFunc("GET /v1/responses/{id}", a.getResponseResource)
	mux.HandleFunc("DELETE /v1/responses/{id}", a.deleteResponseResource)
	mux.HandleFunc("POST /v1/responses/{id}/cancel", a.cancelResponseResource)
	mux.HandleFunc("POST /v1/messages", a.messages)
	mux.HandleFunc("POST /v1/messages/count_tokens", a.countMessageTokens)
	mux.HandleFunc("GET /v1beta/models", a.listGeminiModels)
	mux.HandleFunc("POST /v1beta/models/{operation}", a.geminiGenerateContent)
	if strings.TrimSpace(a.cfg.WebDir) != "" {
		mux.HandleFunc("/self", a.serveWeb)
		mux.HandleFunc("/self/", a.serveWeb)
		mux.HandleFunc("/", a.serveWeb)
	}
	var handler http.Handler = mux
	if a.cfg.EmployeeSelfSubscriptionRenewalEnabled {
		handler = a.selfMonthlyRenewalRouteGuard(handler)
	}
	handler = a.selfRedemptionRouteGuard(handler)
	handler = a.selfClassificationRouteGuard(handler)
	handler = a.selfRedemptionHistoryRouteGuard(handler)
	handler = a.selfAdminAdjustmentRouteGuard(handler)
	handler = a.selfEstimatedCostRouteGuard(handler)
	handler = a.selfOneShotRouteGuard(handler)
	handler = a.selfCancelRouteGuard(handler)
	// Independently authored for
	// docs/employee-self-subscription-purchase-snapshot-route-boundary-contract.md.
	// Run outside sibling guards so a cleaning redirect into a disabled
	// snapshot is rejected before ServeMux exposes the canonical route.
	handler = a.selfPurchaseSnapshotRouteGuard(handler)
	// Independently authored for
	// docs/employee-self-subscription-renewal-links-route-boundary-contract.md.
	// This must wrap sibling guards as well as ServeMux: their decoded cleaning
	// views may otherwise claim an encoded renewal-links tail as their own.
	handler = a.selfRenewalLinksRouteGuard(handler)
	return requestMiddleware(handler)
}

func requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := newID("req")
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
	})
}

type requestIDKey struct{}

func requestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	response := map[string]string{"status": "ok"}
	if a.cfg.InstanceID != "" {
		response["instance_id"] = a.cfg.InstanceID
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) systemStatus(w http.ResponseWriter, _ *http.Request, _ adminSession) {
	limitations := []string{
		"development preview; not production hardened",
		"Responses state and background execution are development-preview features; background stream resume/cursors, managed tools, and failover after upstream dispatch are not implemented",
		"single-instance billing is disabled by default; its generic signed callback is not a validated production payment-provider integration, and automated renewal, tax, invoices, and notifications are not implemented",
		"manual account tests check local credentials or catalogs; generation recovery requires both startup allowance and administrator opt-in, uses bounded synthetic prompts, and consumes upstream usage",
		"scheduled tests, when enabled, check local credentials or model catalogs only and do not prove generation availability",
		"automated backup key custody currently supports Windows current-user DPAPI only; cross-machine key recovery and object storage are not implemented",
		"automated backups are disabled by default and require an explicit startup flag plus an administrator-configured plan",
		"multi-process storage is not implemented",
		"the host administrator can access runtime secrets and must protect the data directory and master key",
		"single process and single SQLite database only",
		"OpenAI embeddings support is a bounded text-only, float, non-streaming subset and requires an explicit embedding model and account-pool route",
	}
	if a.cfg.ExperimentalCodexMembership {
		limitations = append(limitations, "Codex membership support is experimental, uses a fixed observed protocol, and has not been verified with a real account")
		if !a.codexOAuthConfigured() {
			limitations = append(limitations, "Codex OAuth requires explicit --codex-oauth-client-id and --codex-oauth-redirect-uri configuration")
		}
	} else {
		limitations = append(limitations, "only API-key upstreams are enabled; Codex membership is disabled")
	}
	limitations = append(limitations, "Gemini native support uses Gemini Developer API keys; Google account and Code Assist OAuth credentials are not accepted")
	writeJSON(w, http.StatusOK, map[string]any{
		"version": a.cfg.Version,
		"ready":   true,
		"storage": "sqlite-wal",
		"features": map[string]bool{
			"employee_self_service":                         a.cfg.EmployeeSelfServiceEnabled,
			"employee_self_upstream_estimated_cost_summary": a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfUpstreamEstimatedCostSummaryEnabled,
			"employee_self_wallet_balance":                  a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled,
			"employee_self_redemption":                      a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfRedemptionEnabled,
			"employee_self_wallet_activity":                 a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled,
			"employee_self_wallet_entry_classification":     a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled && a.cfg.EmployeeSelfWalletEntryClassificationEnabled,
			"employee_self_redemption_credit_history":       a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled && a.cfg.EmployeeSelfWalletEntryClassificationEnabled && a.cfg.EmployeeSelfRedemptionCreditHistoryEnabled,
			"employee_self_admin_adjustment_history":        a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled && a.cfg.EmployeeSelfWalletEntryClassificationEnabled && a.cfg.EmployeeSelfAdminAdjustmentHistoryEnabled,
			"employee_self_subscription_status":             a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled,
			"employee_self_subscription_purchase_snapshot":  a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled,
			"employee_self_subscription_renewal_links":      a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfSubscriptionRenewalLinksEnabled,
			"employee_self_subscription_renewal":            a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfSubscriptionRenewalEnabled,
			"employee_self_plan_catalog":                    a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfPlanCatalogEnabled,
			"employee_self_plan_purchase":                   a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfPlanCatalogEnabled && a.cfg.EmployeeSelfPlanPurchaseEnabled,
			"employee_self_subscription_cancel":             a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfSubscriptionCancelEnabled,
			"employee_self_one_shot_renewal_disarm":         a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfOneShotRenewalDisarmEnabled,
			"employee_self_key_issuance":                    a.cfg.EmployeeSelfServiceEnabled,
			"codex_membership_import":                       a.cfg.ExperimentalCodexMembership,
			"responses_api":                                 true,
			"openai_embeddings":                             true,
			"responses_streaming":                           true,
			"responses_stateful_resources":                  a.cfg.ResponsesStatefulResources,
			"responses_background_tasks":                    a.cfg.ResponsesStatefulResources && a.cfg.ResponsesBackgroundTasks && a.backgroundResponses != nil,
			"managed_tools":                                 false,
			"codex_membership_oauth":                        a.cfg.ExperimentalCodexMembership && a.codexOAuthConfigured(),
			"gemini_native_api":                             true,
			"anthropic_native_api":                          true,
			"codex_model_discovery":                         a.cfg.ExperimentalCodexMembership,
			"upstream_batch_import":                         true,
			"upstream_account_tests":                        a.healthTests != nil,
			"scheduled_tests_configuration":                 a.scheduledTests != nil,
			"scheduled_tests_running":                       a.scheduledTests != nil && a.cfg.ScheduledTestsEnabled,
			"scheduled_tests_daily_local":                   a.scheduledTests != nil,
			"channel_monitor_configuration":                 a.channelMonitors != nil,
			"channel_monitor_running":                       a.channelMonitors != nil && a.cfg.ChannelMonitorsEnabled,
			"channel_monitor_retained_summary":              a.channelMonitors != nil,
			"automated_backups_configuration":               a.backupAutomation != nil,
			"backup_key_provider_ready":                     a.backupAutomation != nil && a.backupAutomation.Ready(),
			"automated_backups_running":                     a.backupAutomation != nil && a.backupAutomation.Running(),
			"upstream_cooldown_management":                  a.accountPool != nil,
			"account_pool_configuration":                    true,
			"account_pool_runtime_observation":              a.accountPool != nil,
			"account_group_cost_allocation":                 a.accountGroupAllocationRequired,
			"admin_audit_overview":                          true,
			"admin_audit_financial_source":                  true,
			"admin_audit_csv_export":                        true,
			"account_pool_routing":                          a.accountPool != nil,
			"account_pool_preflight_failover":               a.accountPool != nil,
			"usage_reporting":                               true,
			"versioned_cost_prices":                         true,
			"reliable_usage_accounting":                     true,
			"general_budget_enforcement":                    true,
			"single_instance_billing":                       true,
			"billing_one_shot_renewal":                      true,
			"system_probe_accounting":                       a.systemProbes != nil,
			"account_recovery":                              a.recovery != nil,
			"codex_membership_auto_refresh":                 a.refresh != nil && a.refresh.enabled(),
			"account_lifecycle_management":                  true,
			"key_access_policy":                             true,
			"key_source_policy":                             true,
			"key_account_group_policy":                      true,
			"trusted_proxy_source":                          a.trustedProxies.Enabled(),
		},
		"limitations": limitations,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, max int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid request.")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid request.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAdminError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeModelError(w http.ResponseWriter, status int, code, message, reqID string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code, "request_id": reqID}})
}

func (a *App) serveWeb(w http.ResponseWriter, r *http.Request) {
	selfPath := r.URL.Path == "/self" || strings.HasPrefix(r.URL.Path, "/self/")
	if selfPath && !a.cfg.EmployeeSelfServiceEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/self" {
		http.Redirect(w, r, "/self/", http.StatusPermanentRedirect)
		return
	}
	if selfPath {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "." {
		clean = "index.html"
	}
	path := filepath.Join(a.cfg.WebDir, clean)
	root, err1 := filepath.Abs(a.cfg.WebDir)
	resolved, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil || (resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator))) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		resolved = filepath.Join(root, "index.html")
		info, err = os.Stat(resolved)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
	}
	if kind := mime.TypeByExtension(filepath.Ext(resolved)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	http.ServeFile(w, r, resolved)
}

func listenIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ValidateListenConfig(cfg Config) error {
	certSet := strings.TrimSpace(cfg.TLSCert) != ""
	keySet := strings.TrimSpace(cfg.TLSKey) != ""
	if certSet != keySet {
		return errors.New("--tls-cert and --tls-key must be provided together")
	}
	if !listenIsLoopback(cfg.Listen) && !certSet {
		return errors.New("non-loopback listening requires --tls-cert and --tls-key")
	}
	return nil
}

func (a *App) Serve(ctx context.Context) error {
	server := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		var err error
		if a.cfg.TLSCert != "" {
			err = server.ListenAndServeTLS(a.cfg.TLSCert, a.cfg.TLSKey)
		} else {
			err = server.ListenAndServe()
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
