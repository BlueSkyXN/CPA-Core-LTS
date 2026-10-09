package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func analyticsRead(t *testing.T, s *RequestStatistics, q QueryRequest) QueryAnalyticsResult {
	t.Helper()
	raw, err := s.QueryAnalytics(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var r QueryAnalyticsResult
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestUsageAnalyticsExactSnapshotAndOptions(t *testing.T) {
	s := queryFixture(301)
	cap := s.QueryCapabilities()
	q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Timezone: "Asia/Shanghai", IncludeOptions: true}
	r := analyticsRead(t, s, q)
	if cap.AnalyticsVersion != 1 || r.Total != 301 || r.Analyzed != 301 || r.Data.Timings.Latency.SampleCount != 200 || *r.Data.Timings.Latency.P95 != 1000 {
		t.Fatalf("wrong counts/timings: %+v", r)
	}
	if r.Data.Cache.Requests != 301 || r.Data.Errors.FailedRequests != 101 || r.Data.Timings.TTFT.P50 != nil || r.Data.Histogram.SampleCount != 200 {
		t.Fatal("metric population mismatch")
	}
	if len(r.Options.Models) != 1 || len(r.Data.Hour.Latency.Times) != 1 {
		t.Fatal("options or buckets missing")
	}
	missing := "missing"
	q.Filter.Model = &missing
	filtered := analyticsRead(t, s, q)
	if filtered.Total != 0 || len(filtered.Options.Models) != 1 {
		t.Fatal("options must ignore non-time filters")
	}
	q.Filter = QueryFilter{}
	before, _ := json.Marshal(s.Snapshot())
	again := analyticsRead(t, s, q)
	after, _ := json.Marshal(s.Snapshot())
	if string(before) != string(after) || again.Total != r.Total {
		t.Fatal("query changed statistics")
	}
	other := NewRequestStatistics()
	if _, err := other.QueryAnalytics(context.Background(), q); !errors.Is(err, ErrQueryExpired) {
		t.Fatal(err)
	}
}
func TestUsageAnalyticsSharedCacheAndOwnership(t *testing.T) {
	s := queryFixture(10000)
	cap := s.QueryCapabilities()
	q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.QueryAnalytics(context.Background(), q); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s.analytics.mu.Lock()
	builds := s.analytics.builds
	s.analytics.mu.Unlock()
	if builds != 1 {
		t.Fatalf("built %d times", builds)
	}
	raw, _ := s.QueryAnalytics(context.Background(), q)
	raw[0] = '!'
	raw, _ = s.QueryAnalytics(context.Background(), q)
	if !json.Valid(raw) {
		t.Fatal("caller mutated cache")
	}
	for i := 0; i < 12; i++ {
		model := string(rune('a' + i))
		q.Filter.Model = &model
		analyticsRead(t, s, q)
	}
	s.analytics.mu.Lock()
	defer s.analytics.mu.Unlock()
	if len(s.analytics.entries) > analyticsCacheEntries || s.analytics.bytes > analyticsCacheBytes {
		t.Fatal("unbounded cache")
	}
}
func TestUsageAnalyticsCancelCapacityAndValidation(t *testing.T) {
	s := queryFixture(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.QueryAnalytics(ctx, QueryRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, q := range []QueryRequest{{Cursor: "x"}, {Modules: []string{"models"}}, {Timezone: "not-a-timezone"}} {
		if _, err := s.QueryAnalytics(context.Background(), q); !errors.Is(err, ErrQueryInvalid) {
			t.Fatal(err)
		}
	}
	s.analytics.active = analyticsConcurrentQueries
	if _, err := s.QueryAnalytics(context.Background(), QueryRequest{}); !errors.Is(err, ErrAnalyticsBusy) {
		t.Fatal(err)
	}
	s.analytics.active = 0
	s = queryFixture(MaxAnalyticsRows + 1)
	if _, err := s.QueryAnalytics(context.Background(), QueryRequest{}); !errors.Is(err, ErrAnalyticsTooLarge) {
		t.Fatal(err)
	}
	if len(s.analytics.entries) != 0 {
		t.Fatal("cached incomplete result")
	}
}
func TestUsageAnalyticsThroughputBuckets(t *testing.T) {
	s := NewRequestStatistics()
	base := time.UnixMilli(1800000000000)
	record := func(at time.Time, output, reasoning int64, latency, ttfb time.Duration, failed bool) {
		s.Record(context.Background(), coreusage.Record{
			APIKey: "throughput-fixture", Model: "gpt-test", RequestedAt: at, Latency: latency,
			TimingVersion: usageTimingVersion,
			TTFB:          ttfb,
			Detail:        coreusage.Detail{InputTokens: 10, OutputTokens: output, ReasoningTokens: reasoning, TotalTokens: 10 + output},
			Failed:        failed,
		})
	}
	record(base, 200, 50, time.Second, 200*time.Millisecond, false)
	record(base.Add(time.Minute), 100, 100, 2*time.Second, 2*time.Second, false)
	record(base.Add(2*time.Minute), 80, 120, time.Second, 100*time.Millisecond, false)
	record(base.Add(3*time.Minute), 60, 0, time.Second, 0, true)
	record(base.Add(4*time.Minute), 0, 0, time.Second, 100*time.Millisecond, false)

	cap := s.QueryCapabilities()
	r := analyticsRead(t, s, QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Timezone: "UTC"})
	points := r.Data.Hour.Throughput
	if len(points) != 1 || len(r.Data.Day.Throughput) != 1 {
		t.Fatalf("throughput buckets: hour=%d day=%d", len(points), len(r.Data.Day.Throughput))
	}
	p := points[0]
	if p.TimestampMS == 0 || p.AverageSamples != 4 || p.AverageTokens != 440 || p.AverageDurationMS != 5000 {
		t.Fatalf("average series: %+v", p)
	}
	if p.OutputSamples != 2 || p.OutputTokens != 280 || p.DecodeDurationMS != 1700 {
		t.Fatalf("output series: %+v", p)
	}
	if p.VisibleSamples != 3 || p.VisibleTokens != 210 || p.VisibleDurationMS != 4000 || p.ReasoningTokens != 150 || p.ReasoningDenominator != 360 {
		t.Fatalf("visible series: %+v", p)
	}
	summary, err := s.QuerySummary(context.Background(), QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS}, false)
	if err != nil {
		t.Fatal(err)
	}
	m := summary.Totals
	if m.OutputTPS.Numerator != p.OutputTokens || m.OutputTPS.Denominator != p.DecodeDurationMS || m.OutputTPS.Samples != p.OutputSamples ||
		m.AverageTPS.Numerator != p.AverageTokens || m.AverageTPS.Denominator != p.AverageDurationMS ||
		m.VisibleTPS.Numerator != p.VisibleTokens || m.ReasoningRatio.Numerator != p.ReasoningTokens {
		t.Fatalf("throughput diverges from query metrics: %+v vs %+v", p, m)
	}
}

func TestUsageAnalyticsWallClockDST(t *testing.T) {
	tests := []struct {
		zone  string
		day   bool
		dates []string
		count int
	}{
		{"America/New_York", false, []string{"2026-11-01T00:30:00-04:00", "2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00", "2026-11-01T02:30:00-05:00"}, 3},
		{"Australia/Lord_Howe", false, []string{"2026-10-04T01:15:00+10:30", "2026-10-04T02:40:00+11:00", "2026-10-04T03:20:00+11:00", "2026-10-04T04:20:00+11:00"}, 4},
		{"America/Sao_Paulo", true, []string{"2018-11-03T12:00:00-03:00", "2018-11-04T12:00:00-02:00", "2018-11-05T12:00:00-02:00"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			loc, _ := time.LoadLocation(tt.zone)
			buckets := map[int64]*analyticsBucket{}
			var first, last int64
			for i, date := range tt.dates {
				stamp, _ := time.Parse(time.RFC3339, date)
				ms := stamp.UnixMilli()
				if i == 0 {
					first = ms
				}
				last = ms
				buckets[analyticsFloor(ms, tt.day, loc)] = &analyticsBucket{}
			}
			times := analyticsTimes(buckets, first, last, QueryRequest{}, tt.day, loc)
			if len(times) != tt.count {
				t.Fatalf("times=%v", times)
			}
			for key := range buckets {
				found := false
				for _, v := range times {
					found = found || key == v
				}
				if !found {
					t.Fatal("dropped bucket", key)
				}
			}
		})
	}
}
func BenchmarkUsageAnalytics(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		s := queryFixture(n)
		cap := s.QueryCapabilities()
		q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Timezone: "UTC"}
		loc := time.UTC
		b.Run(strconv.Itoa(n)+"/cold", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.buildAnalytics(context.Background(), q, loc); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(strconv.Itoa(n)+"/cached", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.QueryAnalytics(context.Background(), q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestUsageAnalyticsPanelParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/analytics-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string          `json:"name"`
		Timezone string          `json:"timezone"`
		From     *int64          `json:"from_ms"`
		To       *int64          `json:"to_ms"`
		Rows     []RequestDetail `json:"rows"`
		Expected json.RawMessage `json:"expected"`
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			s := NewRequestStatistics()
			snapshot := StatisticsSnapshot{APIs: map[string]APISnapshot{"synthetic": {Models: map[string]ModelSnapshot{"model": {Details: f.Rows}}}}}
			if _, err = s.MergeSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			result := analyticsRead(t, s, QueryRequest{FromMS: f.From, ToMS: f.To, Timezone: f.Timezone})
			actualBytes, _ := json.Marshal(result.Data)
			var actual, expected any
			json.Unmarshal(actualBytes, &actual)
			json.Unmarshal(f.Expected, &expected)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("parity mismatch\nactual: %s\nexpected: %s", actualBytes, f.Expected)
			}
		})
	}
}

func TestUsageAnalyticsCachedSnapshotSurvivesImportAndExpiry(t *testing.T) {
	s := queryFixture(3)
	cap := s.QueryCapabilities()
	q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS}
	before := analyticsRead(t, s, q)
	snapshot := s.Snapshot()
	row := snapshot.APIs["query-fixture"].Models["gpt-test"].Details[0]
	row.Timestamp = time.UnixMilli(1500000000000)
	receipt, err := s.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{"query-fixture": {Models: map[string]ModelSnapshot{"gpt-test": {Details: []RequestDetail{row}}}}}})
	if err != nil || receipt.Added != 1 {
		t.Fatal(err, receipt)
	}
	if analyticsRead(t, s, q).Total != before.Total {
		t.Fatal("old bound changed after import")
	}
	s.analytics.mu.Lock()
	for key, entry := range s.analytics.entries {
		entry.expires = time.Now().Add(-time.Second)
		s.analytics.entries[key] = entry
	}
	s.analytics.mu.Unlock()
	if analyticsRead(t, s, q).Total != before.Total {
		t.Fatal("recomputed old snapshot changed")
	}
	q.Bound = s.QueryCapabilities().Bound
	if analyticsRead(t, s, q).Total != 4 {
		t.Fatal("new snapshot missed import")
	}
	s.mu.Lock()
	s.queryGeneration = newQueryGeneration()
	s.mu.Unlock()
	if _, err = s.QueryAnalytics(context.Background(), q); !errors.Is(err, ErrQueryExpired) {
		t.Fatal("cache bypassed generation validation", err)
	}
}
func TestUsageAnalyticsCanceledWaiterDoesNotPoisonSharedWork(t *testing.T) {
	s := queryFixture(100000)
	cap := s.QueryCapabilities()
	q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	s.mu.RLock()
	go func() { _, err := s.QueryAnalytics(ctx, q); first <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.analytics.mu.Lock()
		active := s.analytics.active
		s.analytics.mu.Unlock()
		if active > 0 {
			break
		}
		if time.Now().After(deadline) {
			s.mu.RUnlock()
			t.Fatal("work did not start")
		}
		time.Sleep(time.Millisecond)
	}
	go func() { _, err := s.QueryAnalytics(context.Background(), q); second <- err }()
	for {
		s.analytics.mu.Lock()
		users := 0
		for _, f := range s.analytics.flights {
			users = f.users
		}
		s.analytics.mu.Unlock()
		if users >= 2 {
			break
		}
		select {
		case err := <-second:
			s.mu.RUnlock()
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			<-first
			return
		default:
		}
		if time.Now().After(deadline) {
			s.mu.RUnlock()
			t.Fatal("second subscriber did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	s.mu.RUnlock()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func BenchmarkUsageAnalyticsDistributed(b *testing.B) {
	for _, n := range []int{5546, 10000, 100000} {
		s := NewRequestStatistics()
		rows := make([]RequestDetail, n)
		start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
		for i := range rows {
			rows[i] = RequestDetail{Timestamp: start.Add(time.Duration(i%86400) * time.Second), LatencyMs: int64(100 + i%30000), Source: "synthetic", AuthIndex: "1", Failed: i%10 == 0, Tokens: TokenStats{InputTokens: 100, OutputTokens: 30, TotalTokens: 130}}
		}
		_, err := s.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{"synthetic": {Models: map[string]ModelSnapshot{"model": {Details: rows}}}}})
		if err != nil {
			b.Fatal(err)
		}
		cap := s.QueryCapabilities()
		q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Timezone: "Asia/Shanghai"}
		loc, _ := time.LoadLocation(q.Timezone)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.buildAnalytics(context.Background(), q, loc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
