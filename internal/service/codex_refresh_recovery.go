package service

// Independently authored guarded credential acquisition for recovery probes.
// The recovery coordinator supplies an immutable failure snapshot; this file
// only proves that snapshot still names the credential being returned.
import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"cpacloud.local/server/internal/membership"
)

type codexRefreshTransition struct {
	accountID    string
	fromRevision int64
	toRevision   int64
	source       string
	clientID     string
}

type codexRefreshAdoptionHook func(context.Context, *sql.Tx, codexRefreshTransition) error

type recoveryQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type codexRecoveryRefreshGuard struct {
	state   accountRecoveryState
	now     func() time.Time
	adopted accountRecoveryState
	changed bool
}

func (g *codexRecoveryRefreshGuard) validateInitial(ctx context.Context, query recoveryQueryRower, accountID string, revision int64, source, clientID string) error {
	if g == nil || accountID != g.state.AccountID || revision != g.state.AccountRevision ||
		source != g.state.SourceSnapshot || clientID != g.state.ClientID ||
		source != "authorization_code" || clientID == "" {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	return g.validateRecoveryAnchor(ctx, query, accountID, revision)
}

func (g *codexRecoveryRefreshGuard) validateRecoveryAnchor(ctx context.Context, query recoveryQueryRower, accountID string, revision int64) error {
	if g == nil || query == nil || ctx == nil || ctx.Err() != nil || accountID != g.state.AccountID || revision != g.state.AccountRevision {
		if ctx != nil && ctx.Err() != nil {
			return &codexRefreshFailure{code: "refresh_cancelled", err: ctx.Err()}
		}
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	var present int
	err := query.QueryRowContext(ctx, `SELECT 1
		FROM account_recovery_states s
		JOIN account_pool_runtime_cooldowns c ON c.account_id=s.account_id AND c.event_id=s.cooldown_event_id
		WHERE s.account_id=? AND s.cooldown_event_id=? AND s.operation_id=? AND s.recovery_revision=?
			AND s.pool_revision=? AND s.account_revision=? AND s.provider_kind=? AND s.source_snapshot=?
			AND COALESCE(s.client_id,'')=? AND s.public_model=? AND s.upstream_model=? AND s.protocol=?
			AND s.state='required'
			AND NOT EXISTS(SELECT 1 FROM account_pool_maintenance_leases l WHERE l.account_id=s.account_id)`,
		g.state.AccountID, g.state.CooldownEventID, g.state.OperationID, g.state.RecoveryRevision,
		g.state.PoolRevision, g.state.AccountRevision, g.state.ProviderKind, g.state.SourceSnapshot,
		g.state.ClientID, g.state.PublicModel, g.state.UpstreamModel, g.state.Protocol).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return &codexRefreshFailure{code: "revision_conflict"}
	}
	return err
}

func (g *codexRecoveryRefreshGuard) adopt(ctx context.Context, tx *sql.Tx, transition codexRefreshTransition) error {
	if g == nil || tx == nil || transition.accountID != g.state.AccountID || transition.fromRevision != g.state.AccountRevision ||
		transition.toRevision != transition.fromRevision+1 || transition.source != "authorization_code" ||
		transition.source != g.state.SourceSnapshot || transition.clientID != g.state.ClientID || g.state.RecoveryRevision == math.MaxInt64 {
		return errAccountRecoveryConflict
	}
	updatedAt := time.Now().UTC()
	if g.now != nil {
		updatedAt = g.now().UTC()
	}
	if updatedAt.Before(g.state.UpdatedAt) {
		updatedAt = g.state.UpdatedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states
		SET account_revision=?,recovery_revision=recovery_revision+1,updated_at=?
		WHERE account_id=? AND cooldown_event_id=? AND operation_id=? AND recovery_revision=?
			AND pool_revision=? AND account_revision=? AND provider_kind=? AND source_snapshot='authorization_code'
			AND client_id=? AND public_model=? AND upstream_model=? AND protocol=? AND state='required'
			AND recovery_revision<?
			AND EXISTS(SELECT 1 FROM account_pool_runtime_cooldowns c
				WHERE c.account_id=account_recovery_states.account_id AND c.event_id=account_recovery_states.cooldown_event_id)
			AND NOT EXISTS(SELECT 1 FROM account_pool_maintenance_leases l
				WHERE l.account_id=account_recovery_states.account_id)
			AND EXISTS(SELECT 1 FROM upstreams u JOIN codex_oauth_bindings b ON b.upstream_id=u.id
				WHERE u.id=account_recovery_states.account_id AND u.provider_kind=? AND u.revision=?
					AND b.source='authorization_code' AND b.client_id=?)`,
		transition.toRevision, formatAccountPoolTime(updatedAt), g.state.AccountID, g.state.CooldownEventID,
		g.state.OperationID, g.state.RecoveryRevision, g.state.PoolRevision, transition.fromRevision,
		g.state.ProviderKind, transition.clientID, g.state.PublicModel, g.state.UpstreamModel, g.state.Protocol,
		int64(math.MaxInt64), codexMembershipProvider, transition.toRevision, transition.clientID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errAccountRecoveryConflict
	}
	g.adopted = g.state
	g.adopted.AccountRevision = transition.toRevision
	g.adopted.RecoveryRevision++
	g.adopted.UpdatedAt = updatedAt
	g.changed = true
	return nil
}

// acquireCodexRecoveryCredential returns a credential only when the selected
// route and persisted recovery snapshot still identify the same credential.
// OAuth rotation, when required, adopts the resulting account revision in the
// same transaction that stores the rotated secret.
func (a *App) acquireCodexRecoveryCredential(ctx context.Context, selected route, state accountRecoveryState) (route, *membership.CodexAuthCredential, accountRecoveryState, *codexRefreshFailure) {
	if a == nil || a.refresh == nil || ctx == nil || ctx.Err() != nil {
		return selected, nil, state, recoveryRefreshFailure(ctx, &codexRefreshFailure{code: "refresh_cancelled"})
	}
	if !validRecoveryState(state) || state.State != recoveryRequired || state.ProviderKind != codexMembershipProvider ||
		selected.AccountID != state.AccountID || selected.ProviderKind != state.ProviderKind || selected.Revision != state.AccountRevision ||
		selected.UpstreamModel != state.UpstreamModel || selected.KeyVersion != 2 {
		return selected, nil, state, &codexRefreshFailure{code: "revision_conflict"}
	}

	switch state.SourceSnapshot {
	case "authorization_code":
		guard := &codexRecoveryRefreshGuard{state: state, now: a.refresh.now}
		expected := state.AccountRevision
		snapshot, err := a.refresh.refreshGuarded(ctx, state.AccountID, &expected, false, guard)
		if snapshot.revision > 0 {
			selected.Revision = snapshot.revision
			selected.Ciphertext = snapshot.ciphertext
			selected.CredentialState = sql.NullString{String: snapshot.credentialState, Valid: snapshot.credentialState != ""}
		}
		if err != nil {
			return selected, nil, state, recoveryRefreshFailure(ctx, err)
		}
		if guard.changed {
			state = guard.adopted
		}
		credential, failure := a.parseRecoveryCodexCredential(selected)
		return selected, credential, state, failure

	case "import":
		if state.ClientID != "" {
			return selected, nil, state, &codexRefreshFailure{code: "revision_conflict"}
		}
		unlock, err := a.acquireCodexMutationLock(ctx, state.AccountID)
		if err != nil {
			return selected, nil, state, recoveryRefreshFailure(ctx, err)
		}
		defer unlock()
		var provider, credentialState string
		var enabled, keyVersion, bindingCount int
		var revision int64
		var ciphertext []byte
		err = a.store.db.QueryRowContext(ctx, `SELECT u.provider_kind,u.enabled,u.revision,u.credential_ciphertext,
			u.key_version,COALESCE(u.credential_state,''),(SELECT COUNT(*) FROM codex_oauth_bindings b WHERE b.upstream_id=u.id)
			FROM upstreams u WHERE u.id=?`, state.AccountID).Scan(&provider, &enabled, &revision, &ciphertext, &keyVersion, &credentialState, &bindingCount)
		if err != nil {
			return selected, nil, state, recoveryRefreshFailure(ctx, err)
		}
		guard := &codexRecoveryRefreshGuard{state: state}
		if provider != codexMembershipProvider || enabled != 1 || revision != state.AccountRevision || keyVersion != 2 ||
			bindingCount != 0 || credentialState == codexStateReauth {
			return selected, nil, state, &codexRefreshFailure{code: "revision_conflict"}
		}
		if err := guard.validateRecoveryAnchor(ctx, a.store.db, state.AccountID, revision); err != nil {
			return selected, nil, state, recoveryRefreshFailure(ctx, err)
		}
		selected.Ciphertext = ciphertext
		selected.CredentialState = sql.NullString{String: credentialState, Valid: credentialState != ""}
		credential, failure := a.parseRecoveryCodexCredential(selected)
		return selected, credential, state, failure

	default:
		return selected, nil, state, &codexRefreshFailure{code: "refresh_not_bound"}
	}
}

func (a *App) parseRecoveryCodexCredential(selected route) (*membership.CodexAuthCredential, *codexRefreshFailure) {
	plaintext, err := a.secrets.decryptCodexAuth(selected.AccountID, selected.Ciphertext)
	if err != nil {
		return nil, &codexRefreshFailure{code: "credential_unavailable", accountSpecific: true}
	}
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	clear(plaintext)
	if err != nil {
		return nil, &codexRefreshFailure{code: "credential_unavailable", accountSpecific: true}
	}
	if err := membership.NewCodexDirectAdapter().ValidateCredentialForExecution(credential); err != nil {
		credential.Destroy()
		return nil, &codexRefreshFailure{code: "credential_unavailable", accountSpecific: true}
	}
	return credential, nil
}

func recoveryRefreshFailure(ctx context.Context, err error) *codexRefreshFailure {
	if ctx != nil && ctx.Err() != nil {
		return &codexRefreshFailure{code: "refresh_cancelled"}
	}
	var failure *codexRefreshFailure
	if errors.As(err, &failure) {
		return &codexRefreshFailure{code: failure.code, accountSpecific: failure.accountSpecific}
	}
	return &codexRefreshFailure{code: "refresh_unavailable"}
}
