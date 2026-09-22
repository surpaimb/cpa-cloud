// Package membership contains provider-specific credential import primitives.
// Parsing a credential establishes only a structurally valid candidate; it does
// not authenticate an account or authorize any provider request.
package membership

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"
)

const MaxCodexAuthJSONBytes = 1 << 20

type VerificationState string

const VerificationRequired VerificationState = "requires_online_verification"

type ImportErrorCode string

const (
	ErrorTooLarge            ImportErrorCode = "codex_auth_too_large"
	ErrorInvalidJSON         ImportErrorCode = "codex_auth_invalid_json"
	ErrorInvalidType         ImportErrorCode = "codex_auth_invalid_type"
	ErrorAPIKeyNotSupported  ImportErrorCode = "codex_api_key_not_membership"
	ErrorUnsupportedAuthMode ImportErrorCode = "codex_auth_mode_unsupported"
	ErrorMissingAccessToken  ImportErrorCode = "codex_auth_missing_access_token"
	ErrorMissingRefreshToken ImportErrorCode = "codex_auth_missing_refresh_token"
)

type ImportError struct {
	code ImportErrorCode
}

func (e *ImportError) Code() ImportErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *ImportError) Error() string {
	if e == nil {
		return "Codex credential import failed."
	}
	switch e.code {
	case ErrorTooLarge:
		return "Codex credential file exceeds the size limit."
	case ErrorInvalidJSON:
		return "Codex credential file is not valid JSON."
	case ErrorInvalidType:
		return "Codex credential file has an invalid structure."
	case ErrorAPIKeyNotSupported:
		return "Codex API key credentials cannot be imported as ChatGPT membership."
	case ErrorUnsupportedAuthMode:
		return "Codex credential authentication mode is not supported."
	case ErrorMissingAccessToken:
		return "Codex ChatGPT credential is missing an access token."
	case ErrorMissingRefreshToken:
		return "Codex ChatGPT credential is missing a refresh token."
	default:
		return "Codex credential import failed."
	}
}

func ErrorCode(err error) (ImportErrorCode, bool) {
	var importError *ImportError
	if !errors.As(err, &importError) {
		return "", false
	}
	return importError.Code(), true
}

// CodexAuthCredential is a structurally valid ChatGPT OAuth credential
// candidate. All fields are deliberately private so default serialization does
// not expose credential material. Callers must still encrypt it at rest and
// verify it with an approved online provider flow before marking an account
// authenticated.
type CodexAuthCredential struct {
	rawJSON      []byte
	accessToken  []byte
	refreshToken []byte
	idToken      []byte
	accountID    []byte
}

func (CodexAuthCredential) CredentialKind() string { return "codex_chatgpt_oauth" }

func (CodexAuthCredential) VerificationState() VerificationState { return VerificationRequired }

// RawAuthJSONSecret returns a defensive copy of the complete imported file,
// including unknown fields. The returned bytes are secret material.
func (c CodexAuthCredential) RawAuthJSONSecret() []byte { return bytes.Clone(c.rawJSON) }

// AccessTokenSecret returns secret material for a future approved verifier.
func (c CodexAuthCredential) AccessTokenSecret() string { return string(c.accessToken) }

// RefreshTokenSecret returns secret material for a future approved refresher.
func (c CodexAuthCredential) RefreshTokenSecret() string { return string(c.refreshToken) }

// IDTokenSecret returns the optional, unverified ID token. Its presence and
// claims are not proof that the account is authenticated.
func (c CodexAuthCredential) IDTokenSecret() string { return string(c.idToken) }

// AccountIDSecret returns the optional account identifier from the cache. It is
// unverified and may be personally identifying.
func (c CodexAuthCredential) AccountIDSecret() string { return string(c.accountID) }

// Destroy clears the credential buffers held by this object. It cannot clear
// copies previously returned to callers or temporary allocations made by the
// JSON decoder.
func (c *CodexAuthCredential) Destroy() {
	if c == nil {
		return
	}
	clear(c.rawJSON)
	clear(c.accessToken)
	clear(c.refreshToken)
	clear(c.idToken)
	clear(c.accountID)
	c.rawJSON = nil
	c.accessToken = nil
	c.refreshToken = nil
	c.idToken = nil
	c.accountID = nil
}

func (c CodexAuthCredential) safeDescription() string {
	return fmt.Sprintf("CodexAuthCredential{credential_kind:%q, verification_state:%q}", c.CredentialKind(), c.VerificationState())
}

func (c CodexAuthCredential) String() string { return c.safeDescription() }

func (c CodexAuthCredential) GoString() string { return c.safeDescription() }

func (c CodexAuthCredential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.safeDescription())
}

func (c CodexAuthCredential) MarshalText() ([]byte, error) {
	return []byte(c.safeDescription()), nil
}

func (c CodexAuthCredential) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		CredentialKind    string            `json:"credential_kind"`
		VerificationState VerificationState `json:"verification_state"`
	}{
		CredentialKind:    c.CredentialKind(),
		VerificationState: c.VerificationState(),
	})
}

func (c CodexAuthCredential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("credential_kind", c.CredentialKind()),
		slog.String("verification_state", string(c.VerificationState())),
	)
}

// ParseCodexAuthJSON validates an explicitly supplied auth.json payload. It
// never reads CODEX_HOME or the current user's filesystem.
func ParseCodexAuthJSON(data []byte) (*CodexAuthCredential, error) {
	if len(data) > MaxCodexAuthJSONBytes {
		return nil, newImportError(ErrorTooLarge)
	}
	if len(data) == 0 || !utf8.Valid(data) || validateJSON(data) != nil {
		return nil, newImportError(ErrorInvalidJSON)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, newImportError(ErrorInvalidType)
	}

	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return nil, newImportError(ErrorInvalidType)
	}
	authMode, authModeSet, err := optionalString(root, "auth_mode")
	if err != nil {
		return nil, err
	}
	_, apiKeySet, err := optionalString(root, "OPENAI_API_KEY")
	if err != nil {
		return nil, err
	}

	if authModeSet {
		switch authMode {
		case "apikey":
			return nil, newImportError(ErrorAPIKeyNotSupported)
		case "chatgpt":
		default:
			return nil, newImportError(ErrorUnsupportedAuthMode)
		}
	} else if apiKeySet {
		// This mirrors Codex's documented legacy resolution: without an
		// explicit mode, a present OPENAI_API_KEY selects API-key auth.
		return nil, newImportError(ErrorAPIKeyNotSupported)
	}

	tokensRaw, ok := root["tokens"]
	if !ok || isJSONNull(tokensRaw) {
		return nil, newImportError(ErrorMissingAccessToken)
	}
	if firstJSONByte(tokensRaw) != '{' {
		return nil, newImportError(ErrorInvalidType)
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(tokensRaw, &tokens) != nil || tokens == nil {
		return nil, newImportError(ErrorInvalidType)
	}
	accessToken, err := requiredSecretString(tokens, "access_token", ErrorMissingAccessToken)
	if err != nil {
		return nil, err
	}
	refreshToken, err := requiredSecretString(tokens, "refresh_token", ErrorMissingRefreshToken)
	if err != nil {
		return nil, err
	}
	idToken, _, err := optionalString(tokens, "id_token")
	if err != nil {
		return nil, err
	}
	accountID, _, err := optionalString(tokens, "account_id")
	if err != nil {
		return nil, err
	}
	if _, _, err := optionalString(root, "last_refresh"); err != nil {
		return nil, err
	}

	return &CodexAuthCredential{
		rawJSON:      bytes.Clone(data),
		accessToken:  []byte(accessToken),
		refreshToken: []byte(refreshToken),
		idToken:      []byte(idToken),
		accountID:    []byte(accountID),
	}, nil
}

func newImportError(code ImportErrorCode) *ImportError { return &ImportError{code: code} }

func optionalString(object map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := object[key]
	if !ok || isJSONNull(raw) {
		return "", false, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false, newImportError(ErrorInvalidType)
	}
	return value, true, nil
}

func requiredSecretString(object map[string]json.RawMessage, key string, missing ImportErrorCode) (string, error) {
	value, set, err := optionalString(object, key)
	if err != nil {
		return "", err
	}
	if !set || strings.TrimSpace(value) == "" {
		return "", newImportError(missing)
	}
	return value, nil
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func firstJSONByte(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func validateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}
