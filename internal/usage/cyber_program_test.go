package usage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageCyberProgramSnapshotQueryAndDedup(t *testing.T) {
	for _, program := range []string{"daybreak_blue", "standard", "unknown", ""} {
		t.Run(program, func(t *testing.T) {
			s := NewRequestStatistics()
			s.Record(context.Background(), coreusage.Record{
				APIKey: "synthetic-client", Model: "gpt-test", RequestedAt: time.Unix(1800000000, 0),
				EffectiveServiceTier: "priority",
				Detail:               coreusage.Detail{InputTokens: 2, OutputTokens: 1, TotalTokens: 3, ResponseCyberProgram: program},
			})
			raw, err := json.Marshal(s.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var snapshot StatisticsSnapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			imported := NewRequestStatistics()
			receipt, err := imported.MergeSnapshot(snapshot)
			if err != nil || receipt.Added != 1 {
				t.Fatalf("import: %+v %v", receipt, err)
			}
			bound := imported.QueryCapabilities()
			q := QueryRequest{Bound: bound.Bound, NowMS: bound.NowMS, Limit: 10}
			result, err := imported.QueryDetails(context.Background(), q)
			if err != nil || len(result.Items) != 1 {
				t.Fatalf("query: %+v %v", result, err)
			}
			if got := result.Items[0].Detail.ResponseCyberProgram; got != program {
				t.Fatalf("program=%q want=%q", got, program)
			}
			if tier, _ := queryServiceTier(result.Items[0].Detail); tier != "fast" {
				t.Fatalf("program changed tier: %q", tier)
			}
			details := snapshot.APIs["synthetic-client"].Models["gpt-test"].Details
			details[0].ResponseCyberProgram = "changed"
			receipt, err = imported.MergeSnapshot(snapshot)
			if err != nil || receipt.Added != 0 || receipt.Skipped != 1 {
				t.Fatalf("metadata changed identity: %+v %v", receipt, err)
			}
		})
	}
}
