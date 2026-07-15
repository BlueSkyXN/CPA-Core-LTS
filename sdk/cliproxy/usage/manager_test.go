package usage

import (
	"context"
	"testing"
)

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

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}
