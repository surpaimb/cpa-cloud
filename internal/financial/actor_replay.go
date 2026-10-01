package financial

// Independently authored for docs/financial-actor-provenance-contract.md.
// These unexported compatibility reads never create or change an operation.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// replayLegacyUnknownPostTx is restricted to trusted internal callers that
// already know an old operation ID. It cannot turn an omitted actor into a new
// write or grant the old row a newly inferred identity.
func (l *Ledger) replayLegacyUnknownPostTx(ctx context.Context, tx *sql.Tx, input Post) ([]Entry, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validPost(input) || input.Actor != (Actor{}) || input.ActorAdminID != "" {
		return nil, ErrInvalid
	}
	stored, found, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	if stored.Version != 1 || stored.Actor.Kind != "legacy_unknown" || stored.Actor.ID != "" {
		return nil, ErrConflict
	}
	digest, err := postDigest(input)
	if err != nil {
		return nil, ErrInvalid
	}
	if !equalBytes(stored.Digest, digest[:]) {
		return nil, ErrConflict
	}
	entries, err := loadOperationEntries(ctx, tx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, ErrSchema
	}
	return entries, nil
}

// replayLegacyUnknownCommercialTx has the same read-only boundary for old
// commercial receipts. It is not reachable through the regular WriteMeta path.
func replayLegacyUnknownCommercialTx(ctx context.Context, tx *sql.Tx, operationID, action string, payloadDigest [32]byte) (CommercialReceipt, error) {
	if ctx == nil || tx == nil || !validCommercialText(operationID, 128) || !validCommercialText(action, 128) {
		return CommercialReceipt{}, ErrInvalid
	}
	var receipt CommercialReceipt
	var storedAction, kind, created string
	var adminID, employeeID, systemID sql.NullString
	var digest []byte
	err := tx.QueryRowContext(ctx, `SELECT operation_id,action,actor_kind,actor_admin_id,actor_employee_id,actor_system_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations WHERE operation_id=?`, operationID).Scan(&receipt.OperationID, &storedAction, &kind, &adminID, &employeeID, &systemID, &digest, &receipt.ResourceKind, &receipt.ResourceID, &receipt.Revision, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CommercialReceipt{}, ErrNotFound
	}
	if err != nil {
		return CommercialReceipt{}, ErrUnavailable
	}
	if kind != "legacy_unknown" || adminID.Valid || employeeID.Valid || systemID.Valid || storedAction != action || !equalBytes(digest, payloadDigest[:]) {
		return CommercialReceipt{}, ErrConflict
	}
	receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return CommercialReceipt{}, ErrSchema
	}
	receipt.Replay = true
	return receipt, nil
}
