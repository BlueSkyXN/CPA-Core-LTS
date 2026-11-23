package usage

import (
	"context"
	"encoding/json"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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
