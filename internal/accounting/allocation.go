package accounting

// Independently implemented from docs/account-group-cost-allocation-contract.md.
import (
	"math"
	"math/bits"
	"strconv"
)

const (
	AllocationMultiplierScale  int64 = 1_000_000
	MinAllocationMultiplierPPM int64 = 1
	MaxAllocationMultiplierPPM int64 = 1_000_000_000
)

// AllocationMultiplier is a fixed-point multiplier used for internal cost
// allocation. Its value is expressed in parts per million.
type AllocationMultiplier struct {
	ppm int64
}

func NewAllocationMultiplier(ppm int64) (AllocationMultiplier, error) {
	if ppm < MinAllocationMultiplierPPM || ppm > MaxAllocationMultiplierPPM {
		return AllocationMultiplier{}, ErrInvalid
	}

	return AllocationMultiplier{ppm: ppm}, nil
}

func ParseAllocationMultiplierPPM(text string) (AllocationMultiplier, error) {
	if text == "" || len(text) > 1 && text[0] == '0' {
		return AllocationMultiplier{}, ErrInvalid
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return AllocationMultiplier{}, ErrInvalid
		}
	}

	ppm, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return AllocationMultiplier{}, ErrInvalid
	}
	return NewAllocationMultiplier(ppm)
}

func DefaultAllocationMultiplier() AllocationMultiplier {
	return AllocationMultiplier{ppm: AllocationMultiplierScale}
}

func (m AllocationMultiplier) PartsPerMillion() int64 {
	return m.ppm
}

func (m AllocationMultiplier) CanonicalPPM() string {
	return strconv.FormatInt(m.ppm, 10)
}

func (m AllocationMultiplier) Apply(base *int64) (*int64, error) {
	if m.ppm < MinAllocationMultiplierPPM || m.ppm > MaxAllocationMultiplierPPM {
		return nil, ErrInvalid
	}
	if base == nil {
		return nil, nil
	}
	if *base < 0 {
		return nil, ErrInvalid
	}

	high, low := bits.Mul64(uint64(*base), uint64(m.ppm))
	divisor := uint64(AllocationMultiplierScale)
	if high >= divisor {
		return nil, ErrInvalid
	}

	quotient, remainder := bits.Div64(high, low, divisor)
	if quotient > math.MaxInt64 || remainder != 0 && quotient == math.MaxInt64 {
		return nil, ErrInvalid
	}
	if remainder != 0 {
		quotient++
	}

	result := int64(quotient)
	return &result, nil
}
