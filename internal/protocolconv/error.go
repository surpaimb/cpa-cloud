// Package protocolconv contains independently implemented, loss-aware protocol
// conversions. It never performs network I/O, persists model content, or runs a
// tool. Callers remain responsible for authentication, admission, accounting,
// cancellation, and upstream dispatch.
package protocolconv

import (
	"errors"
	"fmt"
)

// ErrorCode is stable enough for service adapters to map to public envelopes.
type ErrorCode string

const (
	CodeInvalidRequest     ErrorCode = "invalid_request"
	CodeUnsupportedFeature ErrorCode = "unsupported_feature"
	CodeInvalidUpstream    ErrorCode = "invalid_upstream"
	CodeInterrupted        ErrorCode = "interrupted"
)

// ConversionError deliberately excludes source JSON and model content.
type ConversionError struct {
	Code  ErrorCode
	Field string
	msg   string
}

func (e *ConversionError) Error() string {
	if e == nil {
		return "protocol conversion failed"
	}
	if e.Field == "" {
		return e.msg
	}
	return fmt.Sprintf("%s: %s", e.Field, e.msg)
}

func (e *ConversionError) Is(target error) bool {
	other, ok := target.(*ConversionError)
	return ok && (other.Code == "" || e.Code == other.Code)
}

var (
	ErrInvalidRequest     = &ConversionError{Code: CodeInvalidRequest}
	ErrUnsupportedFeature = &ConversionError{Code: CodeUnsupportedFeature}
	ErrInvalidUpstream    = &ConversionError{Code: CodeInvalidUpstream}
	ErrInterrupted        = &ConversionError{Code: CodeInterrupted}
)

func invalid(field, message string) error {
	return &ConversionError{Code: CodeInvalidRequest, Field: field, msg: message}
}

func unsupported(field string) error {
	return &ConversionError{Code: CodeUnsupportedFeature, Field: field, msg: "cannot be represented by the target protocol"}
}

func invalidUpstream(field, message string) error {
	return &ConversionError{Code: CodeInvalidUpstream, Field: field, msg: message}
}

func interrupted(message string) error {
	return &ConversionError{Code: CodeInterrupted, msg: message}
}

// Field returns the rejected field path without exposing its value.
func Field(err error) string {
	var conversionErr *ConversionError
	if errors.As(err, &conversionErr) {
		return conversionErr.Field
	}
	return ""
}
