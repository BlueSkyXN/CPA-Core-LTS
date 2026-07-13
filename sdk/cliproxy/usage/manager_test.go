package usage

import "testing"

func TestNormalizeUncachedInputTokens(t *testing.T) {
	tests := []struct {
		name  string
		input Detail
		want  Detail
	}{
		{
			name: "valid known",
			input: Detail{
				InputTokens:              10,
				UncachedInputTokens:      4,
				UncachedInputTokensKnown: true,
			},
			want: Detail{
				InputTokens:              10,
				UncachedInputTokens:      4,
				UncachedInputTokensKnown: true,
			},
		},
		{
			name: "valid known zero",
			input: Detail{
				InputTokens:              10,
				UncachedInputTokensKnown: true,
			},
			want: Detail{
				InputTokens:              10,
				UncachedInputTokensKnown: true,
			},
		},
		{
			name: "unknown value cleared",
			input: Detail{
				InputTokens:         10,
				UncachedInputTokens: 4,
			},
			want: Detail{InputTokens: 10},
		},
		{
			name: "known greater than input cleared",
			input: Detail{
				InputTokens:              10,
				UncachedInputTokens:      11,
				UncachedInputTokensKnown: true,
			},
			want: Detail{InputTokens: 10},
		},
		{
			name: "known negative cleared",
			input: Detail{
				InputTokens:              10,
				UncachedInputTokens:      -1,
				UncachedInputTokensKnown: true,
			},
			want: Detail{InputTokens: 10},
		},
		{
			name: "negative input cleared",
			input: Detail{
				InputTokens:              -1,
				UncachedInputTokensKnown: true,
			},
			want: Detail{InputTokens: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeUncachedInputTokens(tt.input); got != tt.want {
				t.Fatalf("NormalizeUncachedInputTokens() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
