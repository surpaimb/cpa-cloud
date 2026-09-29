package service

// Independently implemented from docs/account-group-cost-allocation-contract.md.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func (a *App) updateAccountGroupAllocation(w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" || !validIdentifier(r.PathValue("id"), 128) {
		writeAccountGroupAllocationInvalid(w)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_revision", "allocation_multiplier_ppm") {
		writeAccountGroupAllocationInvalid(w)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	expected, expectedOK := strictJSONInt64Range(object["expected_revision"], 0, accountGroupAllocationMaxRevision-1)
	ppmText, ppmOK := object["allocation_multiplier_ppm"].(string)
	multiplier, multiplierErr := accounting.ParseAllocationMultiplierPPM(ppmText)
	if !operationOK || !validGovernanceOperationID(operationID) || !expectedOK || !ppmOK || multiplierErr != nil {
		writeAccountGroupAllocationInvalid(w)
		return
	}

	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	defer tx.Rollback()
	groupID := r.PathValue("id")
	if prior, found, err := loadAccountGroupAllocationOperation(r.Context(), tx, operationID); err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	} else if found {
		if prior.GroupID != groupID || prior.Revision != expected+1 || prior.MultiplierPPM != multiplier.CanonicalPPM() {
			writeAdminError(w, http.StatusConflict, "operation_conflict", "Allocation operation conflicts with an existing operation.")
			return
		}
		writeJSON(w, http.StatusOK, prior)
		return
	}
	var current int64
	if err := tx.QueryRowContext(r.Context(), `SELECT revision FROM account_groups WHERE id=?`, groupID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Account group was not found.")
		return
	} else if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	if current != expected {
		writeAccountPoolRevisionError(w)
		return
	}
	version, err := newID("agalloc")
	if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	createdAt := time.Now().UTC()
	next := expected + 1
	result, err := tx.ExecContext(r.Context(), `UPDATE account_groups SET revision=? WHERE id=? AND revision=?`, next, groupID, expected)
	if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	if changed != 1 {
		writeAccountPoolRevisionError(w)
		return
	}
	stamp := createdAt.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO account_group_allocation_versions(version,group_id,group_revision,operation_id,expected_revision,multiplier_ppm,created_at) VALUES(?,?,?,?,?,?,?)`, version, groupID, next, operationID, expected, multiplier.PartsPerMillion(), stamp); err != nil {
		if prior, found, loadErr := loadAccountGroupAllocationOperation(r.Context(), tx, operationID); loadErr == nil && found {
			if prior.GroupID == groupID && prior.Revision == next && prior.MultiplierPPM == multiplier.CanonicalPPM() {
				writeJSON(w, http.StatusOK, prior)
				return
			}
			writeAdminError(w, http.StatusConflict, "operation_conflict", "Allocation operation conflicts with an existing operation.")
			return
		}
		writeAccountGroupAllocationStorageError(w)
		return
	}
	result, err = tx.ExecContext(r.Context(), `UPDATE account_group_allocation_current SET version=?,group_revision=? WHERE group_id=?`, version, next, groupID)
	if err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "account_group.allocation.update", "account_group", groupID); err != nil {
		writeAccountGroupAllocationStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		// A failed commit is uncertain. Resolve the immutable operation through a
		// fresh transaction and never create a replacement operation ID here.
		if prior, found, loadErr := loadAccountGroupAllocationOperation(r.Context(), a.store.db, operationID); loadErr == nil && found {
			if prior.GroupID == groupID && prior.Revision == next && prior.MultiplierPPM == multiplier.CanonicalPPM() {
				writeJSON(w, http.StatusOK, prior)
				return
			}
			writeAdminError(w, http.StatusConflict, "operation_conflict", "Allocation operation conflicts with an existing operation.")
			return
		}
		writeAccountGroupAllocationStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, accountGroupAllocationVersionView{GroupID: groupID, Version: version, Revision: next, MultiplierPPM: multiplier.CanonicalPPM(), CreatedAt: stamp})
}

type accountGroupAllocationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAccountGroupAllocationOperation(ctx context.Context, query accountGroupAllocationQuery, operationID string) (accountGroupAllocationVersionView, bool, error) {
	var item accountGroupAllocationVersionView
	var ppm int64
	err := query.QueryRowContext(ctx, `SELECT group_id,version,group_revision,multiplier_ppm,created_at FROM account_group_allocation_versions WHERE operation_id=?`, operationID).
		Scan(&item.GroupID, &item.Version, &item.Revision, &ppm, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return accountGroupAllocationVersionView{}, false, nil
	}
	if err != nil {
		return accountGroupAllocationVersionView{}, false, err
	}
	multiplier, err := accounting.NewAllocationMultiplier(ppm)
	if err != nil {
		return accountGroupAllocationVersionView{}, false, err
	}
	if _, err := time.Parse(time.RFC3339Nano, item.CreatedAt); err != nil {
		return accountGroupAllocationVersionView{}, false, err
	}
	item.MultiplierPPM = multiplier.CanonicalPPM()
	return item, true, nil
}

func writeAccountGroupAllocationInvalid(w http.ResponseWriter) {
	writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid account group allocation request.")
}

func writeAccountGroupAllocationStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Account group allocation storage is temporarily unavailable.")
}
