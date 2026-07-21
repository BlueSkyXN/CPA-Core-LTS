package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageParsersRejectInvalidTokenInputs(t *testing.T) {
	for _, parser := range []struct {
		name  string
		parse func([]byte) usage.Detail
		data  []byte
	}{
		{"openai", ParseOpenAIUsage, []byte(`{"usage":{"input_tokens":"10"}}`)},
		{"claude", ParseClaudeUsage, []byte(`{"usage":{"input_tokens":-1}}`)},
		{"gemini", ParseGeminiUsage, []byte(`{"usageMetadata":{"promptTokenCount":1.5}}`)},
		{"interactions", ParseInteractionsUsage, []byte(`{"usage":{"input_tokens":true}}`)},
	} {
		t.Run(parser.name, func(t *testing.T) {
			if detail := parser.parse(parser.data); detail.InputTokens != 0 {
				t.Fatalf("detail = %+v, want invalid input_tokens rejected", detail)
			}
		})
	}
}

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 12)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.CacheReadTokens != 4 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 4)
	}
	if detail.CacheCreationTokens != 6 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 6)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"service_tier":"default","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.CacheReadTokens != 7 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestParseCodexUsageIncludesCacheWriteTokens(t *testing.T) {
	data := []byte(`{"response":{"service_tier":"priority","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}}`)
	detail, ok := ParseCodexUsage(data)
	if !ok {
		t.Fatal("ParseCodexUsage() ok = false, want true")
	}
	if detail.InputTokens != 100 {
		t.Fatalf("input tokens = %d, want 100", detail.InputTokens)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want 20", detail.OutputTokens)
	}
	if detail.CachedTokens != 30 {
		t.Fatalf("cached tokens = %d, want 30", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 30 {
		t.Fatalf("cache read tokens = %d, want 30", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 40 {
		t.Fatalf("cache creation tokens = %d, want 40", detail.CacheCreationTokens)
	}
	if detail.TotalTokens != 120 {
		t.Fatalf("total tokens = %d, want 120", detail.TotalTokens)
	}
	if detail.ResponseServiceTier != "priority" {
		t.Fatalf("response service tier = %q, want priority", detail.ResponseServiceTier)
	}
}

func TestUsageParsersNormalizeInputTotalsAcrossProviderShapes(t *testing.T) {
	parseCodex := func(data []byte) usage.Detail {
		detail, ok := ParseCodexUsage(data)
		if !ok {
			t.Fatal("ParseCodexUsage() ok = false, want true")
		}
		return detail
	}
	tests := []struct {
		name  string
		parse func([]byte) usage.Detail
		data  []byte
	}{
		{
			name:  "openai input already includes cache categories",
			parse: ParseOpenAIUsage,
			data:  []byte(`{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}`),
		},
		{
			name:  "codex input already includes cache categories",
			parse: parseCodex,
			data:  []byte(`{"response":{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}}`),
		},
		{
			name:  "claude separates normal input and cache categories",
			parse: ParseClaudeUsage,
			data:  []byte(`{"usage":{"input_tokens":30,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`),
		},
		{
			name:  "interactions separates explicit cache categories",
			parse: ParseInteractionsUsage,
			data:  []byte(`{"usage":{"input_tokens":30,"cache_read_tokens":30,"cache_write_tokens":40}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := tt.parse(tt.data)
			if detail.InputTokens != 100 || detail.CacheReadTokens != 30 || detail.CacheCreationTokens != 40 {
				t.Fatalf("detail = %+v, want input=100 cache_read=30 cache_creation=40", detail)
			}
		})
	}

	gemini := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":30}}`))
	if gemini.InputTokens != 100 || gemini.CacheReadTokens != 30 || gemini.CacheCreationTokens != 0 {
		t.Fatalf("gemini detail = %+v, want prompt input total with cached subset", gemini)
	}
	antigravity := ParseAntigravityUsage([]byte(`{"response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":30}}}`))
	if antigravity.InputTokens != 100 || antigravity.CacheReadTokens != 30 || antigravity.CacheCreationTokens != 0 {
		t.Fatalf("antigravity detail = %+v, want prompt input total with cached subset", antigravity)
	}
}

func TestUsageTotalFallbackDoesNotDoubleCountCacheCategories(t *testing.T) {
	detail := normalizeUsageDetailTotal(usage.Detail{
		InputTokens:         100,
		OutputTokens:        20,
		ReasoningTokens:     5,
		CacheReadTokens:     30,
		CacheCreationTokens: 40,
	})
	if detail.TotalTokens != 125 {
		t.Fatalf("total_tokens = %d, want 125 without double-counting input cache categories", detail.TotalTokens)
	}
}

func TestParseOpenAIUsageNormalizesCacheCreationAlias(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cache_creation_tokens":4}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.CacheCreationTokens != 4 {
		t.Fatalf("cache creation tokens = %d, want 4", detail.CacheCreationTokens)
	}
}

func TestParseOpenAIUsagePreservesInputWhenCacheBreakdownExceedsIt(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":6}}}`))
	if detail.InputTokens != 5 || detail.CacheReadTokens != 4 || detail.CacheCreationTokens != 6 {
		t.Fatalf("detail = %+v, want upstream categories preserved", detail)
	}
}

func TestParseOpenAIUsageIgnoresNullUsage(t *testing.T) {
	data := []byte(`{"usage":null}`)
	detail := ParseOpenAIUsage(data)
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero detail", detail)
	}
}

func TestParseOpenAIUsagePreservesResponseTierWithoutUsage(t *testing.T) {
	t.Parallel()

	detail := ParseOpenAIUsage([]byte(`{"service_tier":"default"}`))
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestParseCodexUsagePreservesResponseTierWithoutUsage(t *testing.T) {
	t.Parallel()

	detail, ok := ParseCodexUsage([]byte(`{"response":{"service_tier":"default"}}`))
	if !ok || detail.ResponseServiceTier != "default" {
		t.Fatalf("ParseCodexUsage() = (%+v, %v), want response tier default", detail, ok)
	}
}

func TestParseOpenAIStreamUsageIgnoresNullUsage(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"usage":null}`)
	if detail, ok := ParseOpenAIStreamUsage(line); ok {
		t.Fatalf("ParseOpenAIStreamUsage() = (%+v, true), want false for null usage", detail)
	}
}

func TestParseOpenAIStreamUsageResponsesFields(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","service_tier":"flex","choices":[],"usage":{"input_tokens":8,"output_tokens":5,"total_tokens":13,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("ParseOpenAIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 8 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 8)
	}
	if detail.OutputTokens != 5 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 5)
	}
	if detail.TotalTokens != 13 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 13)
	}
	if detail.CachedTokens != 3 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 3)
	}
	if detail.CacheReadTokens != 3 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 3)
	}
	if detail.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 2)
	}
	if detail.ResponseServiceTier != "flex" {
		t.Fatalf("response service tier = %q, want flex", detail.ResponseServiceTier)
	}
}

func TestStreamUsageBufferKeepsLastUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{}, true)
	buffer.Observe(usage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, false)
	buffer.Observe(usage.Detail{InputTokens: 39320, OutputTokens: 26, TotalTokens: 39346, CachedTokens: 33280}, true)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("buffer detail ok = false, want true")
	}
	if detail.InputTokens != 39320 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 39320)
	}
	if detail.OutputTokens != 26 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 26)
	}
	if detail.TotalTokens != 39346 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 39346)
	}
	if detail.CachedTokens != 33280 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 33280)
	}
}

func TestStreamUsageBufferPreservesTierAcrossChunks(t *testing.T) {
	t.Parallel()

	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("Detail() ok = false, want true")
	}
	if detail.InputTokens != 1 || detail.OutputTokens != 1 || detail.ResponseServiceTier != "default" {
		t.Fatalf("detail = %+v, want usage with response tier default", detail)
	}
}

func TestStreamUsageBufferObserveOpenAIStreamStateTransitions(t *testing.T) {
	t.Parallel()

	t.Run("same chunk", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"flex","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.InputTokens != 2 || detail.ResponseServiceTier != "flex" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("usage before tier", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
		detail, ok := buffer.Detail()
		if !ok || detail.InputTokens != 2 || detail.ResponseServiceTier != "default" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("final usage tier overrides early tier", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"priority","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.ResponseServiceTier != "priority" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("irrelevant and invalid chunks do not change state", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"content":"the word \"usage\" appears here"}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":`))
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":null}`))
		if detail, ok := buffer.Detail(); ok {
			t.Fatalf("detail = %+v ok=true, want empty buffer", detail)
		}
	})

	t.Run("zero token usage is retained", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
		if _, ok := buffer.Detail(); !ok {
			t.Fatal("Detail() ok = false, want true")
		}
	})
}

func TestStreamUsageBufferPreservesOnlyZeroUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{}, true)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("buffer detail ok = false, want true")
	}
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero detail", detail)
	}
}

func TestParseClaudeUsageIncludesCacheTokensInTotal(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_read_input_tokens":7,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 22606 {
		t.Fatalf("input tokens = %d, want 22606", detail.InputTokens)
	}
	if detail.OutputTokens != 253 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 253)
	}
	if detail.CacheReadTokens != 7 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 7)
	}
	if detail.CacheCreationTokens != 19514 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 19514)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.TotalTokens != 22859 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22859)
	}
}

func TestParseClaudeUsageKeepsCacheCreationSeparateFromCachedTokens(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.CachedTokens != 0 {
		t.Fatalf("cached tokens = %d, want 0 for creation-only usage", detail.CachedTokens)
	}
	if detail.CacheCreationTokens != 19514 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 19514)
	}
	if detail.TotalTokens != 22852 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22852)
	}
	if detail.InputTokens != 22599 {
		t.Fatalf("input tokens = %d, want 22599", detail.InputTokens)
	}
}

func TestParseGeminiUsageNormalizesCachedContent(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":12}}`))
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want 4", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 4 {
		t.Fatalf("cache read tokens = %d, want 4", detail.CacheReadTokens)
	}
}

func TestParseGeminiUsageKeepsPromptInputSeparateFromCachedSubset(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":5,"cachedContentTokenCount":6,"totalTokenCount":5}}`))
	if detail.InputTokens != 5 || detail.CacheReadTokens != 6 {
		t.Fatalf("detail = %+v, want upstream prompt and cache categories preserved", detail)
	}
}

func TestParseAntigravityUsageKeepsPromptInputTotal(t *testing.T) {
	detail := ParseAntigravityUsage([]byte(`{"response":{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":12}}}`))
	if detail.InputTokens != 10 || detail.CacheReadTokens != 4 || detail.TotalTokens != 12 {
		t.Fatalf("detail = %+v, want prompt input total with cached subset", detail)
	}
}

func TestParseInteractionsUsage(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"output_tokens":4,"reasoning_tokens":5,"cached_tokens":2}}`))
	if detail.InputTokens != 3 {
		t.Fatalf("input tokens = %d, want 3", detail.InputTokens)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want 4", detail.OutputTokens)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want 5", detail.ReasoningTokens)
	}
	if detail.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want 12", detail.TotalTokens)
	}
	if detail.CachedTokens != 2 {
		t.Fatalf("cached tokens = %d, want 2", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 2 {
		t.Fatalf("cache read tokens = %d, want 2", detail.CacheReadTokens)
	}
}

func TestParseInteractionsUsageNormalizesCacheWriteAlias(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"cache_write_tokens":2}}`))
	if detail.CacheCreationTokens != 2 {
		t.Fatalf("cache creation tokens = %d, want 2", detail.CacheCreationTokens)
	}
	if detail.InputTokens != 5 || detail.TotalTokens != 5 {
		t.Fatalf("detail = %+v, want external cache write added to input total", detail)
	}
}

func TestParseInteractionsUsageKeepsExplicitCacheReadSeparateFromInput(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"cache_read_tokens":2}}`))
	if detail.CacheReadTokens != 2 {
		t.Fatalf("cache read tokens = %d, want 2", detail.CacheReadTokens)
	}
	if detail.InputTokens != 5 || detail.TotalTokens != 5 {
		t.Fatalf("detail = %+v, want explicit cache read added to input total", detail)
	}
}

func TestParseInteractionsUsageKeepsCachedTokensWithinInput(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"cached_tokens":4}}`))
	if detail.InputTokens != 3 || detail.CacheReadTokens != 4 || detail.TotalTokens != 3 {
		t.Fatalf("detail = %+v, want cached_tokens treated as an input subset", detail)
	}
}

func TestParseInteractionsStreamUsage(t *testing.T) {
	detail, ok := ParseInteractionsStreamUsage([]byte(`{"type":"interaction.completed","interaction":{"usage":{"input_tokens":2,"output_tokens":6,"total_tokens":8}}}`))
	if !ok {
		t.Fatal("ParseInteractionsStreamUsage() ok = false, want true")
	}
	if detail.TotalTokens != 8 {
		t.Fatalf("total tokens = %d, want 8", detail.TotalTokens)
	}
}

func TestParseInteractionsStreamUsageOfficialMetadata(t *testing.T) {
	detail, ok := ParseInteractionsStreamUsage([]byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`))
	if !ok {
		t.Fatal("ParseInteractionsStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 2 {
		t.Fatalf("input tokens = %d, want 2", detail.InputTokens)
	}
	if detail.OutputTokens != 6 {
		t.Fatalf("output tokens = %d, want 6", detail.OutputTokens)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want 3", detail.ReasoningTokens)
	}
	if detail.CachedTokens != 1 {
		t.Fatalf("cached tokens = %d, want 1", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 1 {
		t.Fatalf("cache read tokens = %d, want 1", detail.CacheReadTokens)
	}
	if detail.TotalTokens != 11 {
		t.Fatalf("total tokens = %d, want 11", detail.TotalTokens)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestUsageReporterTrackHTTPClientStartsTTFTBeforeRoundTrip(t *testing.T) {
	delay := 40 * time.Millisecond
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(delay)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1/chat/completions", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	if _, errRead := io.ReadAll(resp.Body); errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close error = %v", errClose)
	}
	if got := reporter.ttftDuration(); got < delay {
		t.Fatalf("ttft = %v, want >= %v", got, delay)
	}
}

func TestUsageReporterBuildRecordIncludesRequestedModelAlias(t *testing.T) {
	ctx := usage.WithRequestedModelAlias(context.Background(), "client-gpt")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", record.Model, "gpt-5.4")
	}
	if record.Alias != "client-gpt" {
		t.Fatalf("alias = %q, want %q", record.Alias, "client-gpt")
	}
}

func TestNewExecutorUsageReporterIncludesExecutorType(t *testing.T) {
	reporter := NewExecutorUsageReporter(context.Background(), &TestUsageExecutor{}, "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Provider != "test-provider" {
		t.Fatalf("provider = %q, want %q", record.Provider, "test-provider")
	}
	if record.ExecutorType != "TestUsageExecutor" {
		t.Fatalf("executor type = %q, want %q", record.ExecutorType, "TestUsageExecutor")
	}
}

func TestUsageReporterBuildRecordIncludesReasoningEffort(t *testing.T) {
	ctx := usage.WithReasoningEffort(context.Background(), "medium")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", record.ReasoningEffort, "medium")
	}
}

func TestUsageReporterBuildRecordIncludesServiceTier(t *testing.T) {
	ctx := usage.WithServiceTier(context.Background(), "auto")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3, ResponseServiceTier: "default"}, false)
	if record.ServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", record.ServiceTier, "auto")
	}
	if record.RequestServiceTier != "auto" {
		t.Fatalf("request service tier = %q, want %q", record.RequestServiceTier, "auto")
	}
	if record.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", record.ResponseServiceTier)
	}
	if record.EffectiveServiceTier != "standard" {
		t.Fatalf("effective service tier = %q, want standard", record.EffectiveServiceTier)
	}
}

func TestUsageReporterResolvesEffectiveServiceTierFromFinalOutboundPayload(t *testing.T) {
	tests := []struct {
		name     string
		outbound string
		response string
		want     string
	}{
		{name: "non stream response wins", outbound: `{"service_tier":"priority"}`, response: "standard", want: "standard"},
		{name: "stream response wins", outbound: `{"service_tier":"standard"}`, response: "fast", want: "priority"},
		{name: "missing response uses priority outbound", outbound: `{"service_tier":"fast"}`, want: "priority"},
		{name: "missing response uses standard outbound", outbound: `{"service_tier":"default"}`, want: "standard"},
		{name: "unknown response cannot fall back", outbound: `{"service_tier":"priority"}`, response: "flex", want: ""},
		{name: "outbound omits tier", outbound: `{"model":"gpt-5.6"}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
			reporter.SetOutboundServiceTier([]byte(tt.outbound))
			record := reporter.buildRecord(usage.Detail{TotalTokens: 1, ResponseServiceTier: tt.response}, false)
			if record.EffectiveServiceTier != tt.want {
				t.Fatalf("effective service tier = %q, want %q", record.EffectiveServiceTier, tt.want)
			}
		})
	}
}

func TestUsageReporterResolvesBillingBasisFromAuthEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		auth     *cliproxyauth.Auth
		want     string
	}{
		{
			name:     "Codex OAuth uses credits",
			provider: "codex",
			auth: &cliproxyauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"auth_kind": "oauth"},
			},
			want: usage.BillingBasisChatGPTCredits,
		},
		{
			name:     "Codex API key without explicit endpoint uses USD",
			provider: "codex",
			auth: &cliproxyauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "test-key"},
			},
			want: usage.BillingBasisAPITokenUSD,
		},
		{
			name:     "Codex API key custom endpoint fails closed",
			provider: "codex",
			auth: &cliproxyauth.Auth{
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": "https://proxy.example.com/codex",
				},
			},
			want: usage.BillingBasisUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), tt.provider, "gpt-5.6", tt.auth)
			record := reporter.buildRecord(usage.Detail{TotalTokens: 1}, false)
			if record.BillingBasis != tt.want {
				t.Fatalf("billing basis = %q, want %q", record.BillingBasis, tt.want)
			}
		})
	}
}

func TestUsageReporterBuildRecordDefaultsGenerateTrue(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !usage.GenerateEnabled(record.Generate) {
		t.Fatalf("generate = %v, want true", usage.GenerateEnabled(record.Generate))
	}
}

func TestUsageReporterBuildRecordIncludesGenerateFalse(t *testing.T) {
	ctx := usage.WithGenerate(context.Background(), false)
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if usage.GenerateEnabled(record.Generate) {
		t.Fatalf("generate = %v, want false", usage.GenerateEnabled(record.Generate))
	}
}

func TestUsageReporterSetTranslatedReasoningEffortPreservesClientServiceTier(t *testing.T) {
	ctx := usage.WithServiceTier(context.Background(), "auto")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	reporter.SetTranslatedReasoningEffort([]byte(`{"service_tier":"priority"}`), "openai")

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.ServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", record.ServiceTier, "auto")
	}
}

func TestUsageReporterBuildAdditionalModelRecordSkipsZeroTokens(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "codex",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}

	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{}); ok {
		t.Fatalf("expected all-zero token usage to be skipped")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{InputTokens: 2}); !ok {
		t.Fatalf("expected non-zero input token usage to be recorded")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{CachedTokens: 2}); !ok {
		t.Fatalf("expected non-zero cached token usage to be recorded")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type TestUsageExecutor struct{}

func (TestUsageExecutor) Identifier() string {
	return "test-provider"
}
