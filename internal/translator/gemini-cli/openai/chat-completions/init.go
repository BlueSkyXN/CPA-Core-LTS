package chat_completions

import (
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/constant"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/interfaces"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		GeminiCLI,
		ConvertOpenAIRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertCliResponseToOpenAI,
			NonStream: ConvertCliResponseToOpenAINonStream,
		},
	)
}
