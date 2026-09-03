package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:        "test-key",
		Model:         "gpt-5.4",
		RequestedAt:   time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:       1500 * time.Millisecond,
		TimingVersion: 1,
		TTFB:          450 * time.Millisecond,
		TTFT:          700 * time.Millisecond,
		TTFA:          900 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
	if details[0].TTFBMs != 450 {
		t.Fatalf("ttfb_ms = %d, want 450", details[0].TTFBMs)
	}
	if details[0].TTFTMs != 700 || details[0].TTFAMs != 900 || details[0].TimingVersion != 1 {
		t.Fatalf("semantic timing = version:%d ttft:%d ttfa:%d, want 1/700/900", details[0].TimingVersion, details[0].TTFTMs, details[0].TTFAMs)
	}
	if details[0].TTFRMs != 700 {
		t.Fatalf("ttfr_ms = %d, want 700 (reasoning-only)", details[0].TTFRMs)
	}
	// MarshalJSON should re-compute "ttft_ms" to the backward-compat value.
	encoded, err := json.Marshal(details[0])
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"ttft_ms":700`)) {
		t.Fatalf("backward-compat ttft_ms missing in marshalled detail: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"ttfr_ms":700`)) {
		t.Fatalf("canonical ttfr_ms missing in marshalled detail: %s", encoded)
	}
}

func TestRequestDetailTimingPresencePreservesMeasuredZeroAndMissingFields(t *testing.T) {
	// Pre-split v3 export: has ttft_ms but no ttfr_ms. UnmarshalJSON should
	// migrate ttft_ms → TTFRMs so the canonical internal state is populated.
	data := []byte(`{"timestamp":"2026-03-20T12:00:00Z","latency_ms":10,"timing_version":1,"ttfb_ms":0,"ttft_ms":0,"tokens":{"input_tokens":1,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":2},"failed":false,"generate":true}`)
	var detail RequestDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("unmarshal explicit zero timing detail: %v", err)
	}
	// Migration should copy ttft_ms to TTFRMs.
	if detail.TTFRMs != 0 {
		t.Fatalf("migrated TTFRMs = %d, want 0", detail.TTFRMs)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal explicit zero timing detail: %v", err)
	}
	for _, field := range []string{`"timing_version":1`, `"ttfb_ms":0`, `"ttft_ms":0`, `"ttfr_ms":0`} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Fatalf("round-tripped detail missing %s: %s", field, encoded)
		}
	}
	if bytes.Contains(encoded, []byte(`"ttfa_ms"`)) {
		t.Fatalf("round-tripped detail invented missing ttfa_ms: %s", encoded)
	}

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:        "unobserved-timing-key",
		Model:         "gpt-5.4",
		TimingVersion: coreusage.TimingVersionV1,
		Detail:        coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	generated, err := json.Marshal(stats.Snapshot())
	if err != nil {
		t.Fatalf("marshal unobserved timing snapshot: %v", err)
	}
	if !bytes.Contains(generated, []byte(`"timing_version":1`)) {
		t.Fatalf("generated detail missing timing_version: %s", generated)
	}
	for _, field := range []string{`"ttfb_ms"`, `"ttft_ms"`, `"ttfa_ms"`, `"ttfr_ms"`} {
		if bytes.Contains(generated, []byte(field)) {
			t.Fatalf("generated detail emitted unobserved %s: %s", field, generated)
		}
	}
}

func TestRequestStatisticsRecordIncludesUsageMetadata(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:               "test-key",
		Model:                "gpt-5.4",
		Alias:                "client-gpt",
		ReasoningEffort:      "medium",
		ServiceTier:          "legacy-default",
		RequestServiceTier:   " priority ",
		OutboundServiceTier:  " Priority ",
		ResponseServiceTier:  " standard ",
		EffectiveServiceTier: " fast ",
		Generate:             coreusage.GenerateFlag(false),
		RequestedAt:          time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:         20,
			OutputTokens:        20,
			CacheReadTokens:     7,
			CacheCreationTokens: 3,
			TotalTokens:         40,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	detail := details[0]
	if detail.Alias != "client-gpt" {
		t.Fatalf("alias = %q, want %q", detail.Alias, "client-gpt")
	}
	if detail.ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want %q", detail.ReasoningEffort, "medium")
	}
	if detail.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want %q", detail.ServiceTier, "priority")
	}
	if detail.RequestServiceTier != "priority" {
		t.Fatalf("request_service_tier = %q, want %q", detail.RequestServiceTier, "priority")
	}
	if detail.OutboundServiceTier != "Priority" {
		t.Fatalf("outbound_service_tier = %q, want %q", detail.OutboundServiceTier, "Priority")
	}
	if detail.ResponseServiceTier != "standard" {
		t.Fatalf("response_service_tier = %q, want %q", detail.ResponseServiceTier, "standard")
	}
	if detail.EffectiveServiceTier != "priority" {
		t.Fatalf("effective_service_tier = %q, want priority", detail.EffectiveServiceTier)
	}
	if detail.Generate {
		t.Fatalf("generate = true, want false")
	}
	if detail.Tokens.CacheReadTokens != 7 {
		t.Fatalf("cache_read_tokens = %d, want 7", detail.Tokens.CacheReadTokens)
	}
	if detail.Tokens.CacheCreationTokens != 3 {
		t.Fatalf("cache_creation_tokens = %d, want 3", detail.Tokens.CacheCreationTokens)
	}
	if detail.Tokens.CachedTokens != 7 {
		t.Fatalf("cached_tokens = %d, want 7", detail.Tokens.CachedTokens)
	}
}

func TestRequestStatisticsRecordDefaultsOmittedGenerateTrue(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "test-key",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{TotalTokens: 1},
	})

	detail := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
	if !detail.Generate {
		t.Fatalf("generate = false, want true for omitted legacy record field")
	}
}

func TestRequestStatisticsRecordFallsBackToLegacyAndDetailServiceTiers(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		ServiceTier: " priority ",
		Detail: coreusage.Detail{
			ResponseServiceTier: " default ",
			TotalTokens:         1,
		},
	})

	detail := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
	if detail.ServiceTier != "priority" || detail.RequestServiceTier != "priority" {
		t.Fatalf("request tiers = service:%q request:%q, want priority aliases", detail.ServiceTier, detail.RequestServiceTier)
	}
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response_service_tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestRequestStatisticsDoesNotTreatCacheCreationAsCacheRead(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.6-sol",
		RequestedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:         2224,
			OutputTokens:        10,
			CacheCreationTokens: 1024,
		},
	})

	detail := stats.Snapshot().APIs["test-key"].Models["gpt-5.6-sol"].Details[0]
	if detail.Tokens.CachedTokens != 0 {
		t.Fatalf("cached_tokens = %d, want 0 for creation-only usage", detail.Tokens.CachedTokens)
	}
	if detail.Tokens.CacheCreationTokens != 1024 {
		t.Fatalf("cache_creation_tokens = %d, want 1024", detail.Tokens.CacheCreationTokens)
	}
	if detail.Tokens.TotalTokens != 2234 {
		t.Fatalf("total_tokens = %d, want 2234 with cache creation included", detail.Tokens.TotalTokens)
	}
}

func TestRequestStatisticsRecordPreservesLTSUsageContractFields(t *testing.T) {
	prevEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(prevEnabled) })

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:              "client-api-key",
		Provider:            "anthropic",
		Model:               "claude-sonnet-4.5",
		Alias:               "panel-alias",
		Source:              "auths/anthropic.json",
		AuthIndex:           "2",
		ReasoningEffort:     "high",
		ServiceTier:         coreusage.DefaultServiceTier,
		RequestServiceTier:  coreusage.DefaultServiceTier,
		ResponseServiceTier: "standard",
		RequestedAt:         time.Date(2026, 6, 10, 9, 15, 0, 0, time.UTC),
		Latency:             1234 * time.Millisecond,
		Failed:              true,
		Fail: coreusage.Failure{
			Body: "codex_abnormal_reasoning_response: codex abnormal reasoning response discarded",
		},
		Detail: coreusage.Detail{
			InputTokens:         53,
			OutputTokens:        13,
			ReasoningTokens:     17,
			CacheReadTokens:     19,
			CacheCreationTokens: 23,
			TotalTokens:         83,
		},
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 0 {
		t.Fatalf("success_count = %d, want 0", snapshot.SuccessCount)
	}
	if snapshot.FailureCount != 1 {
		t.Fatalf("failure_count = %d, want 1", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 83 {
		t.Fatalf("total_tokens = %d, want 83", snapshot.TotalTokens)
	}
	if snapshot.RequestsByDay["2026-06-10"] != 1 {
		t.Fatalf("requests_by_day[2026-06-10] = %d, want 1", snapshot.RequestsByDay["2026-06-10"])
	}
	if snapshot.RequestsByHour["09"] != 1 {
		t.Fatalf("requests_by_hour[09] = %d, want 1", snapshot.RequestsByHour["09"])
	}

	apiSnapshot, ok := snapshot.APIs["client-api-key"]
	if !ok {
		t.Fatalf("snapshot missing API key bucket: %#v", snapshot.APIs)
	}
	if apiSnapshot.TotalRequests != 1 || apiSnapshot.TotalTokens != 83 {
		t.Fatalf("api snapshot = %+v, want requests=1 tokens=83", apiSnapshot)
	}

	modelSnapshot, ok := apiSnapshot.Models["claude-sonnet-4.5"]
	if !ok {
		t.Fatalf("snapshot missing model bucket: %#v", apiSnapshot.Models)
	}
	if modelSnapshot.TotalRequests != 1 || modelSnapshot.TotalTokens != 83 {
		t.Fatalf("model snapshot = %+v, want requests=1 tokens=83", modelSnapshot)
	}
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(modelSnapshot.Details))
	}

	detail := modelSnapshot.Details[0]
	if detail.Source != "auths/anthropic.json" {
		t.Fatalf("source = %q, want %q", detail.Source, "auths/anthropic.json")
	}
	if detail.AuthIndex != "2" {
		t.Fatalf("auth_index = %q, want 2", detail.AuthIndex)
	}
	if detail.Alias != "panel-alias" {
		t.Fatalf("alias = %q, want panel-alias", detail.Alias)
	}
	if detail.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", detail.ReasoningEffort)
	}
	if detail.ServiceTier != coreusage.DefaultServiceTier {
		t.Fatalf("service_tier = %q, want %q", detail.ServiceTier, coreusage.DefaultServiceTier)
	}
	if detail.RequestServiceTier != coreusage.DefaultServiceTier {
		t.Fatalf("request_service_tier = %q, want %q", detail.RequestServiceTier, coreusage.DefaultServiceTier)
	}
	if detail.ResponseServiceTier != "standard" {
		t.Fatalf("response_service_tier = %q, want standard", detail.ResponseServiceTier)
	}
	if detail.LatencyMs != 1234 {
		t.Fatalf("latency_ms = %d, want 1234", detail.LatencyMs)
	}
	if !detail.Failed {
		t.Fatalf("failed = false, want true")
	}
	if detail.FailureReason != "codex_abnormal_reasoning_response" {
		t.Fatalf("failure_reason = %q, want codex_abnormal_reasoning_response", detail.FailureReason)
	}
	if detail.Tokens.InputTokens != 53 ||
		detail.Tokens.OutputTokens != 13 ||
		detail.Tokens.ReasoningTokens != 17 ||
		detail.Tokens.CachedTokens != 19 ||
		detail.Tokens.CacheReadTokens != 19 ||
		detail.Tokens.CacheCreationTokens != 23 ||
		detail.Tokens.TotalTokens != 83 {
		t.Fatalf("tokens = %+v, want full LTS token breakdown", detail.Tokens)
	}
}

func TestRequestStatisticsEnabledToggleStopsNewRecordsButKeepsSnapshotReadable(t *testing.T) {
	prevEnabled := StatisticsEnabled()
	t.Cleanup(func() { SetStatisticsEnabled(prevEnabled) })

	stats := NewRequestStatistics()
	SetStatisticsEnabled(false)
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "disabled-key",
		Model:  "disabled-model",
		Detail: coreusage.Detail{TotalTokens: 99},
	})
	if snapshot := stats.Snapshot(); snapshot.TotalRequests != 0 {
		t.Fatalf("disabled snapshot total_requests = %d, want 0", snapshot.TotalRequests)
	}

	SetStatisticsEnabled(true)
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "enabled-key",
		Model:       "enabled-model",
		RequestedAt: time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{InputTokens: 4, OutputTokens: 6},
	})
	SetStatisticsEnabled(false)

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("snapshot total_requests = %d, want 1", snapshot.TotalRequests)
	}
	if _, ok := snapshot.APIs["enabled-key"].Models["enabled-model"]; !ok {
		t.Fatalf("snapshot missing record written before disabling: %#v", snapshot.APIs)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatencyAndServiceTiers(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp:            timestamp,
							LatencyMs:            2500,
							Source:               "user@example.com",
							AuthIndex:            "0",
							ServiceTier:          "priority",
							RequestServiceTier:   "priority",
							ResponseServiceTier:  "standard",
							EffectiveServiceTier: "priority",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := requireMergeSnapshot(t, stats, first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = requireMergeSnapshot(t, stats, second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].ServiceTier != "" {
		t.Fatalf("service_tier = %q, want legacy unknown to remain empty", details[0].ServiceTier)
	}
	if details[0].RequestServiceTier != "" || details[0].OutboundServiceTier != "" || details[0].ResponseServiceTier != "" || details[0].EffectiveServiceTier != "" {
		t.Fatalf("service tier metadata = request:%q outbound:%q response:%q effective:%q, want legacy unknown to remain empty", details[0].RequestServiceTier, details[0].OutboundServiceTier, details[0].ResponseServiceTier, details[0].EffectiveServiceTier)
	}
}

func TestRequestStatisticsMergeSnapshotNormalisesServiceTierAliases(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		detail RequestDetail
	}{
		{
			name: "legacy alias populates request tier",
			detail: RequestDetail{
				ServiceTier:          " priority ",
				OutboundServiceTier:  " Scale ",
				ResponseServiceTier:  " standard ",
				EffectiveServiceTier: " fast ",
			},
		},
		{
			name: "request tier populates legacy alias",
			detail: RequestDetail{
				RequestServiceTier:   " priority ",
				OutboundServiceTier:  " Scale ",
				ResponseServiceTier:  " standard ",
				EffectiveServiceTier: " fast ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.detail.Timestamp = timestamp
			tt.detail.Tokens.TotalTokens = 1
			snapshot := StatisticsSnapshot{APIs: map[string]APISnapshot{
				"test-key": {Models: map[string]ModelSnapshot{
					"gpt-5.4": {Details: []RequestDetail{tt.detail}},
				}},
			}}
			stats := NewRequestStatistics()
			result := requireMergeSnapshot(t, stats, snapshot)
			if result.Added != 1 || result.Skipped != 0 {
				t.Fatalf("merge result = %+v, want added=1 skipped=0", result)
			}
			got := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
			if got.ServiceTier != " priority " || got.RequestServiceTier != " priority " {
				t.Fatalf("request tiers = service:%q request:%q, want preserved aliases", got.ServiceTier, got.RequestServiceTier)
			}
			if got.ResponseServiceTier != " standard " {
				t.Fatalf("response_service_tier = %q, want preserved raw import value", got.ResponseServiceTier)
			}
			if got.OutboundServiceTier != "Scale" {
				t.Fatalf("outbound_service_tier = %q, want trimmed raw import value", got.OutboundServiceTier)
			}
			if got.EffectiveServiceTier != "priority" {
				t.Fatalf("effective_service_tier = %q, want canonical priority", got.EffectiveServiceTier)
			}
		})
	}
}

func TestRequestStatisticsMergeSnapshotKeepsDifferentCacheCreationTokenShapes(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	firstShape := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheCreationTokens: 1024,
		TotalTokens:         1210,
	})
	secondShape := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CacheCreationTokens: 1024,
		TotalTokens:         2234,
	})

	tests := []struct {
		name            string
		first           StatisticsSnapshot
		second          StatisticsSnapshot
		wantTotalTokens int64
	}{
		{
			name:            "first canonical token shape then second",
			first:           firstShape,
			second:          secondShape,
			wantTotalTokens: 3444,
		},
		{
			name:            "second canonical token shape then first",
			first:           secondShape,
			second:          firstShape,
			wantTotalTokens: 3444,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := NewRequestStatistics()

			result := requireMergeSnapshot(t, stats, tt.first)
			if result.Added != 1 || result.Skipped != 0 {
				t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
			}

			result = requireMergeSnapshot(t, stats, tt.second)
			if result.Added != 1 || result.Skipped != 0 {
				t.Fatalf("second merge = %+v, want added=1 skipped=0", result)
			}

			snapshot := stats.Snapshot()
			model := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"]
			if snapshot.TotalRequests != 2 || snapshot.SuccessCount != 2 || snapshot.FailureCount != 0 {
				t.Fatalf("snapshot request totals = requests:%d success:%d failure:%d, want 2/2/0", snapshot.TotalRequests, snapshot.SuccessCount, snapshot.FailureCount)
			}
			if snapshot.TotalTokens != tt.wantTotalTokens || model.TotalTokens != tt.wantTotalTokens {
				t.Fatalf("snapshot token totals = total:%d model:%d, want %d", snapshot.TotalTokens, model.TotalTokens, tt.wantTotalTokens)
			}
			if len(model.Details) != 2 {
				t.Fatalf("details len = %d, want 2", len(model.Details))
			}
			if snapshot.RequestsByDay["2026-07-11"] != 2 || snapshot.RequestsByHour["09"] != 2 {
				t.Fatalf("request buckets = day:%v hour:%v, want two requests", snapshot.RequestsByDay, snapshot.RequestsByHour)
			}
			if snapshot.TokensByDay["2026-07-11"] != tt.wantTotalTokens || snapshot.TokensByHour["09"] != tt.wantTotalTokens {
				t.Fatalf("token buckets = day:%v hour:%v, want %d", snapshot.TokensByDay, snapshot.TokensByHour, tt.wantTotalTokens)
			}
		})
	}
}

func TestTokenTotalFallbackFailsClosedOnOverflow(t *testing.T) {
	detail := normaliseDetail("openai", coreusage.Detail{InputTokens: math.MaxInt64, OutputTokens: 1})
	if detail != (TokenStats{}) {
		t.Fatalf("record tokens = %+v, want a canonical zero vector when the minimum total is not representable", detail)
	}
	tokens := normaliseTokenStats(TokenStats{InputTokens: math.MaxInt64, OutputTokens: 1})
	if tokens != (TokenStats{}) {
		t.Fatalf("import tokens = %+v, want a canonical zero vector when the minimum total is not representable", tokens)
	}
}

func TestTokenTotalFallbackProducesCanonicalV2Minimum(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		detail   coreusage.Detail
		want     int64
	}{
		{
			name:     "OpenAI missing total does not double count reasoning output subset",
			provider: "openai",
			detail:   coreusage.Detail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5},
			want:     120,
		},
		{
			name:     "Codex missing total does not double count reasoning output subset",
			provider: "codex",
			detail:   coreusage.Detail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5},
			want:     120,
		},
		{
			name:   "generic missing total keeps separate reasoning semantics",
			detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5},
			want:   125,
		},
		{
			name:   "explicit total below canonical minimum is repaired",
			detail: coreusage.Detail{InputTokens: 10, OutputTokens: 2, TotalTokens: 1},
			want:   12,
		},
		{
			name:   "reasoning-only detail remains representable",
			detail: coreusage.Detail{ReasoningTokens: 5},
			want:   5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := normaliseDetail(tt.provider, tt.detail)
			if tokens.TotalTokens != tt.want || !validCanonicalTokenStats(tokens) {
				t.Fatalf("normalised tokens = %+v, want total_tokens=%d and canonical v2", tokens, tt.want)
			}
		})
	}
}

func TestValidateCanonicalV2TokenStatsDistinguishesMissingFromExplicitZero(t *testing.T) {
	requiredFields := []string{"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "total_tokens"}
	for _, missing := range requiredFields {
		t.Run("missing_"+missing, func(t *testing.T) {
			fields := map[string]any{
				"input_tokens": 0, "output_tokens": 0, "reasoning_tokens": 0, "cached_tokens": 0, "total_tokens": 0,
			}
			delete(fields, missing)
			var tokens TokenStats
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatalf("marshal missing-field fixture: %v", err)
			}
			if err = json.Unmarshal(data, &tokens); err != nil {
				t.Fatalf("unmarshal missing-field fixture: %v", err)
			}
			snapshot := cacheCreationSnapshot(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), tokens)
			if err = snapshot.ValidateCanonicalV2TokenStats(); !errors.Is(err, ErrInvalidCanonicalTokenStats) {
				t.Fatalf("missing %s validation error = %v, want %v", missing, err, ErrInvalidCanonicalTokenStats)
			}
		})
	}

	var explicitZero TokenStats
	if err := json.Unmarshal([]byte(`{"input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}`), &explicitZero); err != nil {
		t.Fatalf("unmarshal explicit-zero fixture: %v", err)
	}
	snapshot := cacheCreationSnapshot(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), explicitZero)
	if err := snapshot.ValidateCanonicalV2TokenStats(); err != nil {
		t.Fatalf("explicit-zero v2 validation error = %v", err)
	}
}

func TestMigrateV1TokenStatsReleasedFixtureMatrix(t *testing.T) {
	tests := []struct {
		name      string
		tokens    string
		wantInput int64
		wantRead  int64
		wantWrite int64
		wantErr   error
	}{
		{
			name:      "v1-lts-0.0.13 markerless no-cache",
			tokens:    `{"input_tokens":4,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":9}`,
			wantInput: 4,
		},
		{
			name:      "legacy v1 omitted zero reasoning and cached fields",
			tokens:    `{"input_tokens":4,"output_tokens":5,"total_tokens":9}`,
			wantInput: 4,
		},
		{
			name:      "v1-lts-0.0.15 marker-bearing cache read",
			tokens:    `{"input_tokens":100,"uncached_input_tokens":80,"output_tokens":10,"reasoning_tokens":0,"cached_tokens":20,"cache_read_tokens":20,"total_tokens":110}`,
			wantInput: 100,
			wantRead:  20,
		},
		{
			name:      "v1-lts-0.0.15 marker-bearing cache creation alias",
			tokens:    `{"input_tokens":1200,"uncached_input_tokens":176,"output_tokens":10,"reasoning_tokens":0,"cached_tokens":1024,"cache_creation_tokens":1024,"total_tokens":1210}`,
			wantInput: 1200,
			wantWrite: 1024,
		},
		{
			name:      "v1-lts-0.0.15 marker-bearing read and creation",
			tokens:    `{"input_tokens":3085,"uncached_input_tokens":3085,"output_tokens":253,"reasoning_tokens":0,"cached_tokens":7,"cache_read_tokens":7,"cache_creation_tokens":19514,"total_tokens":22859}`,
			wantInput: 22606,
			wantRead:  7,
			wantWrite: 19514,
		},
		{
			name:      "v1-lts-0.0.15 marker-bearing known zero",
			tokens:    `{"input_tokens":0,"uncached_input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}`,
			wantInput: 0,
		},
		{
			name:    "markerless cached_tokens remains ambiguous",
			tokens:  `{"input_tokens":1200,"output_tokens":10,"reasoning_tokens":0,"cached_tokens":1024,"total_tokens":1210}`,
			wantErr: ErrAmbiguousLegacyTokenStats,
		},
		{
			name:    "markerless cache_read_tokens remains ambiguous",
			tokens:  `{"input_tokens":1200,"output_tokens":10,"reasoning_tokens":0,"cached_tokens":0,"cache_read_tokens":1024,"total_tokens":1210}`,
			wantErr: ErrAmbiguousLegacyTokenStats,
		},
		{
			name:    "markerless cache_creation_tokens remains ambiguous",
			tokens:  `{"input_tokens":1200,"output_tokens":10,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":1024,"total_tokens":1210}`,
			wantErr: ErrAmbiguousLegacyTokenStats,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tokens TokenStats
			if err := json.Unmarshal([]byte(tt.tokens), &tokens); err != nil {
				t.Fatalf("unmarshal released v1 fixture: %v", err)
			}
			snapshot := cacheCreationSnapshot(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), tokens)
			err := snapshot.MigrateV1TokenStats()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("MigrateV1TokenStats() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MigrateV1TokenStats() error = %v", err)
			}
			if err = snapshot.ValidateCanonicalTokenStats(); err != nil {
				t.Fatalf("migrated canonical validation error = %v", err)
			}
			got := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"].Details[0].Tokens
			if got.InputTokens != tt.wantInput || got.CacheReadTokens != tt.wantRead || got.CacheCreationTokens != tt.wantWrite || got.CachedTokens != tt.wantRead {
				t.Fatalf("migrated tokens = %+v, want input/read/write %d/%d/%d", got, tt.wantInput, tt.wantRead, tt.wantWrite)
			}
		})
	}
}

func TestValidCanonicalTokenStatsEnforcesCacheAndTotalRelationships(t *testing.T) {
	tests := []struct {
		name   string
		tokens TokenStats
		valid  bool
	}{
		{name: "creation only", tokens: TokenStats{InputTokens: 1200, OutputTokens: 10, CacheCreationTokens: 1024, TotalTokens: 1210}, valid: true},
		{name: "cache read and creation", tokens: TokenStats{InputTokens: 12, OutputTokens: 2, CachedTokens: 3, CacheReadTokens: 3, CacheCreationTokens: 6, TotalTokens: 14}, valid: true},
		{name: "reasoning is output subset", tokens: TokenStats{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 120}, valid: true},
		{name: "cache categories exceed input", tokens: TokenStats{InputTokens: 10, CachedTokens: 9, CacheReadTokens: 9, CacheCreationTokens: 2, TotalTokens: 10}},
		{name: "cache category sum overflows int64", tokens: TokenStats{InputTokens: math.MaxInt64, CachedTokens: math.MaxInt64, CacheReadTokens: math.MaxInt64, CacheCreationTokens: 1, TotalTokens: math.MaxInt64}},
		{name: "cached compatibility field mismatches cache read", tokens: TokenStats{InputTokens: 10, CachedTokens: 9, TotalTokens: 10}},
		{name: "total below input and output", tokens: TokenStats{InputTokens: 10, OutputTokens: 1, TotalTokens: 10}},
		{name: "minimum total sum overflows int64", tokens: TokenStats{InputTokens: math.MaxInt64, OutputTokens: 1, TotalTokens: math.MaxInt64}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validCanonicalTokenStats(tt.tokens); got != tt.valid {
				t.Fatalf("validCanonicalTokenStats(%+v) = %t, want %t", tt.tokens, got, tt.valid)
			}
		})
	}
}

func TestRequestStatisticsAggregatesFailClosedOnTokenOverflow(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	assertAggregates := func(t *testing.T, snapshot StatisticsSnapshot) {
		t.Helper()
		api := snapshot.APIs["cache-key"]
		model := api.Models["gpt-5.6-sol"]
		if snapshot.TotalTokens != math.MaxInt64 || api.TotalTokens != math.MaxInt64 || model.TotalTokens != math.MaxInt64 {
			t.Fatalf(
				"aggregate totals = global:%d api:%d model:%d, want the accepted max-int record preserved",
				snapshot.TotalTokens,
				api.TotalTokens,
				model.TotalTokens,
			)
		}
		if snapshot.TotalRequests != 2 || len(model.Details) != 2 {
			t.Fatalf("request totals = total:%d details:%d, want request metadata preserved with fail-closed tokens", snapshot.TotalRequests, len(model.Details))
		}
		if model.Details[1].Tokens != (TokenStats{}) {
			t.Fatalf("overflowing record tokens = %+v, want canonical zero vector", model.Details[1].Tokens)
		}
		if snapshot.TokensByDay["2026-07-11"] != math.MaxInt64 || snapshot.TokensByHour["09"] != math.MaxInt64 {
			t.Fatalf("token buckets = day:%v hour:%v, want the accepted max-int record preserved", snapshot.TokensByDay, snapshot.TokensByHour)
		}
	}

	t.Run("record", func(t *testing.T) {
		stats := NewRequestStatistics()
		for offset, total := range []int64{math.MaxInt64, 1} {
			stats.Record(context.Background(), coreusage.Record{
				APIKey:      "cache-key",
				Model:       "gpt-5.6-sol",
				RequestedAt: timestamp.Add(time.Duration(offset) * time.Minute),
				Detail:      coreusage.Detail{InputTokens: total, TotalTokens: total},
			})
		}
		assertAggregates(t, stats.Snapshot())
	})

	t.Run("import", func(t *testing.T) {
		stats := NewRequestStatistics()
		if result := requireMergeSnapshot(t, stats, cacheCreationSnapshot(timestamp, TokenStats{InputTokens: math.MaxInt64, TotalTokens: math.MaxInt64})); result.Added != 1 {
			t.Fatalf("first merge = %+v, want one added detail", result)
		}
		beforeOverflow := stats.Snapshot()
		if _, err := stats.MergeSnapshot(cacheCreationSnapshot(timestamp.Add(time.Minute), TokenStats{InputTokens: 1, TotalTokens: 1})); !errors.Is(err, ErrUsageAggregateOverflow) {
			t.Fatalf("second merge error = %v, want %v", err, ErrUsageAggregateOverflow)
		}
		if afterOverflow := stats.Snapshot(); !reflect.DeepEqual(afterOverflow, beforeOverflow) {
			t.Fatalf("overflowing import mutated statistics: before=%+v after=%+v", beforeOverflow, afterOverflow)
		}
	})
}

func TestRequestStatisticsMergeSnapshotRejectsEveryAggregateOverflowAtomically(t *testing.T) {
	const maxInt64 = int64(math.MaxInt64)
	timestamp := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		failed bool
		tokens TokenStats
		seed   func(*RequestStatistics)
	}{
		{name: "global requests", seed: func(stats *RequestStatistics) { stats.totalRequests = maxInt64 }},
		{name: "success requests", seed: func(stats *RequestStatistics) { stats.successCount = maxInt64 }},
		{name: "failure requests", failed: true, seed: func(stats *RequestStatistics) { stats.failureCount = maxInt64 }},
		{name: "global tokens", tokens: TokenStats{InputTokens: 1, TotalTokens: 1}, seed: func(stats *RequestStatistics) { stats.totalTokens = maxInt64 }},
		{name: "api requests", seed: func(stats *RequestStatistics) {
			stats.apis["cache-key"] = &apiStats{TotalRequests: maxInt64, Models: make(map[string]*modelStats)}
		}},
		{name: "api tokens", tokens: TokenStats{InputTokens: 1, TotalTokens: 1}, seed: func(stats *RequestStatistics) {
			stats.apis["cache-key"] = &apiStats{TotalTokens: maxInt64, Models: make(map[string]*modelStats)}
		}},
		{name: "model requests", seed: func(stats *RequestStatistics) {
			stats.apis["cache-key"] = &apiStats{Models: map[string]*modelStats{"gpt-5.6-sol": {TotalRequests: maxInt64}}}
		}},
		{name: "model tokens", tokens: TokenStats{InputTokens: 1, TotalTokens: 1}, seed: func(stats *RequestStatistics) {
			stats.apis["cache-key"] = &apiStats{Models: map[string]*modelStats{"gpt-5.6-sol": {TotalTokens: maxInt64}}}
		}},
		{name: "day requests", seed: func(stats *RequestStatistics) { stats.requestsByDay["2026-07-21"] = maxInt64 }},
		{name: "hour requests", seed: func(stats *RequestStatistics) { stats.requestsByHour[12] = maxInt64 }},
		{name: "day tokens", tokens: TokenStats{InputTokens: 1, TotalTokens: 1}, seed: func(stats *RequestStatistics) { stats.tokensByDay["2026-07-21"] = maxInt64 }},
		{name: "hour tokens", tokens: TokenStats{InputTokens: 1, TotalTokens: 1}, seed: func(stats *RequestStatistics) { stats.tokensByHour[12] = maxInt64 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := NewRequestStatistics()
			tt.seed(stats)
			before := stats.Snapshot()
			detail := RequestDetail{Timestamp: timestamp, Failed: tt.failed, Tokens: tt.tokens}
			candidate := StatisticsSnapshot{APIs: map[string]APISnapshot{
				"cache-key": {Models: map[string]ModelSnapshot{
					"gpt-5.6-sol": {Details: []RequestDetail{detail}},
				}},
			}}

			if _, err := stats.MergeSnapshot(candidate); !errors.Is(err, ErrUsageAggregateOverflow) {
				t.Fatalf("MergeSnapshot() error = %v, want %v", err, ErrUsageAggregateOverflow)
			}
			if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("overflowing %s merge mutated snapshot: before=%+v after=%+v", tt.name, before, after)
			}
		})
	}
}

func TestRequestStatisticsMergeSnapshotKeepsZeroTimestampDeterministic(t *testing.T) {
	stats := NewRequestStatistics()
	snapshot := cacheCreationSnapshot(time.Time{}, TokenStats{InputTokens: 1, TotalTokens: 1})

	first := requireMergeSnapshot(t, stats, snapshot)
	if first.Added != 1 || first.Skipped != 0 {
		t.Fatalf("first zero-timestamp merge = %+v, want added=1 skipped=0", first)
	}
	second := requireMergeSnapshot(t, stats, snapshot)
	if second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("second zero-timestamp merge = %+v, want added=0 skipped=1", second)
	}

	detail := stats.Snapshot().APIs["cache-key"].Models["gpt-5.6-sol"].Details[0]
	if !detail.Timestamp.IsZero() {
		t.Fatalf("stored timestamp = %s, want Go zero time preserved as uncertain identity", detail.Timestamp.Format(time.RFC3339Nano))
	}
}

func TestRequestStatisticsMergeSnapshotKeepsDistinctCacheReadAndCreationTokenShapes(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	creationOnly := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheCreationTokens: 1024,
		TotalTokens:         1210,
	})
	readAndWrite := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheReadTokens:     1024,
		CacheCreationTokens: 1024,
		TotalTokens:         3258,
	})

	stats := NewRequestStatistics()
	if result := requireMergeSnapshot(t, stats, creationOnly); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("creation-only merge = %+v, want added=1 skipped=0", result)
	}
	if result := requireMergeSnapshot(t, stats, readAndWrite); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("read-and-write merge = %+v, want added=1 skipped=0", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"].Details
	if snapshot.TotalRequests != 2 || len(details) != 2 {
		t.Fatalf("merged requests = total:%d details:%d, want 2/2", snapshot.TotalRequests, len(details))
	}
}

func TestRequestStatisticsMergeSnapshotTotalOnlyIdentityRemainsDistinct(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	first := cacheCreationSnapshot(timestamp, TokenStats{TotalTokens: 100})
	second := cacheCreationSnapshot(timestamp, TokenStats{TotalTokens: 200})

	stats := NewRequestStatistics()
	if result := requireMergeSnapshot(t, stats, first); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first total-only merge = %+v, want added=1 skipped=0", result)
	}
	if result := requireMergeSnapshot(t, stats, second); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("second total-only merge = %+v, want added=1 skipped=0", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"].Details
	if snapshot.TotalRequests != 2 || snapshot.TotalTokens != 300 || len(details) != 2 {
		t.Fatalf("total-only snapshot = requests:%d tokens:%d details:%d, want 2/300/2", snapshot.TotalRequests, snapshot.TotalTokens, len(details))
	}
}

func cacheCreationSnapshot(timestamp time.Time, tokens TokenStats) StatisticsSnapshot {
	return StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"cache-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.6-sol": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							Source:    "auths/codex.json",
							AuthIndex: "0",
							Tokens:    tokens,
						}},
					},
				},
			},
		},
	}
}

func requireMergeSnapshot(t *testing.T, stats *RequestStatistics, snapshot StatisticsSnapshot) MergeResult {
	t.Helper()
	result, err := stats.MergeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MergeSnapshot() error = %v", err)
	}
	return result
}
