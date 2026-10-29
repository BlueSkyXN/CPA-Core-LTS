package usage

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCanonicalPerformanceTokensAcrossProviders(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(old) })
	for _, tc := range []struct {
		name      string
		raw       int64
		breakdown coreusage.TokenBreakdown
	}{
		{"gemini", 100, coreusage.NewSeparateReasoningTokenBreakdown(10, 0, 0, 100, 40, 150)},
		{"openai", 140, coreusage.NewSubsetTokenBreakdown(10, 0, 0, 140, 40, 150)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := NewRequestStatistics()
			ms := int64(500)
			stats.Record(context.Background(), coreusage.Record{Provider: tc.name, APIKey: "test", Model: "model", RequestedAt: time.Now(), Latency: time.Second, TimingVersion: 1, TTFB: 100 * time.Millisecond, OutputTiming: &coreusage.OutputTiming{Version: 1, Streamed: true, DurationMS: &ms}, Detail: coreusage.Detail{InputTokens: 10, OutputTokens: tc.raw, ReasoningTokens: 40, TotalTokens: 150, TokenBreakdown: tc.breakdown}})
			snapshot := stats.Snapshot()
			d := snapshot.APIs["test"].Models["model"].Details[0]
			if d.Tokens.OutputTokens != tc.raw {
				t.Fatal("raw output counter changed")
			}
			var m QueryMetrics
			m.add(d, "model", QueryPriceRule{}, false)
			if m.OutputTPS.Numerator != 140 || m.OutputTPS.Denominator != 500 || m.VisibleTPS.Numerator != 100 || m.AverageTPS.Numerator != 140 || m.ReasoningRatio.Numerator != 40 || m.ReasoningRatio.Denominator != 140 {
				t.Fatalf("metrics=%+v", m)
			}
			var trend AnalyticsThroughputPoint
			analyticsAddThroughput(&trend, d)
			if trend.OutputTokens != 140 || trend.VisibleTokens != 100 || trend.ReasoningDenominator != 140 {
				t.Fatalf("trend=%+v", trend)
			}
			min, max := float64(90), float64(110)
			if !queryFilterMatches(QueryFilter{Metric: "displayedOutputTokens", Minimum: &min, Maximum: &max}, "test", "model", d) {
				t.Fatal("filter did not use non-reasoning count")
			}
			imported := NewRequestStatistics()
			if _, err := imported.MergeSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			d.TokenBreakdown.Output.NonReasoningTokens = 999
			for _, s := range []*RequestStatistics{stats, imported} {
				if got := s.Snapshot().APIs["test"].Models["model"].Details[0]; got.TokenBreakdown.Output.NonReasoningTokens != 100 {
					t.Fatal("snapshot/import aliases breakdown")
				}
			}
		})
	}
}

func TestUnclassifiedPerformanceTokensDoNotGuessSubset(t *testing.T) {
	b := coreusage.NewUnclassifiedTokenBreakdown(150)
	d := RequestDetail{LatencyMs: 1000, TTFBMs: 100, Tokens: TokenStats{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 40, TotalTokens: 150}, TokenBreakdown: &b}
	var m QueryMetrics
	m.add(d, "unknown", QueryPriceRule{}, false)
	if m.OutputTPS.Samples != 0 || m.AverageTPS.Samples != 0 || m.VisibleTPS.Samples != 0 || m.ReasoningRatio.Samples != 0 || m.Output != 100 {
		t.Fatalf("unclassified counters=%+v", m)
	}
	min := float64(0)
	if queryFilterMatches(QueryFilter{Metric: "displayedOutputTokens", Minimum: &min}, "test", "unknown", d) {
		t.Fatal("unclassified output must not match a numeric bound")
	}
	d.TokenBreakdown = nil
	m = QueryMetrics{}
	m.add(d, "legacy", QueryPriceRule{}, false)
	if m.AverageTPS.Numerator != 100 || m.VisibleTPS.Numerator != 60 {
		t.Fatal("legacy estimate changed")
	}
}

func TestTokenAccountingOverflowKeepsExportImportValid(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(old) })
	stats := NewRequestStatistics()
	for i, total := range []int64{math.MaxInt64, 1} {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "test", Model: "model", RequestedAt: time.Unix(int64(i+1), 0),
			Detail: coreusage.Detail{
				OutputTokens: total, TotalTokens: total,
				TokenBreakdown: coreusage.NewSubsetTokenBreakdown(0, 0, 0, total, 0, total),
			},
		})
	}
	snapshot := stats.Snapshot()
	details := snapshot.APIs["test"].Models["model"].Details
	if snapshot.TotalRequests != 2 || snapshot.TotalTokens != math.MaxInt64 || len(details) != 2 {
		t.Fatalf("overflow dropped request metadata or changed aggregates: %+v", snapshot)
	}
	if details[1].Tokens != (TokenStats{}) || details[1].TokenBreakdown == nil || !validTokenBreakdownDetail(details[1]) {
		t.Fatalf("overflow quarantine left contradictory accounting: %+v", details[1])
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored StatisticsSnapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.ValidateCanonicalV3TokenStats(); err != nil {
		t.Fatal(err)
	}
	if result, err := NewRequestStatistics().MergeSnapshot(restored); err != nil || result.Added != 2 {
		t.Fatalf("overflow snapshot cannot round trip: %+v %v", result, err)
	}
}

func TestRecordRejectsContradictoryAccountingWithoutLegacyFallback(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(old) })
	for _, mutate := range []func(*coreusage.TokenBreakdown){
		func(b *coreusage.TokenBreakdown) { b.SchemaVersion = 99 },
		func(b *coreusage.TokenBreakdown) { b.Input.UncachedTokens--; b.Input.CacheReadTokens++ },
		func(b *coreusage.TokenBreakdown) { b.Output.NonReasoningTokens++ },
	} {
		b := coreusage.NewSeparateReasoningTokenBreakdown(10, 0, 0, 100, 40, 150)
		mutate(&b)
		stats := NewRequestStatistics()
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "test", Model: "model", Latency: time.Second,
			Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 40, TotalTokens: 150, TokenBreakdown: b},
		})
		d := stats.Snapshot().APIs["test"].Models["model"].Details[0]
		if _, _, known := performanceOutputTokens(d); known || d.Tokens.OutputTokens != 100 {
			t.Fatalf("contradictory metadata fell back or changed raw counters: %+v", d)
		}
	}
}
