package membership

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	CodexDirectProtocolBaseline = "44b857c00e5803adedbc5b2e94c4a33574a157fe"
	codexResponsesURL           = "https://chatgpt.com/backend-api/codex/responses"

	defaultCodexRequestTimeout = 2 * time.Minute
	minimumCodexTokenLifetime  = 5 * time.Minute
	maxCodexRequestBytes       = 1 << 20
	maxCodexResponseBytes      = 8 << 20
	maxCodexSSELineBytes       = 1 << 20
	maxCodexSSEEventBytes      = 1 << 20
	maxCodexErrorBodyBytes     = 64 << 10
)

type CodexMessageRole string

const (
	CodexRoleUser      CodexMessageRole = "user"
	CodexRoleAssistant CodexMessageRole = "assistant"
)

// CodexTextMessage deliberately represents only the verified text subset.
// System and developer roles are rejected by validation.
type CodexTextMessage struct {
	Role CodexMessageRole
	Text string
}

func (m CodexTextMessage) safeDescription() string {
	return fmt.Sprintf("CodexTextMessage{role:%q, text:redacted}", m.Role)
}

func (m CodexTextMessage) String() string               { return m.safeDescription() }
func (m CodexTextMessage) GoString() string             { return m.safeDescription() }
func (m CodexTextMessage) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, m.safeDescription()) }
func (m CodexTextMessage) MarshalText() ([]byte, error) { return []byte(m.safeDescription()), nil }
func (m CodexTextMessage) LogValue() slog.Value {
	return slog.GroupValue(slog.String("role", string(m.Role)), slog.String("text", "redacted"))
}
func (m CodexTextMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Role CodexMessageRole `json:"role"`
		Text string           `json:"text"`
	}{m.Role, "redacted"})
}

// CodexRequestFeatures makes unsupported standard chat features explicit at
// the mapping boundary. A true value is always rejected before network I/O.
type CodexRequestFeatures struct {
	Tools             bool
	Images            bool
	Audio             bool
	SystemPrompt      bool
	DeveloperPrompt   bool
	ResponseFormat    bool
	LogProbs          bool
	Seed              bool
	NonDefaultToolUse bool
}

type CodexTextRequest struct {
	Model    string
	Messages []CodexTextMessage
	Features CodexRequestFeatures
}

func (r CodexTextRequest) safeDescription() string {
	return fmt.Sprintf("CodexTextRequest{message_count:%d, text:redacted}", len(r.Messages))
}

func (r CodexTextRequest) String() string               { return r.safeDescription() }
func (r CodexTextRequest) GoString() string             { return r.safeDescription() }
func (r CodexTextRequest) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, r.safeDescription()) }
func (r CodexTextRequest) MarshalText() ([]byte, error) { return []byte(r.safeDescription()), nil }
func (r CodexTextRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageCount int                  `json:"message_count"`
		Features     CodexRequestFeatures `json:"features"`
		Text         string               `json:"text"`
	}{len(r.Messages), r.Features, "redacted"})
}
func (r CodexTextRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("message_count", len(r.Messages)), slog.String("text", "redacted"), slog.Any("features", r.Features))
}

type CodexAdapterErrorCode string

const (
	CodexErrorInvalidRequest       CodexAdapterErrorCode = "invalid_request"
	CodexErrorUnsupportedFeature   CodexAdapterErrorCode = "unsupported_feature"
	CodexErrorAccountIDRequired    CodexAdapterErrorCode = "account_id_required"
	CodexErrorAccessTokenInvalid   CodexAdapterErrorCode = "access_token_invalid"
	CodexErrorReauthentication     CodexAdapterErrorCode = "reauth_required"
	CodexErrorAuthModeUnsupported  CodexAdapterErrorCode = "auth_mode_unsupported"
	CodexErrorRateLimited          CodexAdapterErrorCode = "rate_limited"
	CodexErrorUsageExhausted       CodexAdapterErrorCode = "usage_exhausted"
	CodexErrorUpstream             CodexAdapterErrorCode = "upstream_error"
	CodexErrorProtocol             CodexAdapterErrorCode = "protocol_error"
	CodexErrorResponseTooLarge     CodexAdapterErrorCode = "response_too_large"
	CodexErrorSSEFrameTooLarge     CodexAdapterErrorCode = "sse_frame_too_large"
	CodexErrorRedirectRejected     CodexAdapterErrorCode = "redirect_rejected"
	CodexErrorTimeout              CodexAdapterErrorCode = "timeout"
	CodexErrorCancelled            CodexAdapterErrorCode = "cancelled"
	CodexErrorEventConsumerStopped CodexAdapterErrorCode = "event_consumer_stopped"
)

// CodexAdapterError contains only allowlisted metadata. It never retains an
// upstream body, transport error string, prompt, response text, or credential.
type CodexAdapterError struct {
	code         CodexAdapterErrorCode
	httpStatus   int
	providerCode string
	retryAfter   time.Duration
	cause        error
}

func (e *CodexAdapterError) Code() CodexAdapterErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *CodexAdapterError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.httpStatus
}

func (e *CodexAdapterError) ProviderCode() string {
	if e == nil {
		return ""
	}
	return e.providerCode
}

func (e *CodexAdapterError) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return e.retryAfter
}

func (e *CodexAdapterError) Error() string {
	if e == nil {
		return "Codex direct request failed."
	}
	switch e.code {
	case CodexErrorInvalidRequest:
		return "Codex direct request is invalid."
	case CodexErrorUnsupportedFeature:
		return "Codex direct request uses an unsupported feature."
	case CodexErrorAccountIDRequired:
		return "Codex direct request requires an imported account ID."
	case CodexErrorAccessTokenInvalid:
		return "Codex access token cannot be scheduled safely."
	case CodexErrorReauthentication:
		return "Codex credential must be re-imported from an authenticated Codex client."
	case CodexErrorAuthModeUnsupported:
		return "Codex account requires an unsupported authentication mode."
	case CodexErrorRateLimited:
		return "Codex upstream rate limit was reached."
	case CodexErrorUsageExhausted:
		return "Codex upstream usage is exhausted."
	case CodexErrorProtocol:
		return "Codex upstream response did not satisfy the pinned protocol."
	case CodexErrorResponseTooLarge:
		return "Codex upstream response exceeds the size limit."
	case CodexErrorSSEFrameTooLarge:
		return "Codex upstream event exceeds the size limit."
	case CodexErrorRedirectRejected:
		return "Codex upstream redirect was rejected."
	case CodexErrorTimeout:
		return "Codex direct request timed out."
	case CodexErrorCancelled:
		return "Codex direct request was cancelled."
	case CodexErrorEventConsumerStopped:
		return "Codex event consumer stopped the request."
	default:
		return "Codex upstream request failed."
	}
}

func (e *CodexAdapterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *CodexAdapterError) safeDescription() string {
	if e == nil {
		return "CodexAdapterError{}"
	}
	return fmt.Sprintf("CodexAdapterError{code:%q, http_status:%d, provider_code:%q, retry_after:%q}", e.code, e.httpStatus, e.providerCode, e.retryAfter)
}

func (e *CodexAdapterError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, e.safeDescription())
}

func (e *CodexAdapterError) MarshalText() ([]byte, error) {
	return []byte(e.safeDescription()), nil
}

func (e *CodexAdapterError) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	return json.Marshal(struct {
		Code         CodexAdapterErrorCode `json:"code"`
		HTTPStatus   int                   `json:"http_status,omitempty"`
		ProviderCode string                `json:"provider_code,omitempty"`
		RetryAfterMS int64                 `json:"retry_after_ms,omitempty"`
	}{e.code, e.httpStatus, e.providerCode, e.retryAfter.Milliseconds()})
}

func (e *CodexAdapterError) LogValue() slog.Value {
	if e == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(
		slog.String("code", string(e.code)),
		slog.Int("http_status", e.httpStatus),
		slog.String("provider_code", e.providerCode),
		slog.Int64("retry_after_ms", e.retryAfter.Milliseconds()),
	)
}

func CodexRequestErrorCode(err error) (CodexAdapterErrorCode, bool) {
	var adapterError *CodexAdapterError
	if !errors.As(err, &adapterError) {
		return "", false
	}
	return adapterError.Code(), true
}

type CodexUsage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}

func (u CodexUsage) known() bool {
	return u.InputTokens != nil || u.CachedInputTokens != nil || u.OutputTokens != nil || u.ReasoningOutputTokens != nil || u.TotalTokens != nil
}

type CodexEventKind string

const (
	CodexEventStarted   CodexEventKind = "response.started"
	CodexEventTextDelta CodexEventKind = "text.delta"
	CodexEventUsage     CodexEventKind = "usage"
	CodexEventCompleted CodexEventKind = "response.completed"
	CodexEventFailed    CodexEventKind = "response.failed"
	CodexEventCancelled CodexEventKind = "response.cancelled"
)

// CodexStreamEvent keeps generated text and response identifiers private so
// normal formatting, JSON serialization, and structured logging are metadata-only.
type CodexStreamEvent struct {
	kind         CodexEventKind
	text         string
	responseID   string
	usage        CodexUsage
	errorCode    CodexAdapterErrorCode
	unknownCount int
}

func (e CodexStreamEvent) Kind() CodexEventKind             { return e.kind }
func (e CodexStreamEvent) Text() string                     { return e.text }
func (e CodexStreamEvent) ResponseID() string               { return e.responseID }
func (e CodexStreamEvent) Usage() CodexUsage                { return e.usage }
func (e CodexStreamEvent) ErrorCode() CodexAdapterErrorCode { return e.errorCode }
func (e CodexStreamEvent) UnknownEventCount() int           { return e.unknownCount }
func (e CodexStreamEvent) String() string                   { return e.safeDescription() }
func (e CodexStreamEvent) GoString() string                 { return e.safeDescription() }
func (e CodexStreamEvent) Format(s fmt.State, _ rune)       { _, _ = io.WriteString(s, e.safeDescription()) }
func (e CodexStreamEvent) MarshalText() ([]byte, error)     { return []byte(e.safeDescription()), nil }
func (e CodexStreamEvent) MarshalJSON() ([]byte, error)     { return json.Marshal(e.safeMetadata()) }
func (e CodexStreamEvent) LogValue() slog.Value             { return slog.AnyValue(e.safeMetadata()) }
func (e CodexStreamEvent) safeDescription() string {
	return fmt.Sprintf("CodexStreamEvent{kind:%q, error_code:%q, unknown_event_count:%d}", e.kind, e.errorCode, e.unknownCount)
}
func (e CodexStreamEvent) safeMetadata() map[string]interface{} {
	return map[string]interface{}{"kind": e.kind, "error_code": e.errorCode, "unknown_event_count": e.unknownCount, "usage": e.usage}
}

type CodexTextResult struct {
	text         string
	responseID   string
	usage        CodexUsage
	unknownCount int
}

func (r *CodexTextResult) Text() string {
	if r == nil {
		return ""
	}
	return r.text
}

func (r *CodexTextResult) ResponseID() string {
	if r == nil {
		return ""
	}
	return r.responseID
}

func (r *CodexTextResult) Usage() CodexUsage {
	if r == nil {
		return CodexUsage{}
	}
	return r.usage
}

func (r *CodexTextResult) UnknownEventCount() int {
	if r == nil {
		return 0
	}
	return r.unknownCount
}

func (r *CodexTextResult) safeDescription() string {
	if r == nil {
		return "CodexTextResult{}"
	}
	return fmt.Sprintf("CodexTextResult{completed:true, unknown_event_count:%d}", r.unknownCount)
}

func (r *CodexTextResult) String() string             { return r.safeDescription() }
func (r *CodexTextResult) GoString() string           { return r.safeDescription() }
func (r *CodexTextResult) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, r.safeDescription()) }
func (r *CodexTextResult) MarshalText() ([]byte, error) {
	return []byte(r.safeDescription()), nil
}
func (r *CodexTextResult) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	return json.Marshal(struct {
		Completed         bool       `json:"completed"`
		Usage             CodexUsage `json:"usage"`
		UnknownEventCount int        `json:"unknown_event_count"`
	}{true, r.usage, r.unknownCount})
}
func (r *CodexTextResult) LogValue() slog.Value {
	if r == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(slog.Bool("completed", true), slog.Int("unknown_event_count", r.unknownCount), slog.Any("usage", r.usage))
}

type CodexDirectAdapter struct {
	client         *http.Client
	endpoint       string
	requestTimeout time.Duration
	now            func() time.Time
	rand           io.Reader
	maxResponse    int64
	maxLine        int
	maxEvent       int
}

var errCodexRedirect = errors.New("codex redirect rejected")

// NewCodexDirectAdapter constructs the production adapter. Its destination is
// not configurable and environment proxy variables are deliberately ignored so
// bearer credentials cannot be routed to an arbitrary intermediary.
func NewCodexDirectAdapter() *CodexDirectAdapter {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	return newCodexDirectAdapter(codexResponsesURL, transport, defaultCodexRequestTimeout)
}

func newCodexDirectAdapter(endpoint string, transport http.RoundTripper, timeout time.Duration) *CodexDirectAdapter {
	return &CodexDirectAdapter{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errCodexRedirect
			},
		},
		endpoint:       endpoint,
		requestTimeout: timeout,
		now:            time.Now,
		rand:           rand.Reader,
		maxResponse:    maxCodexResponseBytes,
		maxLine:        maxCodexSSELineBytes,
		maxEvent:       maxCodexSSEEventBytes,
	}
}

func (a *CodexDirectAdapter) Complete(ctx context.Context, credential *CodexAuthCredential, request CodexTextRequest) (*CodexTextResult, error) {
	result, err := a.execute(ctx, credential, request, nil)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *CodexDirectAdapter) Stream(ctx context.Context, credential *CodexAuthCredential, request CodexTextRequest, consume func(CodexStreamEvent) error) error {
	if consume == nil {
		return newCodexAdapterError(CodexErrorInvalidRequest)
	}
	_, err := a.execute(ctx, credential, request, consume)
	if err == nil {
		return nil
	}
	code, _ := CodexRequestErrorCode(err)
	kind := CodexEventFailed
	if code == CodexErrorCancelled || code == CodexErrorTimeout {
		kind = CodexEventCancelled
	}
	if code != CodexErrorEventConsumerStopped {
		_ = consume(CodexStreamEvent{kind: kind, errorCode: code})
	}
	return err
}

type codexWireRequest struct {
	Model             string             `json:"model"`
	Instructions      string             `json:"instructions"`
	Input             []codexWireMessage `json:"input"`
	ToolChoice        string             `json:"tool_choice"`
	ParallelToolCalls bool               `json:"parallel_tool_calls"`
	Reasoning         interface{}        `json:"reasoning"`
	Store             bool               `json:"store"`
	Stream            bool               `json:"stream"`
	Include           []string           `json:"include"`
	PromptCacheKey    string             `json:"prompt_cache_key"`
	AccessPrograms    interface{}        `json:"access_programs"`
}

type codexWireMessage struct {
	Type    string             `json:"type"`
	Role    CodexMessageRole   `json:"role"`
	Content []codexWireContent `json:"content"`
}

type codexWireContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (a *CodexDirectAdapter) execute(ctx context.Context, credential *CodexAuthCredential, request CodexTextRequest, consume func(CodexStreamEvent) error) (*CodexTextResult, error) {
	if ctx == nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	if a == nil || a.client == nil || a.endpoint == "" || a.now == nil || a.rand == nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	accessToken, accountID, err := a.validateCredential(credential)
	if err != nil {
		return nil, err
	}
	body, err := a.buildRequestBody(request)
	if err != nil {
		return nil, err
	}

	requestContext := ctx
	cancel := func() {}
	if a.requestTimeout > 0 {
		requestContext, cancel = context.WithTimeout(ctx, a.requestTimeout)
	}
	defer cancel()

	httpRequest, requestErr := http.NewRequestWithContext(requestContext, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if requestErr != nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	httpRequest.Header.Set("ChatGPT-Account-ID", accountID)
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("Content-Type", "application/json")

	response, requestErr := a.client.Do(httpRequest)
	if requestErr != nil {
		return nil, classifyCodexTransportError(requestContext, requestErr)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyCodexHTTPError(response)
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return nil, newCodexAdapterError(CodexErrorProtocol)
	}

	result, parseErr := a.parseSSE(requestContext, response.Body, consume)
	if parseErr != nil {
		return nil, parseErr
	}
	return result, nil
}

func (a *CodexDirectAdapter) validateCredential(credential *CodexAuthCredential) (string, string, error) {
	if credential == nil || credential.CredentialKind() != "codex_chatgpt_oauth" {
		return "", "", newCodexAdapterError(CodexErrorInvalidRequest)
	}
	accessToken := credential.AccessTokenSecret()
	if accessToken == "" {
		return "", "", newCodexAdapterError(CodexErrorReauthentication)
	}
	if strings.TrimSpace(accessToken) != accessToken || len(accessToken) > 64<<10 {
		return "", "", newCodexAdapterError(CodexErrorAccessTokenInvalid)
	}
	accountID := credential.AccountIDSecret()
	if strings.TrimSpace(accountID) == "" {
		return "", "", newCodexAdapterError(CodexErrorAccountIDRequired)
	}
	if strings.TrimSpace(accountID) != accountID || len(accountID) > 1024 || !validCodexHeaderValue(accountID) {
		return "", "", newCodexAdapterError(CodexErrorInvalidRequest)
	}
	expiresAt, expiryErr := codexAccessTokenExpiry(accessToken)
	if expiryErr != nil {
		return "", "", newCodexAdapterError(CodexErrorAccessTokenInvalid)
	}
	if !expiresAt.After(a.now().Add(minimumCodexTokenLifetime)) {
		return "", "", newCodexAdapterError(CodexErrorReauthentication)
	}
	return accessToken, accountID, nil
}

func codexAccessTokenExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}, errors.New("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 64<<10 || validateJSON(payload) != nil {
		return time.Time{}, errors.New("invalid JWT payload")
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&claims) != nil || claims.ExpiresAt == "" {
		return time.Time{}, errors.New("missing expiry")
	}
	expiresUnix, err := strconv.ParseInt(string(claims.ExpiresAt), 10, 64)
	if err != nil || expiresUnix <= 0 {
		return time.Time{}, errors.New("invalid expiry")
	}
	return time.Unix(expiresUnix, 0), nil
}

func (a *CodexDirectAdapter) buildRequestBody(request CodexTextRequest) ([]byte, error) {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Model) != request.Model || len(request.Model) > 256 || len(request.Messages) == 0 || len(request.Messages) > 4096 || hasUnsupportedCodexFeature(request.Features) {
		code := CodexErrorInvalidRequest
		if hasUnsupportedCodexFeature(request.Features) {
			code = CodexErrorUnsupportedFeature
		}
		return nil, newCodexAdapterError(code)
	}
	input := make([]codexWireMessage, 0, len(request.Messages))
	totalTextBytes := 0
	for _, message := range request.Messages {
		contentType := ""
		switch message.Role {
		case CodexRoleUser:
			contentType = "input_text"
		case CodexRoleAssistant:
			contentType = "output_text"
		default:
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if message.Text == "" {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		totalTextBytes += len(message.Text)
		if totalTextBytes > maxCodexRequestBytes {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		input = append(input, codexWireMessage{Type: "message", Role: message.Role, Content: []codexWireContent{{Type: contentType, Text: message.Text}}})
	}
	cacheKeyBytes := make([]byte, 16)
	if _, err := io.ReadFull(a.rand, cacheKeyBytes); err != nil {
		return nil, newCodexAdapterError(CodexErrorUpstream)
	}
	body, err := json.Marshal(codexWireRequest{
		Model:             request.Model,
		Instructions:      "",
		Input:             input,
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Reasoning:         nil,
		Store:             false,
		Stream:            true,
		Include:           []string{},
		PromptCacheKey:    hex.EncodeToString(cacheKeyBytes),
		AccessPrograms:    nil,
	})
	if err != nil || len(body) > maxCodexRequestBytes {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	return body, nil
}

func hasUnsupportedCodexFeature(features CodexRequestFeatures) bool {
	return features.Tools || features.Images || features.Audio || features.SystemPrompt || features.DeveloperPrompt || features.ResponseFormat || features.LogProbs || features.Seed || features.NonDefaultToolUse
}

func newCodexAdapterError(code CodexAdapterErrorCode) *CodexAdapterError {
	return &CodexAdapterError{code: code}
}

func classifyCodexTransportError(ctx context.Context, err error) error {
	if errors.Is(err, errCodexRedirect) {
		return newCodexAdapterError(CodexErrorRedirectRejected)
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &CodexAdapterError{code: CodexErrorCancelled, cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &CodexAdapterError{code: CodexErrorTimeout, cause: context.DeadlineExceeded}
	}
	return newCodexAdapterError(CodexErrorUpstream)
}

func classifyCodexHTTPError(response *http.Response) error {
	providerCode := readCodexProviderCode(response.Body)
	adapterError := &CodexAdapterError{httpStatus: response.StatusCode, providerCode: providerCode, retryAfter: parseRetryAfter(response.Header.Get("Retry-After"))}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		adapterError.code = CodexErrorReauthentication
	case http.StatusTooManyRequests:
		adapterError.code = CodexErrorRateLimited
	case http.StatusPaymentRequired:
		adapterError.code = CodexErrorUsageExhausted
	case http.StatusForbidden:
		if providerCode == "agent_identity_required" || providerCode == "agent_assertion_required" {
			adapterError.code = CodexErrorAuthModeUnsupported
		} else {
			adapterError.code = CodexErrorUpstream
		}
	default:
		adapterError.code = CodexErrorUpstream
	}
	return adapterError
}

func readCodexProviderCode(reader io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(reader, maxCodexErrorBodyBytes+1))
	if err != nil || len(data) > maxCodexErrorBodyBytes || validateJSON(data) != nil {
		return ""
	}
	var body struct {
		Code  string `json:"code"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	code := body.Code
	if body.Error != nil && body.Error.Code != "" {
		code = body.Error.Code
	}
	return allowlistedCodexCode(code)
}

func allowlistedCodexCode(code string) string {
	switch code {
	case "agent_assertion_required",
		"agent_identity_required",
		"content_filter",
		"insufficient_quota",
		"invalid_request_error",
		"invalid_token",
		"max_output_tokens",
		"overloaded",
		"rate_limit_exceeded",
		"server_error",
		"usage_limit_reached":
		return code
	default:
		return ""
	}
}

func validCodexHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || seconds <= 0 || seconds > 24*60*60 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
