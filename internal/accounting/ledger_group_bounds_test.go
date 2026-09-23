package accounting

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestCalculateMutuallyExclusiveInputUpperBoundMatchesBigIntegerOracle(t *testing.T) {
	tests := []struct {
		name  string
		usage MutuallyExclusiveInputUpperUsage
		price PriceSnapshot
	}{
		{
			name:  "ordinary input rate is maximum",
			usage: MutuallyExclusiveInputUpperUsage{InputMax: 1_234_567, OutputMax: 765_432},
			price: groupedPrice(41, 17, 23, 31),
		},
		{
			name:  "cache read rate is maximum",
			usage: MutuallyExclusiveInputUpperUsage{InputMax: 2_345_678, OutputMax: 876_543},
			price: groupedPrice(13, 19, 43, 29),
		},
		{
			name:  "cache write rate is maximum",
			usage: MutuallyExclusiveInputUpperUsage{InputMax: 3_456_789, OutputMax: 987_654},
			price: groupedPrice(11, 37, 23, 47),
		},
		{
			name:  "ceil fractional micro",
			usage: MutuallyExclusiveInputUpperUsage{InputMax: 1},
			price: groupedPrice(0, 0, 0, 1),
		},
		{
			name:  "maximum token upper without sum overflow",
			usage: MutuallyExclusiveInputUpperUsage{InputMax: math.MaxInt64 - 7, OutputMax: 7},
			price: groupedPrice(1, 1, 1, 1),
		},
		{
			name:  "zero bounds and zero price",
			usage: MutuallyExclusiveInputUpperUsage{},
			price: groupedPrice(0, 0, 0, 0),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantTokens, wantCost, ok := mutuallyExclusiveInputOracle(test.usage, test.price)
			if !ok {
				t.Fatal("oracle unexpectedly overflowed")
			}
			before := test.price
			gotTokens, gotCost, err := CalculateMutuallyExclusiveInputUpperBound(test.usage, test.price)
			if err != nil || gotTokens != wantTokens || gotCost != wantCost {
				t.Fatalf("tokens=%d cost=%d err=%v wantTokens=%d wantCost=%d", gotTokens, gotCost, err, wantTokens, wantCost)
			}
			if test.price != before {
				t.Fatalf("price snapshot mutated: got=%+v want=%+v", test.price, before)
			}
		})
	}
}

func TestCalculateMutuallyExclusiveInputUpperBoundRejectsInvalidAndOverflow(t *testing.T) {
	valid := groupedPrice(1, 1, 1, 1)
	tests := []struct {
		name  string
		usage MutuallyExclusiveInputUpperUsage
		price PriceSnapshot
	}{
		{name: "negative input", usage: MutuallyExclusiveInputUpperUsage{InputMax: -1}, price: valid},
		{name: "negative output", usage: MutuallyExclusiveInputUpperUsage{OutputMax: -1}, price: valid},
		{name: "token sum overflow", usage: MutuallyExclusiveInputUpperUsage{InputMax: math.MaxInt64, OutputMax: 1}, price: valid},
		{name: "cost overflow", usage: MutuallyExclusiveInputUpperUsage{InputMax: math.MaxInt64}, price: groupedPrice(MaxPriceRate, 0, MaxPriceRate-1, MaxPriceRate-2)},
		{name: "missing price identity", usage: MutuallyExclusiveInputUpperUsage{}, price: PriceSnapshot{}},
		{name: "invalid currency", usage: MutuallyExclusiveInputUpperUsage{}, price: PriceSnapshot{Version: "grouped-price", Currency: "usd"}},
		{name: "negative input rate", usage: MutuallyExclusiveInputUpperUsage{}, price: groupedPrice(-1, 0, 0, 0)},
		{name: "rate above catalog maximum", usage: MutuallyExclusiveInputUpperUsage{}, price: groupedPrice(0, 0, 0, MaxPriceRate+1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.price
			tokens, cost, err := CalculateMutuallyExclusiveInputUpperBound(test.usage, test.price)
			if !errors.Is(err, ErrInvalid) || tokens != 0 || cost != 0 {
				t.Fatalf("tokens=%d cost=%d err=%v", tokens, cost, err)
			}
			if test.price != before {
				t.Fatalf("price snapshot mutated on failure: got=%+v want=%+v", test.price, before)
			}
		})
	}
}

func TestCalculateMutuallyExclusiveInputUpperBoundKeepsFourBucketCostIndependent(t *testing.T) {
	price := groupedPrice(1, 7, 10, 100)
	million := int64(1_000_000)
	fourBucketCost, err := CalculateUpperCost(UpperUsage{
		InputTokens: million, CacheReadTokens: million, CacheWriteTokens: million,
	}, price)
	if err != nil {
		t.Fatal(err)
	}
	if fourBucketCost != 111 {
		t.Fatalf("four-bucket cost=%d want=111", fourBucketCost)
	}

	tokens, groupedCost, err := CalculateMutuallyExclusiveInputUpperBound(
		MutuallyExclusiveInputUpperUsage{InputMax: million}, price,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != million || groupedCost != 100 {
		t.Fatalf("grouped tokens=%d cost=%d wantTokens=%d wantCost=100", tokens, groupedCost, million)
	}
	if price != groupedPrice(1, 7, 10, 100) {
		t.Fatalf("grouped calculation changed original price: %+v", price)
	}
}

func mutuallyExclusiveInputOracle(usage MutuallyExclusiveInputUpperUsage, price PriceSnapshot) (int64, int64, bool) {
	tokens := new(big.Int).Add(big.NewInt(usage.InputMax), big.NewInt(usage.OutputMax))
	if !tokens.IsInt64() {
		return 0, 0, false
	}
	inputRate := price.InputPerMillionMicro
	if price.CacheReadPerMillionMicro > inputRate {
		inputRate = price.CacheReadPerMillionMicro
	}
	if price.CacheWritePerMillionMicro > inputRate {
		inputRate = price.CacheWritePerMillionMicro
	}
	cost := new(big.Int).Mul(big.NewInt(usage.InputMax), big.NewInt(inputRate))
	cost.Add(cost, new(big.Int).Mul(big.NewInt(usage.OutputMax), big.NewInt(price.OutputPerMillionMicro)))
	cost.Add(cost, big.NewInt(999_999))
	cost.Quo(cost, big.NewInt(1_000_000))
	if !cost.IsInt64() {
		return 0, 0, false
	}
	return tokens.Int64(), cost.Int64(), true
}

func groupedPrice(input, output, cacheRead, cacheWrite int64) PriceSnapshot {
	return PriceSnapshot{
		Version:                   "grouped-price-v1",
		Currency:                  "USD",
		InputPerMillionMicro:      input,
		OutputPerMillionMicro:     output,
		CacheReadPerMillionMicro:  cacheRead,
		CacheWritePerMillionMicro: cacheWrite,
	}
}
