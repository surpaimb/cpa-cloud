package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/protocolconv"
	"cpacloud.local/server/internal/scheduling"
)

type backgroundResponseWorker struct {
	app      *App
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	wg       sync.WaitGroup
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

type backgroundResponseClaim struct {
	view         responseResourceView
	auth         employeeAuth
	providerKind string
	claimToken   string
}

func newBackgroundResponseWorker(app *App) *backgroundResponseWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &backgroundResponseWorker{app: app, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), active: make(map[string]context.CancelFunc)}
	app.responseResources.cancelTask = worker.Cancel
	return worker
}

func (w *backgroundResponseWorker) Start() {
	w.wg.Add(1)
	go w.loop()
}

func (w *backgroundResponseWorker) Close() { w.cancel(); w.wg.Wait() }
func (w *backgroundResponseWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *backgroundResponseWorker) Cancel(taskID string) {
	w.activeMu.Lock()
	cancel := w.active[taskID]
	w.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *backgroundResponseWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for {
			claim, err := w.claimOne()
			if err != nil || claim == nil {
				break
			}
			w.execute(claim)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *backgroundResponseWorker) claimOne() (*backgroundResponseClaim, error) {
	c := w.app.responseResources
	tx, err := c.db.BeginTx(w.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var claim backgroundResponseClaim
	var responseID string
	err = tx.QueryRowContext(w.ctx, `SELECT t.id,t.response_id,t.request_id,t.employee_id,t.key_id,t.public_model,t.provider_kind FROM background_tasks t WHERE t.status='queued' AND t.claim_token IS NULL AND t.expires_at>? ORDER BY t.created_at,t.id LIMIT 1`, now.Format(time.RFC3339Nano)).Scan(&claim.view.TaskID, &responseID, &claim.view.RequestID, &claim.auth.EmployeeID, &claim.auth.KeyID, &claim.view.PublicModel, &claim.providerKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claim.view.ID = responseID
	claim.claimToken, err = newID("claim")
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(w.ctx, `UPDATE background_tasks SET claim_token=?,claimed_at=?,updated_at=?,revision=revision+1 WHERE response_id=? AND status='queued' AND claim_token IS NULL`, claim.claimToken, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), responseID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	loaded, err := c.Get(w.ctx, claim.auth, responseID, true)
	if err != nil {
		_ = c.InterruptQueuedClaim(context.Background(), claim.view.TaskID, responseID, claim.view.RequestID, claim.claimToken)
		return nil, err
	}
	claim.view = loaded
	return &claim, nil
}

func (w *backgroundResponseWorker) execute(claim *backgroundResponseClaim) {
	a := w.app
	taskCtx, cancelTask := context.WithCancel(w.ctx)
	w.activeMu.Lock()
	w.active[claim.view.TaskID] = cancelTask
	w.activeMu.Unlock()
	defer func() { cancelTask(); w.activeMu.Lock(); delete(w.active, claim.view.TaskID); w.activeMu.Unlock() }()
	requestID := claim.view.RequestID
	provider, err := usageProvider(claim.providerKind, accounting.ProtocolOpenAIResponses)
	if err != nil {
		w.interruptClaim(claim)
		return
	}
	started := claim.view.CreatedAt
	usageRequest := &usageLedgerRequest{coordinator: a.usage, id: requestID, provider: provider, protocol: accounting.ProtocolOpenAIResponses, publicModel: claim.view.PublicModel, evidence: accounting.EvidenceBackgroundResult, startedAt: started}
	active := &activeUsageRequest{request: usageRequest}
	if _, err := a.store.db.ExecContext(w.ctx, `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES(?,?,?,?,?,'running') ON CONFLICT(id) DO NOTHING`, requestID, claim.auth.EmployeeID, claim.auth.KeyID, claim.view.PublicModel, started.Format(time.RFC3339Nano)); err != nil {
		w.interruptClaim(claim)
		return
	}
	a.usageRequests.Store(requestID, active)
	defer a.cleanupRequestUsage(requestID)
	payload := backgroundResponsePayload(claim.view)
	body, _ := json.Marshal(payload)
	r, _ := http.NewRequestWithContext(taskCtx, http.MethodPost, "http://localhost/v1/responses", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
	r = withUsageStreamEvidence(r, false)
	governed, guard, failure := a.admitGovernedModel(r, claim.auth, claim.view.PublicModel, accounting.ProtocolOpenAIResponses)
	if failure != nil {
		outcome := "failed"
		if taskCtx.Err() != nil {
			outcome = "cancelled"
		}
		w.finishClaim(claim, usageRequest, nil, outcome, 0)
		return
	}
	r = governed
	usageRequest.guard = guard
	if guard != nil {
		usageRequest.governance = a.usage.governance
	}
	if guard != nil {
		defer guard.Close()
	}
	var upstreamReq *http.Request
	var codexPrepared *codexResponsesPreflight
	selected, lease, failure := a.prepareModelRoute(r, claim.auth, claim.view.PublicModel, []string{"openai-compatible", codexMembershipProvider}, accounting.ProtocolOpenAIResponses, false, func(candidateRequest *http.Request, candidate route) (route, *modelPreflightError) {
		capability, capabilityErr := routeCapability(candidate, accounting.ProtocolOpenAIResponses, false)
		if capabilityErr != nil || capability.UpstreamProtocol != protocolconv.ProtocolOpenAIResponses {
			return route{}, requestPreflightFailure(http.StatusBadRequest, "unsupported_feature", "Background execution requires a native Responses route.")
		}
		copyPayload := make(map[string]json.RawMessage, len(payload))
		for key, value := range payload {
			copyPayload[key] = value
		}
		copyPayload["model"], _ = json.Marshal(candidate.UpstreamModel)
		outgoing, _ := json.Marshal(copyPayload)
		if candidate.ProviderKind == codexMembershipProvider {
			prepared, failed := a.prepareCodexResponses(candidateRequest.Context(), outgoing, candidate)
			if failed != nil {
				return route{}, failed
			}
			codexPrepared = prepared
			return prepared.selected, nil
		}
		credential, decryptErr := a.secrets.decryptCredential(candidate.AccountID, candidate.Ciphertext)
		if decryptErr != nil {
			return route{}, accountPreflightFailure(503, "no_available_route", "No available route.", scheduling.FailureAuth)
		}
		endpoint, endpointErr := validateEndpoint(candidateRequest.Context(), candidate.Endpoint, a.cfg.AllowLoopbackUpstream)
		if endpointErr != nil {
			return route{}, accountPreflightFailure(503, "no_available_route", "No available route.", scheduling.FailurePermanent)
		}
		target, targetErr := upstreamResponsesURL(endpoint)
		if targetErr != nil {
			return route{}, requestPreflightFailure(503, "upstream_unavailable", "Upstream unavailable.")
		}
		upstreamReq, _ = http.NewRequestWithContext(candidateRequest.Context(), http.MethodPost, target, bytes.NewReader(outgoing))
		upstreamReq.Header.Set("Authorization", "Bearer "+credential)
		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Accept", "application/json")
		return candidate, nil
	})
	if failure != nil {
		if codexPrepared != nil {
			codexPrepared.Destroy()
		}
		w.finishClaim(claim, usageRequest, nil, "failed", 0)
		return
	}
	if selected.ProviderKind != claim.providerKind {
		if codexPrepared != nil {
			codexPrepared.Destroy()
		}
		w.finishClaim(claim, usageRequest, nil, "failed", 0)
		return
	}
	if lease != nil {
		r = r.WithContext(lease.Context())
	}
	executionCtx := r.Context()
	defer a.releaseModelLease(lease, requestID, true)
	if codexPrepared != nil {
		defer codexPrepared.Destroy()
	}
	r = withDispatchBarrierHook(r, func(ctx context.Context, tx *sql.Tx, attemptID string, at time.Time) error {
		stamp := at.Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status='dispatch_authorized',attempt_id=?,dispatch_operation_id=?,dispatch_authorized_at=?,updated_at=?,revision=revision+1 WHERE id=? AND status='queued' AND claim_token=?`, attemptID, attemptID+":dispatch", stamp, stamp, claim.view.TaskID, claim.claimToken)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errResponseResourceConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE response_resources SET status='dispatch_authorized',updated_at=?,revision=revision+1 WHERE id=? AND status='queued'`, stamp, claim.view.ID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errResponseResourceConflict
		}
		return nil
	})
	client, failure := a.dispatchModelRoute(r, claim.auth, claim.view.PublicModel, selected, lease, true, upstreamReq)
	if failure != nil {
		outcome := "failed"
		if taskCtx.Err() != nil {
			outcome = "cancelled"
		}
		w.finishClaim(claim, usageRequest, nil, outcome, 0)
		return
	}
	if !w.markInProgress(claim) {
		outcome := "interrupted"
		if taskCtx.Err() != nil {
			outcome = "cancelled"
		}
		w.finishClaim(claim, usageRequest, nil, outcome, 0)
		return
	}
	var result []byte
	status := 200
	if codexPrepared != nil {
		var runErr *codexRunError
		result, runErr = a.responses.Responses(executionCtx, codexPrepared.credential, codexPrepared.body, nil)
		if runErr != nil {
			outcome := "failed"
			if taskCtx.Err() != nil {
				outcome = "cancelled"
			}
			w.finishClaim(claim, usageRequest, nil, outcome, runErr.UpstreamStatus)
			return
		}
	} else {
		upstreamReq = upstreamReq.WithContext(executionCtx)
		response, doErr := client.Do(upstreamReq)
		if doErr == nil {
			defer response.Body.Close()
			status = response.StatusCode
			if status >= 200 && status < 300 {
				result, doErr = io.ReadAll(io.LimitReader(response.Body, responsesMaxResponse+1))
			}
		}
		if doErr != nil || status < 200 || status >= 300 {
			outcome := "failed"
			if taskCtx.Err() != nil {
				outcome = "cancelled"
			}
			w.finishClaim(claim, usageRequest, nil, outcome, status)
			return
		}
	}
	if len(result) > responsesMaxResponse || validateCompletedResponse(result) != nil {
		w.finishClaim(claim, usageRequest, nil, "failed", status)
		return
	}
	a.observeRequestUsage(requestID, result)
	w.finishClaim(claim, usageRequest, result, "succeeded", status)
}

func backgroundResponsePayload(view responseResourceView) map[string]json.RawMessage {
	input := make([]json.RawMessage, 0, len(view.Items))
	var instructions json.RawMessage
	for _, item := range view.Items {
		if item.Type == "instructions" {
			instructions = append([]byte(nil), item.Payload...)
		} else {
			input = append(input, append([]byte(nil), item.Payload...))
		}
	}
	payload := map[string]json.RawMessage{"model": mustMarshalJSON(view.PublicModel), "input": mustMarshalJSON(input), "stream": json.RawMessage("false"), "store": json.RawMessage("false")}
	if len(instructions) != 0 {
		payload["instructions"] = instructions
	}
	return payload
}

func (w *backgroundResponseWorker) markInProgress(claim *backgroundResponseClaim) bool {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := w.app.store.db.BeginTx(w.ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(w.ctx, `UPDATE background_tasks SET status='in_progress',started_at=?,updated_at=?,revision=revision+1 WHERE id=? AND status='dispatch_authorized' AND claim_token=? AND cancel_requested_at IS NULL`, stamp, stamp, claim.view.TaskID, claim.claimToken)
	if err != nil {
		return false
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return false
	}
	result, err = tx.ExecContext(w.ctx, `UPDATE response_resources SET status='in_progress',updated_at=?,revision=revision+1 WHERE id=? AND status='dispatch_authorized'`, stamp, claim.view.ID)
	if err != nil {
		return false
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return false
	}
	return tx.Commit() == nil
}

func (w *backgroundResponseWorker) finishClaim(claim *backgroundResponseClaim, request *usageLedgerRequest, body []byte, outcome string, status int) {
	request.terminalTxHook = w.terminalHook(claim, body)
	_ = w.app.finishRequestChecked(claim.view.RequestID, outcome, status)
}

func (w *backgroundResponseWorker) terminalHook(claim *backgroundResponseClaim, body []byte) func(context.Context, *sql.Tx, accounting.Status, time.Time) error {
	return func(ctx context.Context, tx *sql.Tx, status accounting.Status, at time.Time) error {
		storedStatus := string(status)
		if status == accounting.StatusSucceeded {
			storedStatus = "completed"
		}
		stamp := at.Format(time.RFC3339Nano)
		if len(body) != 0 && status == accounting.StatusSucceeded {
			var envelope struct {
				Output []json.RawMessage `json:"output"`
			}
			if json.Unmarshal(body, &envelope) != nil {
				return errResponseResourceUnavailable
			}
			var wrapNonce, wrapped []byte
			if err := tx.QueryRowContext(ctx, `SELECT dek_wrap_nonce,wrapped_dek FROM response_resources WHERE id=?`, claim.view.ID).Scan(&wrapNonce, &wrapped); err != nil {
				return err
			}
			binding := responseDEKBinding{employeeID: claim.auth.EmployeeID, keyID: claim.auth.KeyID, responseID: claim.view.ID}
			dek, err := w.app.secrets.unwrapResponseDEK(binding, wrapNonce, wrapped)
			if err != nil {
				return err
			}
			defer clear(dek)
			sequence := len(claim.view.Items)
			totalPlaintext := 0
			for _, existing := range claim.view.Items {
				totalPlaintext += len(existing.Payload)
			}
			if sequence+len(envelope.Output) > responseStateMaxItems {
				return errResponseResourceUnavailable
			}
			for _, raw := range envelope.Output {
				item, err := validatedResponseStateItem(raw, true)
				if err != nil {
					return err
				}
				if len(item.Payload) > responseStateMaxPlaintext-totalPlaintext {
					return errResponseResourceUnavailable
				}
				totalPlaintext += len(item.Payload)
				nonce, ciphertext, err := sealResponseItem(dek, responseItemBinding{responseDEKBinding: binding, sequence: int64(sequence), itemType: item.Type}, item.Payload)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO response_resource_items(response_id,sequence,item_type,schema_version,nonce,ciphertext,created_at) VALUES(?,?,?,?,?,?,?)`, claim.view.ID, sequence, item.Type, responseStateSchemaVersion, nonce, ciphertext, stamp); err != nil {
					return err
				}
				sequence++
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status=?,claim_token=NULL,claimed_at=NULL,finished_at=?,updated_at=?,revision=revision+1 WHERE id=? AND status IN ('queued','dispatch_authorized','in_progress')`, storedStatus, stamp, stamp, claim.view.TaskID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errResponseResourceConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE response_resources SET status=?,terminal_at=?,updated_at=?,expires_at=?,revision=revision+1 WHERE id=? AND status IN ('queued','dispatch_authorized','in_progress')`, storedStatus, stamp, stamp, at.Add(responseResourceTTL).Format(time.RFC3339Nano), claim.view.ID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errResponseResourceConflict
		}
		return nil
	}
}

func (w *backgroundResponseWorker) interruptClaim(claim *backgroundResponseClaim) {
	_ = w.app.responseResources.InterruptQueuedClaim(context.Background(), claim.view.TaskID, claim.view.ID, claim.view.RequestID, claim.claimToken)
}
