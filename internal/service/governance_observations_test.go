package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/governance"
)

type fakeGovernanceObservationQuerier struct {
	fn    func(context.Context, governance.ObservationQuery) (governance.ObservationPage, error)
	calls int
}

func (f *fakeGovernanceObservationQuerier) QueryObservations(ctx context.Context, query governance.ObservationQuery) (governance.ObservationPage, error) {
	f.calls++
	return f.fn(ctx, query)
}

func TestGovernanceObservationHTTPQueryCursorAndResponse(t *testing.T) {
	observedAt := time.Date(2026, time.September, 23, 12, 0, 2, 123, time.UTC)
	coreCursor := worstGovernanceObservationCoreCursor(t)
	filterScopeID, filterPolicyID := strings.Repeat("<", 200), strings.Repeat("&", 200)
	groupRevision, shadowTPM, shadowCost := "3", "120000", "5000000"
	tpmState, costState := governance.ObservationUnknown, governance.ObservationExceeded
	incomparable := "2"
	queries := make([]governance.ObservationQuery, 0, 2)
	fake := &fakeGovernanceObservationQuerier{fn: func(_ context.Context, query governance.ObservationQuery) (governance.ObservationPage, error) {
		queries = append(queries, query)
		if len(queries) == 2 {
			return governance.ObservationPage{
				WindowEnd: "2026-09-23T12:00:00.000000000Z", ObservedAt: "2026-09-23T12:00:03.000000000Z",
				TPMFrom: "2026-09-23T11:59:00.000000000Z", CostFrom: "2026-09-22T12:00:00.000000000Z", Items: []governance.ObservationItem{},
			}, nil
		}
		return governance.ObservationPage{
			WindowEnd: "2026-09-23T12:00:00.000000000Z", ObservedAt: "2026-09-23T12:00:02.000000000Z",
			TPMFrom: "2026-09-23T11:59:00.000000000Z", CostFrom: "2026-09-22T12:00:00.000000000Z",
			Items: []governance.ObservationItem{{
				Snapshot: governance.ObservationSnapshot{
					SettingsRevision: "4", ScopeKind: governance.ScopeGroup, ScopeID: "group-id", PolicyID: "policy-id", PolicyRevision: "7",
					GroupRevision: &groupRevision, ShadowTPM: &shadowTPM, ShadowCostMicro: &shadowCost, ShadowCurrency: "USD", ShadowWindow: governance.ShadowWindowRolling24h,
				},
				ScopeTotals: governance.ObservationScopeTotals{
					TPM: governance.ObservationTPMTotals{KnownTokens: "93000", KnownAttempts: "12", UnknownTokenAttempts: "1", PendingAttempts: "0", PendingRequestsWithoutAttempt: "0", ZeroAttemptRequests: "2"},
					Cost: governance.ObservationCostTotals{KnownAttempts: "12", UnknownCostAttempts: "1", PendingAttempts: "0", PendingRequestsWithoutAttempt: "0", ZeroAttemptRequests: "2", ByCurrency: []governance.ObservationCurrencyTotal{
						{Currency: "EUR", KnownCostMicro: "700000", Attempts: "2"}, {Currency: "USD", KnownCostMicro: "3100000", Attempts: "10"},
					}},
				},
				Interpretation: governance.ObservationInterpretation{TPMState: &tpmState, CostState: &costState, IncomparableCurrencyAttempts: &incomparable},
			}, {
				Snapshot: governance.ObservationSnapshot{SettingsRevision: "5", ScopeKind: governance.ScopeEmployee, ScopeID: "employee-id", PolicyID: "hard-only", PolicyRevision: "1"},
				ScopeTotals: governance.ObservationScopeTotals{
					TPM:  governance.ObservationTPMTotals{KnownTokens: "0", KnownAttempts: "0", UnknownTokenAttempts: "0", PendingAttempts: "0", PendingRequestsWithoutAttempt: "0", ZeroAttemptRequests: "0"},
					Cost: governance.ObservationCostTotals{KnownAttempts: "0", UnknownCostAttempts: "0", PendingAttempts: "0", PendingRequestsWithoutAttempt: "0", ZeroAttemptRequests: "0"},
				},
			}},
			NextCursor: coreCursor,
		}, nil
	}}
	handler := newSyntheticGovernanceObservationHTTP(fake, observedAt)

	first := httptest.NewRecorder()
	firstValues := url.Values{"limit": {"2"}, "scope_kind": {"group"}, "scope_id": {filterScopeID}, "policy_id": {filterPolicyID}}
	firstRequest := httptest.NewRequest(http.MethodGet, governanceObservationPath+"?"+firstValues.Encode(), nil)
	handler.get(first, firstRequest, adminSession{})
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if len(queries) != 1 || queries[0].Limit != 2 || queries[0].Cursor != "" || queries[0].ScopeKind != governance.ScopeGroup ||
		queries[0].ScopeID != filterScopeID || queries[0].PolicyID != filterPolicyID || !queries[0].ObservedAt.Equal(observedAt) {
		t.Fatalf("first query=%+v", queries)
	}
	var body struct {
		Items []struct {
			Snapshot struct {
				SettingsRevision string  `json:"settings_revision"`
				GroupRevision    *string `json:"group_revision"`
				ShadowTPM        *string `json:"shadow_tpm"`
				ShadowCostMicro  *string `json:"shadow_cost_micro"`
				ShadowCurrency   string  `json:"shadow_currency"`
				ShadowWindow     string  `json:"shadow_window"`
			} `json:"snapshot"`
			ScopeTotals struct {
				TPM struct {
					KnownTokens string `json:"known_tokens"`
				} `json:"tpm"`
				Cost struct {
					ByCurrency []governanceObservationCurrencyTotalView `json:"by_currency"`
				} `json:"cost"`
			} `json:"scope_totals"`
			Interpretation governanceObservationInterpretationView `json:"interpretation"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].Snapshot.SettingsRevision != "4" || body.Items[0].Snapshot.GroupRevision == nil || *body.Items[0].Snapshot.GroupRevision != "3" ||
		body.Items[0].Snapshot.ShadowTPM == nil || *body.Items[0].Snapshot.ShadowTPM != "120000" || body.Items[0].ScopeTotals.TPM.KnownTokens != "93000" ||
		len(body.Items[0].ScopeTotals.Cost.ByCurrency) != 2 || body.Items[0].Interpretation.CostState == nil || *body.Items[0].Interpretation.CostState != governance.ObservationExceeded ||
		body.NextCursor == nil || *body.NextCursor == coreCursor || len(*body.NextCursor) > governanceObservationCursorLimit {
		t.Fatalf("response=%s", first.Body.String())
	}
	if body.Items[1].Snapshot.ShadowCostMicro != nil || body.Items[1].Snapshot.ShadowCurrency != "" || body.Items[1].Snapshot.ShadowWindow != "" ||
		body.Items[1].Interpretation.CostState != nil || body.Items[1].Interpretation.IncomparableCurrencyAttempts != nil ||
		body.Items[1].ScopeTotals.Cost.ByCurrency == nil || len(body.Items[1].ScopeTotals.Cost.ByCurrency) != 0 {
		t.Fatalf("hard-only response=%s", first.Body.String())
	}

	second := httptest.NewRecorder()
	secondValues := url.Values{"limit": {"2"}, "scope_kind": {"group"}, "scope_id": {filterScopeID}, "policy_id": {filterPolicyID}, "cursor": {*body.NextCursor}}
	secondRequest := httptest.NewRequest(http.MethodGet, governanceObservationPath+"?"+secondValues.Encode(), nil)
	handler.get(second, secondRequest, adminSession{})
	if second.Code != http.StatusOK || len(queries) != 2 || queries[1].Cursor != coreCursor || !strings.Contains(second.Body.String(), `"next_cursor":null`) || !strings.Contains(second.Body.String(), `"items":[]`) {
		t.Fatalf("second status=%d query=%+v body=%s", second.Code, queries, second.Body.String())
	}

	tampered, err := base64.RawURLEncoding.DecodeString(*body.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	tampered[10] ^= 1 // Change signed cursor contents while retaining the original MAC.
	for name, cursor := range map[string]string{"tampered": base64.RawURLEncoding.EncodeToString(tampered), "unsigned core": coreCursor} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, governanceObservationPath+"?cursor="+url.QueryEscape(cursor), nil)
		handler.get(recorder, request, adminSession{})
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("%s status=%d body=%s", name, recorder.Code, recorder.Body.String())
		}
	}
	if fake.calls != 2 {
		t.Fatalf("invalid cursors reached core: calls=%d", fake.calls)
	}
}

func TestGovernanceObservationStrictQuery(t *testing.T) {
	valid := []string{
		"", "?limit=1", "?limit=100", "?scope_kind=employee", "?scope_kind=key&scope_id=key-id", "?policy_id=policy-id",
	}
	for _, raw := range valid {
		request := httptest.NewRequest(http.MethodGet, governanceObservationPath+raw, nil)
		query, _, err := parseGovernanceObservationQuery(request)
		if err != nil {
			t.Fatalf("valid query %q: %v", raw, err)
		}
		if raw == "" && query.Limit != governanceObservationDefaultLimit {
			t.Fatalf("default limit=%d", query.Limit)
		}
	}
	invalid := []string{
		"?unknown=x", "?limit=", "?limit=0", "?limit=01", "?limit=101", "?limit=-1", "?limit=1&limit=2", "?cursor=", "?cursor=" + strings.Repeat("a", governanceObservationCursorLimit+1),
		"?scope_id=orphan", "?scope_kind=", "?scope_kind=unknown", "?scope_kind=key&scope_id=", "?scope_kind=key&scope_id=" + strings.Repeat("x", 201), "?policy_id=", "?policy_id=" + strings.Repeat("x", 201),
		"?limit=1;policy_id=x", "?limit=%zz", "?limit=1&&policy_id=x", "?limit=1&",
	}
	for _, raw := range invalid {
		request := httptest.NewRequest(http.MethodGet, governanceObservationPath, nil)
		request.URL.RawQuery = strings.TrimPrefix(raw, "?")
		if _, _, err := parseGovernanceObservationQuery(request); err == nil {
			t.Fatalf("invalid query accepted: %q", raw)
		}
	}
}

func TestGovernanceObservationCursorPersistsAcrossRestartAndIsBounded(t *testing.T) {
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	firstSecrets, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	first := &governanceObservationHTTP{app: &App{secrets: firstSecrets}}
	maximumCore := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 1503)))
	token, err := first.encodeCursor(maximumCore)
	if err != nil || len(token) != governanceObservationCursorLimit {
		t.Fatalf("maximum token length=%d err=%v", len(token), err)
	}
	secondSecrets, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	second := &governanceObservationHTTP{app: &App{secrets: secondSecrets}}
	decoded, err := second.decodeCursor(token)
	if err != nil || decoded != maximumCore {
		t.Fatalf("restart decode length=%d err=%v", len(decoded), err)
	}
	tooLargeCore := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 1504)))
	if _, err := first.encodeCursor(tooLargeCore); err == nil {
		t.Fatal("oversized generated cursor was returned")
	}
	otherDir := t.TempDir()
	if err := createRootKey(otherDir); err != nil {
		t.Fatal(err)
	}
	otherSecrets, err := loadSecrets(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&governanceObservationHTTP{app: &App{secrets: otherSecrets}}).decodeCursor(token); err == nil {
		t.Fatal("cursor signed by another installation was accepted")
	}
}

func TestGovernanceObservationAdminBoundaryErrorsAndCancellation(t *testing.T) {
	dataDir := runtimeTestDataDir(t)
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	mux := http.NewServeMux()
	app.registerGovernanceObservationHandlers(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	cookie, _ := installRuntimeAdminSession(t, app, time.Now().UTC().Add(time.Hour))

	for name, authorization := range map[string]string{"no session": "", "employee bearer": "Bearer synthetic-employee-key"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+governanceObservationPath, nil)
		request.Header.Set("Authorization", authorization)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body := readBody(response)
		if response.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"code":"authentication_required"`) {
			t.Fatalf("%s status=%d body=%s", name, response.StatusCode, body)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+governanceObservationPath, nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(readBody(response), `"items":[]`) {
		t.Fatalf("admin status=%d", response.StatusCode)
	}

	page := governance.ObservationPage{
		WindowEnd: "2026-09-23T12:00:00.000000000Z", ObservedAt: "2026-09-23T12:00:00.000000000Z",
		TPMFrom: "2026-09-23T11:59:00.000000000Z", CostFrom: "2026-09-22T12:00:00.000000000Z", Items: []governance.ObservationItem{},
	}
	deadlineFake := &fakeGovernanceObservationQuerier{fn: func(ctx context.Context, _ governance.ObservationQuery) (governance.ObservationPage, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Errorf("query context deadline=%v ok=%v", deadline, ok)
		}
		return page, nil
	}}
	deadlineHandler := &governanceObservationHTTP{app: app, query: deadlineFake, now: time.Now}
	deadlineRecorder := httptest.NewRecorder()
	deadlineHandler.get(deadlineRecorder, httptest.NewRequest(http.MethodGet, governanceObservationPath, nil), adminSession{})
	if deadlineRecorder.Code != http.StatusOK || deadlineFake.calls != 1 {
		t.Fatalf("deadline handler status=%d calls=%d", deadlineRecorder.Code, deadlineFake.calls)
	}

	for _, kind := range []governance.ObservationErrorKind{governance.ObservationUnavailable, governance.ObservationSchema, governance.ObservationOverflow} {
		errorHandler := newSyntheticGovernanceObservationHTTP(&fakeGovernanceObservationQuerier{fn: func(context.Context, governance.ObservationQuery) (governance.ObservationPage, error) {
			return governance.ObservationPage{}, &governance.ObservationError{Kind: kind}
		}}, time.Now().UTC())
		recorder := httptest.NewRecorder()
		errorHandler.get(recorder, httptest.NewRequest(http.MethodGet, governanceObservationPath, nil), adminSession{})
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"storage_unavailable"`) {
			t.Fatalf("kind=%s status=%d body=%s", kind, recorder.Code, recorder.Body.String())
		}
	}
	for _, kind := range []governance.ObservationErrorKind{governance.ObservationInvalidQuery, governance.ObservationInvalidCursor} {
		errorHandler := newSyntheticGovernanceObservationHTTP(&fakeGovernanceObservationQuerier{fn: func(context.Context, governance.ObservationQuery) (governance.ObservationPage, error) {
			return governance.ObservationPage{}, &governance.ObservationError{Kind: kind}
		}}, time.Now().UTC())
		recorder := httptest.NewRecorder()
		errorHandler.get(recorder, httptest.NewRequest(http.MethodGet, governanceObservationPath, nil), adminSession{})
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("kind=%s status=%d body=%s", kind, recorder.Code, recorder.Body.String())
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelHandler := newSyntheticGovernanceObservationHTTP(&fakeGovernanceObservationQuerier{fn: func(ctx context.Context, _ governance.ObservationQuery) (governance.ObservationPage, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("cancelled context error=%v", ctx.Err())
		}
		return governance.ObservationPage{}, &governance.ObservationError{Kind: governance.ObservationUnavailable}
	}}, time.Now().UTC())
	recorder := httptest.NewRecorder()
	cancelHandler.get(recorder, httptest.NewRequest(http.MethodGet, governanceObservationPath, nil).WithContext(cancelled), adminSession{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newSyntheticGovernanceObservationHTTP(query governanceObservationQuerier, now time.Time) *governanceObservationHTTP {
	return &governanceObservationHTTP{
		app:   &App{secrets: &secrets{digestKey: []byte("synthetic-governance-observation-digest-key")}},
		query: query,
		now:   func() time.Time { return now },
	}
}

func worstGovernanceObservationCoreCursor(t *testing.T) string {
	t.Helper()
	groupRevision := int64(9007199254740991)
	value := struct {
		Version              int    `json:"v"`
		WindowEnd            string `json:"w"`
		FilterFingerprint    string `json:"f"`
		LastScopeKind        string `json:"lk"`
		LastScopeID          string `json:"li"`
		LastPolicyID         string `json:"lp"`
		LastPolicyRevision   int64  `json:"lr"`
		LastGroupRevision    *int64 `json:"lg"`
		LastSettingsRevision int64  `json:"ls"`
	}{
		Version: 1, WindowEnd: "2026-09-23T12:00:00.000000000Z", FilterFingerprint: strings.Repeat("a", 43), LastScopeKind: "group",
		LastScopeID: strings.Repeat(`"`, 256), LastPolicyID: strings.Repeat(`"`, 256), LastPolicyRevision: 9007199254740991,
		LastGroupRevision: &groupRevision, LastSettingsRevision: 9007199254740991,
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	raw := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	return base64.RawURLEncoding.EncodeToString(raw)
}
