package claude

import (
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/constant"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/interfaces"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		OpenAI,
		ConvertClaudeRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToClaude,
			NonStream:  ConvertOpenAIResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
