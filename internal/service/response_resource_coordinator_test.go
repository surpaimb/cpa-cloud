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
