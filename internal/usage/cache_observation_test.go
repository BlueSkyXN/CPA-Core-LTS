package usage

import (
	"context"
	"encoding/json"
	"fmt"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"strings"
	"testing"
	"time"
)

func TestCacheObservationExportImportAndIsolation(t *testing.T) {
	source := NewRequestStatistics()
	record := sizedUsageRecord()
	record.RequestID = "execution"
	record.DiagnosticsID = "trace"
	record.DiagnosticsInstanceID = "instance"
	record.Detail.CacheObservation = &coreusage.CacheObservation{Version: 1, Read: coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(0), Source: "input_tokens_details.cached_tokens"}, Write: coreusage.CacheTokenObservation{State: "absent"}}
	source.Record(context.Background(), record)
	*record.Detail.CacheObservation.Read.Value = 99
	snapshot := source.Snapshot()
	detail := snapshot.APIs["size-key"].Models["gpt-5.6"].Details[0]
	if *detail.CacheObservation.Read.Value != 0 || detail.DiagnosticsID != "trace" || detail.DiagnosticsInstanceID != "instance" || detail.RequestID != "execution" {
		t.Fatal("capture lost provenance or aliases caller")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var imported StatisticsSnapshot
	if err = json.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	target := NewRequestStatistics()
	requireMergeSnapshot(t, target, imported)
	got := target.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0]
	if got.CacheObservation.Read.State != "present" || *got.CacheObservation.Read.Value != 0 || got.CacheObservation.Write.State != "absent" || got.DiagnosticsInstanceID != "instance" {
		t.Fatal("roundtrip lost evidence")
	}
	*detail.CacheObservation.Read.Value = 22
	if *source.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0].CacheObservation.Read.Value != 0 {
		t.Fatal("snapshot aliases store")
	}
	got.CacheObservation.Read.State = "absent"
	imported.APIs["size-key"].Models["gpt-5.6"].Details[0].CacheObservation.Read.State = "absent"
	if imported.ValidateCanonicalV3TokenStats() == nil {
		t.Fatal("canonical import accepted a contradictory cache field")
	}
	if got.CacheObservation.Valid() {
		t.Fatal("contradictory imported evidence accepted")
	}
	// Old records keep numeric totals and absent provenance.
	record.Detail.CacheObservation = nil
	record.RequestedAt = record.RequestedAt.Add(1)
	legacy := NewRequestStatistics()
	legacy.Record(context.Background(), record)
	if legacy.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0].CacheObservation != nil {
		t.Fatal("legacy observation invented")
	}
}

func TestUsageCacheRouteRoundTripAndScope(t *testing.T) {
	source := NewRequestStatistics()
	start := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	digest, scope := strings.Repeat("a", 64), strings.Repeat("b", 64)
	target := coreusage.CacheRouteKey{Kind: "pck", Digest: digest, Scope: scope}
	for i, key := range []coreusage.CacheRouteKey{
		target,
		{Kind: "pck", Digest: digest[:63] + "c", Scope: scope},
		{Kind: "pck", Digest: digest, Scope: strings.Repeat("c", 64)},
		{Kind: "input_session", Digest: digest, Scope: scope},
		{Kind: "pck", Digest: digest},
	} {
		meta := &coreusage.CacheRouteSummary{Version: 1, AttemptCount: 1,
			References: []coreusage.CacheRouteReference{{CacheRouteKey: key, Method: "client"}}}
		source.Record(t.Context(), coreusage.Record{APIKey: "fixture", Model: "synthetic", CacheRoute: meta,
			DiagnosticsID: strings.Repeat("d", 64), DiagnosticsInstanceID: "fixture-origin",
			RequestID: fmt.Sprintf("cache-key-%d", i), RequestedAt: start.Add(time.Duration(i) * time.Second)})
		meta.References[0].Digest = strings.Repeat("f", 64)
	}
	wire, err := json.Marshal(source.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(wire, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateCanonicalV3TokenStats(); err != nil {
		t.Fatal(err)
	}
	reloaded := NewRequestStatistics()
	for _, added := range []int64{5, 0} {
		receipt, err := reloaded.MergeSnapshot(snapshot)
		if err != nil || receipt.Added != added {
			t.Fatalf("merge %+v: %v", receipt, err)
		}
	}
	q := QueryRequest{Filter: QueryFilter{CacheRoute: &target}}
	page, err := reloaded.QueryDetails(t.Context(), q)
	if err != nil || page.Total != 1 || page.Items[0].Detail.RequestID != "cache-key-0" {
		t.Fatalf("scoped route mismatch: %+v %v", page, err)
	}
	page.Items[0].Detail.CacheRoute.References[0].Digest = ""
	exported, err := reloaded.QueryExport(t.Context(), q)
	if err != nil || exported.Total != 1 || exported.Items[0].Detail.RequestID != "cache-key-0" {
		t.Fatalf("route snapshot was mutated: %+v %v", exported, err)
	}
}
