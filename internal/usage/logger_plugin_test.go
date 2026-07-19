package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
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
		ResponseServiceTier:  " standard ",
		EffectiveServiceTier: " fast ",
		Generate:             coreusage.GenerateFlag(false),
		RequestedAt:          time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:              10,
			OutputTokens:             20,
			CacheReadTokens:          7,
			CacheCreationTokens:      3,
			TotalTokens:              30,
			UncachedInputTokens:      0,
			UncachedInputTokensKnown: true,
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
	if detail.Tokens.UncachedInputTokens == nil || *detail.Tokens.UncachedInputTokens != 0 {
		t.Fatalf("uncached_input_tokens = %v, want pointer to 0", detail.Tokens.UncachedInputTokens)
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

func TestRequestStatisticsSnapshotDoesNotExposeMutableUncachedInputPointer(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "test-key",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{
			InputTokens:              10,
			TotalTokens:              10,
			UncachedInputTokens:      0,
			UncachedInputTokensKnown: true,
		},
	})

	first := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
	if first.Tokens.UncachedInputTokens == nil {
		t.Fatal("first snapshot missing uncached_input_tokens")
	}
	*first.Tokens.UncachedInputTokens = 99

	second := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
	if second.Tokens.UncachedInputTokens == nil || *second.Tokens.UncachedInputTokens != 0 {
		t.Fatalf("second snapshot uncached_input_tokens = %v, want pointer to 0", second.Tokens.UncachedInputTokens)
	}
}

func TestRequestStatisticsRecordOmitsInvalidUncachedInputTokens(t *testing.T) {
	tests := []struct {
		name        string
		value       int64
		wantPresent bool
	}{
		{name: "negative", value: -1},
		{name: "greater than input", value: 11},
		{name: "known zero", value: 0, wantPresent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := NewRequestStatistics()
			stats.Record(context.Background(), coreusage.Record{
				APIKey: "test-key",
				Model:  "gpt-5.4",
				Detail: coreusage.Detail{
					InputTokens:              10,
					TotalTokens:              10,
					UncachedInputTokens:      tt.value,
					UncachedInputTokensKnown: true,
				},
			})

			detail := stats.Snapshot().APIs["test-key"].Models["gpt-5.4"].Details[0]
			if gotPresent := detail.Tokens.UncachedInputTokens != nil; gotPresent != tt.wantPresent {
				t.Fatalf("uncached_input_tokens present = %v, want %v; tokens=%+v", gotPresent, tt.wantPresent, detail.Tokens)
			}
		})
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
			InputTokens:         1200,
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
			InputTokens:         11,
			OutputTokens:        13,
			ReasoningTokens:     17,
			CacheReadTokens:     19,
			CacheCreationTokens: 23,
			TotalTokens:         60,
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
	if snapshot.TotalTokens != 60 {
		t.Fatalf("total_tokens = %d, want 60", snapshot.TotalTokens)
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
	if apiSnapshot.TotalRequests != 1 || apiSnapshot.TotalTokens != 60 {
		t.Fatalf("api snapshot = %+v, want requests=1 tokens=60", apiSnapshot)
	}

	modelSnapshot, ok := apiSnapshot.Models["claude-sonnet-4.5"]
	if !ok {
		t.Fatalf("snapshot missing model bucket: %#v", apiSnapshot.Models)
	}
	if modelSnapshot.TotalRequests != 1 || modelSnapshot.TotalTokens != 60 {
		t.Fatalf("model snapshot = %+v, want requests=1 tokens=60", modelSnapshot)
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
	if detail.Tokens.InputTokens != 11 ||
		detail.Tokens.OutputTokens != 13 ||
		detail.Tokens.ReasoningTokens != 17 ||
		detail.Tokens.CachedTokens != 19 ||
		detail.Tokens.CacheReadTokens != 19 ||
		detail.Tokens.CacheCreationTokens != 23 ||
		detail.Tokens.TotalTokens != 60 {
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

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
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
	if details[0].RequestServiceTier != "" || details[0].ResponseServiceTier != "" || details[0].EffectiveServiceTier != "" {
		t.Fatalf("service tier metadata = request:%q response:%q effective:%q, want legacy unknown to remain empty", details[0].RequestServiceTier, details[0].ResponseServiceTier, details[0].EffectiveServiceTier)
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
				ResponseServiceTier:  " standard ",
				EffectiveServiceTier: " fast ",
			},
		},
		{
			name: "request tier populates legacy alias",
			detail: RequestDetail{
				RequestServiceTier:   " priority ",
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
			result := stats.MergeSnapshot(snapshot)
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
			if got.EffectiveServiceTier != "priority" {
				t.Fatalf("effective_service_tier = %q, want canonical priority", got.EffectiveServiceTier)
			}
		})
	}
}

func TestRequestStatisticsMergeSnapshotDeduplicatesLegacyAndCanonicalCacheCreation(t *testing.T) {
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	uncachedInputTokens := int64(176)
	legacy := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheCreationTokens: 1024,
		TotalTokens:         1210,
	})
	canonical := cacheCreationSnapshot(timestamp, TokenStats{
		InputTokens:         1200,
		UncachedInputTokens: &uncachedInputTokens,
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
			name:            "legacy then canonical",
			first:           legacy,
			second:          canonical,
			wantTotalTokens: 1210,
		},
		{
			name:            "canonical then legacy",
			first:           canonical,
			second:          legacy,
			wantTotalTokens: 2234,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := NewRequestStatistics()

			result := stats.MergeSnapshot(tt.first)
			if result.Added != 1 || result.Skipped != 0 {
				t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
			}

			result = stats.MergeSnapshot(tt.second)
			if result.Added != 0 || result.Skipped != 1 {
				t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
			}

			snapshot := stats.Snapshot()
			model := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"]
			if snapshot.TotalRequests != 1 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 0 {
				t.Fatalf("snapshot request totals = requests:%d success:%d failure:%d, want 1/1/0", snapshot.TotalRequests, snapshot.SuccessCount, snapshot.FailureCount)
			}
			if snapshot.TotalTokens != tt.wantTotalTokens || model.TotalTokens != tt.wantTotalTokens {
				t.Fatalf("snapshot token totals = total:%d model:%d, want %d", snapshot.TotalTokens, model.TotalTokens, tt.wantTotalTokens)
			}
			if len(model.Details) != 1 {
				t.Fatalf("details len = %d, want 1", len(model.Details))
			}
			if snapshot.RequestsByDay["2026-07-11"] != 1 || snapshot.RequestsByHour["09"] != 1 {
				t.Fatalf("request buckets = day:%v hour:%v, want one request", snapshot.RequestsByDay, snapshot.RequestsByHour)
			}
			if snapshot.TokensByDay["2026-07-11"] != tt.wantTotalTokens || snapshot.TokensByHour["09"] != tt.wantTotalTokens {
				t.Fatalf("token buckets = day:%v hour:%v, want %d", snapshot.TokensByDay, snapshot.TokensByHour, tt.wantTotalTokens)
			}
		})
	}
}

func TestRequestStatisticsMergeSnapshotCacheCreationAliasDoesNotHideCacheReadAndWrite(t *testing.T) {
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
	if result := stats.MergeSnapshot(creationOnly); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("creation-only merge = %+v, want added=1 skipped=0", result)
	}
	if result := stats.MergeSnapshot(readAndWrite); result.Added != 1 || result.Skipped != 0 {
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
	if result := stats.MergeSnapshot(first); result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first total-only merge = %+v, want added=1 skipped=0", result)
	}
	if result := stats.MergeSnapshot(second); result.Added != 1 || result.Skipped != 0 {
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
