package usage

import (
	"context"
	"net/http"
	"testing"
)

func TestStreamFromContextDefaultsMissingToFalse(t *testing.T) {
	if StreamFromContext(context.Background()) {
		t.Fatalf("StreamFromContext(background) = true, want false")
	}
}

func TestStreamFromContextHonorsExplicitTrue(t *testing.T) {
	ctx := WithStream(context.Background(), true)
	if !StreamFromContext(ctx) {
		t.Fatalf("StreamFromContext(true) = false, want true")
	}
}

func TestRecordStreamField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		Stream:   true,
	}
	if !record.Stream {
		t.Fatalf("Record.Stream = false, want true")
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

func TestCanonicalUsageProvenance(t *testing.T) {
	for _, value := range []string{
		UsageProvenanceExact,
		UsageProvenanceProviderReportedUnverified,
		UsageProvenanceEstimated,
		UsageProvenanceUnavailable,
		UsageProvenanceQuotaSnapshot,
	} {
		if got := CanonicalUsageProvenance("  " + value + "  "); got != value {
			t.Fatalf("CanonicalUsageProvenance(%q) = %q", value, got)
		}
	}
	if got := CanonicalUsageProvenance("provider_guess"); got != "" {
		t.Fatalf("CanonicalUsageProvenance(unknown) = %q, want empty", got)
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
