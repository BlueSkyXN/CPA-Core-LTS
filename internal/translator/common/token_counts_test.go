package common

import (
	"math"
	"testing"
)

func TestSumNonNegativeTokenCounts(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		want   int64
		ok     bool
	}{
		{name: "normal", values: []int64{3, 4, 5}, want: 12, ok: true},
		{name: "maximum", values: []int64{math.MaxInt64}, want: math.MaxInt64, ok: true},
		{name: "overflow", values: []int64{math.MaxInt64, 1}, want: 0, ok: false},
		{name: "negative", values: []int64{1, -1}, want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SumNonNegativeTokenCounts(tt.values...)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("SumNonNegativeTokenCounts(%v) = (%d, %t), want (%d, %t)", tt.values, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestNonNegativeTokenCount(t *testing.T) {
	if got := NonNegativeTokenCount(-1); got != 0 {
		t.Fatalf("NonNegativeTokenCount(-1) = %d, want 0", got)
	}
	if got := NonNegativeTokenCount(7); got != 7 {
		t.Fatalf("NonNegativeTokenCount(7) = %d, want 7", got)
	}
}
