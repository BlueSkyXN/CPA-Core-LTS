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
		APIKey:          "test-key",
		Model:           "gpt-5.4",
		Alias:           "client-gpt",
		ReasoningEffort: "medium",
		ServiceTier:     " priority ",
		RequestedAt:     time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:         10,
			OutputTokens:        20,
			CacheReadTokens:     7,
			CacheCreationTokens: 3,
			TotalTokens:         30,
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
			TotalTokens:         1210,
		},
	})

	detail := stats.Snapshot().APIs["test-key"].Models["gpt-5.6-sol"].Details[0]
	if detail.Tokens.CachedTokens != 0 {
		t.Fatalf("cached_tokens = %d, want 0 for creation-only usage", detail.Tokens.CachedTokens)
	}
	if detail.Tokens.CacheCreationTokens != 1024 {
		t.Fatalf("cache_creation_tokens = %d, want 1024", detail.Tokens.CacheCreationTokens)
	}
}

func TestRequestStatisticsRecordPreservesLTSUsageContractFields(t *testing.T) {
	prevEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(prevEnabled) })

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:          "client-api-key",
		Provider:        "anthropic",
		Model:           "claude-sonnet-4.5",
		Alias:           "panel-alias",
		Source:          "auths/anthropic.json",
		AuthIndex:       "2",
		ReasoningEffort: "high",
		ServiceTier:     coreusage.DefaultServiceTier,
		RequestedAt:     time.Date(2026, 6, 10, 9, 15, 0, 0, time.UTC),
		Latency:         1234 * time.Millisecond,
		Failed:          true,
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

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatencyAndServiceTier(t *testing.T) {
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
							Timestamp:   timestamp,
							LatencyMs:   2500,
							Source:      "user@example.com",
							AuthIndex:   "0",
							ServiceTier: "priority",
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
}
