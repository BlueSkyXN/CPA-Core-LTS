package usage

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageCacheObservationStatesAndRatio(t *testing.T) {
	zero := coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(0)}
	positive := coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(20)}
	absent := coreusage.CacheTokenObservation{State: "absent"}
	invalid := coreusage.CacheTokenObservation{State: "invalid"}
	observe := func(read, write coreusage.CacheTokenObservation) *coreusage.CacheObservation {
		return &coreusage.CacheObservation{Version: 1, Read: read, Write: write}
	}
	s := NewRequestStatistics()
	start := time.Date(2026, 11, 20, 7, 15, 0, 0, time.UTC)
	for i, row := range []struct {
		input, legacyRead int64
		obs               *coreusage.CacheObservation
		failed            bool
	}{
		{100, 0, observe(zero, positive), false},
		{100, 0, observe(zero, absent), false},
		{100, 0, observe(absent, positive), false},
		{100, 0, observe(absent, absent), false},
		{100, 40, nil, false},
		{100, 0, nil, false},
		{100, 0, observe(invalid, zero), false},
		{100, 20, observe(positive, zero), true},
		{0, 0, observe(zero, absent), false},
		{10, 0, observe(positive, absent), false},
	} {
		s.Record(context.Background(), coreusage.Record{
			RequestID: fmt.Sprint(i), RequestedAt: start.Add(time.Duration(i) * time.Hour),
			Model: "synthetic-cache", APIKey: "synthetic", Failed: row.failed,
			Detail: coreusage.Detail{InputTokens: row.input, CacheReadTokens: row.legacyRead, CacheObservation: row.obs},
		})
	}
	q := QueryRequest{Bound: s.QueryCapabilities().Bound}
	result := analyticsRead(t, s, q)
	o := result.Data.Cache.Observed
	if o.Read != (CacheStateCounts{2, 3, 5}) || o.Write != (CacheStateCounts{2, 2, 6}) ||
		o.InputTokens != 300 || o.ReadTokens != 20 || o.Rate == nil || *o.Rate != 20.0/300 ||
		result.Data.Day.Cache[0].Observed.Read != o.Read {
		t.Fatalf("unexpected observations: %+v", o)
	}
	if result.Data.Hour.Cache[7].Observed.ReadTokens != 20 || result.Data.Hour.Cache[8].Observed.Rate != nil ||
		result.Data.Hour.Cache[9].Observed.Rate != nil {
		t.Fatal("failed known value or invalid ratio lost")
	}
	filters := []struct {
		f   QueryFilter
		ids []string
	}{
		{QueryFilter{CacheReadState: "zero"}, []string{"8", "1", "0"}},
		{QueryFilter{CacheReadState: "zero", CacheWriteState: "unknown"}, []string{"8", "1"}},
		{QueryFilter{CacheReadState: "unknown", CacheWriteState: "positive"}, []string{"2"}},
		{QueryFilter{CacheReadState: "positive", Failed: new(true)}, []string{"7"}},
		{QueryFilter{CacheReadState: "unknown", Cached: new(true)}, []string{"4"}},
	}
	for _, tc := range filters {
		q.Filter = tc.f
		page, err := s.QueryDetails(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, item := range page.Items {
			ids = append(ids, item.Detail.RequestID)
		}
		if !slices.Equal(ids, tc.ids) || page.Total != int64(len(tc.ids)) {
			t.Fatalf("filter %+v: %v want %v", tc.f, ids, tc.ids)
		}
	}
}

func TestUsageCacheObservationLargeQuery(t *testing.T) {
	ctx := t.Context()
	s := NewRequestStatistics()
	base := time.Date(2026, 11, 20, 7, 15, 0, 0, time.UTC)
	const size, stride = 100_000, 137
	observed := &coreusage.CacheObservation{Version: 1,
		Read:  coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(0)},
		Write: coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(2048)},
	}
	for i := range size {
		entry := coreusage.Record{RequestID: fmt.Sprint(i), APIKey: "synthetic", Model: "synthetic",
			RequestedAt: base.Add(time.Duration(i) * time.Millisecond), Detail: coreusage.Detail{InputTokens: 8192}}
		if i%stride == 0 {
			entry.Detail.CacheObservation = observed
		}
		s.Record(ctx, entry)
	}
	q := QueryRequest{Bound: s.QueryCapabilities().Bound, Filter: QueryFilter{CacheReadState: "zero", CacheWriteState: "positive"}, Limit: 73}
	expected := (size-1)/stride + 1
	start := time.Now()
	details := []string{}
	pages := 0
	for {
		p, err := s.QueryDetails(ctx, q)
		if err != nil || p.Total != int64(expected) {
			t.Fatalf("details page: %v", err)
		}
		for _, item := range p.Items {
			details = append(details, item.Detail.RequestID)
		}
		pages++
		q.Cursor = p.NextCursor
		if q.Cursor == "" {
			break
		}
	}
	exported := []string{}
	q.Limit = 111
	for {
		p, err := s.QueryExport(ctx, q)
		if err != nil || p.Total != int64(expected) {
			t.Fatalf("export page: %v", err)
		}
		for _, item := range p.Items {
			exported = append(exported, item.Detail.RequestID)
		}
		q.Cursor = p.NextCursor
		if q.Cursor == "" {
			break
		}
	}
	if len(details) != expected || !slices.Equal(details, exported) {
		t.Fatalf("details/export differ: %d/%d", len(details), len(exported))
	}
	t.Logf("%d input rows, %d matches, %d detail pages; query and export %v", size, len(details), pages, time.Since(start))
}

func TestUsageCacheObservationAllTimeIncludesUndatedRows(t *testing.T) {
	stats := NewRequestStatistics()
	for _, timestamp := range []time.Time{time.Unix(0, 0), time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)} {
		stats.Record(t.Context(), coreusage.Record{APIKey: "fixture", Model: "fixture", RequestedAt: timestamp,
			Detail: coreusage.Detail{InputTokens: 100}})
	}
	result := analyticsRead(t, stats, QueryRequest{})
	page, err := stats.QueryDetails(t.Context(), QueryRequest{Filter: QueryFilter{CacheReadState: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Analyzed != 1 || result.Data.Cache.Requests != 1 ||
		result.Data.Cache.Observed.Read.Unknown != page.Total || page.Total != 2 ||
		result.Data.Hour.Cache[0].Observed.Read.Unknown != 1 {
		t.Fatalf("rows outside timeline lost from observed totals: %+v, details %d", result.Data.Cache, page.Total)
	}
}
