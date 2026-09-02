package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
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

func observeFormalPluginStreamUsage(ctx context.Context, reporter *helps.UsageReporter, format sdktranslator.Format, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if reporter == nil {
		return in
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if in == nil {
		reporter.PublishFailure(ctx, emptyFormalPluginStreamError())
		closed := make(chan pluginapi.ExecutorStreamChunk)
		close(closed)
		return closed
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var usageBuffer helps.StreamUsageBuffer
		sawPayload := false
		firstPayload := true
		var lineBuffer []byte
		const maxLineBufferSize = 64 * 1024

		observePayload := func(payload []byte) {
			if firstPayload {
				reporter.MarkFirstResponseByte()
				firstPayload = false
			}
			reporter.ObserveTimingPayload(format.String(), payload)
			helps.ObservePluginExecutorStreamTTFT(format.String(), reporter, payload)
			lineBuffer = append(lineBuffer, payload...)
			for {
				idx := bytes.IndexByte(lineBuffer, '\n')
				if idx < 0 {
					break
				}
				line := lineBuffer[:idx+1]
				lineBuffer = lineBuffer[idx+1:]
				helps.ObservePluginExecutorStreamUsage(format.String(), line, &usageBuffer)
			}
			if jsonPayload := helps.ExtractStreamJSONPayload(lineBuffer); len(jsonPayload) > 0 && json.Valid(jsonPayload) {
				helps.ObservePluginExecutorStreamUsage(format.String(), lineBuffer, &usageBuffer)
				lineBuffer = nil
			}
			if len(lineBuffer) > maxLineBufferSize {
				helps.ObservePluginExecutorStreamUsage(format.String(), lineBuffer, &usageBuffer)
				lineBuffer = nil
			}
		}
		flushUsage := func() {
			if len(lineBuffer) == 0 {
				return
			}
			helps.ObservePluginExecutorStreamUsage(format.String(), lineBuffer, &usageBuffer)
			lineBuffer = nil
		}
		for {
			select {
			case <-ctx.Done():
				flushUsage()
				setPluginUsageProvenance(reporter, &usageBuffer)
				if !usageBuffer.PublishFailure(ctx, reporter, ctx.Err()) {
					reporter.PublishFailure(ctx, ctx.Err())
				}
				return
			case chunk, ok := <-in:
				if !ok {
					flushUsage()
					if errContext := ctx.Err(); errContext != nil {
						setPluginUsageProvenance(reporter, &usageBuffer)
						if !usageBuffer.PublishFailure(ctx, reporter, errContext) {
							reporter.PublishFailure(ctx, errContext)
						}
						return
					}
					if !sawPayload {
						reporter.PublishFailure(ctx, emptyFormalPluginStreamError())
						return
					}
					setPluginUsageProvenance(reporter, &usageBuffer)
					usageBuffer.Publish(ctx, reporter)
					reporter.EnsurePublished(ctx)
					return
				}
				if chunk.Err != nil {
					flushUsage()
					setPluginUsageProvenance(reporter, &usageBuffer)
					if !usageBuffer.PublishFailure(ctx, reporter, chunk.Err) {
						reporter.PublishFailure(ctx, chunk.Err)
					}
				} else if len(chunk.Payload) > 0 {
					sawPayload = true
					observePayload(chunk.Payload)
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					setPluginUsageProvenance(reporter, &usageBuffer)
					if !usageBuffer.PublishFailure(ctx, reporter, ctx.Err()) {
						reporter.PublishFailure(ctx, ctx.Err())
					}
					return
				}
				if chunk.Err != nil {
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
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
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
		value := gjson.GetBytes(payload, path)
		if value.Exists() && value.Type != gjson.Null {
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
