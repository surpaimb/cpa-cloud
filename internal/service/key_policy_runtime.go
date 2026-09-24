package service

import (
	"context"
	"database/sql"
	"errors"

	"cpacloud.local/server/internal/keypolicy"
)

func authorizeKeyPolicy(auth employeeAuth, protocol keypolicy.ClientProtocol, publicModel string) (employeeAuth, *modelAdmissionError) {
	if !keypolicy.Allows(auth.Policy, protocol, publicModel) {
		return employeeAuth{}, &modelAdmissionError{status: 403, code: "model_not_allowed", message: "The requested protocol or model is not allowed for this key."}
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
	return current.Revision == auth.Policy.Revision && keypolicy.Allows(current, auth.ClientProtocol, publicModel), nil
}

func (a *App) loadKeyPolicySnapshot(ctx context.Context, keyID string, protocol keypolicy.ClientProtocol, publicModel string) (keypolicy.Policy, error) {
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return keypolicy.Policy{}, err
	}
	defer tx.Rollback()
	policy, err := keypolicy.LoadTx(ctx, tx, keyID)
	if err != nil {
		return keypolicy.Policy{}, err
	}
	if !keypolicy.Allows(policy, protocol, publicModel) {
		return keypolicy.Policy{}, keypolicy.ErrInvalidPolicy
	}
	if err := tx.Commit(); err != nil {
		return keypolicy.Policy{}, err
	}
	return policy, nil
}
