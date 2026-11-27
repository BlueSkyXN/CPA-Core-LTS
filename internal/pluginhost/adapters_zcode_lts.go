package pluginhost

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// prepareZcodeResponsesRequest restores cached BigModel web_search_call results
// and preserves readable legacy search snapshots for glm-5.4 when native replay
// capability metadata is absent.
func (a *executorAdapter) prepareZcodeResponsesRequest(
	ctx context.Context,
	runtimeCfg *config.Config,
	auth *coreauth.Auth,
	req coreexecutor.Request,
	opts coreexecutor.Options,
	inputRequested sdktranslator.Format,
	baseModel string,
	modelInfo *registry.ModelInfo,
	body []byte,
) ([]byte, zcodeSearchReplay) {
	if inputRequested != sdktranslator.FormatOpenAIResponse {
		return body, zcodeSearchReplay{}
	}
	replay := a.zcodeSearchReplayScope(ctx, runtimeCfg, auth, req, opts, modelInfo)
	body = replay.restoreRequest(ctx, body)
	if a.shouldPreserveZcodeLegacySearchHistory(baseModel, modelInfo) {
		body = preserveZcodeLegacySearchHistory(body)
	}
	return body, replay
}

func (a *executorAdapter) shouldPreserveZcodeLegacySearchHistory(baseModel string, modelInfo *registry.ModelInfo) bool {
	return a != nil &&
		a.provider == "zcode-coding-plan" &&
		strings.EqualFold(baseModel, "glm-5.4") &&
		(modelInfo == nil || modelInfo.NativeCapabilities == nil || modelInfo.NativeCapabilities.WebSearchReplay != registry.NativeWebSearchReplayBigModel)
}

func (a *executorAdapter) zcodeResponsesSignedReasoningHistory(inputRequested sdktranslator.Format, payload []byte) bool {
	return a != nil &&
		a.provider == "zcode-coding-plan" &&
		inputRequested == sdktranslator.FormatOpenAIResponse &&
		hasZcodeSignedReasoningHistory(payload)
}

func (a *executorAdapter) finalizeZcodeExecuteResponse(ctx context.Context, prepared preparedExecutorCall, pluginReq pluginapi.ExecutorRequest, translated []byte) []byte {
	prepared.zcodeReplay.recordResponse(ctx, translated)
	return a.adjustZcodeContextUsage(ctx, prepared, pluginReq, translated, nil)
}
