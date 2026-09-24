package service

// Independently authored KEY-02 administrator adapter. Handler registration,
// key creation/list wiring, authentication, and dispatch enforcement belong to
// the integration layer.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

type keyPolicyView struct {
	Revision           int64                      `json:"revision"`
	ProtocolMode       keypolicy.Mode             `json:"protocol_mode"`
	Protocols          []keypolicy.ClientProtocol `json:"protocols"`
	ModelMode          keypolicy.Mode             `json:"model_mode"`
	Models             []string                   `json:"models"`
	EffectiveProtocols []keypolicy.ClientProtocol `json:"effective_protocols"`
	EffectiveModels    []string                   `json:"effective_models"`
}

type replaceKeyPolicyRequest struct {
	ExpectedRevision int64                       `json:"expected_revision"`
	ProtocolMode     *keypolicy.Mode             `json:"protocol_mode"`
	Protocols        *[]keypolicy.ClientProtocol `json:"protocols"`
	ModelMode        *keypolicy.Mode             `json:"model_mode"`
	Models           *[]string                   `json:"models"`
}

type keyPolicyOwner struct {
	EmployeeID   string
	EmployeeMode string
	Status       string
	ExpiresAt    sql.NullString
	RevokedAt    sql.NullString
}

func (a *App) getKeyPolicy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	view, err := a.readKeyPolicyView(r.Context(), r.PathValue("id"))
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) putKeyPolicy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input replaceKeyPolicyRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 1 || input.ProtocolMode == nil || input.Protocols == nil || input.ModelMode == nil || input.Models == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_key_policy", "Invalid key policy.")
		return
	}
	protocols := make([]keypolicy.ClientProtocol, len(*input.Protocols))
	copy(protocols, *input.Protocols)
	models := make([]string, len(*input.Models))
	copy(models, *input.Models)
	replacement := keypolicy.Replacement{
		ProtocolMode: *input.ProtocolMode,
		Protocols:    protocols,
		ModelMode:    *input.ModelMode,
		Models:       models,
	}

	// Employee grants, model lifecycle, and policy CAS must share one database
	// snapshot. The admission lock also serializes the existing employee policy
	// writers with this administrator replacement.
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	owner, err := loadKeyPolicyOwnerTx(r.Context(), tx, r.PathValue("id"))
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	if err := a.validateKeyPolicyModelsTx(r.Context(), tx, owner, replacement); err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	if _, err := keypolicy.ReplaceTx(r.Context(), tx, r.PathValue("id"), input.ExpectedRevision, replacement, time.Now().UTC()); err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	view, err := a.readKeyPolicyView(r.Context(), r.PathValue("id"))
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) readKeyPolicyView(ctx context.Context, keyID string) (keyPolicyView, error) {
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return keyPolicyView{}, err
	}
	defer tx.Rollback()
	owner, err := loadKeyPolicyOwnerTx(ctx, tx, keyID)
	if err != nil {
		return keyPolicyView{}, err
	}
	policy, err := keypolicy.LoadTx(ctx, tx, keyID)
	if err != nil {
		return keyPolicyView{}, err
	}
	view := keyPolicyView{
		Revision: policy.Revision, ProtocolMode: policy.ProtocolMode, Protocols: policy.Protocols,
		ModelMode: policy.ModelMode, Models: policy.Models,
		EffectiveProtocols: []keypolicy.ClientProtocol{}, EffectiveModels: []string{},
	}
	if !keyPolicyOwnerActive(owner, time.Now().UTC()) {
		return view, nil
	}
	if policy.ProtocolMode == keypolicy.ModeAll {
		view.EffectiveProtocols = append(view.EffectiveProtocols, keypolicy.AllClientProtocols...)
	} else {
		view.EffectiveProtocols = append(view.EffectiveProtocols, policy.Protocols...)
	}
	query := `SELECT m.id FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.enabled=1 AND m.archived=0 AND ` + a.availableModelRouteSQL(false)
	args := []any{}
	if owner.EmployeeMode == "selected" {
		query += ` AND EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id)`
		args = append(args, owner.EmployeeID)
	}
	if policy.ModelMode == keypolicy.ModeSelected {
		query += ` AND EXISTS(SELECT 1 FROM access_key_policy_models kpm WHERE kpm.key_id=? AND kpm.model_id=m.id)`
		args = append(args, keyID)
	}
	query += ` ORDER BY m.id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return keyPolicyView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return keyPolicyView{}, err
		}
		view.EffectiveModels = append(view.EffectiveModels, model)
	}
	if err := rows.Err(); err != nil {
		return keyPolicyView{}, err
	}
	return view, nil
}

func loadKeyPolicyOwnerTx(ctx context.Context, tx *sql.Tx, keyID string) (keyPolicyOwner, error) {
	var owner keyPolicyOwner
	err := tx.QueryRowContext(ctx, `SELECT k.employee_id,e.model_mode,e.status,k.expires_at,k.revoked_at FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.id=?`, keyID).
		Scan(&owner.EmployeeID, &owner.EmployeeMode, &owner.Status, &owner.ExpiresAt, &owner.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return keyPolicyOwner{}, keypolicy.ErrNotFound
	}
	if err != nil {
		return keyPolicyOwner{}, err
	}
	return owner, nil
}

func (a *App) validateKeyPolicyModelsTx(ctx context.Context, tx *sql.Tx, owner keyPolicyOwner, replacement keypolicy.Replacement) error {
	if replacement.ModelMode != keypolicy.ModeSelected {
		return nil
	}
	for _, model := range replacement.Models {
		query := `SELECT 1 FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND m.archived=0 AND ` + a.availableModelRouteSQL(false)
		args := []any{model}
		if owner.EmployeeMode == "selected" {
			query += ` AND EXISTS(SELECT 1 FROM employee_models WHERE employee_id=? AND model_id=m.id)`
			args = append(args, owner.EmployeeID)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return keypolicy.ErrInvalidPolicy
		} else if err != nil {
			return err
		}
	}
	return nil
}

func keyPolicyOwnerActive(owner keyPolicyOwner, now time.Time) bool {
	if owner.Status != "active" || owner.RevokedAt.Valid {
		return false
	}
	if !owner.ExpiresAt.Valid {
		return true
	}
	expiresAt, err := parseTime(owner.ExpiresAt.String)
	return err == nil && now.Before(expiresAt)
}

func writeKeyPolicyAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, keypolicy.ErrNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "Key was not found.")
	case errors.Is(err, keypolicy.ErrInvalidPolicy):
		writeAdminError(w, http.StatusBadRequest, "invalid_key_policy", "Invalid key policy.")
	case errors.Is(err, keypolicy.ErrRevisionConflict):
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
	}
}
