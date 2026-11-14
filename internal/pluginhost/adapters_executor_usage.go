package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/MoeForge/moego/ojson"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// formalUsageReporter returns a reporter only for the selected-auth provider
// path. Direct ModelRouter executor calls pass nil auth and already have a
// handler-owned reporter, so reporting here as well would double count them.
func (a *executorAdapter) formalUsageReporter(ctx context.Context, auth *coreauth.Auth, prepared preparedExecutorCall) *helps.UsageReporter {
	if a == nil || auth == nil {
		return nil
	}
	modelName := strings.TrimSpace(thinking.ParseSuffix(prepared.req.Model).ModelName)
	if modelName == "" {
		modelName = prepared.req.Model
	}
	reporter := helps.NewExecutorUsageReporter(ctx, a, modelName, auth)
	reporter.SetTranslatedReasoningEffort(prepared.req.Payload, prepared.inputFormat.String())
	reporter.SetOutboundServiceTier(prepared.req.Payload)
	reporter.SetUsageProvenance(coreusage.UsageProvenanceUnavailable)
	// 插件执行器的响应是上游流式 payload 的逐 chunk 转发；启用语义 timing
	// 让 TTFT/TTFA/output_timing 由帧分类产生，而不是只剩首包兜底。非流式
	// 执行路径（Execute）不走 StartResponseTiming，不受影响。
	reporter.EnableSemanticTiming(prepared.outputFormat.String())
	return reporter
}

func pluginExecutorUsageDetail(format sdktranslator.Format, payload []byte) coreusage.Detail {
	switch format {
	case sdktranslator.FormatClaude:
		return helps.ParseClaudeUsage(payload)
	case sdktranslator.FormatGemini:
		return helps.ParseGeminiUsage(payload)
	case sdktranslator.FormatInteractions:
		return helps.ParseInteractionsUsage(payload)
	case sdktranslator.FormatAntigravity:
		return helps.ParseAntigravityUsage(payload)
	case sdktranslator.FormatOpenAIResponse:
		if detail, ok := helps.ParseCodexUsage(payload); ok {
			return detail
		}
		return helps.ParseOpenAIUsage(payload)
	default:
		return helps.ParseOpenAIUsage(payload)
	}
}

// formalPluginStreamUsage 由单个流协程观察原始用量，按最终交付结果发布统计。
type formalPluginStreamUsage struct {
	reporter   *helps.UsageReporter
	format     sdktranslator.Format
	buffer     helps.StreamUsageBuffer
	lineBuffer []byte
	sawPayload bool
}

func (u *formalPluginStreamUsage) observe(payload []byte) {
	if u.reporter == nil || len(payload) == 0 {
		return
	}
	if !u.sawPayload {
		u.reporter.MarkFirstResponseByte()
		u.sawPayload = true
	}
	u.reporter.ObserveTimingPayload(u.format.String(), payload)
	helps.ObservePluginExecutorStreamTTFT(u.format.String(), u.reporter, payload)
	u.lineBuffer = append(u.lineBuffer, payload...)
	for {
		idx := bytes.IndexByte(u.lineBuffer, '\n')
		if idx < 0 {
			break
		}
		line := u.lineBuffer[:idx+1]
		u.lineBuffer = u.lineBuffer[idx+1:]
		helps.ObservePluginExecutorStreamUsage(u.format.String(), line, &u.buffer)
	}
	if payload := helps.ExtractStreamJSONPayload(u.lineBuffer); len(payload) > 0 && json.Valid(payload) {
		helps.ObservePluginExecutorStreamUsage(u.format.String(), u.lineBuffer, &u.buffer)
		u.lineBuffer = nil
	}
}

func (u *formalPluginStreamUsage) publish(ctx context.Context, err error) {
	if u.reporter == nil {
		return
	}
	if len(u.lineBuffer) > 0 {
		helps.ObservePluginExecutorStreamUsage(u.format.String(), u.lineBuffer, &u.buffer)
		u.lineBuffer = nil
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && !u.sawPayload {
		err = emptyFormalPluginStreamError()
	}
	setPluginUsageProvenance(u.reporter, &u.buffer)
	if err != nil {
		if !u.buffer.PublishFailure(ctx, u.reporter, err) {
			u.reporter.PublishFailure(ctx, err)
		}
		return
	}
	u.buffer.Publish(ctx, u.reporter)
	u.reporter.EnsurePublished(ctx)
}

func observeFormalPluginStreamUsage(ctx context.Context, reporter *helps.UsageReporter, format sdktranslator.Format, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	return observeFormalPluginStreamUsageWithCancel(ctx, reporter, format, in, nil)
}

// observeFormalPluginStreamUsageWithCancel 在 usage 结算完成后释放派生的执行 ctx
// （cancelStream）。直通分支没有转换协程，观察协程就是最后一个持有者。
func observeFormalPluginStreamUsageWithCancel(ctx context.Context, reporter *helps.UsageReporter, format sdktranslator.Format, in <-chan pluginapi.ExecutorStreamChunk, cancelStream context.CancelFunc) <-chan pluginapi.ExecutorStreamChunk {
	if reporter == nil {
		if cancelStream != nil {
			// 没有 usage 可结算，观察协程不启动；同步释放派生 ctx。
			if in == nil {
				cancelStream()
			} else {
				go func() {
					defer cancelStream()
					for range in { //nolint:revive // 排空上游，等待其自然结束
					}
				}()
			}
		}
		return in
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if in == nil {
		reporter.PublishFailure(ctx, emptyFormalPluginStreamError())
		if cancelStream != nil {
			cancelStream()
		}
		closed := make(chan pluginapi.ExecutorStreamChunk)
		close(closed)
		return closed
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		usage := formalPluginStreamUsage{reporter: reporter, format: format}
		var streamErr error
		defer func() {
			usage.publish(ctx, streamErr)
			if cancelStream != nil {
				cancelStream()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					return
				}
				if chunk.Err != nil {
					streamErr = chunk.Err
					usage.publish(ctx, streamErr)
				} else {
					usage.observe(chunk.Payload)
				}
				if !sendExecutorPluginStreamChunk(ctx, out, chunk) || streamErr != nil {
					return
				}
			}
		}
	}()
	return out
}

func emptyFormalPluginStreamError() error {
	return &coreauth.Error{Code: "empty_stream", Message: "plugin executor stream closed before first payload", Retryable: true}
}

func setPluginUsageProvenance(reporter *helps.UsageReporter, buffer *helps.StreamUsageBuffer) {
	if reporter == nil {
		return
	}
	if _, ok := buffer.Detail(); ok {
		reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
		return
	}
	reporter.SetUsageProvenance(coreusage.UsageProvenanceUnavailable)
}

func pluginExecutorUsageReported(format sdktranslator.Format, payload []byte) bool {
	if len(payload) == 0 || !ojson.ValidBytes(payload) {
		return false
	}
	paths := []string{"usage"}
	switch format {
	case sdktranslator.FormatOpenAIResponse:
		paths = append(paths, "response.usage")
	case sdktranslator.FormatClaude:
		paths = append(paths, "message.usage")
	case sdktranslator.FormatGemini:
		paths = append(paths, "usageMetadata", "usage_metadata")
	case sdktranslator.FormatInteractions:
		paths = append(paths,
			"total_usage",
			"metadata.total_usage",
			"metadata.usage",
			"usageMetadata",
			"usage_metadata",
			"interaction.usage",
			"interaction.total_usage",
			"interaction.metadata.total_usage",
		)
	case sdktranslator.FormatAntigravity:
		paths = append(paths, "response.usageMetadata", "usageMetadata", "usage_metadata")
	}
	for _, path := range paths {
		value := ojson.GetBytes(payload, path)
		if value.Exists() && value.Type != ojson.Null {
			return true
		}
	}
	return false
}

func observePluginUsageChunk(format sdktranslator.Format, payload []byte, buffer *helps.StreamUsageBuffer) {
	if buffer == nil || len(payload) == 0 {
		return
	}
	switch format {
	case sdktranslator.FormatClaude:
		if detail, ok := helps.ParseClaudeStreamUsage(payload); ok {
			buffer.Observe(detail, true)
		}
	case sdktranslator.FormatGemini:
		if detail, ok := helps.ParseGeminiStreamUsage(payload); ok {
			buffer.Observe(detail, true)
		}
	case sdktranslator.FormatInteractions:
		if detail, ok := helps.ParseInteractionsStreamUsage(payload); ok {
			buffer.Observe(detail, true)
		}
	case sdktranslator.FormatAntigravity:
		if detail, ok := helps.ParseAntigravityStreamUsage(payload); ok {
			buffer.Observe(detail, true)
		}
	case sdktranslator.FormatOpenAIResponse:
		if detail, ok := helps.ParseCodexUsage(payload); ok {
			buffer.Observe(detail, true)
			return
		}
		buffer.ObserveOpenAIStream(payload)
	default:
		buffer.ObserveOpenAIStream(payload)
	}
}
