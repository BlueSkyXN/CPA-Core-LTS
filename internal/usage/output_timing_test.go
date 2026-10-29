package usage

import (
	"context"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"testing"
	"time"
)

func TestOutputTimingMetricsAndTrendUseSameSamples(t *testing.T) {
	measured := int64(20)
	zero := int64(0)
	invalid := int64(10000)
	for _, tc := range []struct {
		name     string
		timing   *coreusage.OutputTiming
		duration float64
		samples  int64
	}{
		{"legacy", nil, 9900, 1},
		{"measured", &coreusage.OutputTiming{Version: 1, Streamed: true, DurationMS: &measured}, 20, 1},
		{"buffered", &coreusage.OutputTiming{Version: 1}, 0, 0},
		{"single-read", &coreusage.OutputTiming{Version: 1, Streamed: true}, 0, 0},
		{"sub-ms", &coreusage.OutputTiming{Version: 1, Streamed: true, DurationMS: &zero}, 0, 0},
		{"invalid", &coreusage.OutputTiming{Version: 1, Streamed: true, DurationMS: &invalid}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := RequestDetail{LatencyMs: 10000, TTFBMs: 100, OutputTiming: tc.timing, Tokens: TokenStats{OutputTokens: 100}, Failed: true}
			var m QueryMetrics
			m.add(d, "test", QueryPriceRule{}, false)
			var p AnalyticsThroughputPoint
			analyticsAddThroughput(&p, d)
			if m.OutputTPS.Samples != tc.samples || m.OutputTPS.Denominator != tc.duration || p.OutputSamples != tc.samples || p.DecodeDurationMS != tc.duration {
				t.Fatalf("query=%+v trend=%+v", m.OutputTPS, p)
			}
			if m.AverageTPS.Samples != 1 {
				t.Fatal("output eligibility changed average TPS")
			}
		})
	}
}

func TestOutputTimingSnapshotAndMergeOwnMeasurements(t *testing.T) {
	old := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(old) })
	stats := NewRequestStatistics()
	duration := int64(100)
	stats.Record(context.Background(), coreusage.Record{APIKey: "timing-test", Model: "model", RequestedAt: time.Now(), Latency: time.Second, TimingVersion: coreusage.TimingVersionV1, TTFB: time.Millisecond, OutputTiming: &coreusage.OutputTiming{Version: 1, Streamed: true, DurationMS: &duration}, Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}})
	duration = 900
	snapshot := stats.Snapshot()
	d := snapshot.APIs["timing-test"].Models["model"].Details[0]
	if *d.OutputTiming.DurationMS != 100 {
		t.Fatal("record retained caller pointer")
	}
	imported := NewRequestStatistics()
	if _, err := imported.MergeSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	*d.OutputTiming.DurationMS = 800
	for _, s := range []*RequestStatistics{stats, imported} {
		if got := s.Snapshot().APIs["timing-test"].Models["model"].Details[0]; *got.OutputTiming.DurationMS != 100 {
			t.Fatal("snapshot or import aliases mutable timing")
		}
	}
}
