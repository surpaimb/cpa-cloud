package financial

// Independently authored for docs/expiry-observation-pass-reader-contract-2026-10-21.md.
// Source: this project's existing private binding/writer; Go1.26.9 context API
// (BSD-style). No archived implementation, new dependency or business wiring.

import "context"

// ExpiryPassReadState classifies metadata shape/range, never trusted ownership.
type ExpiryPassReadState uint8

const (
	ExpiryPassNoContext ExpiryPassReadState = iota
	ExpiryPassAbsent
	ExpiryPassInvalid
	ExpiryPassInRangeUnattributed
	ExpiryPassOverLimit
)

// ExpiryPassRead is a scalar copy, not an identity, admission or authorization.
// OverLimit preserves RawPass exactly; it must never become an admitted ID.
type ExpiryPassRead struct {
	State   ExpiryPassReadState
	RawPass uint16
}

// ReadExpiryObservationPass reads only the stored private binding. A caller
// must not interpret even InRangeUnattributed as App/Conn/pass/Tx provenance.
func ReadExpiryObservationPass(ctx context.Context) ExpiryPassRead {
	if ctx == nil {
		return ExpiryPassRead{State: ExpiryPassNoContext}
	}
	value := ctx.Value(expiryObservationKey{})
	if value == nil {
		return ExpiryPassRead{State: ExpiryPassAbsent}
	}
	binding, ok := value.(expiryObservationBinding)
	if !ok || binding.observer == nil || binding.pass == 0 {
		return ExpiryPassRead{State: ExpiryPassInvalid}
	}
	state := ExpiryPassInRangeUnattributed
	if binding.pass > 128 {
		state = ExpiryPassOverLimit
	}
	return ExpiryPassRead{State: state, RawPass: binding.pass}
}
