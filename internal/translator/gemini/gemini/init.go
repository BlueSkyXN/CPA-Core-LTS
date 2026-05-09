package gemini

import (
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/constant"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/interfaces"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/translator"
)

// Register a no-op response translator and a request normalizer for Gemini→Gemini.
// The request converter ensures missing or invalid roles are normalized to valid values.
func init() {
	translator.Register(
		Gemini,
		Gemini,
		ConvertGeminiRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     PassthroughGeminiResponseStream,
			NonStream:  PassthroughGeminiResponseNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
