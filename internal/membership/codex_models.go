package membership

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	CodexModelsEndpoint     = "https://chatgpt.com/backend-api/codex/models"
	MaxCodexModelsBodyBytes = 1 << 20
	MaxCodexModelsItems     = 512
	MaxCodexModelsETagBytes = 512
	CodexModelsTotalTimeout = 5 * time.Second
)

var wholeClientVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type CodexModelsErrorCode string

const (
	CodexModelsInvalidRequest CodexModelsErrorCode = "codex_models_invalid_request"
	CodexModelsUnauthorized   CodexModelsErrorCode = "codex_models_unauthorized"
	CodexModelsForbidden      CodexModelsErrorCode = "codex_models_forbidden"
	CodexModelsRateLimited    CodexModelsErrorCode = "codex_models_rate_limited"
	CodexModelsTimeout        CodexModelsErrorCode = "codex_models_timeout"
	CodexModelsRedirect       CodexModelsErrorCode = "codex_models_redirect_rejected"
	CodexModelsUpstream       CodexModelsErrorCode = "codex_models_upstream_error"
	CodexModelsBodyTooLarge   CodexModelsErrorCode = "codex_models_body_too_large"
	CodexModelsTooManyItems   CodexModelsErrorCode = "codex_models_too_many_items"
	CodexModelsInvalidPayload CodexModelsErrorCode = "codex_models_invalid_payload"
)

type CodexModelsError struct{ code CodexModelsErrorCode }

func (e *CodexModelsError) Code() CodexModelsErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *CodexModelsError) Error() string {
	if e == nil {
		return "Codex model catalog request failed."
	}
	switch e.code {
	case CodexModelsInvalidRequest:
		return "Codex model catalog request is invalid."
	case CodexModelsUnauthorized:
		return "Codex membership authentication was rejected."
	case CodexModelsForbidden:
		return "Codex membership is not allowed to read the model catalog."
	case CodexModelsRateLimited:
		return "Codex model catalog request was rate limited."
	case CodexModelsTimeout:
		return "Codex model catalog request timed out."
	case CodexModelsRedirect:
		return "Codex model catalog redirect was rejected."
	case CodexModelsBodyTooLarge:
		return "Codex model catalog response exceeds the size limit."
	case CodexModelsTooManyItems:
		return "Codex model catalog contains too many models."
	case CodexModelsInvalidPayload:
		return "Codex model catalog response is invalid."
	default:
		return "Codex model catalog upstream request failed."
	}
}

func CodexModelsErrorCodeOf(err error) (CodexModelsErrorCode, bool) {
	var catalogError *CodexModelsError
	if !errors.As(err, &catalogError) {
		return "", false
	}
	return catalogError.Code(), true
}

type CodexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// CodexCatalogModel contains only model metadata observed in the remote
// catalog. Pointer fields preserve the difference between false/zero and an
// omitted capability.
type CodexCatalogModel struct {
	ID                          string
	DisplayName                 string
	Description                 *string
	Visibility                  *string
	SupportedInAPI              *bool
	DefaultReasoningLevel       *string
	SupportedReasoningLevels    []CodexReasoningLevel
	InputModalities             []string
	ContextWindow               *int64
	SupportsSearchTool          *bool
	SupportsVerbosity           *bool
	SupportsImageDetailOriginal *bool
	ExperimentalSupportedTools  []string
}

type CodexModelCatalog struct {
	Models []CodexCatalogModel
	ETag   string
}

type CodexModelsClient struct {
	client        *http.Client
	clientVersion string
}

func NewCodexModelsClient(clientVersion string) (*CodexModelsClient, error) {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   CodexModelsTotalTimeout,
		ResponseHeaderTimeout: CodexModelsTotalTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return NewCodexModelsClientWithTransport(clientVersion, transport)
}

// NewCodexModelsClientWithTransport permits deterministic tests without
// weakening the fixed production target. The transport receives a request for
// CodexModelsEndpoint and cannot supply a different endpoint.
func NewCodexModelsClientWithTransport(clientVersion string, transport http.RoundTripper) (*CodexModelsClient, error) {
	if !wholeClientVersion.MatchString(clientVersion) || transport == nil {
		return nil, newCodexModelsError(CodexModelsInvalidRequest)
	}
	return &CodexModelsClient{
		clientVersion: clientVersion,
		client: &http.Client{
			Transport: transport,
			Timeout:   CodexModelsTotalTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errCodexModelsRedirect
			},
		},
	}, nil
}

var errCodexModelsRedirect = errors.New("codex model catalog redirect rejected")

func (c *CodexModelsClient) List(ctx context.Context, credential *CodexAuthCredential) (CodexModelCatalog, error) {
	if c == nil || c.client == nil || ctx == nil || credential == nil {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidRequest)
	}
	accessToken := credential.AccessTokenSecret()
	if accessToken == "" || strings.ContainsAny(accessToken, "\r\n") {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidRequest)
	}
	accountID := credential.AccountIDSecret()
	if strings.ContainsAny(accountID, "\r\n") {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidRequest)
	}

	requestURL, err := url.Parse(CodexModelsEndpoint)
	if err != nil {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidRequest)
	}
	query := requestURL.Query()
	query.Set("client_version", c.clientVersion)
	requestURL.RawQuery = query.Encode()

	requestContext, cancel := context.WithTimeout(ctx, CodexModelsTotalTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidRequest)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("version", c.clientVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		if errors.Is(err, errCodexModelsRedirect) {
			return CodexModelCatalog{}, newCodexModelsError(CodexModelsRedirect)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return CodexModelCatalog{}, newCodexModelsError(CodexModelsTimeout)
		}
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsUpstream)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CodexModelCatalog{}, statusCodexModelsError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCodexModelsBodyBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return CodexModelCatalog{}, newCodexModelsError(CodexModelsTimeout)
		}
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsUpstream)
	}
	if len(body) > MaxCodexModelsBodyBytes {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsBodyTooLarge)
	}

	var envelope struct {
		Models json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Models) == 0 || string(envelope.Models) == "null" {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidPayload)
	}
	var wireModels []codexCatalogModelWire
	if json.Unmarshal(envelope.Models, &wireModels) != nil {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidPayload)
	}
	if len(wireModels) > MaxCodexModelsItems {
		return CodexModelCatalog{}, newCodexModelsError(CodexModelsTooManyItems)
	}

	models := make([]CodexCatalogModel, len(wireModels))
	for i, model := range wireModels {
		if model.Slug == "" || model.DisplayName == "" || !validCodexVisibility(model.Visibility) {
			return CodexModelCatalog{}, newCodexModelsError(CodexModelsInvalidPayload)
		}
		models[i] = CodexCatalogModel{
			ID: model.Slug, DisplayName: model.DisplayName, Description: model.Description,
			Visibility: model.Visibility, SupportedInAPI: model.SupportedInAPI,
			DefaultReasoningLevel:    model.DefaultReasoningLevel,
			SupportedReasoningLevels: model.SupportedReasoningLevels,
			InputModalities:          model.InputModalities, ContextWindow: model.ContextWindow,
			SupportsSearchTool: model.SupportsSearchTool, SupportsVerbosity: model.SupportsVerbosity,
			SupportsImageDetailOriginal: model.SupportsImageDetailOriginal,
			ExperimentalSupportedTools:  model.ExperimentalSupportedTools,
		}
	}

	etag := resp.Header.Get("ETag")
	if len(etag) > MaxCodexModelsETagBytes {
		etag = ""
	}
	return CodexModelCatalog{Models: models, ETag: etag}, nil
}

type codexCatalogModelWire struct {
	Slug                        string                `json:"slug"`
	DisplayName                 string                `json:"display_name"`
	Description                 *string               `json:"description"`
	Visibility                  *string               `json:"visibility"`
	SupportedInAPI              *bool                 `json:"supported_in_api"`
	DefaultReasoningLevel       *string               `json:"default_reasoning_level"`
	SupportedReasoningLevels    []CodexReasoningLevel `json:"supported_reasoning_levels"`
	InputModalities             []string              `json:"input_modalities"`
	ContextWindow               *int64                `json:"context_window"`
	SupportsSearchTool          *bool                 `json:"supports_search_tool"`
	SupportsVerbosity           *bool                 `json:"support_verbosity"`
	SupportsImageDetailOriginal *bool                 `json:"supports_image_detail_original"`
	ExperimentalSupportedTools  []string              `json:"experimental_supported_tools"`
}

func validCodexVisibility(visibility *string) bool {
	if visibility == nil {
		return true
	}
	switch *visibility {
	case "list", "hide", "none":
		return true
	default:
		return false
	}
}

func statusCodexModelsError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return newCodexModelsError(CodexModelsUnauthorized)
	case http.StatusForbidden:
		return newCodexModelsError(CodexModelsForbidden)
	case http.StatusTooManyRequests:
		return newCodexModelsError(CodexModelsRateLimited)
	default:
		return newCodexModelsError(CodexModelsUpstream)
	}
}

func newCodexModelsError(code CodexModelsErrorCode) error {
	return &CodexModelsError{code: code}
}

func (c CodexModelCatalog) String() string {
	return fmt.Sprintf("CodexModelCatalog{models:%d,etag_present:%t}", len(c.Models), c.ETag != "")
}
