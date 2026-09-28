package responses

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Claude,
		ConvertOpenAIResponsesRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAIResponses,
			NonStream: ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, func(_ context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
		if req.ModelInfo != nil && req.ModelInfo.IsCompat {
			req.Body = ConvertOpenAIResponsesRequestToClaudeWithCompat(req.Model, req.Body, req.Stream)
		} else {
			req.Body = ConvertOpenAIResponsesRequestToClaude(req.Model, req.Body, req.Stream)
		}
		return req
	})
}
