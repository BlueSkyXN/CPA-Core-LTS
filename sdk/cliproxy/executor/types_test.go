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
