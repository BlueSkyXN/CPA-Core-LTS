package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func TestReasoningEffortExtractionOwnsInput(t *testing.T) {
	for _, tc := range []struct{ provider, body string }{
		{"claude", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`},
		{"gemini", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`},
		{"openai", `{"reasoning_effort":"high"}`},
		{"codex", `{"reasoning":{"effort":"high"}}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			payload := []byte(tc.body)
			requested := thinking.ExtractReasoningEffort(payload, tc.provider, "test-model")
			translated := thinking.ExtractTranslatedReasoningEffort(payload, tc.provider)
			for i := range payload {
				payload[i] = 'X'
			}
			if requested != "high" || translated != "high" {
				t.Fatalf("effort changed after input reuse: requested=%q translated=%q", requested, translated)
			}
		})
	}
}
