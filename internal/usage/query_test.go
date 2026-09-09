package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func queryFixture(n int) *RequestStatistics {
	s := NewRequestStatistics()
	for i := 0; i < n; i++ {
		s.Record(context.Background(), coreusage.Record{APIKey: "query-fixture", Model: "gpt-test", RequestedAt: time.UnixMilli(1800000000000 + int64(i/3)), Latency: time.Second, Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}, Failed: i%3 == 0})
	}
	return s
}

func TestUsageQueryStablePaginationAndImport(t *testing.T) {
	s := queryFixture(301)
	session := s.QueryCapabilities()
	q := QueryRequest{Bound: session.Bound, NowMS: session.NowMS, Limit: 100, IncludeOptions: true}
	first, err := s.QueryDetails(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 100 || first.Total != 301 || len(first.Options.Models) != 1 {
		t.Fatal("unexpected first page")
	}
	// 已存在的记录不能因重复导入获得新序号。
	snapshot := s.Snapshot()
	receipt, err := s.MergeSnapshot(snapshot)
	if err != nil || receipt.Added != 0 || receipt.Skipped != 301 {
		t.Fatalf("dedupe: %+v %v", receipt, err)
	}
	s.Record(context.Background(), coreusage.Record{APIKey: "query-fixture", Model: "gpt-test", RequestedAt: time.UnixMilli(1700000000000)})
	seen := map[string]bool{}
	page := first
	for {
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate item")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
		q.IncludeOptions = false
		page, err = s.QueryDetails(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 301 {
		t.Fatalf("received %d", len(seen))
	}
	summary, err := s.QuerySummary(context.Background(), q, false)
	if err != nil || summary.Totals.Requests != 301 {
		t.Fatalf("bound summary: %+v %v", summary, err)
	}
	q.Bound = s.QueryCapabilities().Bound
	q.Cursor = ""
	latest, err := s.QuerySummary(context.Background(), q, false)
	if err != nil || latest.Totals.Requests != 302 {
		t.Fatal("late record missing after refresh")
	}
	if _, err = NewRequestStatistics().QueryDetails(context.Background(), q); !errors.Is(err, ErrQueryExpired) {
		t.Fatalf("cross-generation: %v", err)
	}
}

func TestUsageQueryMillisecondBoundariesAndModules(t *testing.T) {
	s := queryFixture(9)
	from, to := int64(1800000000001), int64(1800000000001)
	q := QueryRequest{FromMS: &from, ToMS: &to, Timezone: "Asia/Shanghai", Modules: []string{"models", "api_models", "days"}}
	r, err := s.QuerySummary(context.Background(), q, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Requests != 3 || r.Totals.Tokens != 360 || r.Totals.Failure != 1 || len(r.Groups["models"]) != 1 {
		t.Fatalf("wrong aggregation: %+v", r)
	}
	if r.Totals.Prices != nil {
		t.Fatal("summary calculated prices")
	}
	threshold := int64(100)
	q.Rules = map[string]QueryPriceRule{"gpt-test": {LongThreshold: &threshold}}
	p, err := s.QuerySummary(context.Background(), q, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Totals.Prices) != 1 || p.Totals.Prices[0].Band != "long" || p.Totals.Prices[0].Prompt != 300 {
		t.Fatalf("price classification: %+v", p.Totals.Prices)
	}
	q.Modules = []string{"invalid"}
	if _, err = s.QuerySummary(context.Background(), q, false); !errors.Is(err, ErrQueryInvalid) {
		t.Fatal("unknown module accepted")
	}
}

func TestUsageQueryFilterOptionsAndCursorBinding(t *testing.T) {
	s := queryFixture(12)
	failed := true
	q := QueryRequest{Filter: QueryFilter{Failed: &failed}, Limit: 2, IncludeOptions: true}
	r, err := s.QueryDetails(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 4 || r.Options.Metrics.Requests != 12 || r.Metrics.Requests != 4 {
		t.Fatal("options must ignore non-time filters")
	}
	q.Bound = r.Bound
	q.Cursor = r.NextCursor
	failed = false
	if _, err = s.QueryDetails(context.Background(), q); !errors.Is(err, ErrQueryInvalid) {
		t.Fatal("cursor accepted with different filter")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q.Cursor = ""
	if _, err = s.QueryDetails(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestUsageQueryDoesNotChangeExportShape(t *testing.T) {
	s := queryFixture(1)
	before, _ := json.Marshal(s.Snapshot())
	if _, err := s.QuerySummary(context.Background(), QueryRequest{}, true); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s.Snapshot())
	if string(before) != string(after) {
		t.Fatal("query mutated export")
	}
	var shape map[string]any
	_ = json.Unmarshal(after, &shape)
	if _, ok := shape["querySequence"]; ok {
		t.Fatal("internal sequence leaked")
	}
}

func TestUsageQueryConcurrentWriters(t *testing.T) {
	s := queryFixture(100000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, _ = s.QuerySummary(ctx, QueryRequest{Modules: []string{"models", "api_models"}}, false)
		}
	}()
	defer func() { cancel(); <-done }()
	var total, longest time.Duration
	for i := 0; i < 1000; i++ {
		start := time.Now()
		s.Record(context.Background(), coreusage.Record{APIKey: "concurrent-test", Model: "gpt-test", RequestedAt: time.UnixMilli(1900000000000 + int64(i)), Detail: coreusage.Detail{InputTokens: 1, TotalTokens: 1}})
		elapsed := time.Since(start)
		total += elapsed
		if elapsed > longest {
			longest = elapsed
		}
	}
	count, _ := s.Counts()
	if count != 101000 {
		t.Fatalf("lost records: %d", count)
	}
	t.Logf("100k existing records, 1000 concurrent writes: mean=%s max=%s (scheduler-dependent)", total/1000, longest)
}

func BenchmarkUsageQuery(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		s := queryFixture(n)
		b.Run(fmt.Sprintf("%d/details", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.QueryDetails(context.Background(), QueryRequest{}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%d/summary", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.QuerySummary(context.Background(), QueryRequest{Modules: []string{"models", "api_models"}}, false); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%d/snapshot-json", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := json.Marshal(s.Snapshot()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
