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
		// buffered / single-read: v1 output_timing without duration_ms falls
		// back to the TTFB estimate so plugin-path streams keep an OutputTPS
		// denominator (PRO deepseek-v4.1-flash all-empty TPS regression).
		{"buffered", &coreusage.OutputTiming{Version: 1}, 9900, 1},
		{"single-read", &coreusage.OutputTiming{Version: 1, Streamed: true}, 9900, 1},
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

// TestOutputDurationFallbackForMissingDurationMS locks the plugin-path shape:
// a v1 output_timing block without duration_ms must fall back to the TTFB
// estimate instead of dropping the OutputTPS denominator (PRO deepseek-v4.1-flash
// records showed all-empty output TPS because of this gap).
func TestOutputDurationFallbackForMissingDurationMS(t *testing.T) {
	d := RequestDetail{
		LatencyMs:           2416,
		TTFBMs:              2224,
		OutputTiming:        &coreusage.OutputTiming{Version: 1, Streamed: false},
		TimingVersion:       1,
		timingFieldsPresent: timingTTFBPresent,
	}
	duration, ok := outputDurationMS(d)
	if !ok || duration != 192 {
		t.Fatalf("fallback duration = %v ok=%v, want 192 true", duration, ok)
	}
	// A measured window still wins over the estimate. Measured windows only
	// exist on streamed responses (OutputTiming.Valid requires Streamed).
	measured := int64(150)
	d.OutputTiming.Streamed = true
	d.OutputTiming.DurationMS = &measured
	duration, ok = outputDurationMS(d)
	if !ok || duration != 150 {
		t.Fatalf("measured duration = %v ok=%v, want 150 true", duration, ok)
	}
	// Invalid TTFB still rejects the fallback.
	d.OutputTiming.DurationMS = nil
	d.TTFBMs = 3000
	if _, ok := outputDurationMS(d); ok {
		t.Fatal("fallback accepted with TTFB beyond latency")
	}
}
