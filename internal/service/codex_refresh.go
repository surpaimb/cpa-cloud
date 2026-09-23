package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"cpacloud.local/server/internal/membership"
	"cpacloud.local/server/internal/scheduling"
)

const (
	codexRefreshAhead       = 5 * time.Minute
	codexRefreshScanEvery   = time.Minute
	codexRefreshScanLimit   = 16
	codexRefreshConcurrency = 2
)

type codexRefreshFailure struct {
	code            string
	err             error
	accountSpecific bool
}

func (e *codexRefreshFailure) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.code
}

func (e *codexRefreshFailure) Unwrap() error { return e.err }

type codexRefreshSnapshot struct {
	revision        int64
	ciphertext      []byte
	credentialState string
}

type codexAccountLock struct{ token chan struct{} }

func newCodexAccountLock() *codexAccountLock {
	l := &codexAccountLock{token: make(chan struct{}, 1)}
	l.token <- struct{}{}
	return l
}

func (l *codexAccountLock) lock(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-l.token:
		return true
	}
}

func (l *codexAccountLock) unlock() { l.token <- struct{}{} }

type codexRefreshCoordinator struct {
	app *App

	locksMu       sync.Mutex
	locks         map[string]*codexAccountLock
	scanMu        sync.Mutex
	scanCursor    string
	now           func() time.Time
	interval      time.Duration
	beforePersist func(string) error

	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startErr  error
}

func newCodexRefreshCoordinator(app *App) *codexRefreshCoordinator {
	return &codexRefreshCoordinator{
		app: app, locks: make(map[string]*codexAccountLock), now: time.Now, interval: codexRefreshScanEvery,
	}
}

func (c *codexRefreshCoordinator) enabled() bool {
	return c != nil && c.app.cfg.ExperimentalCodexMembership && c.app.codexOAuthConfigured()
}

func (c *codexRefreshCoordinator) Start() error {
	if !c.enabled() {
		return nil
	}
	c.startOnce.Do(func() {
		if _, err := c.app.store.db.Exec(`UPDATE codex_oauth_refresh_states
			SET state='paused',reason_code='refresh_interrupted',updated_at=? WHERE state='in_progress'`, utcNow()); err != nil {
			c.startErr = err
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		c.wg.Add(1)
		go c.run(ctx)
	})
	return c.startErr
}

func (c *codexRefreshCoordinator) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.wg.Wait()
	})
}

func (c *codexRefreshCoordinator) run(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scan(ctx)
		}
	}
}

func (c *codexRefreshCoordinator) scan(ctx context.Context) {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	ids, err := c.scanIDs(ctx, c.scanCursor)
	if err != nil {
		return
	}
	if len(ids) == 0 && c.scanCursor != "" {
		c.scanCursor = ""
		ids, err = c.scanIDs(ctx, "")
		if err != nil {
			return
		}
	}
	if len(ids) != 0 {
		c.scanCursor = ids[len(ids)-1]
	}
	sem := make(chan struct{}, codexRefreshConcurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(upstreamID string) {
			defer wg.Done()
			defer func() { <-sem }()
			refreshCtx, cancel := context.WithTimeout(ctx, codexOAuthHTTPTimeout+10*time.Second)
			defer cancel()
			_, _ = c.refresh(refreshCtx, upstreamID, nil, false)
		}(id)
	}
	wg.Wait()
}

func (c *codexRefreshCoordinator) scanIDs(ctx context.Context, after string) ([]string, error) {
	rows, err := c.app.store.db.QueryContext(ctx, `SELECT r.upstream_id
		FROM codex_oauth_refresh_states r
		JOIN codex_oauth_bindings b ON b.upstream_id=r.upstream_id
		JOIN upstreams u ON u.id=r.upstream_id
		WHERE r.state='ready' AND u.provider_kind=? AND u.enabled=1 AND r.upstream_id>?
		ORDER BY r.upstream_id LIMIT ?`, codexMembershipProvider, after, codexRefreshScanLimit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, codexRefreshScanLimit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return ids, nil
}

func (c *codexRefreshCoordinator) accountLock(id string) *codexAccountLock {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()
	lock := c.locks[id]
	if lock == nil {
		lock = newCodexAccountLock()
		c.locks[id] = lock
	}
	return lock
}

func (a *App) acquireCodexMutationLock(ctx context.Context, id string) (func(), error) {
	if a.refresh == nil {
		return func() {}, nil
	}
	lock := a.refresh.accountLock(id)
	if !lock.lock(ctx) {
		return nil, ctx.Err()
	}
	return lock.unlock, nil
}

func (c *codexRefreshCoordinator) refresh(ctx context.Context, id string, expected *int64, force bool) (codexRefreshSnapshot, error) {
	return c.refreshGuarded(ctx, id, expected, force, nil)
}

func (c *codexRefreshCoordinator) refreshGuarded(ctx context.Context, id string, expected *int64, force bool, guard *codexRecoveryRefreshGuard) (codexRefreshSnapshot, error) {
	if !c.enabled() {
		return codexRefreshSnapshot{}, &codexRefreshFailure{code: "refresh_unavailable"}
	}
	lock := c.accountLock(id)
	if !lock.lock(ctx) {
		return codexRefreshSnapshot{}, &codexRefreshFailure{code: "refresh_cancelled", err: ctx.Err()}
	}
	notify := false
	defer func() {
		lock.unlock()
		if notify {
			c.app.notifyAccountPoolChanged()
		}
	}()

	var snapshot codexRefreshSnapshot
	var provider, boundClient, boundSource string
	var enabled, keyVersion int
	var refreshState, reason sql.NullString
	err := c.app.store.db.QueryRowContext(ctx, `SELECT u.provider_kind,u.enabled,u.revision,u.credential_ciphertext,
		u.key_version,COALESCE(u.credential_state,''),COALESCE(b.client_id,''),COALESCE(b.source,''),r.state,r.reason_code
		FROM upstreams u
		LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		LEFT JOIN codex_oauth_refresh_states r ON r.upstream_id=u.id
		WHERE u.id=?`, id).Scan(&provider, &enabled, &snapshot.revision, &snapshot.ciphertext, &keyVersion, &snapshot.credentialState,
		&boundClient, &boundSource, &refreshState, &reason)
	if err != nil {
		return snapshot, err
	}
	if provider != codexMembershipProvider {
		return snapshot, &codexRefreshFailure{code: "invalid_upstream_type"}
	}
	if !force && enabled == 0 {
		return snapshot, &codexRefreshFailure{code: "refresh_unavailable"}
	}
	if expected != nil && snapshot.revision != *expected {
		return snapshot, &codexRefreshFailure{code: "revision_conflict"}
	}
	if guard != nil {
		if keyVersion != 2 {
			return snapshot, &codexRefreshFailure{code: "revision_conflict"}
		}
		if err := guard.validateInitial(ctx, c.app.store.db, id, snapshot.revision, boundSource, boundClient); err != nil {
			return snapshot, err
		}
	}
	if boundClient == "" || boundSource != "authorization_code" || boundClient != c.app.cfg.CodexOAuthClientID {
		return snapshot, &codexRefreshFailure{code: "refresh_not_bound"}
	}
	if !refreshState.Valid {
		result, err := c.app.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO codex_oauth_refresh_states
			(upstream_id,state,reason_code,attempt_revision,updated_at)
			SELECT u.id,'ready',NULL,u.revision,? FROM upstreams u
			JOIN codex_oauth_bindings b ON b.upstream_id=u.id
			WHERE u.id=? AND u.provider_kind=? AND u.enabled=? AND u.revision=?
				AND b.client_id=? AND b.source='authorization_code'`, utcNow(), id, codexMembershipProvider, enabled, snapshot.revision, boundClient)
		if err != nil {
			return snapshot, err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return snapshot, &codexRefreshFailure{code: "revision_conflict"}
		}
		refreshState = sql.NullString{String: "ready", Valid: true}
	}
	if snapshot.credentialState == codexStateReauth || refreshState.String == "reauth_required" {
		return snapshot, &codexRefreshFailure{code: "reauthorization_required"}
	}
	if refreshState.String == "paused" {
		return snapshot, &codexRefreshFailure{code: "refresh_paused", accountSpecific: true}
	}
	if refreshState.String != "ready" {
		return snapshot, &codexRefreshFailure{code: "refresh_unavailable"}
	}

	plaintext, err := c.app.secrets.decryptCodexAuth(id, snapshot.ciphertext)
	if err != nil {
		return snapshot, &codexRefreshFailure{code: "credential_unavailable", err: err}
	}
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	clear(plaintext)
	if err != nil {
		return snapshot, &codexRefreshFailure{code: "credential_unavailable", err: err}
	}
	expiresAt, expiryErr := membership.CodexAccessTokenExpiresAt(credential.AccessTokenSecret())
	refreshToken := credential.RefreshTokenSecret()
	credential.Destroy()
	if expiryErr != nil {
		return snapshot, &codexRefreshFailure{code: "credential_unavailable", err: expiryErr}
	}
	if !force && expiresAt.After(c.now().UTC().Add(codexRefreshAhead)) {
		return snapshot, nil
	}

	result, err := c.app.store.db.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
		SET state='in_progress',reason_code=NULL,attempt_revision=?,updated_at=?
		WHERE upstream_id=? AND state='ready'
			AND EXISTS(SELECT 1 FROM upstreams u JOIN codex_oauth_bindings b ON b.upstream_id=u.id
				WHERE u.id=codex_oauth_refresh_states.upstream_id AND u.provider_kind=? AND u.enabled=? AND u.revision=?
					AND b.client_id=? AND b.source='authorization_code')`, snapshot.revision, utcNow(), id,
		codexMembershipProvider, enabled, snapshot.revision, boundClient)
	if err != nil {
		return snapshot, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return snapshot, &codexRefreshFailure{code: "refresh_unavailable"}
	}
	pause := func(reason string, cause error) (codexRefreshSnapshot, error) {
		if err := c.markPaused(id, snapshot.revision, reason); err != nil {
			return snapshot, err
		}
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: cause, accountSpecific: true}
	}
	pauseThenGlobal := func(reason string, cause error) (codexRefreshSnapshot, error) {
		if err := c.markPaused(id, snapshot.revision, reason); err != nil {
			return snapshot, err
		}
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: cause}
	}

	tokens, wireErr := c.app.requestCodexOAuthTokensWithRetryGuard(ctx, codexOAuthTokenRequest{
		GrantType: "refresh_token", ClientID: boundClient, RefreshToken: refreshToken,
	}, func(retryCtx context.Context) error {
		if err := c.requireCurrentAttempt(retryCtx, id, snapshot.revision, enabled, boundClient); err != nil {
			return err
		}
		if guard != nil {
			return guard.validateRecoveryAnchor(retryCtx, c.app.store.db, id, snapshot.revision)
		}
		return nil
	})
	if wireErr != nil {
		if wireErr.internal != nil {
			if err := c.markPaused(id, snapshot.revision, "uncertain_refresh_outcome"); err != nil {
				return snapshot, err
			}
			return snapshot, wireErr.internal
		}
		if wireErr.Status == http.StatusUnauthorized || wireErr.Code == "invalid_grant" {
			if err := c.markReauthorization(id, snapshot.revision); err != nil {
				return snapshot, err
			}
			return snapshot, &codexRefreshFailure{code: "reauthorization_required"}
		}
		if wireErr.Status == http.StatusTooManyRequests {
			result, err := c.app.store.db.ExecContext(ctx, `UPDATE codex_oauth_refresh_states SET state='ready',reason_code='rate_limited',updated_at=?
				WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?
					AND EXISTS(SELECT 1 FROM upstreams u JOIN codex_oauth_bindings b ON b.upstream_id=u.id
						WHERE u.id=codex_oauth_refresh_states.upstream_id AND u.provider_kind=? AND u.enabled=? AND u.revision=?
							AND b.client_id=? AND b.source='authorization_code')`, utcNow(), id, snapshot.revision,
				codexMembershipProvider, enabled, snapshot.revision, boundClient)
			if err != nil {
				return snapshot, err
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return snapshot, &codexRefreshFailure{code: "revision_conflict"}
			}
			return snapshot, &codexRefreshFailure{code: "refresh_rate_limited"}
		}
		return pause("uncertain_refresh_outcome", ctx.Err())
	}

	rawAuth, err := codexAuthJSONFromTokens(tokens)
	if err != nil {
		return pause("uncertain_refresh_response", err)
	}
	defer clear(rawAuth)
	validated, err := parseSchedulableCodexAuth(rawAuth)
	if err != nil {
		return pause("uncertain_refresh_response", err)
	}
	validated.Destroy()
	rotated, err := c.app.secrets.encryptCodexAuth(id, rawAuth)
	if err != nil {
		return pauseThenGlobal("rotated_credential_save_failed", err)
	}
	if c.beforePersist != nil {
		if err := c.beforePersist(id); err != nil {
			return pauseThenGlobal("rotated_credential_save_failed", err)
		}
	}
	var adopt codexRefreshAdoptionHook
	if guard != nil {
		adopt = guard.adopt
	}
	if err := c.persistRotated(ctx, id, snapshot.revision, enabled, boundClient, rotated, adopt); err != nil {
		return pauseThenGlobal("rotated_credential_save_failed", err)
	}
	notify = true
	snapshot.revision++
	snapshot.ciphertext = rotated
	snapshot.credentialState = codexStateImported
	return snapshot, nil
}

func (c *codexRefreshCoordinator) persistRotated(ctx context.Context, id string, revision int64, enabled int, clientID string, ciphertext []byte, adopt codexRefreshAdoptionHook) error {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE upstreams SET credential_ciphertext=?,key_version=2,
		revision=revision+1,credential_state=?,verified_at=NULL
		WHERE id=? AND provider_kind=? AND enabled=? AND revision=?
			AND EXISTS(SELECT 1 FROM codex_oauth_bindings b WHERE b.upstream_id=upstreams.id
				AND b.client_id=? AND b.source='authorization_code')`, ciphertext, codexStateImported, id, codexMembershipProvider, enabled, revision, clientID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	result, err = tx.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
		SET state='ready',reason_code=NULL,attempt_revision=?,updated_at=?
		WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?`, revision+1, utcNow(), id, revision)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	if adopt != nil {
		if err := adopt(ctx, tx, codexRefreshTransition{
			accountID: id, fromRevision: revision, toRevision: revision + 1,
			source: "authorization_code", clientID: clientID,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (c *codexRefreshCoordinator) requireCurrentAttempt(ctx context.Context, id string, revision int64, enabled int, clientID string) error {
	var present int
	err := c.app.store.db.QueryRowContext(ctx, `SELECT 1
		FROM codex_oauth_refresh_states r
		JOIN upstreams u ON u.id=r.upstream_id
		JOIN codex_oauth_bindings b ON b.upstream_id=r.upstream_id
		WHERE r.upstream_id=? AND r.state='in_progress' AND r.attempt_revision=?
			AND u.provider_kind=? AND u.enabled=? AND u.revision=?
			AND b.client_id=? AND b.source='authorization_code'`, id, revision, codexMembershipProvider, enabled, revision, clientID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	return err
}

func (c *codexRefreshCoordinator) markPaused(id string, revision int64, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.app.store.db.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
		SET state='paused',reason_code=?,updated_at=?
		WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?`, reason, utcNow(), id, revision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	return nil
}

func (c *codexRefreshCoordinator) markReauthorization(id string, revision int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE upstreams SET credential_state=?,verified_at=NULL
		WHERE id=? AND provider_kind=? AND revision=?`, codexStateReauth, id, codexMembershipProvider, revision)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	result, err = tx.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
		SET state='reauth_required',reason_code='invalid_grant',updated_at=?
		WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?`, utcNow(), id, revision)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.app.notifyAccountPoolChanged()
	return nil
}

func (a *App) refreshCodexRoute(ctx context.Context, selected route) (route, error) {
	if a.refresh == nil || !a.refresh.enabled() {
		return selected, nil
	}
	snapshot, err := a.refresh.refresh(ctx, selected.AccountID, nil, false)
	if snapshot.revision > 0 {
		selected.Revision = snapshot.revision
		selected.Ciphertext = snapshot.ciphertext
		selected.CredentialState = sql.NullString{String: snapshot.credentialState, Valid: snapshot.credentialState != ""}
	}
	if err == nil {
		return selected, nil
	}
	if ctx.Err() != nil {
		return selected, err
	}
	var failure *codexRefreshFailure
	if !errors.As(err, &failure) {
		return selected, err
	}
	// Imported or otherwise unbound credentials remain request-usable and are
	// never mutated by the automatic lifecycle.
	if failure.code == "refresh_not_bound" {
		return selected, nil
	}
	if failure.code == "refresh_paused" || failure.code == "refresh_rate_limited" {
		plaintext, decryptErr := a.secrets.decryptCodexAuth(selected.AccountID, selected.Ciphertext)
		if decryptErr != nil {
			return selected, err
		}
		credential, parseErr := membership.ParseCodexAuthJSON(plaintext)
		clear(plaintext)
		if parseErr != nil {
			return selected, err
		}
		expiresAt, expiryErr := membership.CodexAccessTokenExpiresAt(credential.AccessTokenSecret())
		credential.Destroy()
		if expiryErr == nil && expiresAt.After(a.refresh.now().UTC()) {
			return selected, nil
		}
	}
	return selected, err
}

// acquireCodexCredential is the single request-side entry point shared by
// Chat, Responses, and model discovery. The returned route always matches the
// ciphertext/revision used to build the credential.
func (a *App) acquireCodexCredential(ctx context.Context, selected route) (route, *membership.CodexAuthCredential, *codexRunError) {
	updated, err := a.refreshCodexRoute(ctx, selected)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return updated, nil, &codexRunError{Code: membership.CodexErrorCancelled}
		}
		var failure *codexRefreshFailure
		if errors.As(err, &failure) && (failure.code == "reauthorization_required" || (failure.code == "refresh_paused" && failure.accountSpecific)) {
			return updated, nil, codexAccountPreflightError(membership.CodexErrorReauthentication, scheduling.FailureAuth)
		}
		if errors.As(err, &failure) && failure.code == "credential_unavailable" {
			return updated, nil, codexAccountPreflightError(membership.CodexErrorUpstream, scheduling.FailureAuth)
		}
		if errors.As(err, &failure) && failure.code == "refresh_rate_limited" {
			return updated, nil, codexAccountPreflightError(membership.CodexErrorUpstream, scheduling.FailureRateLimit)
		}
		return updated, nil, &codexRunError{Code: membership.CodexErrorUpstream}
	}
	plaintext, err := a.secrets.decryptCodexAuth(updated.AccountID, updated.Ciphertext)
	if err != nil {
		return updated, nil, codexAccountPreflightError(membership.CodexErrorUpstream, scheduling.FailureAuth)
	}
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	clear(plaintext)
	if err != nil {
		return updated, nil, codexAccountPreflightError(membership.CodexErrorUpstream, scheduling.FailureAuth)
	}
	if err := membership.NewCodexDirectAdapter().ValidateCredentialForExecution(credential); err != nil {
		credential.Destroy()
		runErr := normalizeCodexRunError(err)
		runErr.PreflightAccountSpecific = true
		runErr.PreflightClass = scheduling.FailureAuth
		return updated, nil, runErr
	}
	return updated, credential, nil
}

func codexAccountPreflightError(code membership.CodexAdapterErrorCode, class scheduling.FailureClass) *codexRunError {
	return &codexRunError{Code: code, PreflightAccountSpecific: true, PreflightClass: class}
}
