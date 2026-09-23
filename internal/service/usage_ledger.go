package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const usageLedgerShutdownTimeout = 3 * time.Second

var (
	errUsageLedgerInvalid     = errors.New("invalid usage ledger metadata")
	errUsageLedgerConflict    = errors.New("usage ledger state conflict")
	errUsageLedgerUnavailable = errors.New("usage ledger unavailable")
)

// usageLedgerCoordinator is the package-local bridge between request
// executors and accounting. It does not select a route, retry an upstream, or
// retain protocol bodies.
type usageLedgerCoordinator struct {
	ledger *accounting.Ledger
	now    func() time.Time
}

type usageRequestStart struct {
	RequestID    string
	EmployeeID   string
	KeyID        string
	PublicModel  string
	ProviderKind string
	Protocol     accounting.UsageProtocol
	StartedAt    time.Time
}

type usageLedgerRequest struct {
	coordinator *usageLedgerCoordinator
	id          string
	provider    accounting.Provider
	protocol    accounting.UsageProtocol
	startedAt   time.Time

	mu             sync.Mutex
	attempt        *usageLedgerAttempt
	finishSnapshot *usageFinishSnapshot
}

type usageLedgerAttempt struct {
	request   *usageLedgerRequest
	id        string
	accountID string
	startedAt time.Time
	usage     *accounting.UsageAccumulator

	mu             sync.Mutex
	finishSnapshot *usageAttemptFinishSnapshot
}

type usageFinishSnapshot struct {
	status     accounting.Status
	finishedAt time.Time
}

type usageAttemptFinishSnapshot struct {
	usageFinishSnapshot
	usage accounting.Usage
}

func newUsageLedgerCoordinator(db *sql.DB) *usageLedgerCoordinator {
	return &usageLedgerCoordinator{
		ledger: accounting.NewLedger(db),
		now:    time.Now,
	}
}

// start performs the accounting migration and marks any pending records from a
// previous process as interrupted. A caller must treat any returned error as a
// startup failure for usage accounting.
func (c *usageLedgerCoordinator) start(ctx context.Context) error {
	if c == nil || c.ledger == nil || c.now == nil || ctx == nil {
		return errUsageLedgerUnavailable
	}
	if err := c.ledger.Migrate(ctx); err != nil {
		return classifyUsageLedgerError(err)
	}
	if _, err := c.ledger.RecoverInterrupted(ctx, c.now().UTC()); err != nil {
		return classifyUsageLedgerError(err)
	}
	return nil
}

// beginRequest records the employee-visible request after route selection has
// identified the provider and before upstream execution is prepared. Protocol
// is explicit because OpenAI-compatible and Codex routes can execute either
// Chat Completions or Responses wire contracts.
func (c *usageLedgerCoordinator) beginRequest(ctx context.Context, input usageRequestStart) (*usageLedgerRequest, error) {
	if c == nil || c.ledger == nil || ctx == nil {
		return nil, errUsageLedgerUnavailable
	}
	provider, err := usageProvider(input.ProviderKind, input.Protocol)
	if err != nil {
		return nil, err
	}
	start := accounting.RequestStart{
		ID:         input.RequestID,
		EmployeeID: input.EmployeeID,
		KeyID:      input.KeyID,
		ModelID:    input.PublicModel,
		Provider:   provider,
		StartedAt:  input.StartedAt,
	}
	if err := c.ledger.BeginRequest(ctx, start); err != nil {
		return nil, classifyUsageLedgerError(err)
	}
	return &usageLedgerRequest{
		coordinator: c,
		id:          input.RequestID,
		provider:    provider,
		protocol:    input.Protocol,
		startedAt:   input.StartedAt,
	}, nil
}

// beginAttempt is called immediately before the selected upstream adapter is
// executed. Adapter-local validation after this point is still a real attempt;
// a routing failure before this point is finished through finishWithoutAttempt.
func (r *usageLedgerRequest) beginAttempt(ctx context.Context, accountID string, startedAt time.Time) (*usageLedgerAttempt, error) {
	if r == nil || r.coordinator == nil || ctx == nil {
		return nil, errUsageLedgerInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishSnapshot != nil {
		return nil, errUsageLedgerConflict
	}
	if r.attempt != nil {
		if r.attempt.accountID != accountID || !r.attempt.startedAt.Equal(startedAt) {
			return nil, errUsageLedgerConflict
		}
		return r.attempt, nil
	}
	accumulator, err := accounting.NewUsageAccumulator(r.protocol)
	if err != nil {
		return nil, classifyUsageLedgerError(err)
	}
	attempt := &usageLedgerAttempt{
		request:   r,
		id:        r.id + ":1",
		accountID: accountID,
		startedAt: startedAt,
		usage:     accumulator,
	}
	if err := r.coordinator.ledger.BeginAttempt(ctx, accounting.AttemptStart{
		ID:        attempt.id,
		RequestID: r.id,
		AccountID: accountID,
		Provider:  r.provider,
		Dispatch:  accounting.DispatchPrimary,
		StartedAt: startedAt,
		Price:     nil,
	}); err != nil {
		return nil, classifyUsageLedgerError(err)
	}
	r.attempt = attempt
	return attempt, nil
}

// observe accepts one complete, already validated JSON response or SSE data
// object. accounting.UsageAccumulator retains counters only.
func (a *usageLedgerAttempt) observe(dataJSON []byte) error {
	if a == nil || a.usage == nil {
		return errUsageLedgerInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finishSnapshot != nil {
		return errUsageLedgerConflict
	}
	if err := a.usage.Observe(dataJSON); err != nil {
		return classifyUsageLedgerError(err)
	}
	return nil
}

// finish stores an immutable first finish snapshot, writes the attempt, then
// writes its parent request. Repeating the same status reuses that snapshot,
// including its first timestamp and usage, so deferred cleanup is idempotent.
func (a *usageLedgerAttempt) finish(ctx context.Context, status accounting.Status, finishedAt time.Time) error {
	if a == nil || a.request == nil || a.request.coordinator == nil || ctx == nil {
		return errUsageLedgerInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finishSnapshot == nil {
		if !validUsageTerminalStatus(status) || finishedAt.IsZero() || finishedAt.Before(a.startedAt) {
			return errUsageLedgerInvalid
		}
		a.finishSnapshot = &usageAttemptFinishSnapshot{
			usageFinishSnapshot: usageFinishSnapshot{status: status, finishedAt: finishedAt},
			usage:               a.usage.Usage(),
		}
	} else if a.finishSnapshot.status != status {
		return errUsageLedgerConflict
	}
	snapshot := a.finishSnapshot
	if err := a.request.coordinator.persist(ctx, func(writeCtx context.Context) error {
		return a.request.coordinator.ledger.FinishAttempt(writeCtx, accounting.AttemptFinish{
			ID:         a.id,
			Status:     snapshot.status,
			FinishedAt: snapshot.finishedAt,
			Usage:      snapshot.usage,
		})
	}); err != nil {
		return err
	}
	return a.request.finishAfterAttempt(ctx, snapshot.status, snapshot.finishedAt)
}

// finishWithoutAttempt terminates a request that failed or was cancelled after
// request admission but before a selected adapter was executed. It never
// invents an upstream attempt and cannot mark such a request successful.
func (r *usageLedgerRequest) finishWithoutAttempt(ctx context.Context, status accounting.Status, finishedAt time.Time) error {
	if r == nil || r.coordinator == nil || ctx == nil {
		return errUsageLedgerInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempt != nil {
		return errUsageLedgerConflict
	}
	if status == accounting.StatusSucceeded {
		return errUsageLedgerInvalid
	}
	return r.finishLocked(ctx, status, finishedAt)
}

func (r *usageLedgerRequest) finishAfterAttempt(ctx context.Context, status accounting.Status, finishedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempt == nil {
		return errUsageLedgerConflict
	}
	return r.finishLocked(ctx, status, finishedAt)
}

func (r *usageLedgerRequest) finishLocked(ctx context.Context, status accounting.Status, finishedAt time.Time) error {
	if r.finishSnapshot == nil {
		if !validUsageTerminalStatus(status) || finishedAt.IsZero() || finishedAt.Before(r.startedAt) {
			return errUsageLedgerInvalid
		}
		r.finishSnapshot = &usageFinishSnapshot{status: status, finishedAt: finishedAt}
	} else if r.finishSnapshot.status != status {
		return errUsageLedgerConflict
	}
	snapshot := r.finishSnapshot
	return r.coordinator.persist(ctx, func(writeCtx context.Context) error {
		return r.coordinator.ledger.FinishRequest(writeCtx, accounting.RequestFinish{
			ID:         r.id,
			Status:     snapshot.status,
			FinishedAt: snapshot.finishedAt,
		})
	})
}

func (c *usageLedgerCoordinator) persist(ctx context.Context, operation func(context.Context) error) error {
	if ctx == nil || operation == nil {
		return errUsageLedgerInvalid
	}
	if ctx.Err() != nil {
		return c.persistDuringShutdown(operation)
	}
	err := operation(ctx)
	if err != nil && ctx.Err() != nil {
		return c.persistDuringShutdown(operation)
	}
	return classifyUsageLedgerError(err)
}

func (c *usageLedgerCoordinator) persistDuringShutdown(operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), usageLedgerShutdownTimeout)
	defer cancel()
	return classifyUsageLedgerError(operation(ctx))
}

func usageProvider(providerKind string, protocol accounting.UsageProtocol) (accounting.Provider, error) {
	switch providerKind {
	case "openai-compatible":
		if protocol == accounting.ProtocolOpenAIChatCompletions || protocol == accounting.ProtocolOpenAIResponses {
			return accounting.ProviderOpenAICompatible, nil
		}
	case codexMembershipProvider:
		if protocol == accounting.ProtocolOpenAIChatCompletions || protocol == accounting.ProtocolOpenAIResponses {
			return accounting.ProviderCodex, nil
		}
	case anthropicAPIKeyProvider:
		if protocol == accounting.ProtocolAnthropicMessages {
			return accounting.ProviderAnthropic, nil
		}
	case geminiAPIKeyProvider:
		if protocol == accounting.ProtocolGeminiGenerateContent {
			return accounting.ProviderGemini, nil
		}
	}
	return "", errUsageLedgerInvalid
}

func validUsageTerminalStatus(status accounting.Status) bool {
	switch status {
	case accounting.StatusSucceeded, accounting.StatusFailed, accounting.StatusCancelled, accounting.StatusInterrupted:
		return true
	default:
		return false
	}
}

func classifyUsageLedgerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, accounting.ErrConflict):
		return errUsageLedgerConflict
	case errors.Is(err, accounting.ErrInvalid), errors.Is(err, accounting.ErrInvalidUsage), errors.Is(err, accounting.ErrNotFound):
		return errUsageLedgerInvalid
	default:
		return errUsageLedgerUnavailable
	}
}
