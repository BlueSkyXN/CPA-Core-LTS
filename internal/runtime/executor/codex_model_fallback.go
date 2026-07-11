package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexModelFallbackContinuityError struct {
	sourceModel string
	targetModel string
	reason      string
	cause       error
}

func (e *codexModelFallbackContinuityError) Error() string {
	if e == nil {
		return "codex model fallback blocked"
	}
	return fmt.Sprintf("codex model fallback from %s to %s blocked: %s", e.sourceModel, e.targetModel, e.reason)
}

func (e *codexModelFallbackContinuityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *codexModelFallbackContinuityError) ModelFallbackBlocked() bool { return true }

func isCodexModelFallbackBlockedError(err error) bool {
	if err == nil {
		return false
	}
	var blocked interface{ ModelFallbackBlocked() bool }
	return errors.As(err, &blocked) && blocked != nil && blocked.ModelFallbackBlocked()
}

func codexModelFallbackMetadata(opts cliproxyexecutor.Options) (sourceModel, reasoningContinuity string, ok bool) {
	sourceModel = metadataString(opts.Metadata, cliproxyexecutor.CodexModelFallbackSourceModelMetadataKey)
	if sourceModel == "" {
		return "", "", false
	}
	reasoningContinuity = strings.ToLower(metadataString(opts.Metadata, cliproxyexecutor.CodexModelFallbackReasoningContinuityMetadataKey))
	if reasoningContinuity != config.CodexModelFallbackReasoningContinuityContextReset {
		reasoningContinuity = config.CodexModelFallbackReasoningContinuitySameModelOnly
	}
	return sourceModel, reasoningContinuity, true
}

func prepareCodexModelFallbackBody(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope, bool, error) {
	sourceModel, reasoningContinuity, ok := codexModelFallbackMetadata(opts)
	if !ok {
		return body, codexReasoningReplayScope{}, false, nil
	}
	sourceBase := strings.TrimSpace(thinking.ParseSuffix(sourceModel).ModelName)
	targetBase := strings.TrimSpace(thinking.ParseSuffix(req.Model).ModelName)
	if sourceBase == "" {
		sourceBase = strings.TrimSpace(sourceModel)
	}
	if targetBase == "" {
		targetBase = strings.TrimSpace(req.Model)
	}
	if strings.EqualFold(sourceBase, targetBase) {
		return body, codexReasoningReplayScope{}, false, nil
	}

	sourceReq := req
	sourceReq.Model = sourceModel
	sourceScope := codexReasoningReplayScopeFromRequest(ctx, from, sourceReq, opts, body)
	var sourceItems [][]byte
	if sourceScope.valid() {
		items, found, errReplay := internalcache.GetCodexReasoningReplayItemsRequired(ctx, sourceScope.modelName, sourceScope.sessionKey)
		if errReplay != nil {
			return body, codexReasoningReplayScope{}, true, &codexModelFallbackContinuityError{
				sourceModel: sourceBase,
				targetModel: targetBase,
				reason:      "source reasoning replay state is unavailable",
				cause:       errReplay,
			}
		}
		if found {
			sourceItems = items
		}
	}

	hasContinuity := codexInputHasReasoningItem(body) || len(sourceItems) > 0
	if reasoningContinuity != config.CodexModelFallbackReasoningContinuityContextReset && hasContinuity {
		return body, codexReasoningReplayScope{}, true, &codexModelFallbackContinuityError{
			sourceModel: sourceBase,
			targetModel: targetBase,
			reason:      "reasoning continuity is scoped to the source model",
		}
	}

	updated := body
	if reasoningContinuity == config.CodexModelFallbackReasoningContinuityContextReset {
		var errReset error
		updated, errReset = dropCodexReasoningInputItems(updated)
		if errReset != nil || codexInputHasReasoningItem(updated) {
			return body, codexReasoningReplayScope{}, true, &codexModelFallbackContinuityError{
				sourceModel: sourceBase,
				targetModel: targetBase,
				reason:      "reasoning context reset could not be applied safely",
				cause:       errReset,
			}
		}
		toolItems := codexModelFallbackToolReplayItems(sourceItems)
		toolItems = filterCodexReasoningReplayItemsForInput(updated, toolItems)
		if len(toolItems) > 0 {
			if next, inserted := insertCodexReasoningReplayItems(updated, toolItems); inserted {
				updated = next
			}
		}
		helps.LogWithRequestID(ctx).WithFields(map[string]any{
			"source_model": sourceBase,
			"target_model": targetBase,
		}).Info("codex model fallback: reset model-private reasoning continuity")
	}

	targetScope := codexReasoningReplayScopeFromRequest(ctx, from, req, opts, updated)
	return updated, targetScope, true, nil
}

func codexInputHasReasoningItem(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
			return true
		}
	}
	return false
}

func dropCodexReasoningInputItems(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	items := input.Array()
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
			changed = true
			continue
		}
		kept = append(kept, item.Raw)
	}
	if !changed {
		return body, nil
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return body, err
	}
	return updated, nil
}

func codexModelFallbackToolReplayItems(items [][]byte) [][]byte {
	if len(items) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(items))
	for _, item := range items {
		switch strings.TrimSpace(gjson.GetBytes(item, "type").String()) {
		case "function_call", "custom_tool_call":
			out = append(out, append([]byte(nil), item...))
		}
	}
	return out
}
