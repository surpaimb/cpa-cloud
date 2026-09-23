package service

// Independent proof construction over the exact server-prepared HTTP request.
// This helper performs no network IO and never persists the body or its hash.
import (
	"io"
	"net/http"
	"strings"

	"cpacloud.local/server/internal/accounting"
)

func proveModelBudgetWire(request *http.Request, protocol accounting.UsageProtocol, provider accounting.Provider, actualModel string) (accounting.BoundProfileProof, error) {
	if request == nil || request.URL == nil || request.Method != http.MethodPost || request.Body == nil || request.GetBody == nil || request.ContentLength <= 0 || request.ContentLength > modelMaxBody || len(request.TransferEncoding) != 0 || request.Header.Get("Content-Encoding") != "" || !strings.EqualFold(request.Header.Get("Content-Type"), "application/json") {
		return accounting.BoundProfileProof{}, accounting.ErrBoundProfileUnsupported
	}
	// Executors construct requests with bytes.Reader, so GetBody returns the
	// same immutable final serialized bytes without consuming the send body.
	copy, err := request.GetBody()
	if err != nil || copy == nil {
		return accounting.BoundProfileProof{}, accounting.ErrBoundProfileInvalid
	}
	defer copy.Close()
	payload, err := io.ReadAll(io.LimitReader(copy, modelMaxBody+1))
	if err != nil || int64(len(payload)) != request.ContentLength || len(payload) > modelMaxBody {
		return accounting.BoundProfileProof{}, accounting.ErrBoundProfileInvalid
	}
	return accounting.ProveBoundProfile(accounting.BoundProfileInput{Payload: payload, Protocol: protocol, Provider: provider, Endpoint: request.URL.String(), ActualModel: actualModel})
}
