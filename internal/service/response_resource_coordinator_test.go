package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestResponseResourceCoordinatorEncryptedOwnershipAndDeletion(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 24, 6, 0, 0, 0, time.UTC)
	coordinator.now = func() time.Time { return now }
	fingerprint := bytes.Repeat([]byte{7}, 32)
	input := responseResourceCreateInput{
		OperationID: "op_state_one", InputFingerprint: fingerprint,
		EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
		StoreBody: true, CreatedAt: now,
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
	conflict.InputFingerprint = bytes.Repeat([]byte{8}, 32)
	if _, err := coordinator.Create(context.Background(), conflict); !errors.Is(err, errResponseResourceConflict) {
		t.Fatalf("operation conflict not rejected: %v", err)
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
		OperationID: "op_background_one", InputFingerprint: bytes.Repeat([]byte{9}, 32),
		EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one", ProviderKind: "openai-compatible",
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
	failedInput.InputFingerprint = bytes.Repeat([]byte{10}, 32)
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
