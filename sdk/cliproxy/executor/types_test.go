package executor

import (
	"math"
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

func TestUsageAccumulatorSumsNormalizedTokenCategories(t *testing.T) {
	accumulator := NewUsageAccumulator(coreusage.Detail{
		InputTokens:         100,
		CacheReadTokens:     30,
		CacheCreationTokens: 40,
		TotalTokens:         100,
	})
	accumulator.Add(coreusage.Detail{
		InputTokens: 5,
		TotalTokens: 5,
	})

	snapshot := accumulator.Snapshot()
	if snapshot.InputTokens != 105 || snapshot.CacheReadTokens != 30 || snapshot.CacheCreationTokens != 40 || snapshot.TotalTokens != 105 {
		t.Fatalf("accumulator snapshot = %+v, want normalized token categories summed", snapshot)
	}
}

func TestUsageAccumulatorFailsClosedOnTokenOverflow(t *testing.T) {
	accumulator := NewUsageAccumulator(coreusage.Detail{
		InputTokens:         math.MaxInt64,
		OutputTokens:        math.MaxInt64,
		ReasoningTokens:     math.MaxInt64,
		CachedTokens:        math.MaxInt64,
		CacheReadTokens:     math.MaxInt64,
		CacheCreationTokens: math.MaxInt64,
		TotalTokens:         math.MaxInt64,
	})
	accumulator.Add(coreusage.Detail{
		InputTokens:         1,
		OutputTokens:        1,
		ReasoningTokens:     1,
		CachedTokens:        1,
		CacheReadTokens:     1,
		CacheCreationTokens: 1,
		TotalTokens:         1,
	})

	snapshot := accumulator.Snapshot()
	if snapshot.InputTokens != 0 || snapshot.OutputTokens != 0 || snapshot.ReasoningTokens != 0 || snapshot.CachedTokens != 0 || snapshot.CacheReadTokens != 0 || snapshot.CacheCreationTokens != 0 || snapshot.TotalTokens != 0 {
		t.Fatalf("accumulator snapshot = %+v, want overflowed token fields to fail closed", snapshot)
	}
	if retrySnapshot := accumulator.RetryWithoutPenaltySnapshot(); retrySnapshot.FoldedOutputTokens != 0 {
		t.Fatalf("folded_output_tokens = %d, want 0 when the aggregate is not representable", retrySnapshot.FoldedOutputTokens)
	}
}
