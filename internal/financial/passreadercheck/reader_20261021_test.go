// Independently authored external-path public API controls; no test-only bridge.
package passreadercheck

import (
	"context"
	"testing"

	"cpacloud.local/server/internal/financial"
)

var _ func(context.Context) financial.ExpiryPassRead = financial.ReadExpiryObservationPass

func TestExpiryPassReaderExternal20261021(t *testing.T) {
	observer := func(financial.ExpiryObservation) { t.Fatal("EPR_EXTERNAL_OBSERVER_CALLED") }
	for _, tc := range []struct {
		name  string
		pass  uint16
		state financial.ExpiryPassReadState
		raw   uint16
	}{
		{"zero", 0, financial.ExpiryPassInvalid, 0},
		{"one", 1, financial.ExpiryPassInRangeUnattributed, 1},
		{"128", 128, financial.ExpiryPassInRangeUnattributed, 128},
		{"129", 129, financial.ExpiryPassOverLimit, 129},
		{"65535", 65535, financial.ExpiryPassOverLimit, 65535},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := financial.WithExpiryObservation(context.Background(), observer, tc.pass)
			if financial.ReadExpiryObservationPass(ctx) != (financial.ExpiryPassRead{State: tc.state, RawPass: tc.raw}) {
				t.Fatal("EPR_EXTERNAL_PUBLIC_API")
			}
		})
	}
	t.Run("nil-and-absent", func(t *testing.T) {
		if financial.ReadExpiryObservationPass(nil) != (financial.ExpiryPassRead{State: financial.ExpiryPassNoContext}) ||
			financial.ReadExpiryObservationPass(context.Background()) != (financial.ExpiryPassRead{State: financial.ExpiryPassAbsent}) {
			t.Fatal("EPR_EXTERNAL_UNMARKED")
		}
	})
	t.Run("different-sources-indistinguishable", func(t *testing.T) {
		left := financial.WithExpiryObservation(context.Background(), func(financial.ExpiryObservation) { t.Fatal("EPR_EXTERNAL_LEFT_CALLED") }, 7)
		right := financial.WithExpiryObservation(context.Background(), func(financial.ExpiryObservation) { t.Fatal("EPR_EXTERNAL_RIGHT_CALLED") }, 7)
		a, b := financial.ReadExpiryObservationPass(left), financial.ReadExpiryObservationPass(right)
		if a != b || a != (financial.ExpiryPassRead{State: financial.ExpiryPassInRangeUnattributed, RawPass: 7}) {
			t.Fatal("EPR_EXTERNAL_LIMITATION_WITNESS")
		}
		if financial.WithExpiryObservation(left, nil, 129) != left || financial.ReadExpiryObservationPass(left) != a {
			t.Fatal("EPR_EXTERNAL_NIL_WRITER_PRESERVATION")
		}
	})
}
