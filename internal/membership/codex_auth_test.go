package membership

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const (
	testAccessToken  = "fake-access-secret-7d10f3"
	testRefreshToken = "fake-refresh-secret-b421a8"
	testIDToken      = "not.a.verified-jwt"
	testAccountID    = "fake-account-sensitive"
)

func TestParseCodexAuthJSONChatGPTCandidateAndRedaction(t *testing.T) {
	input := []byte(`{
  "auth_mode":"chatgpt",
  "OPENAI_API_KEY":null,
  "tokens":{
    "access_token":"` + testAccessToken + `",
    "refresh_token":"` + testRefreshToken + `",
    "id_token":"` + testIDToken + `",
    "account_id":"` + testAccountID + `",
    "future_token_field":{"kept":true}
  },
  "last_refresh":"2026-09-22T00:00:00Z",
  "future_top_level":{"kept":true}
}`)

	credential, err := ParseCodexAuthJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if credential.CredentialKind() != "codex_chatgpt_oauth" {
		t.Fatalf("credential kind=%q", credential.CredentialKind())
	}
	if credential.VerificationState() != VerificationRequired {
		t.Fatalf("verification state=%q", credential.VerificationState())
	}
	if credential.AccessTokenSecret() != testAccessToken || credential.RefreshTokenSecret() != testRefreshToken {
		t.Fatal("OAuth token accessors changed the supplied values")
	}
	if credential.IDTokenSecret() != testIDToken || credential.AccountIDSecret() != testAccountID {
		t.Fatal("optional credential fields changed the supplied values")
	}
	raw := credential.RawAuthJSONSecret()
	if !bytes.Equal(raw, input) || !bytes.Contains(raw, []byte("future_top_level")) {
		t.Fatal("complete source file was not preserved")
	}
	raw[0] = 'x'
	if credential.RawAuthJSONSecret()[0] == 'x' {
		t.Fatal("raw credential accessor did not return a defensive copy")
	}

	assertCredentialFormattingRedacted(t, *credential)

	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	assertNoTestSecrets(t, string(encoded))
	var metadata map[string]string
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["credential_kind"] != "codex_chatgpt_oauth" || metadata["verification_state"] != string(VerificationRequired) {
		t.Fatalf("safe JSON metadata=%v", metadata)
	}

	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	logger.Info("candidate", "credential", *credential)
	assertNoTestSecrets(t, log.String())
	if !strings.Contains(log.String(), string(VerificationRequired)) {
		t.Fatalf("safe log metadata missing: %s", log.String())
	}
}

func TestParseCodexAuthJSONLegacyChatGPTResolution(t *testing.T) {
	credential, err := ParseCodexAuthJSON([]byte(`{
  "OPENAI_API_KEY":null,
  "tokens":{"access_token":"access","refresh_token":"refresh"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if credential.VerificationState() != VerificationRequired {
		t.Fatal("legacy ChatGPT candidate was treated as verified")
	}
	if credential.IDTokenSecret() != "" || credential.AccountIDSecret() != "" {
		t.Fatal("absent optional fields were invented")
	}
}

func TestParseCodexAuthJSONRejectsAPIKeyFiles(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "explicit API key mode",
			body: `{"auth_mode":"apikey","OPENAI_API_KEY":"fake-api-key","tokens":null}`,
		},
		{
			name: "legacy API key resolution",
			body: `{"OPENAI_API_KEY":"fake-api-key"}`,
		},
		{
			name: "legacy empty API key still selects API mode",
			body: `{"OPENAI_API_KEY":""}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertImportError(t, []byte(test.body), ErrorAPIKeyNotSupported)
		})
	}
}

func TestParseCodexAuthJSONRejectsInvalidJSONAndTypes(t *testing.T) {
	invalidJSON := [][]byte{
		{},
		[]byte(`{"auth_mode":"chatgpt"`),
		[]byte(`{} {}`),
		[]byte(`{"auth_mode":"chatgpt","auth_mode":"chatgpt"}`),
		[]byte(`{"tokens":{"access_token":"a","access_token":"b"}}`),
		{0xff, 0xfe},
	}
	for index, input := range invalidJSON {
		t.Run(fmt.Sprintf("invalid JSON %d", index), func(t *testing.T) {
			assertImportError(t, input, ErrorInvalidJSON)
		})
	}

	validTokens := `"tokens":{"access_token":"access","refresh_token":"refresh"}`
	invalidTypes := []string{
		`[]`,
		`null`,
		`{"auth_mode":17,` + validTokens + `}`,
		`{"auth_mode":"chatgpt","OPENAI_API_KEY":false,` + validTokens + `}`,
		`{"auth_mode":"chatgpt","tokens":[]}`,
		`{"auth_mode":"chatgpt","tokens":{"access_token":17,"refresh_token":"refresh"}}`,
		`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":{}}}`,
		`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":"refresh","id_token":false}}`,
		`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":"refresh","account_id":17}}`,
		`{"auth_mode":"chatgpt","last_refresh":17,` + validTokens + `}`,
	}
	for index, input := range invalidTypes {
		t.Run(fmt.Sprintf("invalid type %d", index), func(t *testing.T) {
			assertImportError(t, []byte(input), ErrorInvalidType)
		})
	}
}

func TestParseCodexAuthJSONRequiredTokens(t *testing.T) {
	tests := []struct {
		name string
		body string
		code ImportErrorCode
	}{
		{name: "missing tokens", body: `{"auth_mode":"chatgpt"}`, code: ErrorMissingAccessToken},
		{name: "null tokens", body: `{"auth_mode":"chatgpt","tokens":null}`, code: ErrorMissingAccessToken},
		{name: "missing access", body: `{"auth_mode":"chatgpt","tokens":{"refresh_token":"refresh"}}`, code: ErrorMissingAccessToken},
		{name: "null access", body: `{"auth_mode":"chatgpt","tokens":{"access_token":null,"refresh_token":"refresh"}}`, code: ErrorMissingAccessToken},
		{name: "empty access", body: `{"auth_mode":"chatgpt","tokens":{"access_token":"","refresh_token":"refresh"}}`, code: ErrorMissingAccessToken},
		{name: "blank access", body: `{"auth_mode":"chatgpt","tokens":{"access_token":"  ","refresh_token":"refresh"}}`, code: ErrorMissingAccessToken},
		{name: "missing refresh", body: `{"auth_mode":"chatgpt","tokens":{"access_token":"access"}}`, code: ErrorMissingRefreshToken},
		{name: "null refresh", body: `{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":null}}`, code: ErrorMissingRefreshToken},
		{name: "empty refresh", body: `{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":""}}`, code: ErrorMissingRefreshToken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertImportError(t, []byte(test.body), test.code)
		})
	}
}

func TestParseCodexAuthJSONRejectsUnsupportedModes(t *testing.T) {
	for _, mode := range []string{"headers", "chatgptAuthTokens", "personalAccessToken", "agentIdentity", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			assertImportError(t, []byte(`{"auth_mode":`+quoteTestJSON(mode)+`,"tokens":{"access_token":"access","refresh_token":"refresh"}}`), ErrorUnsupportedAuthMode)
		})
	}
}

func TestParseCodexAuthJSONSizeBoundary(t *testing.T) {
	base := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":"refresh"}}`)
	atLimit := append(bytes.Clone(base), bytes.Repeat([]byte{' '}, MaxCodexAuthJSONBytes-len(base))...)
	credential, err := ParseCodexAuthJSON(atLimit)
	if err != nil {
		t.Fatalf("exact size limit rejected: %v", err)
	}
	credential.Destroy()
	assertImportError(t, append(atLimit, ' '), ErrorTooLarge)
}

func TestImportErrorsAreFixedAndRedacted(t *testing.T) {
	secret := "do-not-echo-this-secret"
	_, err := ParseCodexAuthJSON([]byte(`{"auth_mode":"chatgpt","tokens":{"access_token":17,"refresh_token":"` + secret + `"}}`))
	if err == nil {
		t.Fatal("invalid input was accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%v", err), secret) {
		t.Fatalf("error exposed input: %v", err)
	}
	if code, ok := ErrorCode(err); !ok || code != ErrorInvalidType {
		t.Fatalf("error code=%q ok=%v", code, ok)
	}
	if code, ok := ErrorCode(fmt.Errorf("outer context: %w", err)); !ok || code != ErrorInvalidType {
		t.Fatalf("wrapped error code=%q ok=%v", code, ok)
	}
}

func TestDestroyClearsHeldCredentialBuffers(t *testing.T) {
	credential, err := ParseCodexAuthJSON([]byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"` + testAccessToken + `","refresh_token":"` + testRefreshToken + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	credential.Destroy()
	if credential.AccessTokenSecret() != "" || credential.RefreshTokenSecret() != "" || len(credential.RawAuthJSONSecret()) != 0 {
		t.Fatal("Destroy retained credential material")
	}
}

func assertCredentialFormattingRedacted(t *testing.T, credential CodexAuthCredential) {
	t.Helper()
	formats := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"}
	for _, format := range formats {
		formatted := fmt.Sprintf(format, credential)
		assertNoTestSecrets(t, formatted)
		if !strings.Contains(formatted, string(VerificationRequired)) {
			t.Fatalf("format %q omitted verification state: %s", format, formatted)
		}
	}
}

func assertNoTestSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{testAccessToken, testRefreshToken, testIDToken, testAccountID} {
		if strings.Contains(text, secret) {
			t.Fatalf("output exposed credential material: %s", text)
		}
	}
}

func assertImportError(t *testing.T, input []byte, want ImportErrorCode) {
	t.Helper()
	credential, err := ParseCodexAuthJSON(input)
	if credential != nil || err == nil {
		t.Fatalf("ParseCodexAuthJSON returned credential=%v err=%v", credential, err)
	}
	got, ok := ErrorCode(err)
	if !ok || got != want {
		t.Fatalf("error code=%q ok=%v want=%q error=%v", got, ok, want, err)
	}
}

func quoteTestJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
