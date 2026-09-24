package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cpacloud.local/server/internal/protocolconv"
)

// protocolRuntime is the narrow execution adapter between route selection and
// the durable dispatch barrier. prepared.Body is the exact wire body that the
// budget proof and eventual upstream request must share.
type protocolRuntime struct {
	prepared protocolconv.PreparedRequest
}

func prepareProtocolRuntime(capability protocolconv.RouteCapability, model string, body []byte) (*protocolRuntime, error) {
	prepared, err := protocolconv.PrepareRequest(capability, model, body)
	if err != nil {
		return nil, err
	}
	return &protocolRuntime{prepared: prepared}, nil
}

func (p *protocolRuntime) plan() protocolconv.Plan { return p.prepared.Plan }

func (p *protocolRuntime) model() string { return p.prepared.Model }

func (p *protocolRuntime) newRequest(ctx context.Context, method, target string, headers http.Header) (*http.Request, error) {
	if p == nil {
		return nil, errors.New("protocol runtime is not prepared")
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(p.prepared.Body))
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	return request, nil
}

type protocolRuntimeResult struct {
	StatusCode  int
	Header      http.Header
	RawUpstream []byte
	ClientBody  []byte
}

type protocolUpstreamStatusError struct{ StatusCode int }

func (e *protocolUpstreamStatusError) Error() string {
	return fmt.Sprintf("upstream returned status %d", e.StatusCode)
}

// execute performs exactly one network call. observe receives the raw upstream
// JSON and actual upstream wire protocol before employee-facing conversion.
func (p *protocolRuntime) execute(client upstreamHTTPDoer, request *http.Request, maxBody int64, observe func(protocolconv.Protocol, []byte) error) (protocolRuntimeResult, error) {
	if p == nil || request == nil || client == nil {
		return protocolRuntimeResult{}, errors.New("protocol runtime is incomplete")
	}
	response, err := client.Do(request)
	if err != nil {
		return protocolRuntimeResult{}, err
	}
	defer response.Body.Close()
	result := protocolRuntimeResult{StatusCode: response.StatusCode, Header: response.Header.Clone()}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, &protocolUpstreamStatusError{StatusCode: response.StatusCode}
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return result, errors.New("upstream returned a non-JSON response")
	}
	if maxBody <= 0 {
		return result, errors.New("protocol response limit is not configured")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return result, err
	}
	if int64(len(raw)) > maxBody {
		return result, errors.New("upstream response exceeded the configured limit")
	}
	if request.Context().Err() != nil {
		return result, request.Context().Err()
	}
	result.RawUpstream = append([]byte(nil), raw...)
	if observe != nil {
		if err := observe(p.prepared.Plan.UpstreamProtocol, raw); err != nil {
			return result, err
		}
	}
	converted, err := protocolconv.ConvertResponse(p.prepared, raw)
	if err != nil {
		return result, err
	}
	result.ClientBody = converted
	return result, nil
}
