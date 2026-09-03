package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	data := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 6 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 6)
	}
	if detail.TotalTokens != 16 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 16)
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
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 0 || detail.TokenBreakdown.Output.NonReasoningTokens != 1 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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
	if detail.TokenBreakdown.Input.UncachedTokens != 3 || detail.TokenBreakdown.Output.NonReasoningTokens != 11 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseOpenAIUsageTotalOnlyIsUnclassified(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"total_tokens":42}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityUnclassified ||
		detail.TotalTokens != 42 || detail.TokenBreakdown.UnclassifiedTokens != 42 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestParseOpenAIUsagePartialBucketsPreserveKnownTokens(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":10,"total_tokens":15}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityUnclassified ||
		detail.TokenBreakdown.Input.TotalTokens != 10 || detail.TokenBreakdown.UnclassifiedTokens != 5 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestParseOpenAIUsageExplicitZeroBucketsRemainInconsistent(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":42}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityInconsistent {
		t.Fatalf("detail = %+v", detail)
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
	if detail.TokenBreakdown.Input.UncachedTokens != 30 || detail.TokenBreakdown.Input.CacheWriteTokens != 40 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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
	}, "openai", "")
	if detail.TotalTokens != 120 {
		t.Fatalf("total_tokens = %d, want 120 without double-counting cache or reasoning subsets", detail.TotalTokens)
	}
}

func TestParseOpenAIUsageFallbackDoesNotDoubleCountReasoningSubset(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":100,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5}}}`))
	if detail.TotalTokens != 120 {
		t.Fatalf("total_tokens = %d, want 120 because OpenAI output_tokens includes reasoning_tokens", detail.TotalTokens)
	}
}

func TestParseCodexUsagePreservesReasoningOnlyDetail(t *testing.T) {
	detail, ok := ParseCodexUsage([]byte(`{"response":{"usage":{"output_tokens_details":{"reasoning_tokens":7}}}}`))
	if !ok {
		t.Fatal("ParseCodexUsage returned ok=false for reasoning-only usage")
	}
	if detail.ReasoningTokens != 7 || detail.TotalTokens != 7 {
		t.Fatalf("detail = %+v, want reasoning_tokens=7 and total_tokens=7", detail)
	}
}

func TestParseCodexUsageRejectsNegativeReasoningOnlyFallback(t *testing.T) {
	detail, ok := ParseCodexUsage([]byte(`{"response":{"usage":{"output_tokens_details":{"reasoning_tokens":-7}}}}`))
	if !ok {
		t.Fatal("ParseCodexUsage returned ok=false for an explicit usage object")
	}
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want a zero token vector for negative reasoning", detail)
	}
}

func TestUsageTotalFallbackFailsClosedOnOverflow(t *testing.T) {
	detail := normalizeUsageDetailTotal(usage.Detail{
		InputTokens:  math.MaxInt64,
		OutputTokens: 1,
	}, "openai", "")
	if detail.TotalTokens != 0 {
		t.Fatalf("total_tokens = %d, want 0 when the fallback sum is not representable", detail.TotalTokens)
	}
}

func TestParseOpenAIUsageNormalizesCacheCreationAlias(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cache_creation_tokens":4}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.CacheCreationTokens != 4 {
		t.Fatalf("cache creation tokens = %d, want 4", detail.CacheCreationTokens)
	}
}

func TestParseOpenAIUsageDropsCacheBreakdownThatExceedsInput(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":6}}}`))
	if detail.InputTokens != 5 || detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 {
		t.Fatalf("detail = %+v, want input preserved and impossible cache breakdown dropped", detail)
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
	if detail.TokenBreakdown.Input.TotalTokens != 22606 || detail.TokenBreakdown.Input.UncachedTokens != 3085 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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

func TestParseClaudeUsageFailsClosedWhenCanonicalInputOverflows(t *testing.T) {
	detail := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":9223372036854775807,"output_tokens":1,"cache_read_input_tokens":1}}`))
	if detail.InputTokens != math.MaxInt64 {
		t.Fatalf("input_tokens = %d, want original upstream input %d", detail.InputTokens, int64(math.MaxInt64))
	}
	if detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 {
		t.Fatalf("cache categories = %+v, want cleared when canonical input cannot be represented", detail)
	}
	if detail.TotalTokens != 0 {
		t.Fatalf("total_tokens = %d, want 0 when the canonical total is not representable", detail.TotalTokens)
	}
	if normalized := normalizeUsageDetailTotal(detail, "claude", ""); normalized.TotalTokens != 0 {
		t.Fatalf("reporter fallback total_tokens = %d, want 0 rather than a wrapped value", normalized.TotalTokens)
	}
}

func TestParseClaudeUsagePreservesThinkingTokensAsReasoningSubset(t *testing.T) {
	// Sanitized shape from local Anthropic request logs under ~/.config/cpa/logs.
	data := []byte(`{"usage":{"input_tokens":2,"cache_creation_input_tokens":831,"cache_read_input_tokens":44225,"output_tokens":244,"output_tokens_details":{"thinking_tokens":40}}}`)
	detail := ParseClaudeUsage(data)
	if detail.OutputTokens != 244 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 244)
	}
	if detail.ReasoningTokens != 40 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 40)
	}
	if detail.TotalTokens != 45302 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 45302)
	}
	if !detail.TokenBreakdown.Valid() ||
		detail.TokenBreakdown.Output.TotalTokens != 244 ||
		detail.TokenBreakdown.Output.NonReasoningTokens != 204 ||
		detail.TokenBreakdown.Output.ReasoningTokens != 40 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeStreamUsagePreservesThinkingTokensAsReasoningSubset(t *testing.T) {
	line := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2,"cache_creation_input_tokens":831,"cache_read_input_tokens":44225,"output_tokens":244,"output_tokens_details":{"thinking_tokens":40}}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage to parse")
	}
	if detail.OutputTokens != 244 || detail.ReasoningTokens != 40 || detail.TotalTokens != 45302 {
		t.Fatalf("stream usage detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Output.NonReasoningTokens != 204 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeUsageFallsBackToTopLevelThinkingTokens(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3,"output_tokens":10,"thinking_tokens":4}}`)
	detail := ParseClaudeUsage(data)
	if detail.OutputTokens != 10 || detail.ReasoningTokens != 4 || detail.TotalTokens != 13 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.TokenBreakdown.Output.NonReasoningTokens != 6 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeUsageRejectsReasoningSubsetLargerThanOutput(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3,"output_tokens":10,"output_tokens_details":{"thinking_tokens":11}}}`)
	detail := ParseClaudeUsage(data)
	if detail.OutputTokens != 10 || detail.ReasoningTokens != 0 || detail.TotalTokens != 13 {
		t.Fatalf("detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Output.NonReasoningTokens != 10 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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
	if detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TokenBreakdown.TotalTokens != 12 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseGeminiUsageIncludesToolUsePromptTokens(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"toolUsePromptTokenCount":5,"totalTokenCount":20}}`))
	if detail.InputTokens != 15 || detail.TotalTokens != 20 {
		t.Fatalf("detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Input.UncachedTokens != 15 || detail.TokenBreakdown.Output.ReasoningTokens != 3 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseGeminiStreamUsageSkipsZeroPlaceholder(t *testing.T) {
	lines := [][]byte{
		[]byte(`data: {"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":0}}`),
		[]byte(`data: {"usageMetadata":{"promptTokenCount":17984,"candidatesTokenCount":2668,"thoughtsTokenCount":1028,"totalTokenCount":21680}}`),
	}

	accepted := make([]usage.Detail, 0, len(lines))
	for _, line := range lines {
		detail, ok := ParseGeminiStreamUsage(line)
		if ok {
			accepted = append(accepted, detail)
		}
	}

	if len(accepted) != 1 {
		t.Fatalf("accepted usage count = %d, want 1", len(accepted))
	}
	detail := accepted[0]
	if detail.InputTokens != 17984 || detail.OutputTokens != 2668 || detail.ReasoningTokens != 1028 || detail.TotalTokens != 21680 {
		t.Fatalf("accepted usage detail = %+v", detail)
	}
}

func TestParseGeminiUsageRejectsInvalidToolUseSums(t *testing.T) {
	tests := map[string]string{
		"negative": `{"usageMetadata":{"promptTokenCount":10,"toolUsePromptTokenCount":-1,"totalTokenCount":10}}`,
		"overflow": `{"usageMetadata":{"promptTokenCount":9223372036854775807,"toolUsePromptTokenCount":1,"totalTokenCount":9223372036854775807}}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			detail := ParseGeminiUsage([]byte(payload))
			if detail.InputTokens < 0 || !detail.TokenBreakdown.Valid() ||
				detail.TokenBreakdown.Quality != usage.TokenAccountingQualityInconsistent {
				t.Fatalf("detail = %+v", detail)
			}
		})
	}
}

func TestParseGeminiUsageDropsCachedSubsetThatExceedsPromptInput(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":5,"cachedContentTokenCount":6,"totalTokenCount":5}}`))
	if detail.InputTokens != 5 || detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 {
		t.Fatalf("detail = %+v, want prompt input preserved and impossible cache breakdown dropped", detail)
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
	if detail.TokenBreakdown.Input.UncachedTokens != 1 || detail.TokenBreakdown.Output.TotalTokens != 9 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestNormalizeUsageDetailTotalDoesNotDoubleCountReasoning(t *testing.T) {
	detail := normalizeUsageDetailTotal(usage.Detail{
		InputTokens:     100,
		OutputTokens:    30,
		ReasoningTokens: 12,
	}, "openai", "")
	if detail.TotalTokens != 130 {
		t.Fatalf("total tokens = %d, want 130", detail.TotalTokens)
	}
	if detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete || detail.TokenBreakdown.Output.ReasoningTokens != 12 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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

func TestParseInteractionsUsageDropsCachedTokensThatExceedInput(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"cached_tokens":4}}`))
	if detail.InputTokens != 3 || detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 || detail.TotalTokens != 3 {
		t.Fatalf("detail = %+v, want input preserved and impossible cache breakdown dropped", detail)
	}
}

func TestParseInteractionsUsageFailsClosedWhenCanonicalInputOverflows(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":9223372036854775807,"output_tokens":1,"cache_write_tokens":1}}`))
	if detail.InputTokens != math.MaxInt64 {
		t.Fatalf("input_tokens = %d, want original upstream input %d", detail.InputTokens, int64(math.MaxInt64))
	}
	if detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 {
		t.Fatalf("cache categories = %+v, want cleared when canonical input cannot be represented", detail)
	}
	if detail.TotalTokens != 0 {
		t.Fatalf("total_tokens = %d, want 0 when the canonical total is not representable", detail.TotalTokens)
	}
	if normalized := normalizeUsageDetailTotal(detail, "interactions", ""); normalized.TotalTokens != 0 {
		t.Fatalf("reporter fallback total_tokens = %d, want 0 rather than a wrapped value", normalized.TotalTokens)
	}
}

func TestParseInteractionsUsageIncludesToolUseTokens(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_tool_use_tokens":4,"total_tokens":15}}`))
	if detail.InputTokens != 6 || detail.OutputTokens != 6 || detail.ReasoningTokens != 3 || detail.TotalTokens != 15 {
		t.Fatalf("detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TokenBreakdown.Output.TotalTokens != 9 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
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

func TestUsageReporterTrackHTTPClientStartsTTFBBeforeRoundTrip(t *testing.T) {
	delay := 40 * time.Millisecond
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
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

	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/chat/completions", strings.NewReader("{}"))
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
	if got := reporter.timingSnapshot().TTFB; got < delay {
		t.Fatalf("ttfb = %v, want >= %v", got, delay)
	}
}

func TestUsageReporterTrackedStreamCapturesSemanticTiming(t *testing.T) {
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.6", nil)
	reporter.EnableSemanticTiming("openai-response")
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n" +
						"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n",
				)),
				Request: req,
			}, nil
		}),
	})
	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader("{}"))
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
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.TimingVersion != UsageTimingVersionV1 || record.TTFB <= 0 || record.TTFT <= 0 || record.TTFA <= 0 {
		t.Fatalf("record timing = version:%d ttfb:%v ttft:%v ttfa:%v, want all populated", record.TimingVersion, record.TTFB, record.TTFT, record.TTFA)
	}
	if !cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatal("HTTP RoundTrip did not mark an upstream attempt")
	}
}

func TestUsageReporterCanonicalTTFTDoesNotUseEffectiveAssistantToken(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	reporter.EnableSemanticTiming("openai-response")
	reporter.StartResponseTiming()
	time.Sleep(time.Millisecond)

	payload := []byte(`{"type":"response.output_text.delta","delta":"answer"}`)
	reporter.MarkFirstResponseByte()
	reporter.ObserveTimingPayload("openai-response", payload)
	ObserveResponsesTokenEvent(reporter, payload)

	if !reporter.IsTTFTSet() {
		t.Fatal("effective-token compatibility timing was not recorded")
	}
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.TTFT != 0 {
		t.Fatalf("canonical reasoning TTFT = %v, want missing for assistant-only stream", record.TTFT)
	}
	if record.TTFA <= 0 {
		t.Fatalf("canonical assistant TTFA = %v, want positive", record.TTFA)
	}
}

func TestUsageReporterTrackHTTPClientRoundTripOnly_DoesNotTriggerOnBodyRead(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	client := reporter.TrackHTTPClientRoundTripOnly(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(10 * time.Millisecond)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\"}\n\n")),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close error = %v", errClose)
	}

	// 1. Plain body reading must NOT set TTFT
	if reporter.IsTTFTSet() {
		t.Fatalf("TrackHTTPClientRoundTripOnly must not set TTFT on plain body read")
	}

	// 2. Observing metadata event records fallback, but does NOT set effective TTFT
	ObserveResponsesTokenEvent(reporter, bodyBytes)
	if reporter.IsTTFTSet() {
		t.Fatalf("Observing metadata event must not set effective TTFT")
	}
	if reporter.ttftDuration() <= 0 {
		t.Fatalf("Fallback TTFT should be recorded and > 0, got %v", reporter.ttftDuration())
	}

	// 3. Observing substantive token event sets effective TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if !reporter.IsTTFTSet() {
		t.Fatalf("Observing token event must set effective TTFT")
	}
}

func TestUsageReporterTrackHTTPClientRoundTripOnly_ErrorResponseRecordsFirstPacketFallback(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	client := reporter.TrackHTTPClientRoundTripOnly(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit"}}`)),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	_, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	_ = resp.Body.Close()

	if reporter.IsTTFTSet() {
		t.Fatalf("error response read must not set substantive token TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("error response read must record first packet set fallback")
	}
}

func TestUsageReporterObserveTokenEvent_FastPathNonTokenAndToken(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	reporter.StartResponseTTFT()

	// 1. Initial state
	if reporter.IsTTFTSet() {
		t.Fatalf("expected IsTTFTSet() == false initially")
	}

	// 2. First non-token event records firstPacketDuration, but does not mark TTFT set
	reporter.ObserveTokenEvent(false)
	if reporter.IsTTFTSet() {
		t.Fatalf("ObserveTokenEvent(false) must not set TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("expected IsFirstPacketSet() == true")
	}
	firstPacketDuration := reporter.firstPacketDuration

	// 3. Subsequent non-token event is a fast-path return and does not alter firstPacketDuration
	reporter.ObserveTokenEvent(false)
	if reporter.firstPacketDuration != firstPacketDuration {
		t.Fatalf("subsequent ObserveTokenEvent(false) must preserve original firstPacketDuration")
	}

	// 4. Substantive token event sets effective TTFT
	reporter.ObserveTokenEvent(true)
	if !reporter.IsTTFTSet() {
		t.Fatalf("ObserveTokenEvent(true) must set IsTTFTSet() == true")
	}
	tokenTTFT := reporter.ttft

	// 5. Subsequent token event is a fast-path return and does not alter TTFT
	reporter.ObserveTokenEvent(true)
	if reporter.ttft != tokenTTFT {
		t.Fatalf("subsequent ObserveTokenEvent(true) must not alter already recorded TTFT")
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

func TestUsageReporterBuildRecordVersionsUnobservedTiming(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.TimingVersion != UsageTimingVersionV1 {
		t.Fatalf("record timing version = %d, want %d", record.TimingVersion, UsageTimingVersionV1)
	}
	if record.TTFB != 0 || record.TTFT != 0 || record.TTFA != 0 {
		t.Fatalf("unobserved timing = ttfb:%v ttft:%v ttfa:%v, want all missing", record.TTFB, record.TTFT, record.TTFA)
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
		name          string
		outbound      string
		response      string
		wantOutbound  string
		wantEffective string
	}{
		{name: "non stream response wins", outbound: `{"service_tier":" priority "}`, response: "default", wantOutbound: "priority", wantEffective: "standard"},
		{name: "stream response wins", outbound: `{"service_tier":"standard"}`, response: "fast", wantOutbound: "standard", wantEffective: "priority"},
		{name: "missing response uses priority outbound", outbound: `{"service_tier":"fast"}`, wantOutbound: "fast", wantEffective: "priority"},
		{name: "missing response uses standard outbound", outbound: `{"service_tier":"default"}`, wantOutbound: "default", wantEffective: "standard"},
		{name: "unknown response cannot fall back", outbound: `{"service_tier":"priority"}`, response: "flex", wantOutbound: "priority", wantEffective: ""},
		{name: "raw unknown outbound is retained", outbound: `{"service_tier":" Scale "}`, wantOutbound: "Scale", wantEffective: ""},
		{name: "outbound omits tier", outbound: `{"model":"gpt-5.6"}`, wantOutbound: "", wantEffective: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
			reporter.SetOutboundServiceTier([]byte(tt.outbound))
			record := reporter.buildRecord(usage.Detail{TotalTokens: 1, ResponseServiceTier: tt.response}, false)
			if record.OutboundServiceTier != tt.wantOutbound {
				t.Fatalf("outbound service tier = %q, want %q", record.OutboundServiceTier, tt.wantOutbound)
			}
			if record.EffectiveServiceTier != tt.wantEffective {
				t.Fatalf("effective service tier = %q, want %q", record.EffectiveServiceTier, tt.wantEffective)
			}
		})
	}
}

func TestUsageReporterKeepsOutboundServiceTierAttemptLocal(t *testing.T) {
	priorityReporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", &cliproxyauth.Auth{ID: "auth-priority", Index: "1"})
	standardReporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", &cliproxyauth.Auth{ID: "auth-standard", Index: "2"})
	priorityReporter.SetOutboundServiceTier([]byte(`{"service_tier":"priority"}`))
	standardReporter.SetOutboundServiceTier([]byte(`{"service_tier":"default"}`))

	priorityRecord := priorityReporter.buildRecord(usage.Detail{TotalTokens: 1}, false)
	standardRecord := standardReporter.buildRecord(usage.Detail{TotalTokens: 1}, false)
	if priorityRecord.AuthIndex != "1" || priorityRecord.OutboundServiceTier != "priority" || priorityRecord.EffectiveServiceTier != "priority" {
		t.Fatalf("priority attempt record = %+v, want auth_index=1 outbound=priority effective=priority", priorityRecord)
	}
	if standardRecord.AuthIndex != "2" || standardRecord.OutboundServiceTier != "default" || standardRecord.EffectiveServiceTier != "standard" {
		t.Fatalf("standard attempt record = %+v, want auth_index=2 outbound=default effective=standard", standardRecord)
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

func TestUsageReporterBuildRecordDefaultsStreamFalse(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Stream {
		t.Fatalf("stream = %v, want false", record.Stream)
	}
}

func TestUsageReporterBuildRecordIncludesStreamTrue(t *testing.T) {
	ctx := usage.WithStream(context.Background(), true)
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !record.Stream {
		t.Fatalf("stream = %v, want true", record.Stream)
	}
}

func TestUsageReporterSetStream(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)
	reporter.SetStream(true)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !record.Stream {
		t.Fatalf("stream = %v, want true", record.Stream)
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

type usageResponseBodyError struct {
	status  int
	message string
	body    []byte
}

func (e usageResponseBodyError) Error() string {
	return e.message
}

func (e usageResponseBodyError) StatusCode() int {
	return e.status
}

func (e usageResponseBodyError) ResponseBody() []byte {
	return e.body
}

func TestFailFromErrorsPrefersResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "original response body", body: []byte(" \n{\"error\":\"upstream rejected request\"}\r\n")},
		{name: "empty response body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errExecute := fmt.Errorf("execute failed: %w", usageResponseBodyError{
				status:  http.StatusUnauthorized,
				message: "generic upstream error",
				body:    tc.body,
			})
			failure := failFromErrors(errExecute)
			wantBody := errExecute.Error()
			if len(tc.body) > 0 {
				wantBody = string(tc.body)
			}
			if failure.StatusCode != http.StatusUnauthorized || failure.Body != wantBody {
				t.Fatalf("failure = %#v, want status %d body %q", failure, http.StatusUnauthorized, wantBody)
			}
		})
	}
}

func TestFailFromErrorsMapsContextStatuses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "canceled", err: context.Canceled, want: clienterror.StatusClientClosedRequest},
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{
			name: "url error wraps canceled",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
			want: clienterror.StatusClientClosedRequest,
		},
		{name: "plain error", err: errors.New("boom"), want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fail := failFromErrors(tc.err)
			if fail.StatusCode != tc.want {
				t.Fatalf("StatusCode = %d, want %d; body=%q", fail.StatusCode, tc.want, fail.Body)
			}
			if strings.TrimSpace(fail.Body) == "" {
				t.Fatalf("expected non-empty failure body")
			}
		})
	}

	if fail := failFromErrors(nil, nil); fail.StatusCode != 0 || fail.Body != "" {
		t.Fatalf("failFromErrors(nil) = %+v, want empty failure", fail)
	}
}

func TestStreamUsageBufferPublishFailure(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, true)

	reporter := &UsageReporter{
		provider: "openai",
		model:    "gpt-5.4",
	}

	record := reporter.buildRecord(buffer.detail, true, failFromErrors(context.Canceled))
	if !record.Failed {
		t.Fatal("expected record to be marked failed")
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.TotalTokens != 15 {
		t.Fatalf("Detail.TotalTokens = %d, want 15", record.Detail.TotalTokens)
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
