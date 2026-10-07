package usage

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// A record keeps its own outcome even when the inbound request later finishes
// with an error status (for example an earlier successful attempt or an
// independent image tool record).
func TestRequestStatisticsOutcomeComesFromRecordNotFinalStatus(t *testing.T) {
	ctx := internallogging.WithResponseStatusHolder(context.Background())
	internallogging.SetResponseStatus(ctx, http.StatusBadGateway)
	stats := NewRequestStatistics()
	base := coreusage.Record{APIKey: "outcome-key", Model: "gpt-5.5", RequestedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	stats.Record(ctx, base)
	failedByFlag := base
	failedByFlag.Failed = true
	stats.Record(ctx, failedByFlag)
	failedByStatus := base
	failedByStatus.Fail = coreusage.Failure{StatusCode: http.StatusTooManyRequests}
	stats.Record(context.Background(), failedByStatus)

	details := stats.Snapshot().APIs["outcome-key"].Models["gpt-5.5"].Details
	if len(details) != 3 {
		t.Fatalf("details len=%d, want 3", len(details))
	}
	want := []bool{false, true, true}
	for i, detail := range details {
		if detail.Failed != want[i] {
			t.Fatalf("detail %d failed=%t, want %t", i, detail.Failed, want[i])
		}
	}
}
