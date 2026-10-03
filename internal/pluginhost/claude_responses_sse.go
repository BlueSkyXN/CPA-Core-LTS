package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	claudecommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const maxClaudePluginEventBytes = 50 * 1024 * 1024

type claudePluginSSE struct {
	line  []byte
	data  []byte
	event string
	cr    bool
	size  int
}

func claudePluginConversionError() error {
	return coreauth.NewRequestScopedError("invalid or unsupported Anthropic plugin response", http.StatusBadGateway)
}

func (p *claudePluginSSE) feed(chunk []byte, emit func([]byte, string) error) error {
	for len(chunk) > 0 {
		if p.cr {
			p.cr = false
			if chunk[0] == '\n' {
				chunk = chunk[1:]
				continue
			}
		}
		end := bytes.IndexAny(chunk, "\r\n")
		length := len(chunk)
		if end >= 0 {
			length = end + 1
		}
		// 超限在复制前拒绝，避免逐字节遍历和扩容拖延上游取消。
		if length > maxClaudePluginEventBytes-p.size {
			return claudePluginConversionError()
		}
		p.size += length
		if end < 0 {
			p.line = append(p.line, chunk...)
			return nil
		}
		p.line = append(p.line, chunk[:end]...)
		p.cr = chunk[end] == '\r'
		chunk = chunk[length:]
		if len(p.line) == 0 {
			if len(p.data) > 0 {
				if err := emit(p.data[:len(p.data)-1], p.event); err != nil {
					return err
				}
			}
			p.data = p.data[:0]
			p.event = ""
			p.size = 0
			continue
		}
		field, value, _ := bytes.Cut(p.line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			p.data = append(p.data, value...)
			p.data = append(p.data, '\n')
		case "event":
			p.event = string(value)
		}
		p.line = p.line[:0]
	}
	return nil
}

func isClaudePluginOpenAI(prepared preparedExecutorCall) bool {
	return prepared.outputFormat == tr.FormatClaude && (prepared.requestedFormat == tr.FormatOpenAIResponse || prepared.requestedFormat == tr.FormatOpenAI)
}

func claudePluginUpstreamError(payload []byte) error {
	status := http.StatusBadGateway
	switch gjson.GetBytes(payload, "error.type").String() {
	case "authentication_error":
		status = http.StatusUnauthorized
	case "permission_error":
		status = http.StatusForbidden
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	case "overloaded_error":
		status = http.StatusServiceUnavailable
	case "invalid_request_error":
		status = http.StatusBadRequest
	}
	return &coreauth.Error{Code: "upstream_error", Message: "Anthropic plugin upstream reported an error", HTTPStatus: status}
}

func (a *executorAdapter) translateClaudePluginStream(ctx context.Context, cancel context.CancelFunc, req pluginapi.ExecutorRequest, prepared preparedExecutorCall, reporter *helps.UsageReporter, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var outcome error
		upstreamClosed := false
		var stopOnce sync.Once
		stop := func() {
			stopOnce.Do(func() {
				cancel()
				if !upstreamClosed {
					a.cancelExecutionAsync(req, pluginapi.ExecutionCancelReasonContextCanceled)
				}
			})
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				outcome = claudePluginConversionError()
				stop()
				_ = sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Err: outcome})
			}
			// 转换结果决定统计终态；清理取消不能抢先记成成功或覆盖协议错误。
			if reporter != nil {
				setPluginUsageProvenance(reporter, &usage)
				if outcome != nil {
					if !usage.PublishFailure(ctx, reporter, outcome) {
						reporter.PublishFailure(ctx, outcome)
					}
				} else {
					usage.Publish(ctx, reporter)
					reporter.EnsurePublished(ctx)
				}
			}
			stop()
		}()
		fail := func(err error) {
			outcome = err
			stop()
			_ = sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Err: err})
		}
		if in == nil {
			fail(claudePluginConversionError())
			return
		}
		state := &claudecommon.PluginResponseState{Chat: prepared.requestedFormat == tr.FormatOpenAI}
		chatFinished := false
		var param any = state
		var parser claudePluginSSE
		first := true
		emit := func(data []byte, event string) error {
			if state.Terminal {
				return nil
			}
			var compact bytes.Buffer
			if !utf8.Valid(data) || json.Compact(&compact, data) != nil {
				return claudePluginConversionError()
			}
			payload := compact.Bytes()
			if event == "error" || gjson.GetBytes(payload, "type").String() == "error" {
				return claudePluginUpstreamError(payload)
			}
			line := append([]byte("data: "), payload...)
			helps.ObservePluginExecutorStreamUsage("claude", line, &usage)
			if reporter != nil {
				reporter.ObserveTimingPayload("claude", line)
				helps.ObservePluginExecutorStreamTTFT("claude", reporter, line)
			}
			frames := a.translateExecutorStreamPayload(ctx, prepared, line, &param)
			if state.Err != nil {
				return claudePluginConversionError()
			}
			if state.Chat && claudePluginHasChatFinish(frames) {
				chatFinished = true
			}
			if state.Terminal && ((!state.Chat && !claudePluginHasResponsesTerminal(frames)) || (state.Chat && !chatFinished)) {
				return claudePluginConversionError()
			}
			for _, frame := range frames {
				if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: frame}) {
					return ctx.Err()
				}
			}
			if state.Terminal {
				return io.EOF
			}
			return nil
		}
		for {
			select {
			case <-ctx.Done():
				outcome = ctx.Err()
				return
			case chunk, ok := <-in:
				if !ok {
					upstreamClosed = true
					if ctx.Err() != nil {
						outcome = ctx.Err()
						return
					}
					if !state.Terminal {
						fail(claudePluginConversionError())
					}
					return
				}
				if chunk.Err != nil {
					fail(chunk.Err)
					return
				}
				if len(chunk.Payload) == 0 {
					continue
				}
				if first {
					if reporter != nil {
						reporter.MarkFirstResponseByte()
					}
					first = false
				}
				if err := parser.feed(chunk.Payload, emit); err != nil {
					if err == io.EOF && state.Terminal {
						return
					}
					fail(err)
					return
				}
				if state.Terminal {
					return
				}
			}
		}
	}()
	return out
}

func claudePluginHasResponsesTerminal(frames [][]byte) bool {
	for _, frame := range frames {
		for _, line := range bytes.Split(frame, []byte("\n")) {
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if !json.Valid(payload) {
				continue
			}
			event := gjson.ParseBytes(payload)
			if (event.Get("type").String() == "response.completed" || event.Get("type").String() == "response.incomplete") && validClaudePluginResponsesJSON([]byte(event.Get("response").Raw)) {
				return true
			}
		}
	}
	return false
}

func validClaudePluginResponsesJSON(body []byte) bool {
	if !json.Valid(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	status := root.Get("status").String()
	return root.Get("object").String() == "response" && strings.TrimSpace(root.Get("id").String()) != "" && root.Get("output").IsArray() && (status == "completed" || status == "incomplete")
}

func validClaudePluginChatJSON(body []byte) bool {
	if !json.Valid(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	return root.Get("object").String() == "chat.completion" && strings.TrimSpace(root.Get("id").String()) != "" && root.Get("choices").IsArray() && root.Get("choices.0.message.role").String() == "assistant" && validChatFinish(root.Get("choices.0.finish_reason").String())
}

func validChatFinish(reason string) bool {
	switch reason {
	case "stop", "length", "tool_calls", "content_filter":
		return true
	}
	return false
}

func claudePluginHasChatFinish(frames [][]byte) bool {
	for _, frame := range frames {
		if !json.Valid(frame) {
			continue
		}
		root := gjson.ParseBytes(frame)
		if root.Get("object").String() == "chat.completion.chunk" && root.Get("id").String() != "" && validChatFinish(root.Get("choices.0.finish_reason").String()) {
			return true
		}
	}
	return false
}

func validClaudePluginOpenAIJSON(prepared preparedExecutorCall, body []byte) bool {
	if prepared.requestedFormat == tr.FormatOpenAI {
		return validClaudePluginChatJSON(body)
	}
	return validClaudePluginResponsesJSON(body)
}
