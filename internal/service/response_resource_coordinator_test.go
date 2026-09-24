package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/keypolicy"
)

func TestResponseResourceCoordinatorEncryptedOwnershipAndDeletion(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 6, 0, 0, 0, time.UTC)
	coordinator.now = func() time.Time { return now }
	input := responseResourceCreateInput{
		OperationID: "op_state_one",
		EmployeeID:  "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
		StoreBody: true, CreatedAt: now, TerminalAt: responseTimePointer(now.Add(time.Second)),
		Items: []responseStateItem{
			{Type: "instructions", Payload: []byte(`"synthetic-instructions-secret"`)},
			{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"synthetic-prompt-secret"}`)},
			{Type: "function_call", Payload: []byte(`{"type":"function_call","call_id":"call_one","name":"lookup","arguments":"{}"}`)},
			{Type: "function_call_output", Payload: []byte(`{"type":"function_call_output","call_id":"call_one","output":"synthetic-output-secret"}`)},
		},
	}
	created, err := coordinator.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "completed" || created.Background || created.TerminalAt == nil || created.Revision != 1 {
		t.Fatalf("unexpected created resource: %+v", created)
	}
	replayed, err := coordinator.Create(context.Background(), input)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("idempotent replay id=%q want=%q err=%v", replayed.ID, created.ID, err)
	}
	conflict := input
	conflict.Items = append([]responseStateItem(nil), input.Items...)
	conflict.Items[1].Payload = []byte(`{"type":"message","role":"user","content":"changed"}`)
	if _, err := coordinator.Create(context.Background(), conflict); !errors.Is(err, errResponseResourceConflict) {
		t.Fatalf("operation conflict not rejected: %v", err)
	}
	otherOwner := input
	otherOwner.EmployeeID, otherOwner.KeyID = "emp_other", "key_other"
	otherCreated, err := coordinator.Create(context.Background(), otherOwner)
	if err != nil || otherCreated.ID == created.ID {
		t.Fatalf("owner-scoped operation id create id=%q err=%v", otherCreated.ID, err)
	}

	loaded, err := coordinator.Get(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"}, created.ID, true)
	if err != nil || len(loaded.Items) != len(input.Items) {
		t.Fatalf("load items=%d err=%v", len(loaded.Items), err)
	}
	for index := range input.Items {
		if loaded.Items[index].Type != input.Items[index].Type || !bytes.Equal(loaded.Items[index].Payload, input.Items[index].Payload) {
			t.Fatalf("loaded item %d mismatch", index)
		}
		clear(loaded.Items[index].Payload)
	}
	if _, err := coordinator.Get(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_other"}, created.ID, true); !errors.Is(err, errResponseResourceNotFound) {
		t.Fatalf("cross-key read was not hidden: %v", err)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT group_concat(hex(ciphertext),'') FROM response_resource_items WHERE response_id=?`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"synthetic-instructions-secret", "synthetic-prompt-secret", "synthetic-output-secret"} {
		if bytes.Contains(bytes.ToLower(stored), bytes.ToLower([]byte(marker))) {
			t.Fatalf("ciphertext query exposed %s", marker)
		}
	}
	var storedFingerprint []byte
	if err := db.QueryRow(`SELECT input_fingerprint FROM response_resources WHERE id=?`, created.ID).Scan(&storedFingerprint); err != nil {
		t.Fatal(err)
	}
	rawHash := sha256.Sum256(input.Items[1].Payload)
	if len(storedFingerprint) != sha256.Size || bytes.Equal(storedFingerprint, rawHash[:]) {
		t.Fatal("operation fingerprint was not independently keyed")
	}
	assertResponseSecretsAbsentFromSQLite(t, db, rawHash[:], "synthetic-instructions-secret", "synthetic-prompt-secret", "synthetic-output-secret")
	if err := coordinator.Delete(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"}, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Delete(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"}, created.ID); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if _, err := coordinator.Get(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"}, created.ID, true); !errors.Is(err, errResponseResourceNotFound) {
		t.Fatalf("tombstoned resource remained readable: %v", err)
	}
	var keys, items int
	if err := db.QueryRow(`SELECT COUNT(dek_wrap_nonce)+COUNT(wrapped_dek) FROM response_resources WHERE id=?`, created.ID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM response_resource_items WHERE response_id=?`, created.ID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || items != 0 {
		t.Fatalf("delete retained keys=%d items=%d", keys, items)
	}
}

func TestBackgroundResponseCreationIsAtomicWithAccountingRequest(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)
	coordinator.now = func() time.Time { return now }
	input := responseResourceCreateInput{
		OperationID: "op_background_one",
		EmployeeID:  "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
		Background: true, CreatedAt: now,
		Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"queued"}`)}},
	}
	created, err := coordinator.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "queued" || created.TaskID == "" || created.RequestID == "" || !created.ExpiresAt.Equal(now.Add(backgroundTaskTTL)) {
		t.Fatalf("unexpected background resource: %+v", created)
	}
	var requestCount, taskCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE id=? AND status='pending'`, created.RequestID).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM background_tasks WHERE id=? AND response_id=? AND status='queued'`, created.TaskID, created.ID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || taskCount != 1 {
		t.Fatalf("background creation request=%d task=%d", requestCount, taskCount)
	}

	failing, _ := newResponseResourceTestCoordinatorFromDB(t, db, coordinator.secrets)
	failing.now = coordinator.now
	failing.commitTx = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
	failedInput := input
	failedInput.OperationID = "op_background_failed"
	if _, err := failing.Create(context.Background(), failedInput); !errors.Is(err, errResponseResourceUnavailable) {
		t.Fatalf("commit failure classification: %v", err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM response_resources WHERE operation_id='op_background_failed')+(SELECT COUNT(*) FROM accounting_requests WHERE started_at=? AND id<>?)`, now.Format(time.RFC3339Nano), created.RequestID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("failed commit leaked %d durable rows", leaked)
	}
}

func TestBackgroundResponseCancelAndRestartRecoveryAreNoReplay(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 7, 30, 0, 0, time.UTC)
	coordinator.now = func() time.Time { return now }
	create := func(operation string) responseResourceView {
		view, err := coordinator.Create(context.Background(), responseResourceCreateInput{
			OperationID: operation, EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
			Background: true, CreatedAt: now, Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"queued"}`)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	cancelled := create("op_cancel_queued")
	view, err := coordinator.Cancel(context.Background(), employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"}, cancelled.ID)
	if err != nil || view.Status != "cancelled" {
		t.Fatalf("cancelled view=%+v err=%v", view, err)
	}
	var requestStatus string
	if err := db.QueryRow(`SELECT status FROM accounting_requests WHERE id=?`, cancelled.RequestID).Scan(&requestStatus); err != nil || requestStatus != "cancelled" {
		t.Fatalf("cancel request status=%q err=%v", requestStatus, err)
	}
	claimed := create("op_restart_claimed")
	if _, err := db.Exec(`UPDATE background_tasks SET claim_token='claim_crashed',claimed_at=? WHERE id=?`, now.Format(time.RFC3339Nano), claimed.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	var claimToken sql.NullString
	if err := db.QueryRow(`SELECT status,claim_token FROM background_tasks WHERE id=?`, claimed.TaskID).Scan(&requestStatus, &claimToken); err != nil || requestStatus != "queued" || claimToken.Valid {
		t.Fatalf("pre-dispatch claim recovery status=%q claim=%v err=%v", requestStatus, claimToken, err)
	}

	dispatched := create("op_restart_dispatched")
	attemptID := dispatched.RequestID + ":1"
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger := accounting.NewLedger(db)
	if err := ledger.BeginAttemptTx(context.Background(), tx, accounting.AttemptStart{ID: attemptID, RequestID: dispatched.RequestID, AccountID: "ups_one", Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: now, Protocol: accounting.ProtocolOpenAIResponses, EffectiveModel: "actual", Evidence: accounting.EvidenceBackgroundResult}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatchedTx(context.Background(), tx, accounting.AttemptDispatch{ID: attemptID, OperationID: attemptID + ":dispatch", DispatchedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE background_tasks SET status='in_progress',attempt_id=?,dispatch_operation_id=?,dispatch_authorized_at=?,started_at=? WHERE id=?`, attemptID, attemptID+":dispatch", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), dispatched.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE response_resources SET status='in_progress',terminal_at=NULL WHERE id=?`, dispatched.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	var taskStatus, responseStatus, attemptStatus string
	if err := db.QueryRow(`SELECT t.status,r.status,a.status FROM background_tasks t JOIN response_resources r ON r.id=t.response_id JOIN accounting_attempts a ON a.id=t.attempt_id WHERE t.id=?`, dispatched.TaskID).Scan(&taskStatus, &responseStatus, &attemptStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "interrupted" || responseStatus != "interrupted" || attemptStatus != "interrupted" {
		t.Fatalf("recovered task=%s response=%s attempt=%s", taskStatus, responseStatus, attemptStatus)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("recovery was not idempotent: %v", err)
	}
}

func TestResponseResourceCreateRequiresTerminalSuccessAndAggregateBounds(t *testing.T) {
	coordinator, _ := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	base := responseResourceCreateInput{
		OperationID: "op_bounds", EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
		StoreBody: true, CreatedAt: now,
		Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"ok"}`)}},
	}
	if _, err := coordinator.Create(context.Background(), base); !errors.Is(err, errResponseResourceInvalid) {
		t.Fatalf("synchronous create without terminal success was accepted: %v", err)
	}
	terminalBefore := now.Add(-time.Second)
	base.TerminalAt = &terminalBefore
	if _, err := coordinator.Create(context.Background(), base); !errors.Is(err, errResponseResourceInvalid) {
		t.Fatalf("terminal timestamp before creation was accepted: %v", err)
	}
	terminal := now.Add(time.Second)
	base.TerminalAt = &terminal
	base.Items = make([]responseStateItem, responseStateMaxItems+1)
	for index := range base.Items {
		base.Items[index] = responseStateItem{Type: "message", Payload: []byte(`"x"`)}
	}
	if _, err := coordinator.Create(context.Background(), base); !errors.Is(err, errResponseResourceInvalid) {
		t.Fatalf("excessive item count was accepted: %v", err)
	}
	chunk := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, responseStateMaxPlaintext/2)...)
	chunk = append(chunk, '"')
	base.Items = []responseStateItem{{Type: "message", Payload: chunk}, {Type: "message", Payload: chunk}}
	if _, err := coordinator.Create(context.Background(), base); !errors.Is(err, errResponseResourceInvalid) {
		t.Fatalf("excessive aggregate plaintext was accepted: %v", err)
	}
}

func TestStoredResponseTerminalWriteSurvivesClientCancellation(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 8, 30, 0, 0, time.UTC)
	plan := &responsePersistencePlan{
		coordinator: coordinator, auth: employeeAuth{EmployeeID: "emp_one", KeyID: "key_one"},
		model: "model_one", operationID: "op_cancelled_client_terminal", createdAt: now,
		items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"secret"}`)}},
	}
	clientCtx, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	writeCtx, cancelWrite := durableResponseWriteContext(clientCtx)
	defer cancelWrite()
	stored, err := plan.persistCompleted(writeCtx, "openai-compatible", []byte(`{"id":"provider-id","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":"done"}]}`))
	if err != nil || !bytes.Contains(stored, []byte(`"store":true`)) {
		t.Fatalf("durable terminal write err=%v body=%s", err, stored)
	}
	var resources int
	if err := db.QueryRow(`SELECT COUNT(*) FROM response_resources WHERE operation_id=? AND status='completed'`, plan.operationID).Scan(&resources); err != nil || resources != 1 {
		t.Fatalf("completed resources=%d err=%v", resources, err)
	}
}

func responseTimePointer(value time.Time) *time.Time {
	return &value
}

func assertResponseSecretsAbsentFromSQLite(t *testing.T, db *sql.DB, rawHash []byte, markers ...string) {
	t.Helper()
	var sequence int
	var name, databasePath string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal"} {
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, rawHash) {
			t.Fatalf("SQLite file %s exposed a predictable raw request hash", path)
		}
		for _, marker := range markers {
			if bytes.Contains(content, []byte(marker)) {
				t.Fatalf("SQLite file %s exposed plaintext marker %q", path, marker)
			}
		}
	}
}

func newResponseResourceTestCoordinator(t *testing.T) (*responseResourceCoordinator, *sql.DB) {
	t.Helper()
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	if err := keypolicy.Migrate(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	seedResponseResourceParents(t, s.db)
	coordinator, _ := newResponseResourceTestCoordinatorFromDB(t, s.db, secretStore)
	return coordinator, s.db
}

func newResponseResourceTestCoordinatorFromDB(t *testing.T, db *sql.DB, secretStore *secrets) (*responseResourceCoordinator, *accounting.Ledger) {
	t.Helper()
	ctx := context.Background()
	if err := migrateResponseResources(ctx, db); err != nil {
		t.Fatal(err)
	}
	ledger := accounting.NewLedger(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MigrateV2(ctx); err != nil {
		t.Fatal(err)
	}
	coordinator, err := newResponseResourceCoordinator(&App{store: &store{db: db}, secrets: secretStore}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, ledger
}
