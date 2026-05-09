package responses

import (
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/constant"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/interfaces"
	"github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Codex,
		ConvertOpenAIResponsesRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAIResponses,
			NonStream: ConvertCodexResponseToOpenAIResponsesNonStream,
		},
	)
}
