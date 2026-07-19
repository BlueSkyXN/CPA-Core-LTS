package usage

import (
	"context"
	"net/http"
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

func TestResolveEffectiveServiceTier(t *testing.T) {
	tests := []struct {
		name     string
		response string
		outbound string
		want     string
	}{
		{name: "recognized response wins", response: " fast ", outbound: "standard", want: "priority"},
		{name: "recognized standard response wins", response: "default", outbound: "priority", want: "standard"},
		{name: "unknown response blocks fallback", response: "flex", outbound: "priority", want: ""},
		{name: "missing response uses explicit outbound priority", outbound: "fast", want: "priority"},
		{name: "missing response uses explicit outbound standard", outbound: "default", want: "standard"},
		{name: "missing values stay unknown", want: ""},
		{name: "unknown outbound stays unknown", outbound: "auto", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveEffectiveServiceTier(tt.response, tt.outbound); got != tt.want {
				t.Fatalf("ResolveEffectiveServiceTier(%q, %q) = %q, want %q", tt.response, tt.outbound, got, tt.want)
			}
		})
	}
}

func TestManagerDequeueClearsConsumedContextAndBackingArray(t *testing.T) {
	m := NewManager(2)
	ctx := context.WithValue(context.Background(), struct{}{}, bytesMarker(1))
	m.queue = append(m.queue,
		queueItem{ctx: ctx, record: Record{ResponseHeaders: http.Header{"X-Test": {"first"}}}},
		queueItem{ctx: context.Background(), record: Record{Provider: "second"}},
	)
	backing := m.queue

	item := m.dequeueLocked()
	if item.ctx != ctx {
		t.Fatal("dequeueLocked returned the wrong queue item")
	}
	if backing[0].ctx != nil || backing[0].record.ResponseHeaders != nil {
		t.Fatal("dequeueLocked retained consumed context or record references in the backing array")
	}
	if len(m.queue) != 1 || m.queue[0].record.Provider != "second" {
		t.Fatalf("remaining queue = %+v, want second item", m.queue)
	}

	_ = m.dequeueLocked()
	if m.queue != nil {
		t.Fatalf("queue after final dequeue = %#v, want nil", m.queue)
	}
}

type bytesMarker byte
