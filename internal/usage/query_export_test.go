package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueryExportEquivalentAndCached(t *testing.T) {
	s := queryFixture(1201)
	bound := s.QueryCapabilities().Bound
	failed, from, to, model := true, int64(1800000000010), int64(1800000000100), "gpt-test"
	for _, q := range []QueryRequest{
		{}, {Filter: QueryFilter{Failed: &failed}}, {FromMS: &from, ToMS: &to},
		{Filter: QueryFilter{Model: &model, TierFlow: "std", Effort: new(string)}},
		{Filter: QueryFilter{Identities: []QueryIdentity{{Source: "missing", AuthIndex: "missing"}}}},
	} {
		q.Bound, q.Limit = bound, 200
		var expected []QueryItem
		for {
			page, err := s.QueryDetails(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, page.Items...)
			q.Cursor = page.NextCursor
			if q.Cursor == "" {
				break
			}
		}
		q.Limit = 113
		before := s.exportQueries.scans
		var actual []QueryItem
		for {
			page, err := s.QueryExport(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != int64(len(expected)) {
				t.Fatalf("total %d != %d", page.Total, len(expected))
			}
			actual = append(actual, page.Items...)
			q.Cursor = page.NextCursor
			if q.Cursor == "" {
				break
			}
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatal("records or stable order changed")
		}
		if s.exportQueries.scans-before != 1 {
			t.Fatal("export rescanned the same snapshot")
		}
	}
}

func TestUsageQueryExportSnapshotAndEviction(t *testing.T) {
	s := queryFixture(301)
	q := QueryRequest{Bound: s.QueryCapabilities().Bound, Limit: 200}
	first, err := s.QueryExport(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	s.Record(context.Background(), coreusage.Record{APIKey: "late", Model: "gpt-test", RequestedAt: time.UnixMilli(1700000000000)})
	q.Cursor = first.NextCursor
	second, err := s.QueryExport(context.Background(), q)
	if err != nil || second.Total != 301 || len(second.Items) != 101 {
		t.Fatalf("late write crossed snapshot: %v", err)
	}
	// TTL eviction rebuilds the identical index and accepts the same continuation.
	for key, entry := range s.exportQueries.entries {
		entry.expires = time.Time{}
		s.exportQueries.entries[key] = entry
	}
	rebuilt, err := s.QueryExport(context.Background(), q)
	if err != nil || !reflect.DeepEqual(second, rebuilt) {
		t.Fatal("eviction changed continuation")
	}
	changed := q
	changed.Filter.Failed = new(bool)
	if _, err := s.QueryExport(context.Background(), changed); !errors.Is(err, ErrQueryInvalid) {
		t.Fatal("cross-filter cursor accepted")
	}
	for i := 0; i < 7; i++ {
		start := int64(1800000000000 + i)
		if _, err := s.QueryExport(context.Background(), QueryRequest{Bound: q.Bound, FromMS: &start}); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.exportQueries.entries) > exportCacheEntries || s.exportQueries.rows > exportCacheRows {
		t.Fatal("unbounded index cache")
	}
	s.queryGeneration = newQueryGeneration()
	if _, err := s.QueryExport(context.Background(), q); !errors.Is(err, ErrQueryExpired) {
		t.Fatal("cache bypassed generation check")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.QueryExport(ctx, QueryRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored")
	}
}

func TestUsageQueryExportLargeFallback(t *testing.T) {
	s := queryFixture(exportIndexRows + 1)
	q := QueryRequest{Bound: s.QueryCapabilities().Bound, Limit: 200}
	for i := 0; i < 2; i++ {
		old, err := s.QueryDetails(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.QueryExport(context.Background(), q)
		if err != nil || !reflect.DeepEqual(page.Items, old.Items) || page.Total != old.Total || page.NextCursor != old.NextCursor {
			t.Fatalf("large fallback changed output: %v", err)
		}
		q.Cursor = page.NextCursor
	}
	if s.exportQueries.scans != 3 || s.exportQueries.rows != 0 {
		t.Fatal("oversized index retained or repeatedly rebuilt")
	}
	// Reach the oldest tail directly, proving the index capacity is not a result limit.
	q.Cursor = queryEncode(queryCursor{q.Bound, queryFilterID(q), 1800000000003, 11})
	old, _ := s.QueryDetails(context.Background(), q)
	page, err := s.QueryExport(context.Background(), q)
	if err != nil || len(page.Items) == 0 || !reflect.DeepEqual(page.Items, old.Items) || page.NextCursor != "" {
		t.Fatal("fallback truncated tail")
	}
}

func TestUsageQueryExportValidationAndLegacyShape(t *testing.T) {
	s := queryFixture(1)
	before, _ := json.Marshal(s.Snapshot())
	for _, q := range []QueryRequest{{Limit: -1}, {Limit: 1001}, {IncludeOptions: true}, {Modules: []string{"models"}}, {Rules: map[string]QueryPriceRule{"x": {}}}, {Bound: "invalid"}, {Cursor: "invalid"}} {
		if _, err := s.QueryExport(context.Background(), q); !errors.Is(err, ErrQueryInvalid) {
			t.Fatalf("invalid export accepted: %v", err)
		}
	}
	if _, err := s.QueryDetails(context.Background(), QueryRequest{Limit: 201}); !errors.Is(err, ErrQueryInvalid) {
		t.Fatal("legacy page limit changed")
	}
	page, err := s.QueryExport(context.Background(), QueryRequest{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	var shape map[string]any
	_ = json.Unmarshal(encoded, &shape)
	if _, ok := shape["metrics"]; ok {
		t.Fatal("export repeated summary")
	}
	if _, ok := shape["options"]; ok {
		t.Fatal("export repeated options")
	}
	after, _ := json.Marshal(s.Snapshot())
	if string(before) != string(after) {
		t.Fatal("export mutated canonical data")
	}
	s.exportQueries.entries = nil
	s.exportQueries.active = 2
	if _, err := s.QueryExport(context.Background(), QueryRequest{}); !errors.Is(err, ErrQueryExportBusy) {
		t.Fatal("concurrency limit ignored")
	}
}

func TestUsageQueryExportConcurrentWrites(t *testing.T) {
	s := queryFixture(2001)
	bound := s.QueryCapabilities().Bound
	var workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			q := QueryRequest{Bound: bound, Limit: 200}
			count := 0
			for {
				page, err := s.QueryExport(context.Background(), q)
				if errors.Is(err, ErrQueryExportBusy) {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if page.Total != 2001 {
					t.Error("concurrent writes crossed bound")
					return
				}
				count += len(page.Items)
				q.Cursor = page.NextCursor
				if q.Cursor == "" {
					break
				}
			}
			if count != 2001 {
				t.Error("incomplete concurrent export")
			}
		}()
	}
	for i := 0; i < 100; i++ {
		s.Record(context.Background(), coreusage.Record{APIKey: "late", Model: "gpt-test", RequestedAt: time.UnixMilli(1700000000000 + int64(i))})
	}
	workers.Wait()
	if s.exportQueries.rows > exportCacheRows {
		t.Fatal("cache budget exceeded")
	}
}

func BenchmarkUsageFullExport(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		s := queryFixture(n)
		bound := s.QueryCapabilities().Bound
		for _, mode := range []string{"details", "export-cold", "export-warm"} {
			b.Run(fmt.Sprintf("%d/%s", n, mode), func(b *testing.B) {
				if mode == "export-warm" {
					_, _ = s.QueryExport(context.Background(), QueryRequest{Bound: bound})
				}
				b.ReportAllocs()
				b.ResetTimer()
				var scans uint64
				for i := 0; i < b.N; i++ {
					if mode == "export-cold" {
						s.exportQueries.entries = nil
						s.exportQueries.rows = 0
					}
					before := s.exportQueries.scans
					q := QueryRequest{Bound: bound, Limit: 200}
					count := 0
					for {
						if mode == "details" {
							page, err := s.QueryDetails(context.Background(), q)
							if err != nil {
								b.Fatal(err)
							}
							count += len(page.Items)
							q.Cursor = page.NextCursor
							scans++
						} else {
							q.Limit = MaxQueryExportPageSize
							page, err := s.QueryExport(context.Background(), q)
							if err != nil {
								b.Fatal(err)
							}
							count += len(page.Items)
							q.Cursor = page.NextCursor
						}
						if q.Cursor == "" {
							break
						}
					}
					if count != n {
						b.Fatal("incomplete export")
					}
					scans += s.exportQueries.scans - before
				}
				b.ReportMetric(float64(scans)/float64(b.N), "scans/op")
			})
		}
	}
}
