package usage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageBusinessFailureReasonSnapshotImportIsIdempotent(t *testing.T) {
	previous := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previous) })
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "synthetic-client", Provider: "zcode-coding-plan", Model: "glm-5.3", AuthIndex: "synthetic-auth",
		RequestedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), Failed: true,
		Fail: coreusage.Failure{StatusCode: 502, Body: "bigmodel_1301"},
	})
	wire, err := json.Marshal(stats.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatisticsSnapshot
	if err = json.Unmarshal(wire, &snapshot); err != nil {
		t.Fatal(err)
	}
	target := NewRequestStatistics()
	for i := range 2 {
		result, err := target.MergeSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && result.Skipped != 1 {
			t.Fatalf("repeat import = %+v", result)
		}
	}
	details := target.Snapshot().APIs["synthetic-client"].Models["glm-5.3"].Details
	if len(details) != 1 || details[0].FailureStatus != 502 || details[0].FailureReason != "bigmodel_1301" {
		t.Fatalf("details = %+v", details)
	}
}
