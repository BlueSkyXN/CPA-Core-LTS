package chat_completions

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Claude,
		ConvertOpenAIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAI,
			NonStream: ConvertClaudeResponseToOpenAINonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, func(_ context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
		req.Body = convertOpenAIRequestToClaude(req.Model, req.Body, req.Stream, req.ModelInfo != nil && req.ModelInfo.IsCompat, req.ModelInfo)
		return req
	})
}
