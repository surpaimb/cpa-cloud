package service

// Independently authored resource coordinator for the encrypted Responses
// state contract. Model execution is deliberately separate: no method in this
// file performs network I/O.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/keypolicy"
)

const (
	responseResourceTTL = 30 * 24 * time.Hour
	backgroundTaskTTL   = 24 * time.Hour
)

var (
	errResponseResourceInvalid     = errors.New("invalid response resource")
	errResponseResourceNotFound    = errors.New("response resource not found")
	errResponseResourceForbidden   = errors.New("response resource forbidden")
	errResponseResourceConflict    = errors.New("response resource conflict")
	errResponseResourceUnavailable = errors.New("response resource unavailable")
)

type responseResourceCoordinator struct {
	app        *App
	db         *sql.DB
	secrets    *secrets
	events     accounting.TransactionalEventRecorder
	now        func() time.Time
	commitTx   func(*sql.Tx) error
	cancelTask func(string)
}

type responseStateItem struct {
	Type    string
	Payload []byte
}

type responseResourceCreateInput struct {
	OperationID      string
	EmployeeID       string
	KeyID            string
	PublicModel      string
	ParentResponseID string
	ProviderKind     string
	SourceAddr       netip.Addr
	PolicyRevision   int64
	Background       bool
	StoreBody        bool
	Items            []responseStateItem
	CreatedAt        time.Time
	TerminalAt       *time.Time
}

type responseResourceView struct {
	ID               string
	EmployeeID       string
	KeyID            string
	PublicModel      string
	ParentResponseID string
	Background       bool
	StoreBody        bool
	Status           string
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	TerminalAt       *time.Time
	ExpiresAt        time.Time
	Tombstoned       bool
	TaskID           string
	RequestID        string
	Items            []responseStateItem
}

func newResponseResourceCoordinator(app *App, events accounting.EventRecorder) (*responseResourceCoordinator, error) {
	txEvents, ok := events.(accounting.TransactionalEventRecorder)
	if app == nil || app.store == nil || app.store.db == nil || app.secrets == nil || !ok {
		return nil, errResponseResourceUnavailable
	}
	return &responseResourceCoordinator{
		app: app, db: app.store.db, secrets: app.secrets, events: txEvents,
		now:      func() time.Time { return time.Now().UTC() },
		commitTx: func(tx *sql.Tx) error { return tx.Commit() },
	}, nil
}

func (c *responseResourceCoordinator) Create(ctx context.Context, input responseResourceCreateInput) (responseResourceView, error) {
	if !validResponseResourceCreate(input) || c == nil || c.db == nil || c.secrets == nil || c.events == nil || c.now == nil || c.commitTx == nil {
		return responseResourceView{}, errResponseResourceInvalid
	}
	created := input.CreatedAt.UTC()
	if created.IsZero() {
		created = c.now().UTC()
	}
	if created.IsZero() || created.Location() != time.UTC {
		return responseResourceView{}, errResponseResourceInvalid
	}
	var terminal time.Time
	if input.TerminalAt != nil {
		terminal = input.TerminalAt.UTC()
		if terminal.IsZero() || terminal.Before(created) {
			return responseResourceView{}, errResponseResourceInvalid
		}
	}
	fingerprint, err := c.inputFingerprint(input)
	if err != nil {
		return responseResourceView{}, err
	}
	responseID, err := newID("resp")
	if err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	dek, err := newResponseDEK()
	if err != nil {
		return responseResourceView{}, err
	}
	defer clear(dek)
	binding := responseDEKBinding{employeeID: input.EmployeeID, keyID: input.KeyID, responseID: responseID}
	wrapNonce, wrappedDEK, err := c.secrets.wrapResponseDEK(binding, dek)
	if err != nil {
		return responseResourceView{}, err
	}
	type encryptedItem struct {
		kind              string
		nonce, ciphertext []byte
	}
	encrypted := make([]encryptedItem, 0, len(input.Items))
	for sequence, item := range input.Items {
		nonce, ciphertext, err := sealResponseItem(dek, responseItemBinding{responseDEKBinding: binding, sequence: int64(sequence), itemType: item.Type}, item.Payload)
		if err != nil {
			return responseResourceView{}, err
		}
		encrypted = append(encrypted, encryptedItem{kind: item.Type, nonce: nonce, ciphertext: ciphertext})
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	defer tx.Rollback()
	if err := c.authorizeOwnerTx(ctx, tx, input.EmployeeID, input.KeyID, input.PublicModel, created, true, input.SourceAddr, input.PolicyRevision); err != nil {
		return responseResourceView{}, err
	}
	if existing, storedFingerprint, found, err := loadResponseResourceByOperationTx(ctx, tx, input.EmployeeID, input.KeyID, input.OperationID); err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	} else if found {
		if subtle.ConstantTimeCompare(storedFingerprint, fingerprint) != 1 {
			return responseResourceView{}, errResponseResourceConflict
		}
		return existing, nil
	}
	if input.ParentResponseID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM response_resources WHERE id=? AND employee_id=? AND key_id=? AND public_model=? AND status='completed' AND tombstoned_at IS NULL AND expires_at>?`, input.ParentResponseID, input.EmployeeID, input.KeyID, input.PublicModel, created.Format(time.RFC3339Nano)).Scan(&count); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if count != 1 {
			return responseResourceView{}, errResponseResourceForbidden
		}
	}
	status := "completed"
	var terminalAt any = terminal.Format(time.RFC3339Nano)
	expires := terminal.Add(responseResourceTTL)
	if input.Background {
		status, terminalAt, expires = "queued", nil, created.Add(backgroundTaskTTL)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO response_resources(id,operation_id,input_fingerprint,employee_id,key_id,public_model,parent_response_id,background,store_body,status,revision,schema_version,dek_wrap_nonce,wrapped_dek,created_at,updated_at,terminal_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?,?)`,
		responseID, input.OperationID, fingerprint, input.EmployeeID, input.KeyID, input.PublicModel, nullableResponseID(input.ParentResponseID), responseBoolInteger(input.Background), responseBoolInteger(input.StoreBody), status, responseStateSchemaVersion, wrapNonce, wrappedDEK, created.Format(time.RFC3339Nano), created.Format(time.RFC3339Nano), terminalAt, expires.Format(time.RFC3339Nano))
	if err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	for sequence, item := range encrypted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO response_resource_items(response_id,sequence,item_type,schema_version,nonce,ciphertext,created_at) VALUES(?,?,?,?,?,?,?)`, responseID, sequence, item.kind, responseStateSchemaVersion, item.nonce, item.ciphertext, created.Format(time.RFC3339Nano)); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
	}
	view := responseResourceView{ID: responseID, EmployeeID: input.EmployeeID, KeyID: input.KeyID, PublicModel: input.PublicModel, ParentResponseID: input.ParentResponseID, Background: input.Background, StoreBody: input.StoreBody, Status: status, Revision: 1, CreatedAt: created, UpdatedAt: created, ExpiresAt: expires}
	if !input.Background {
		view.TerminalAt = &terminal
	} else {
		taskID, idErr := newID("task")
		if idErr != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		requestID, idErr := newID("req")
		if idErr != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		provider, providerErr := usageProvider(input.ProviderKind, accounting.ProtocolOpenAIResponses)
		if providerErr != nil {
			return responseResourceView{}, errResponseResourceInvalid
		}
		if err := c.events.BeginRequestTx(ctx, tx, accounting.RequestStart{ID: requestID, EmployeeID: input.EmployeeID, KeyID: input.KeyID, ModelID: input.PublicModel, Provider: provider, StartedAt: created}); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO background_tasks(id,response_id,request_id,employee_id,key_id,public_model,provider_kind,status,revision,created_at,updated_at,expires_at) VALUES(?,?,?,?,?,?,?,'queued',1,?,?,?)`, taskID, responseID, requestID, input.EmployeeID, input.KeyID, input.PublicModel, input.ProviderKind, created.Format(time.RFC3339Nano), created.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano))
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO background_task_policy_contexts(task_id,source_addr,key_policy_revision) VALUES(?,?,?)`, taskID, input.SourceAddr.String(), input.PolicyRevision); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		view.TaskID, view.RequestID = taskID, requestID
	}
	if err := c.commitTx(tx); err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	return view, nil
}

func (c *responseResourceCoordinator) Get(ctx context.Context, auth employeeAuth, responseID string, includeBody bool) (responseResourceView, error) {
	if c == nil || c.db == nil || c.secrets == nil || !validIdentifier(responseID, 128) || !validIdentifier(auth.EmployeeID, 128) || !validIdentifier(auth.KeyID, 128) {
		return responseResourceView{}, errResponseResourceInvalid
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	defer tx.Rollback()
	view, wrapNonce, wrappedDEK, err := loadOwnedResponseResourceTx(ctx, tx, auth, responseID)
	if err != nil {
		return responseResourceView{}, err
	}
	if err := c.authorizeOwnerTx(ctx, tx, auth.EmployeeID, auth.KeyID, view.PublicModel, c.now().UTC(), includeBody, auth.SourceAddr, 0); err != nil {
		return responseResourceView{}, err
	}
	if view.Tombstoned || !view.ExpiresAt.After(c.now().UTC()) {
		return responseResourceView{}, errResponseResourceNotFound
	}
	if includeBody {
		binding := responseDEKBinding{employeeID: auth.EmployeeID, keyID: auth.KeyID, responseID: responseID}
		dek, err := c.secrets.unwrapResponseDEK(binding, wrapNonce, wrappedDEK)
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		defer clear(dek)
		rows, err := tx.QueryContext(ctx, `SELECT sequence,item_type,nonce,ciphertext FROM response_resource_items WHERE response_id=? ORDER BY sequence`, responseID)
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		for rows.Next() {
			var sequence int64
			var kind string
			var nonce, ciphertext []byte
			if err := rows.Scan(&sequence, &kind, &nonce, &ciphertext); err != nil {
				rows.Close()
				return responseResourceView{}, errResponseResourceUnavailable
			}
			plaintext, err := openResponseItem(dek, responseItemBinding{responseDEKBinding: binding, sequence: sequence, itemType: kind}, nonce, ciphertext)
			if err != nil {
				rows.Close()
				return responseResourceView{}, errResponseResourceUnavailable
			}
			view.Items = append(view.Items, responseStateItem{Type: kind, Payload: plaintext})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if err := rows.Close(); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	return view, nil
}

func (c *responseResourceCoordinator) Delete(ctx context.Context, auth employeeAuth, responseID string) error {
	if c == nil || c.db == nil || !validIdentifier(responseID, 128) {
		return errResponseResourceInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errResponseResourceUnavailable
	}
	defer tx.Rollback()
	view, _, _, err := loadOwnedResponseResourceTx(ctx, tx, auth, responseID)
	if err != nil {
		return err
	}
	if view.Tombstoned {
		return tx.Commit()
	}
	if view.Status != "completed" && view.Status != "failed" && view.Status != "cancelled" && view.Status != "interrupted" {
		return errResponseResourceConflict
	}
	now := c.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE response_resources SET dek_wrap_nonce=NULL,wrapped_dek=NULL,tombstoned_at=?,updated_at=?,revision=revision+1 WHERE id=? AND employee_id=? AND key_id=? AND tombstoned_at IS NULL`, now, now, responseID, auth.EmployeeID, auth.KeyID)
	if err != nil {
		return errResponseResourceUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errResponseResourceConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM response_resource_items WHERE response_id=?`, responseID); err != nil {
		return errResponseResourceUnavailable
	}
	if err := c.commitTx(tx); err != nil {
		return errResponseResourceUnavailable
	}
	return nil
}

// Recover makes the no-replay rule durable across process restarts. Any task
// which may have crossed its dispatch barrier is terminally interrupted. A
// queued task is retained unless its fixed pre-dispatch TTL has elapsed.
func (c *responseResourceCoordinator) Recover(ctx context.Context) error {
	if c == nil || c.db == nil || c.events == nil || c.now == nil || ctx == nil {
		return errResponseResourceUnavailable
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errResponseResourceUnavailable
	}
	defer tx.Rollback()
	stamp := c.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE background_tasks SET claim_token=NULL,claimed_at=NULL,updated_at=?,revision=revision+1 WHERE status='queued' AND claim_token IS NOT NULL`, stamp); err != nil {
		return errResponseResourceUnavailable
	}
	now := c.now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.response_id,t.request_id,COALESCE(t.attempt_id,''),t.status,t.created_at FROM background_tasks t LEFT JOIN background_task_policy_contexts pc ON pc.task_id=t.id WHERE t.status IN ('dispatch_authorized','in_progress') OR (t.status='queued' AND (t.expires_at<=? OR pc.task_id IS NULL)) ORDER BY t.created_at,t.id`, now.Format(time.RFC3339Nano))
	if err != nil {
		return errResponseResourceUnavailable
	}
	type recovery struct{ taskID, responseID, requestID, attemptID, status, createdAt string }
	var recoveries []recovery
	for rows.Next() {
		var item recovery
		if err := rows.Scan(&item.taskID, &item.responseID, &item.requestID, &item.attemptID, &item.status, &item.createdAt); err != nil {
			rows.Close()
			return errResponseResourceUnavailable
		}
		recoveries = append(recoveries, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errResponseResourceUnavailable
	}
	if err := rows.Close(); err != nil {
		return errResponseResourceUnavailable
	}
	for _, item := range recoveries {
		terminal := now
		if created, parseErr := parseTime(item.createdAt); parseErr != nil {
			return errResponseResourceUnavailable
		} else if terminal.Before(created) {
			terminal = created
		}
		if item.attemptID != "" {
			if err := c.events.FinishAttemptTx(ctx, tx, accounting.AttemptFinish{ID: item.attemptID, Status: accounting.StatusInterrupted, FinishedAt: terminal, ReliableUsage: true}); err != nil {
				return errResponseResourceUnavailable
			}
		}
		if err := c.events.FinishRequestTx(ctx, tx, accounting.RequestFinish{ID: item.requestID, Status: accounting.StatusInterrupted, FinishedAt: terminal}); err != nil {
			return errResponseResourceUnavailable
		}
		stamp := terminal.Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status='interrupted',claim_token=NULL,claimed_at=NULL,finished_at=?,updated_at=?,revision=revision+1 WHERE id=? AND status=?`, stamp, stamp, item.taskID, item.status)
		if err != nil {
			return errResponseResourceUnavailable
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errResponseResourceUnavailable
		}
		result, err = tx.ExecContext(ctx, `UPDATE response_resources SET status='interrupted',terminal_at=?,updated_at=?,expires_at=?,revision=revision+1 WHERE id=? AND status=?`, stamp, stamp, terminal.Add(responseResourceTTL).Format(time.RFC3339Nano), item.responseID, item.status)
		if err != nil {
			return errResponseResourceUnavailable
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errResponseResourceUnavailable
		}
	}
	if err := c.commitTx(tx); err != nil {
		return errResponseResourceUnavailable
	}
	return nil
}

func (c *responseResourceCoordinator) Cancel(ctx context.Context, auth employeeAuth, responseID string) (responseResourceView, error) {
	if c == nil || c.db == nil || c.events == nil || c.now == nil || !validIdentifier(responseID, 128) {
		return responseResourceView{}, errResponseResourceInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	defer tx.Rollback()
	view, _, _, err := loadOwnedResponseResourceTx(ctx, tx, auth, responseID)
	if err != nil {
		return responseResourceView{}, err
	}
	if err := c.authorizeOwnerTx(ctx, tx, auth.EmployeeID, auth.KeyID, view.PublicModel, c.now().UTC(), false, netip.Addr{}, 0); err != nil {
		return responseResourceView{}, err
	}
	if !view.Background || responseTerminalStatus(view.Status) {
		if err := tx.Commit(); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		return view, nil
	}
	now := c.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if view.Status == "queued" {
		if err := c.events.FinishRequestTx(ctx, tx, accounting.RequestFinish{ID: view.RequestID, Status: accounting.StatusCancelled, FinishedAt: now}); err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status='cancelled',claim_token=NULL,claimed_at=NULL,cancel_requested_at=?,finished_at=?,updated_at=?,revision=revision+1 WHERE id=? AND status='queued'`, stamp, stamp, stamp, view.TaskID)
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return responseResourceView{}, errResponseResourceConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE response_resources SET status='cancelled',terminal_at=?,updated_at=?,expires_at=?,revision=revision+1 WHERE id=? AND status='queued'`, stamp, stamp, now.Add(responseResourceTTL).Format(time.RFC3339Nano), responseID)
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return responseResourceView{}, errResponseResourceConflict
		}
		view.Status, view.UpdatedAt, view.TerminalAt, view.ExpiresAt, view.Revision = "cancelled", now, &now, now.Add(responseResourceTTL), view.Revision+1
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET cancel_requested_at=COALESCE(cancel_requested_at,?),updated_at=?,revision=revision+1 WHERE id=? AND status IN ('dispatch_authorized','in_progress')`, stamp, stamp, view.TaskID)
		if err != nil {
			return responseResourceView{}, errResponseResourceUnavailable
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return responseResourceView{}, errResponseResourceConflict
		}
	}
	if err := c.commitTx(tx); err != nil {
		return responseResourceView{}, errResponseResourceUnavailable
	}
	if !responseTerminalStatus(view.Status) && c.cancelTask != nil {
		c.cancelTask(view.TaskID)
	}
	return view, nil
}

func (c *responseResourceCoordinator) InterruptQueuedClaim(ctx context.Context, taskID, responseID, requestID, claimToken string) error {
	if c == nil || c.events == nil || !validIdentifier(taskID, 128) || !validIdentifier(responseID, 128) || !validIdentifier(requestID, 128) || !validIdentifier(claimToken, 128) {
		return errResponseResourceInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return errResponseResourceUnavailable
	}
	defer tx.Rollback()
	now := c.now().UTC()
	var createdText string
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM background_tasks WHERE id=? AND response_id=? AND request_id=? AND status='queued' AND claim_token=?`, taskID, responseID, requestID, claimToken).Scan(&createdText); err != nil {
		return errResponseResourceConflict
	}
	created, err := parseTime(createdText)
	if err != nil {
		return errResponseResourceUnavailable
	}
	if now.Before(created) {
		now = created
	}
	stamp := now.Format(time.RFC3339Nano)
	if err := c.events.FinishRequestTx(ctx, tx, accounting.RequestFinish{ID: requestID, Status: accounting.StatusInterrupted, FinishedAt: now}); err != nil {
		return errResponseResourceUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status='interrupted',claim_token=NULL,claimed_at=NULL,finished_at=?,updated_at=?,revision=revision+1 WHERE id=? AND response_id=? AND request_id=? AND status='queued' AND claim_token=?`, stamp, stamp, taskID, responseID, requestID, claimToken)
	if err != nil {
		return errResponseResourceUnavailable
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errResponseResourceConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE response_resources SET status='interrupted',terminal_at=?,updated_at=?,expires_at=?,revision=revision+1 WHERE id=? AND status='queued'`, stamp, stamp, now.Add(responseResourceTTL).Format(time.RFC3339Nano), responseID)
	if err != nil {
		return errResponseResourceUnavailable
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errResponseResourceConflict
	}
	if err := c.commitTx(tx); err != nil {
		return errResponseResourceUnavailable
	}
	return nil
}

func responseTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func validResponseResourceCreate(input responseResourceCreateInput) bool {
	if !validIdentifier(input.OperationID, 128) || !validIdentifier(input.EmployeeID, 128) || !validIdentifier(input.KeyID, 128) || !validIdentifier(input.PublicModel, 128) || !input.SourceAddr.IsValid() || input.SourceAddr.Zone() != "" || input.SourceAddr != input.SourceAddr.Unmap() || input.PolicyRevision < 1 || input.PolicyRevision > 9007199254740991 || len(input.Items) == 0 || len(input.Items) > responseStateMaxItems || (!input.Background && !input.StoreBody) {
		return false
	}
	if input.Background == (input.TerminalAt != nil) {
		return false
	}
	if input.ParentResponseID != "" && !validIdentifier(input.ParentResponseID, 128) {
		return false
	}
	if input.ProviderKind != "openai-compatible" && input.ProviderKind != codexMembershipProvider {
		return false
	}
	totalPlaintext := 0
	for _, item := range input.Items {
		if !json.Valid(item.Payload) || len(item.Payload) == 0 || len(item.Payload) > responseStateMaxPlaintext || !validResponseItemBinding(responseItemBinding{responseDEKBinding: responseDEKBinding{employeeID: input.EmployeeID, keyID: input.KeyID, responseID: "resp_validation"}, itemType: item.Type}) {
			return false
		}
		if len(item.Payload) > responseStateMaxPlaintext-totalPlaintext {
			return false
		}
		totalPlaintext += len(item.Payload)
	}
	if !input.CreatedAt.IsZero() && input.CreatedAt.Location() != time.UTC {
		return false
	}
	return input.TerminalAt == nil || (input.TerminalAt.Location() == time.UTC && (input.CreatedAt.IsZero() || !input.TerminalAt.Before(input.CreatedAt)))
}

func (c *responseResourceCoordinator) inputFingerprint(input responseResourceCreateInput) ([]byte, error) {
	if c == nil || c.secrets == nil || len(c.secrets.responseFingerprintKey) != sha256.Size {
		return nil, errResponseResourceUnavailable
	}
	h := hmac.New(sha256.New, c.secrets.responseFingerprintKey)
	writeFingerprintPart := func(value []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(value)
	}
	writeFingerprintPart([]byte("cpacloud/response-state-operation/v1"))
	for _, value := range []string{input.OperationID, input.EmployeeID, input.KeyID, input.PublicModel, input.ParentResponseID, input.ProviderKind} {
		writeFingerprintPart([]byte(value))
	}
	writeFingerprintPart([]byte(input.SourceAddr.String()))
	var revision [8]byte
	binary.BigEndian.PutUint64(revision[:], uint64(input.PolicyRevision))
	writeFingerprintPart(revision[:])
	flags := byte(0)
	if input.Background {
		flags |= 1
	}
	if input.StoreBody {
		flags |= 2
	}
	writeFingerprintPart([]byte{flags})
	for _, item := range input.Items {
		writeFingerprintPart([]byte(item.Type))
		writeFingerprintPart(item.Payload)
	}
	return h.Sum(nil), nil
}

func (c *responseResourceCoordinator) authorizeOwnerTx(ctx context.Context, tx *sql.Tx, employeeID, keyID, model string, at time.Time, requireCurrentModelPolicy bool, sourceAddr netip.Addr, expectedPolicyRevision int64) error {
	var status, mode string
	var expires, revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT e.status,e.model_mode,k.expires_at,k.revoked_at FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.id=? AND k.employee_id=?`, keyID, employeeID).Scan(&status, &mode, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) || status != "active" || revoked.Valid {
		return errResponseResourceForbidden
	}
	if err != nil {
		return errResponseResourceUnavailable
	}
	if expires.Valid {
		expiry, err := parseTime(expires.String)
		if err != nil || !at.Before(expiry) {
			return errResponseResourceForbidden
		}
	}
	if !requireCurrentModelPolicy {
		return nil
	}
	policy, err := keypolicy.LoadTx(ctx, tx, keyID)
	if err != nil {
		if errors.Is(err, keypolicy.ErrNotFound) || errors.Is(err, keypolicy.ErrPolicyMissing) {
			return errResponseResourceForbidden
		}
		return errResponseResourceUnavailable
	}
	if expectedPolicyRevision > 0 && policy.Revision != expectedPolicyRevision {
		return errResponseResourceForbidden
	}
	if !keypolicy.Allows(policy, keypolicy.ProtocolOpenAIResponses, model) || !keypolicy.AllowsSource(policy, sourceAddr) {
		return errResponseResourceForbidden
	}
	var allowed int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND m.archived=0 AND u.enabled=1 AND u.archived=0 AND (?='all' OR EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id))`, model, mode, employeeID).Scan(&allowed)
	if err != nil {
		return errResponseResourceUnavailable
	}
	if allowed != 1 {
		return errResponseResourceForbidden
	}
	return nil
}

func loadResponseResourceByOperationTx(ctx context.Context, tx *sql.Tx, employeeID, keyID, operationID string) (responseResourceView, []byte, bool, error) {
	var id string
	var fingerprint []byte
	err := tx.QueryRowContext(ctx, `SELECT id,input_fingerprint FROM response_resources WHERE employee_id=? AND key_id=? AND operation_id=?`, employeeID, keyID, operationID).Scan(&id, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return responseResourceView{}, nil, false, nil
	}
	if err != nil {
		return responseResourceView{}, nil, false, err
	}
	view, _, _, err := loadOwnedResponseResourceTx(ctx, tx, employeeAuth{EmployeeID: employeeID, KeyID: keyID}, id)
	return view, fingerprint, err == nil, err
}

func loadOwnedResponseResourceTx(ctx context.Context, tx *sql.Tx, auth employeeAuth, responseID string) (responseResourceView, []byte, []byte, error) {
	var view responseResourceView
	var parent, terminal, tombstone sql.NullString
	var background, store int
	var created, updated, expires string
	var wrapNonce, wrappedDEK []byte
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.employee_id,r.key_id,r.public_model,r.parent_response_id,r.background,r.store_body,r.status,r.revision,r.created_at,r.updated_at,r.terminal_at,r.expires_at,r.tombstoned_at,r.dek_wrap_nonce,r.wrapped_dek,COALESCE(t.id,''),COALESCE(t.request_id,'') FROM response_resources r LEFT JOIN background_tasks t ON t.response_id=r.id WHERE r.id=? AND r.employee_id=? AND r.key_id=?`, responseID, auth.EmployeeID, auth.KeyID).Scan(&view.ID, &view.EmployeeID, &view.KeyID, &view.PublicModel, &parent, &background, &store, &view.Status, &view.Revision, &created, &updated, &terminal, &expires, &tombstone, &wrapNonce, &wrappedDEK, &view.TaskID, &view.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return responseResourceView{}, nil, nil, errResponseResourceNotFound
	}
	if err != nil {
		return responseResourceView{}, nil, nil, errResponseResourceUnavailable
	}
	view.ParentResponseID, view.Background, view.StoreBody, view.Tombstoned = parent.String, background == 1, store == 1, tombstone.Valid
	view.CreatedAt, err = parseTime(created)
	if err == nil {
		view.UpdatedAt, err = parseTime(updated)
	}
	if err == nil {
		view.ExpiresAt, err = parseTime(expires)
	}
	if err != nil {
		return responseResourceView{}, nil, nil, errResponseResourceUnavailable
	}
	if terminal.Valid {
		parsed, err := parseTime(terminal.String)
		if err != nil {
			return responseResourceView{}, nil, nil, errResponseResourceUnavailable
		}
		view.TerminalAt = &parsed
	}
	return view, wrapNonce, wrappedDEK, nil
}

func nullableResponseID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func responseBoolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
