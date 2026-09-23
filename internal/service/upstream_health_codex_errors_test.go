package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestUpstreamHealthCodexCatalogResponseErrorsAreInvalidResponse(t *testing.T) {
	tooManyModels := make([]map[string]string, membership.MaxCodexModelsItems+1)
	for index := range tooManyModels {
		tooManyModels[index] = map[string]string{"slug": "model-" + strings.Repeat("x", index%7) + string(rune('a'+index%26)), "display_name": "Synthetic"}
	}
	tooManyPayload, err := json.Marshal(map[string]any{"models": tooManyModels})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed payload", body: `{"models":"private-invalid-payload"}`},
		{name: "body too large", body: strings.Repeat("private-oversize-", membership.MaxCodexModelsBodyBytes/len("private-oversize-")+2)},
		{name: "too many items", body: string(tooManyPayload)},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := openOAuthTestApp(t, t.TempDir())
			defer app.Close()
			id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_invalid_"+string(rune('a'+index)), time.Now().Add(time.Hour), true)
			client, err := membership.NewCodexModelsClientWithTransport("0.1.0", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			app.codexCatalog = client
			operationID := []string{
				"ca2bdd4e-7a25-4ff4-91ed-f503a8a81001",
				"ca2bdd4e-7a25-4ff4-91ed-f503a8a81002",
				"ca2bdd4e-7a25-4ff4-91ed-f503a8a81003",
			}[index]
			view := runCodexHealthOperation(t, app, id, operationID, 1, "catalog")
			if view.ResultCode == nil || *view.ResultCode != "invalid_response" {
				t.Fatalf("catalog error result=%+v", view)
			}
			encoded, _ := json.Marshal(view)
			if strings.Contains(string(encoded), "private-") {
				t.Fatalf("provider response leaked: %s", encoded)
			}
		})
	}
}

func TestCodexHealthCatalogInvalidRequestIsConfigurationChanged(t *testing.T) {
	client, err := membership.NewCodexModelsClientWithTransport("0.1.0", roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid local request reached transport")
		return nil, context.Canceled
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, requestErr := client.List(nil, nil)
	if result := codexHealthCatalogErrorResult(requestErr); result != "configuration_changed" {
		t.Fatalf("invalid request result=%q err=%v", result, requestErr)
	}
}
