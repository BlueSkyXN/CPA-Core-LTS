package common

// NonNegativeTokenCount drops malformed negative token values before they are
// projected into a translated client response.
func NonNegativeTokenCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// SumNonNegativeTokenCounts returns false instead of wrapping when a token
// total cannot be represented as a non-negative int64.
func SumNonNegativeTokenCounts(values ...int64) (int64, bool) {
	const maxInt64 = int64(1<<63 - 1)
	var total int64
	for _, value := range values {
		if value < 0 || value > maxInt64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}
