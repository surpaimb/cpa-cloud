package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"cpacloud.local/server/internal/membership"
)

const (
	codexRefreshAhead       = 5 * time.Minute
	codexRefreshScanEvery   = time.Minute
	codexRefreshScanLimit   = 16
	codexRefreshConcurrency = 2
)

type codexRefreshFailure struct {
	code string
	err  error
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

func (c *codexRefreshCoordinator) refresh(ctx context.Context, id string, expected *int64, force bool) (codexRefreshSnapshot, error) {
	if !c.enabled() {
		return codexRefreshSnapshot{}, &codexRefreshFailure{code: "refresh_unavailable"}
	}
	lock := c.accountLock(id)
	if !lock.lock(ctx) {
		return codexRefreshSnapshot{}, &codexRefreshFailure{code: "refresh_cancelled", err: ctx.Err()}
	}
	defer lock.unlock()

	var snapshot codexRefreshSnapshot
	var provider, boundClient, boundSource string
	var enabled int
	var refreshState, reason sql.NullString
	err := c.app.store.db.QueryRowContext(ctx, `SELECT u.provider_kind,u.enabled,u.revision,u.credential_ciphertext,
		COALESCE(u.credential_state,''),COALESCE(b.client_id,''),COALESCE(b.source,''),r.state,r.reason_code
		FROM upstreams u
		LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		LEFT JOIN codex_oauth_refresh_states r ON r.upstream_id=u.id
		WHERE u.id=?`, id).Scan(&provider, &enabled, &snapshot.revision, &snapshot.ciphertext, &snapshot.credentialState,
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
	if boundClient == "" || boundSource != "authorization_code" || boundClient != c.app.cfg.CodexOAuthClientID {
		return snapshot, &codexRefreshFailure{code: "refresh_not_bound"}
	}
	if !refreshState.Valid {
		if _, err := c.app.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO codex_oauth_refresh_states
			(upstream_id,state,reason_code,attempt_revision,updated_at) VALUES(?,'ready',NULL,?,?)`, id, snapshot.revision, utcNow()); err != nil {
			return snapshot, err
		}
		refreshState = sql.NullString{String: "ready", Valid: true}
	}
	if snapshot.credentialState == codexStateReauth || refreshState.String == "reauth_required" {
		return snapshot, &codexRefreshFailure{code: "reauthorization_required"}
	}
	if refreshState.String == "paused" {
		return snapshot, &codexRefreshFailure{code: "refresh_paused"}
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
		WHERE upstream_id=? AND state='ready'`, snapshot.revision, utcNow(), id)
	if err != nil {
		return snapshot, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return snapshot, &codexRefreshFailure{code: "refresh_unavailable"}
	}

	tokens, wireErr := c.app.requestCodexOAuthTokens(ctx, codexOAuthTokenRequest{
		GrantType: "refresh_token", ClientID: boundClient, RefreshToken: refreshToken,
	})
	if wireErr != nil {
		if wireErr.Status == http.StatusUnauthorized || wireErr.Code == "invalid_grant" {
			if err := c.markReauthorization(id, snapshot.revision); err != nil {
				return snapshot, err
			}
			return snapshot, &codexRefreshFailure{code: "reauthorization_required"}
		}
		if wireErr.Status == http.StatusTooManyRequests {
			result, err := c.app.store.db.ExecContext(ctx, `UPDATE codex_oauth_refresh_states SET state='ready',reason_code='rate_limited',updated_at=?
				WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?`, utcNow(), id, snapshot.revision)
			if err != nil {
				return snapshot, err
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return snapshot, &codexRefreshFailure{code: "revision_conflict"}
			}
			return snapshot, &codexRefreshFailure{code: "refresh_rate_limited"}
		}
		c.markPaused(id, snapshot.revision, "uncertain_refresh_outcome")
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: ctx.Err()}
	}

	rawAuth, err := codexAuthJSONFromTokens(tokens)
	if err != nil {
		c.markPaused(id, snapshot.revision, "uncertain_refresh_response")
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: err}
	}
	defer clear(rawAuth)
	validated, err := parseSchedulableCodexAuth(rawAuth)
	if err != nil {
		c.markPaused(id, snapshot.revision, "uncertain_refresh_response")
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: err}
	}
	validated.Destroy()
	rotated, err := c.app.secrets.encryptCodexAuth(id, rawAuth)
	if err != nil {
		c.markPaused(id, snapshot.revision, "rotated_credential_save_failed")
		return snapshot, &codexRefreshFailure{code: "refresh_paused", err: err}
	}
	if c.beforePersist != nil {
		if err := c.beforePersist(id); err != nil {
			c.markPaused(id, snapshot.revision, "rotated_credential_save_failed")
			return snapshot, &codexRefreshFailure{code: "refresh_paused", err: err}
		}
	}
	if err := c.persistRotated(ctx, id, snapshot.revision, rotated); err != nil {
		c.markPaused(id, snapshot.revision, "rotated_credential_save_failed")
		return snapshot, err
	}
	snapshot.revision++
	snapshot.ciphertext = rotated
	snapshot.credentialState = codexStateImported
	return snapshot, nil
}

func (c *codexRefreshCoordinator) persistRotated(ctx context.Context, id string, revision int64, ciphertext []byte) error {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE upstreams SET credential_ciphertext=?,key_version=2,
		revision=revision+1,credential_state=?,verified_at=NULL
		WHERE id=? AND provider_kind=? AND revision=?`, ciphertext, codexStateImported, id, codexMembershipProvider, revision)
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
	return tx.Commit()
}

func (c *codexRefreshCoordinator) markPaused(id string, revision int64, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.app.store.db.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
		SET state='paused',reason_code=?,updated_at=?
		WHERE upstream_id=? AND state='in_progress' AND attempt_revision=?`, reason, utcNow(), id, revision)
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
	return tx.Commit()
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
		if errors.As(err, &failure) && (failure.code == "reauthorization_required" || failure.code == "refresh_paused") {
			return updated, nil, &codexRunError{Code: membership.CodexErrorReauthentication}
		}
		return updated, nil, &codexRunError{Code: membership.CodexErrorUpstream}
	}
	plaintext, err := a.secrets.decryptCodexAuth(updated.AccountID, updated.Ciphertext)
	if err != nil {
		return updated, nil, &codexRunError{Code: membership.CodexErrorUpstream}
	}
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	clear(plaintext)
	if err != nil {
		return updated, nil, &codexRunError{Code: membership.CodexErrorUpstream}
	}
	if err := membership.NewCodexDirectAdapter().ValidateCredentialForExecution(credential); err != nil {
		credential.Destroy()
		return updated, nil, normalizeCodexRunError(err)
	}
	return updated, credential, nil
}
