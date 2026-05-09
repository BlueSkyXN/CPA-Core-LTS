package responses

import (
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/antigravity/gemini"
	. "github.com/BlueSkyXN/CPA-Core-LTS/v6/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return ConvertGeminiRequestToAntigravity(modelName, rawJSON, stream)
}
