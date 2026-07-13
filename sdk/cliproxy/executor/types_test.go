package executor

import (
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestResponseFormatOrSourceUsesExplicitResponseFormat(t *testing.T) {
	opts := Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatClaude,
	}

	if got := ResponseFormatOrSource(opts); got != sdktranslator.FormatClaude {
		t.Fatalf("ResponseFormatOrSource() = %q, want %q", got, sdktranslator.FormatClaude)
	}
}

func TestResponseFormatOrSourceFallsBackToSourceFormat(t *testing.T) {
	opts := Options{SourceFormat: sdktranslator.FormatGemini}

	if got := ResponseFormatOrSource(opts); got != sdktranslator.FormatGemini {
		t.Fatalf("ResponseFormatOrSource() = %q, want %q", got, sdktranslator.FormatGemini)
	}
}

func TestUsageAccumulatorOnlyKeepsKnownUncachedInputWhenAllContributionsAreKnown(t *testing.T) {
	accumulator := NewUsageAccumulator(coreusage.Detail{
		InputTokens:              10,
		CacheReadTokens:          10,
		TotalTokens:              10,
		UncachedInputTokens:      0,
		UncachedInputTokensKnown: true,
	})
	accumulator.Add(coreusage.Detail{
		InputTokens:              5,
		TotalTokens:              5,
		UncachedInputTokens:      5,
		UncachedInputTokensKnown: true,
	})

	snapshot := accumulator.Snapshot()
	if !snapshot.UncachedInputTokensKnown || snapshot.UncachedInputTokens != 5 {
		t.Fatalf("known accumulator snapshot = %+v, want known uncached input 5", snapshot)
	}

	accumulator.Add(coreusage.Detail{InputTokens: 1, TotalTokens: 1})
	snapshot = accumulator.Snapshot()
	if snapshot.UncachedInputTokensKnown {
		t.Fatalf("mixed-known accumulator snapshot = %+v, want uncached input unknown", snapshot)
	}
}
