package usage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageQueryTierFlowAndEffortParity(t *testing.T) {
	s := NewRequestStatistics()
	records := []coreusage.Record{
		{RequestServiceTier: "priority", EffectiveServiceTier: "priority", ReasoningEffort: "high"},
		{RequestServiceTier: "standard", ResponseServiceTier: "standard", ReasoningEffort: "high"},
		{RequestServiceTier: "priority", ResponseServiceTier: "default", ReasoningEffort: " HIGH "},
		{RequestServiceTier: "default", EffectiveServiceTier: "priority", ReasoningEffort: "high"},
		{RequestServiceTier: "priority", OutboundServiceTier: "default", ReasoningEffort: "medium"},
		{ServiceTier: "priority", ReasoningEffort: "custom:42"},
		{},
		{RequestServiceTier: "priority", ResponseServiceTier: "future-tier", ReasoningEffort: "medium"},
	}
	for i, r := range records {
		r.APIKey, r.Model, r.Source, r.AuthIndex = "synthetic-api", "model-a", "synthetic-source", "1"
		r.RequestedAt = time.UnixMilli(1800000000000 + int64(i))
		r.Latency = time.Second
		r.Detail = coreusage.Detail{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
		s.Record(context.Background(), r)
	}
	cap := s.QueryCapabilities()
	var partition int64
	for _, test := range []struct {
		flow string
		want int64
	}{{"fast", 2}, {"std", 2}, {"fast_to_std", 3}, {"std_to_fast", 1}} {
		t.Run(test.flow, func(t *testing.T) {
			q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Filter: QueryFilter{TierFlow: test.flow}, IncludeOptions: true, Limit: 1}
			a := analyticsRead(t, s, q)
			d, err := s.QueryDetails(context.Background(), q)
			if err != nil || a.Total != test.want || d.Total != test.want || a.Data.Cache.Requests != test.want {
				t.Fatalf("flow %s: aggregate=%d details=%+v err=%v", test.flow, a.Total, d, err)
			}
			if !reflect.DeepEqual(a.Options.Efforts, []string{"", "custom:42", "high", "medium"}) {
				t.Fatalf("effort directory was filtered: %+v", a.Options.Efforts)
			}
			seen := map[string]bool{}
			for {
				for _, item := range d.Items {
					if queryServiceTierFlow(item.Detail) != test.flow || seen[item.ID] {
						t.Fatal("mixed tier flow or repeated page")
					}
					seen[item.ID] = true
				}
				if d.NextCursor == "" {
					break
				}
				q.Cursor = d.NextCursor
				d, err = s.QueryDetails(context.Background(), q)
				if err != nil {
					t.Fatal(err)
				}
			}
			if int64(len(seen)) != test.want {
				t.Fatal("pagination lost rows")
			}
			q.Cursor = ""
			summary, err := s.QuerySummary(context.Background(), q, true)
			if err != nil || summary.Totals.Requests != test.want {
				t.Fatal("summary differs from analytics")
			}
			partition += test.want
		})
	}
	if partition != int64(len(records)) {
		t.Fatal("tier flows do not partition all rows")
	}
	for _, test := range []struct {
		effort string
		want   int64
	}{{"high", 1}, {" HIGH ", 1}, {"medium", 2}, {"", 0}} {
		q := QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS, Filter: QueryFilter{TierFlow: "fast_to_std", Effort: &test.effort}}
		if a := analyticsRead(t, s, q); a.Total != test.want {
			t.Fatalf("effort %q: %d", test.effort, a.Total)
		}
	}
	empty := ""
	if a := analyticsRead(t, s, QueryRequest{Filter: QueryFilter{Effort: &empty}}); a.Total != 1 {
		t.Fatal("missing effort was not isolated")
	}
	// 既有 tier 仍按最终档位分组，不因新增互斥条件而改变旧调用者的语义。
	if a := analyticsRead(t, s, QueryRequest{Filter: QueryFilter{Tier: "fast"}}); a.Total != 3 {
		t.Fatal("legacy fast changed")
	}
	if a := analyticsRead(t, s, QueryRequest{Filter: QueryFilter{Tier: "std"}}); a.Total != 5 {
		t.Fatal("legacy std changed")
	}
	if _, err := s.QueryAnalytics(context.Background(), QueryRequest{Filter: QueryFilter{TierFlow: "unknown"}}); !errors.Is(err, ErrQueryInvalid) {
		t.Fatal("invalid flow accepted")
	}
}
