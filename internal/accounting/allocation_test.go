package accounting

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestAllocationMultiplierConstructionAndParsing(t *testing.T) {
	t.Parallel()

	valid := []struct {
		text string
		ppm  int64
	}{
		{text: "1", ppm: MinAllocationMultiplierPPM},
		{text: "1000000", ppm: AllocationMultiplierScale},
		{text: "1000000000", ppm: MaxAllocationMultiplierPPM},
	}
	for _, tc := range valid {
		t.Run(tc.text, func(t *testing.T) {
			multiplier, err := ParseAllocationMultiplierPPM(tc.text)
			if err != nil {
				t.Fatalf("ParseAllocationMultiplierPPM(%q): %v", tc.text, err)
			}
			if got := multiplier.PartsPerMillion(); got != tc.ppm {
				t.Fatalf("PartsPerMillion() = %d, want %d", got, tc.ppm)
			}
			if got := multiplier.CanonicalPPM(); got != tc.text {
				t.Fatalf("CanonicalPPM() = %q, want %q", got, tc.text)
			}
		})
	}

	invalid := []string{
		"",
		"0",
		"00",
		"01",
		"-1",
		"+1",
		" 1",
		"1 ",
		"1.0",
		"1e6",
		"１００００００",
		"1000000001",
		"9223372036854775808",
	}
	for _, text := range invalid {
		t.Run("invalid_"+text, func(t *testing.T) {
			if _, err := ParseAllocationMultiplierPPM(text); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ParseAllocationMultiplierPPM(%q) error = %v, want ErrInvalid", text, err)
			}
		})
	}

	for _, ppm := range []int64{MinAllocationMultiplierPPM, AllocationMultiplierScale, MaxAllocationMultiplierPPM} {
		multiplier, err := NewAllocationMultiplier(ppm)
		if err != nil {
			t.Fatalf("NewAllocationMultiplier(%d): %v", ppm, err)
		}
		if got := multiplier.PartsPerMillion(); got != ppm {
			t.Fatalf("NewAllocationMultiplier(%d).PartsPerMillion() = %d", ppm, got)
		}
	}
	for _, ppm := range []int64{-1, 0, MaxAllocationMultiplierPPM + 1} {
		if _, err := NewAllocationMultiplier(ppm); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewAllocationMultiplier(%d) error = %v, want ErrInvalid", ppm, err)
		}
	}

	defaultMultiplier := DefaultAllocationMultiplier()
	if got := defaultMultiplier.PartsPerMillion(); got != AllocationMultiplierScale {
		t.Fatalf("default PartsPerMillion() = %d, want %d", got, AllocationMultiplierScale)
	}
	if got := defaultMultiplier.CanonicalPPM(); got != "1000000" {
		t.Fatalf("default CanonicalPPM() = %q, want 1000000", got)
	}
}

func TestAllocationMultiplierApplyNilZeroRoundingAndValidation(t *testing.T) {
	t.Parallel()

	defaultMultiplier := DefaultAllocationMultiplier()
	result, err := defaultMultiplier.Apply(nil)
	if err != nil || result != nil {
		t.Fatalf("Apply(nil) = (%v, %v), want (nil, nil)", result, err)
	}

	zero := int64(0)
	result, err = defaultMultiplier.Apply(&zero)
	if err != nil {
		t.Fatalf("Apply(&zero): %v", err)
	}
	if result == nil || *result != 0 {
		t.Fatalf("Apply(&zero) = %v, want pointer to zero", result)
	}
	if result == &zero {
		t.Fatal("Apply(&zero) returned the input pointer")
	}

	base := int64(7)
	first, err := defaultMultiplier.Apply(&base)
	if err != nil {
		t.Fatalf("first Apply(&base): %v", err)
	}
	second, err := defaultMultiplier.Apply(&base)
	if err != nil {
		t.Fatalf("second Apply(&base): %v", err)
	}
	if first == nil || second == nil || *first != base || *second != base {
		t.Fatalf("default Apply(&base) = (%v, %v), want two pointers to %d", first, second, base)
	}
	if first == &base || second == &base || first == second {
		t.Fatal("Apply did not allocate a fresh result pointer")
	}
	if base != 7 {
		t.Fatalf("Apply mutated input to %d", base)
	}

	roundingMultiplier, err := NewAllocationMultiplier(1_250_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		base int64
		want int64
	}{
		{base: 1, want: 2},
		{base: 4, want: 5},
		{base: 5, want: 7},
	} {
		got, applyErr := roundingMultiplier.Apply(&tc.base)
		if applyErr != nil {
			t.Fatalf("Apply(%d): %v", tc.base, applyErr)
		}
		if got == nil || *got != tc.want {
			t.Fatalf("Apply(%d) = %v, want %d", tc.base, got, tc.want)
		}
	}

	minimumMultiplier, err := NewAllocationMultiplier(MinAllocationMultiplierPPM)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		base int64
		want int64
	}{
		{base: 1, want: 1},
		{base: AllocationMultiplierScale, want: 1},
	} {
		got, applyErr := minimumMultiplier.Apply(&tc.base)
		if applyErr != nil || got == nil || *got != tc.want {
			t.Fatalf("minimum multiplier Apply(%d) = (%v, %v), want %d", tc.base, got, applyErr, tc.want)
		}
	}

	negative := int64(-1)
	if _, err := defaultMultiplier.Apply(&negative); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Apply(negative) error = %v, want ErrInvalid", err)
	}

	var zeroValue AllocationMultiplier
	if _, err := zeroValue.Apply(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero-value Apply(nil) error = %v, want ErrInvalid", err)
	}
}

func TestAllocationMultiplierApplyMatchesBigIntOracle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base int64
		ppm  int64
	}{
		{name: "known_zero", base: 0, ppm: MaxAllocationMultiplierPPM},
		{name: "minimum_fraction_rounds_up", base: 999_999, ppm: MinAllocationMultiplierPPM},
		{name: "minimum_exact", base: AllocationMultiplierScale, ppm: MinAllocationMultiplierPPM},
		{name: "default_maximum_base", base: math.MaxInt64, ppm: AllocationMultiplierScale},
		{name: "maximum_multiplier", base: 9_223_372_036_854_775, ppm: MaxAllocationMultiplierPPM},
		{name: "maximum_multiplier_overflow", base: 9_223_372_036_854_776, ppm: MaxAllocationMultiplierPPM},
		{name: "wide_product_overflow", base: math.MaxInt64, ppm: MaxAllocationMultiplierPPM},
		{name: "non_integral", base: 7_654_321, ppm: 1_234_567},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			multiplier, err := NewAllocationMultiplier(tc.ppm)
			if err != nil {
				t.Fatalf("NewAllocationMultiplier(%d): %v", tc.ppm, err)
			}

			want, fits := allocationBigIntOracle(tc.base, tc.ppm)
			got, err := multiplier.Apply(&tc.base)
			if !fits {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("Apply(%d) error = %v, want ErrInvalid", tc.base, err)
				}
				if got != nil {
					t.Fatalf("Apply(%d) result = %d on overflow, want nil", tc.base, *got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply(%d): %v", tc.base, err)
			}
			if got == nil || *got != want {
				t.Fatalf("Apply(%d) = %v, want %d", tc.base, got, want)
			}
		})
	}
}

func allocationBigIntOracle(base, ppm int64) (int64, bool) {
	product := new(big.Int).Mul(big.NewInt(base), big.NewInt(ppm))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(product, big.NewInt(AllocationMultiplierScale), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}
