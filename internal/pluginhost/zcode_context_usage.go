package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	zcodeContextCountTimeout = 5 * time.Second
	zcodeContextMaxBytes     = 16 * 1024 * 1024
)

func (a *executorAdapter) zcodeContextUsageMode(prepared preparedExecutorCall) config.CodexZcodeContextUsageMode {
	if a == nil || a.host == nil || a.provider != "zcode-coding-plan" ||
		prepared.inputRequested != tr.FormatOpenAIResponse || prepared.requestedFormat != tr.FormatOpenAIResponse ||
		prepared.inputFormat != tr.FormatClaude || prepared.outputFormat != tr.FormatClaude {
		return config.ZcodeContextUsageDisabled
	}
	headers := prepared.opts.Headers
	originator := strings.ToLower(strings.TrimSpace(headers.Get("Originator")))
	isCodex := multiagentv2.IsCodexClientUserAgent(headers.Get("User-Agent"))
	for _, name := range []string{"codex desktop", "codex-tui", "codex_cli_rs"} {
		isCodex = isCodex || originator == name || strings.HasPrefix(originator, name+"/")
	}
	if !isCodex {
		return config.ZcodeContextUsageDisabled
	}
	a.host.mu.Lock()
	defer a.host.mu.Unlock()
	if a.host.runtimeConfig == nil {
		return config.ZcodeContextUsageDisabled
	}
	return a.host.runtimeConfig.Codex.ZcodeContextUsage.Effective()
}

// Only the terminal total sent to Codex is shaped. The raw Claude usage has
// already reached the accounting observer and keeps its provider semantics.
func (a *executorAdapter) adjustZcodeContextUsage(ctx context.Context, prepared preparedExecutorCall, req pluginapi.ExecutorRequest, response []byte, waitClosed func(context.Context) bool) []byte {
	mode := a.zcodeContextUsageMode(prepared)
	if mode == config.ZcodeContextUsageDisabled || !json.Valid(response) {
		return response
	}
	root := gjson.ParseBytes(response)
	if root.Get("status").String() != "completed" || !root.Get("output").IsArray() {
		return response
	}
	search := false
	for _, item := range root.Get("output").Array() {
		if item.Get("type").String() == "web_search_call" {
			search = true
			break
		}
	}
	if !search {
		return response
	}
	started := time.Now()
	status := "invalid_context"
	var counted int64
	originalTotal := root.Get("usage.total_tokens").Int()
	clientTotal := originalTotal
	defer func() {
		log.WithFields(log.Fields{
			"mode": mode, "status": status, "provider_total_tokens": originalTotal,
			"counted_context_tokens": counted, "client_total_tokens": clientTotal,
			"duration_ms": time.Since(started).Milliseconds(),
		}).Info("zcode context usage")
	}()
	if len(req.Payload)+len(response) > zcodeContextMaxBytes {
		status = "size_limit"
		return response
	}
	// Native metadata is required for BigModel search replay. Never guess from
	// the model name or reuse another provider's catalog entry.
	if prepared.modelInfo == nil || prepared.modelInfo.NativeCapabilities == nil ||
		prepared.modelInfo.NativeCapabilities.WebSearchReplay != registry.NativeWebSearchReplayBigModel {
		status = "unsupported_replay"
		return response
	}
	payload, err := clauderesponses.AppendResponseOutputForTokenCount(prepared.req.Model, req.Payload, []byte(root.Get("output").Raw), prepared.modelInfo)
	if err != nil {
		return response
	}
	if len(payload) > zcodeContextMaxBytes {
		status = "size_limit"
		return response
	}
	// This auxiliary call inherits transport and cancellation, not generation usage,
	// request-size or raw-request logging holders. Its identity/session/auth are
	// carried explicitly in req; no routing or credential selection runs again.
	countCtx, cancel := context.WithTimeout(context.Background(), zcodeContextCountTimeout)
	defer cancel()
	if ctx != nil {
		if transport := ctx.Value("cliproxy.roundtripper"); transport != nil {
			countCtx = context.WithValue(countCtx, "cliproxy.roundtripper", transport)
		}
		countCtx = coreexecutor.WithRequestProxyURL(countCtx, coreexecutor.RequestProxyURL(ctx))
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if ctx.Err() != nil {
			status = "canceled"
			return response
		}
	}
	if waitClosed != nil && !waitClosed(countCtx) {
		status = "stream_cleanup_incomplete"
		return response
	}
	req.Payload = payload
	req.Stream = false
	req.OriginalRequest = nil
	req.RequestID = uuid.NewString()
	req.Metadata = cloneAnyMap(req.Metadata)
	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	req.Metadata[coreexecutor.RequestIDMetadataKey] = req.RequestID
	counted, status = a.countZcodeContext(countCtx, req)
	if status != "counted" || mode != config.ZcodeContextUsageCorrect {
		return response
	}
	adjusted, err := sjson.SetBytes(response, "usage.total_tokens", counted)
	if err != nil {
		status = "invalid_usage"
		return response
	}
	clientTotal = counted // The count already includes the delivered assistant output.
	status = "corrected"
	return adjusted
}

func (a *executorAdapter) countZcodeContext(ctx context.Context, req pluginapi.ExecutorRequest) (count int64, status string) {
	if a == nil || a.host == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return 0, "count_unavailable"
	}
	if ctx.Err() != nil {
		return 0, "count_canceled_or_timed_out"
	}
	// Native plugin calls can cross a blocking ABI. Bound the client wait even
	// when a plugin is slow to finish its cancellation cleanup.
	type result struct {
		count  int64
		status string
	}
	done := make(chan result, 1)
	stop := a.watchExecutionCancellation(ctx, req)
	defer stop()
	go func() {
		count, status := a.invokeZcodeContextCount(ctx, req)
		done <- result{count, status}
	}()
	select {
	case got := <-done:
		return got.count, got.status
	case <-ctx.Done():
		return 0, "count_canceled_or_timed_out"
	}
}

func (a *executorAdapter) invokeZcodeContextCount(ctx context.Context, req pluginapi.ExecutorRequest) (count int64, status string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			a.fuseCurrentPlugin("Executor.CountTokens", recovered)
			count, status = 0, "count_panic"
		}
	}()
	response, err := a.executor.CountTokens(ctx, req)
	if ctx.Err() != nil {
		return 0, "count_canceled_or_timed_out"
	}
	if err != nil {
		return 0, "count_failed"
	}
	if len(response.Payload) > 1024*1024 || !json.Valid(response.Payload) {
		return 0, "invalid_count"
	}
	value := gjson.GetBytes(response.Payload, "input_tokens")
	if value.Type != gjson.Number {
		return 0, "invalid_count"
	}
	count, err = strconv.ParseInt(value.Raw, 10, 64)
	if err != nil || count <= 0 {
		return 0, "invalid_count"
	}
	return count, "counted"
}

func (a *executorAdapter) adjustZcodeContextFrame(ctx context.Context, prepared preparedExecutorCall, req pluginapi.ExecutorRequest, frame []byte, waitClosed func(context.Context) bool) []byte {
	lines := bytes.Split(frame, []byte("\n"))
	for i, line := range lines {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if gjson.GetBytes(data, "type").String() != "response.completed" {
			continue
		}
		response := []byte(gjson.GetBytes(data, "response").Raw)
		adjusted := a.adjustZcodeContextUsage(ctx, prepared, req, response, waitClosed)
		if !bytes.Equal(adjusted, response) {
			updated, err := sjson.SetRawBytes(data, "response", adjusted)
			if err == nil {
				lines[i] = append([]byte("data: "), updated...)
			}
		}
	}
	return bytes.Join(lines, []byte("\n"))
}
