package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsPreservesUsageProvenance(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Provider:        "codebuddy",
		Model:           "hy3-preview-agent",
		APIKey:          "test-key",
		UsageProvenance: coreusage.UsageProvenanceProviderReportedUnverified,
		Detail: coreusage.Detail{
			InputTokens:  20,
			OutputTokens: 3,
			TotalTokens:  23,
		},
	})
	details := stats.Snapshot().APIs["test-key"].Models["hy3-preview-agent"].Details
	if len(details) != 1 || details[0].UsageProvenance != coreusage.UsageProvenanceProviderReportedUnverified {
		t.Fatalf("details = %+v", details)
	}
}

func TestMergeSnapshotCanonicalizesUsageProvenance(t *testing.T) {
	stats := NewRequestStatistics()
	_, errMerge := stats.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{
		"qoder": {Models: map[string]ModelSnapshot{
			"qfmodel": {Details: []RequestDetail{{
				Timestamp: time.Now().UTC(), UsageProvenance: "vendor_guess",
			}}},
		}},
	}})
	if errMerge != nil {
		t.Fatal(errMerge)
	}
	details := stats.Snapshot().APIs["qoder"].Models["qfmodel"].Details
	if len(details) != 1 || details[0].UsageProvenance != "" {
		t.Fatalf("imported provenance = %#v, want unknown value cleared", details)
	}
}
