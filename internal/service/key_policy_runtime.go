package service

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"

	"cpacloud.local/server/internal/keypolicy"
)

const keyPolicyDeniedMessage = "This request is not allowed for this key."

func authorizeKeySource(auth employeeAuth, remoteAddr string) (employeeAuth, *modelAdmissionError) {
	peer, err := keypolicy.ParseSocketPeer(remoteAddr)
	if err != nil || !keypolicy.AllowsSource(auth.Policy, peer) {
		return employeeAuth{}, &modelAdmissionError{status: 403, code: "model_not_allowed", message: keyPolicyDeniedMessage}
	}
	auth.SourceAddr = peer
	return auth, nil
}

func authorizeKeyPolicy(auth employeeAuth, protocol keypolicy.ClientProtocol, publicModel string) (employeeAuth, *modelAdmissionError) {
	if !keypolicy.Allows(auth.Policy, protocol, publicModel) {
		return employeeAuth{}, &modelAdmissionError{status: 403, code: "model_not_allowed", message: keyPolicyDeniedMessage}
	}
	auth.ClientProtocol = protocol
	return auth, nil
}

func keyPolicyAllowsProtocol(policy keypolicy.Policy, protocol keypolicy.ClientProtocol) bool {
	if policy.ProtocolMode == keypolicy.ModeAll {
		return true
	}
	for _, allowed := range policy.Protocols {
		if allowed == protocol {
			return true
		}
	}
	return false
}

func keyPolicyCurrentTx(ctx context.Context, tx *sql.Tx, auth employeeAuth, publicModel string) (bool, error) {
	if auth.ClientProtocol == "" || auth.Policy.Revision < 1 {
		return false, nil
	}
	current, err := keypolicy.LoadTx(ctx, tx, auth.KeyID)
	if errors.Is(err, keypolicy.ErrNotFound) || errors.Is(err, keypolicy.ErrPolicyMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return current.Revision == auth.Policy.Revision && keypolicy.Allows(current, auth.ClientProtocol, publicModel) && keypolicy.AllowsSource(current, auth.SourceAddr), nil
}

func (a *App) loadKeyPolicySnapshot(ctx context.Context, keyID string, protocol keypolicy.ClientProtocol, publicModel string, sourceAddr netip.Addr, expectedRevision int64) (keypolicy.Policy, error) {
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return keypolicy.Policy{}, err
	}
	defer tx.Rollback()
	policy, err := keypolicy.LoadTx(ctx, tx, keyID)
	if err != nil {
		return keypolicy.Policy{}, err
	}
	if policy.Revision != expectedRevision || !keypolicy.Allows(policy, protocol, publicModel) || !keypolicy.AllowsSource(policy, sourceAddr) {
		return keypolicy.Policy{}, keypolicy.ErrInvalidPolicy
	}
	if err := tx.Commit(); err != nil {
		return keypolicy.Policy{}, err
	}
	return policy, nil
}
