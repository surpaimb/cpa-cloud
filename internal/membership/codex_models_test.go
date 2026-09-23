package membership

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type codexModelsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codexModelsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func syntheticCodexCredential(t *testing.T) *CodexAuthCredential {
	t.Helper()
	credential, err := ParseCodexAuthJSON([]byte(`{
  "auth_mode":"chatgpt",
  "tokens":{
    "access_token":"synthetic-access-secret",
    "refresh_token":"synthetic-refresh-secret",
    "account_id":"synthetic-account-id"
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credential.Destroy)
	return credential
}

func codexModelsResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCodexModelsClientRequestAndCapabilities(t *testing.T) {
	credential := syntheticCodexCredential(t)
	transport := codexModelsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != CodexModelsEndpoint+"?client_version=1.2.3" {
			t.Fatalf("url=%q", got)
		}
		if req.Method != http.MethodGet || req.Body != nil {
			t.Fatalf("method=%q body=%v", req.Method, req.Body)
		}
		for name, want := range map[string]string{
			"Authorization":      "Bearer synthetic-access-secret",
			"ChatGPT-Account-ID": "synthetic-account-id",
			"Version":            "1.2.3",
			"Accept":             "application/json",
		} {
			if got := req.Header.Get(name); got != want {
				t.Fatalf("%s=%q", name, got)
			}
		}
		response := codexModelsResponse(http.StatusOK, `{"models":[{
          "slug":"gpt-example","display_name":"GPT Example","description":"desc",
          "visibility":"hide","supported_in_api":false,
          "default_reasoning_level":"medium",
          "supported_reasoning_levels":[{"effort":"low","description":"Low"}],
          "input_modalities":["text","image"],"context_window":272000,
          "supports_search_tool":true,"support_verbosity":false,
          "supports_image_detail_original":true,
          "experimental_supported_tools":["example_tool"]
        },{"slug":"gpt-minimal","display_name":"Minimal"}]}`)
		response.Header.Set("ETag", `"catalog-1"`)
		return response, nil
	})
	client, err := NewCodexModelsClientWithTransport("1.2.3", transport)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := client.List(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ETag != `"catalog-1"` || len(catalog.Models) != 2 {
		t.Fatalf("catalog=%v", catalog)
	}
	model := catalog.Models[0]
	if model.ID != "gpt-example" || model.Visibility == nil || *model.Visibility != "hide" {
		t.Fatalf("model=%+v", model)
	}
	if model.SupportedInAPI == nil || *model.SupportedInAPI || model.SupportsSearchTool == nil || !*model.SupportsSearchTool {
		t.Fatalf("capabilities=%+v", model)
	}
	if model.ContextWindow == nil || *model.ContextWindow != 272000 || len(model.InputModalities) != 2 {
		t.Fatalf("limits/modalities=%+v", model)
	}
	minimal := catalog.Models[1]
	if minimal.SupportedInAPI != nil || minimal.Visibility != nil || minimal.InputModalities != nil || minimal.SupportsVerbosity != nil {
		t.Fatalf("omitted capabilities were fabricated: %+v", minimal)
	}
}

func TestCodexModelsClientRejectsInvalidConfiguration(t *testing.T) {
	for _, version := range []string{"", "dev", "1.2", "1.2.3-beta", "1.2.3\nsecret"} {
		if _, err := NewCodexModelsClientWithTransport(version, codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })); codexModelsCode(err) != CodexModelsInvalidRequest {
			t.Fatalf("version=%q err=%v", version, err)
		}
	}
	if _, err := NewCodexModelsClientWithTransport("1.2.3", nil); codexModelsCode(err) != CodexModelsInvalidRequest {
		t.Fatalf("nil transport err=%v", err)
	}
	client, err := NewCodexModelsClient("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("default transport must not use a proxy: %#v", client.client.Transport)
	}
	if _, err := client.List(nil, syntheticCodexCredential(t)); codexModelsCode(err) != CodexModelsInvalidRequest {
		t.Fatalf("nil context err=%v", err)
	}
}

func TestCodexModelsClientMapsStatusesWithoutLeakingSecrets(t *testing.T) {
	credential := syntheticCodexCredential(t)
	for _, test := range []struct {
		status int
		code   CodexModelsErrorCode
	}{
		{http.StatusUnauthorized, CodexModelsUnauthorized},
		{http.StatusForbidden, CodexModelsForbidden},
		{http.StatusTooManyRequests, CodexModelsRateLimited},
		{http.StatusBadGateway, CodexModelsUpstream},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			client, err := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return codexModelsResponse(test.status, `{"error":"synthetic-access-secret synthetic-account-id upstream-detail"}`), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.List(context.Background(), credential)
			if codexModelsCode(err) != test.code {
				t.Fatalf("code=%q err=%v", codexModelsCode(err), err)
			}
			for _, secret := range []string{"synthetic-access-secret", "synthetic-account-id", "upstream-detail"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestCodexModelsClientRejectsRedirect(t *testing.T) {
	client, err := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		response := codexModelsResponse(http.StatusFound, "")
		response.Header.Set("Location", "https://chatgpt.com/backend-api/codex/models")
		return response, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), syntheticCodexCredential(t))
	if codexModelsCode(err) != CodexModelsRedirect {
		t.Fatalf("err=%v", err)
	}
}

func TestCodexModelsClientHonorsDeadline(t *testing.T) {
	client, err := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.List(ctx, syntheticCodexCredential(t))
	if codexModelsCode(err) != CodexModelsTimeout {
		t.Fatalf("err=%v", err)
	}
}

func TestCodexModelsClientEnforcesBodyAndItemLimits(t *testing.T) {
	credential := syntheticCodexCredential(t)
	oversizedBody := strings.Repeat(" ", MaxCodexModelsBodyBytes+1)
	client, _ := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return codexModelsResponse(http.StatusOK, oversizedBody), nil
	}))
	if _, err := client.List(context.Background(), credential); codexModelsCode(err) != CodexModelsBodyTooLarge {
		t.Fatalf("body limit err=%v", err)
	}

	items := strings.Repeat(`{"slug":"m","display_name":"M"},`, MaxCodexModelsItems) + `{"slug":"m","display_name":"M"}`
	client, _ = NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return codexModelsResponse(http.StatusOK, `{"models":[`+items+`]}`), nil
	}))
	if _, err := client.List(context.Background(), credential); codexModelsCode(err) != CodexModelsTooManyItems {
		t.Fatalf("item limit err=%v", err)
	}
}

func TestCodexModelsClientRejectsMalformedCatalogs(t *testing.T) {
	credential := syntheticCodexCredential(t)
	for _, body := range []string{
		`{}`,
		`{"models":null}`,
		`{"models":{}}`,
		`{"models":[{"slug":"","display_name":"M"}]}`,
		`{"models":[{"slug":"m","display_name":""}]}`,
		`{"models":[{"slug":"m","display_name":"M","visibility":"future"}]}`,
		`{"models":[]} trailing`,
	} {
		client, _ := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return codexModelsResponse(http.StatusOK, body), nil
		}))
		if _, err := client.List(context.Background(), credential); codexModelsCode(err) != CodexModelsInvalidPayload {
			t.Fatalf("body=%q err=%v", body, err)
		}
	}
}

func codexModelsCode(err error) CodexModelsErrorCode {
	code, ok := CodexModelsErrorCodeOf(err)
	if !ok {
		return ""
	}
	return code
}

func TestCodexModelsErrorsDoNotWrapTransportDetails(t *testing.T) {
	client, _ := NewCodexModelsClientWithTransport("1.2.3", codexModelsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic-access-secret transport detail")
	}))
	_, err := client.List(context.Background(), syntheticCodexCredential(t))
	if codexModelsCode(err) != CodexModelsUpstream || strings.Contains(err.Error(), "synthetic-access-secret") {
		t.Fatalf("err=%v", err)
	}
}
