package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageManagementResponseShapeAndImportExportRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() { usage.SetStatisticsEnabled(prevEnabled) })

	stats := usage.NewRequestStatistics()
	recordPanelContractUsage(stats)

	h := &Handler{}
	h.SetUsageStatistics(stats)

	getPayload := readUsageStatisticsResponse(t, h)
	requirePanelUsageShape(t, getPayload.Usage)
	if getPayload.FailedRequests != 0 {
		t.Fatalf("failed_requests = %d, want 0", getPayload.FailedRequests)
	}

	exported := exportUsageStatistics(t, h)
	if exported.Version != 1 {
		t.Fatalf("export version = %d, want 1", exported.Version)
	}
	if exported.ExportedAt.IsZero() {
		t.Fatalf("exported_at is zero")
	}
	requirePanelUsageShape(t, exported.Usage)

	importStats := usage.NewRequestStatistics()
	importHandler := &Handler{}
	importHandler.SetUsageStatistics(importStats)
	importResult := importUsageStatistics(t, importHandler, exported.Usage)
	if importResult.Added != 1 || importResult.Skipped != 0 {
		t.Fatalf("import result = %+v, want added=1 skipped=0", importResult)
	}
	if importResult.TotalRequests != 1 || importResult.FailedRequests != 0 {
		t.Fatalf("import totals = %+v, want total_requests=1 failed_requests=0", importResult)
	}
	requirePanelUsageShape(t, importStats.Snapshot())
}

func TestUsageManagementFailedDetailIncludesFailureReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() { usage.SetStatisticsEnabled(prevEnabled) })

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "panel-client-key",
		Provider:    "codex",
		Model:       "gpt-5.5",
		Source:      "auths/codex.json",
		AuthIndex:   "1",
		RequestedAt: time.Date(2026, 6, 10, 11, 31, 0, 0, time.UTC),
		Failed:      true,
		Fail: coreusage.Failure{
			Body: "codex_abnormal_reasoning_response: codex abnormal reasoning response discarded",
		},
		Detail: coreusage.Detail{
			InputTokens:     1,
			OutputTokens:    2,
			ReasoningTokens: 516,
			TotalTokens:     3,
		},
	})

	h := &Handler{}
	h.SetUsageStatistics(stats)
	payload := readUsageStatisticsResponse(t, h)
	if payload.FailedRequests != 1 {
		t.Fatalf("failed_requests = %d, want 1", payload.FailedRequests)
	}
	modelSnapshot := payload.Usage.APIs["panel-client-key"].Models["gpt-5.5"]
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(modelSnapshot.Details))
	}
	detail := modelSnapshot.Details[0]
	if !detail.Failed {
		t.Fatalf("detail.failed = false, want true")
	}
	if detail.FailureReason != "codex_abnormal_reasoning_response" {
		t.Fatalf("detail.failure_reason = %q, want codex_abnormal_reasoning_response", detail.FailureReason)
	}
}

func recordPanelContractUsage(stats *usage.RequestStatistics) {
	stats.Record(context.Background(), coreusage.Record{
		APIKey:          "panel-client-key",
		Provider:        "openai",
		Model:           "gpt-5.4",
		Alias:           "panel-visible-model",
		Source:          "auths/openai.json",
		AuthIndex:       "1",
		ReasoningEffort: "medium",
		RequestedAt:     time.Date(2026, 6, 10, 11, 30, 0, 0, time.UTC),
		Latency:         2 * time.Second,
		Detail: coreusage.Detail{
			InputTokens:     5,
			OutputTokens:    7,
			ReasoningTokens: 3,
			CachedTokens:    2,
			TotalTokens:     17,
		},
	})
}

func readUsageStatisticsResponse(t *testing.T, h *Handler) struct {
	Usage          usage.StatisticsSnapshot `json:"usage"`
	FailedRequests int64                    `json:"failed_requests"`
} {
	t.Helper()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	h.GetUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal usage response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func exportUsageStatistics(t *testing.T, h *Handler) usageExportPayload {
	t.Helper()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	h.ExportUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload usageExportPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal export response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func importUsageStatistics(t *testing.T, h *Handler, snapshot usage.StatisticsSnapshot) struct {
	Added          int64 `json:"added"`
	Skipped        int64 `json:"skipped"`
	TotalRequests  int64 `json:"total_requests"`
	FailedRequests int64 `json:"failed_requests"`
} {
	t.Helper()

	body, err := json.Marshal(usageImportPayload{Version: 1, Usage: snapshot})
	if err != nil {
		t.Fatalf("marshal import payload: %v", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))

	h.ImportUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Added          int64 `json:"added"`
		Skipped        int64 `json:"skipped"`
		TotalRequests  int64 `json:"total_requests"`
		FailedRequests int64 `json:"failed_requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal import response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func requirePanelUsageShape(t *testing.T, snapshot usage.StatisticsSnapshot) {
	t.Helper()

	if snapshot.TotalRequests != 1 {
		t.Fatalf("usage.total_requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 1 {
		t.Fatalf("usage.success_count = %d, want 1", snapshot.SuccessCount)
	}
	if snapshot.FailureCount != 0 {
		t.Fatalf("usage.failure_count = %d, want 0", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 17 {
		t.Fatalf("usage.total_tokens = %d, want 17", snapshot.TotalTokens)
	}
	if snapshot.RequestsByDay["2026-06-10"] != 1 {
		t.Fatalf("usage.requests_by_day[2026-06-10] = %d, want 1", snapshot.RequestsByDay["2026-06-10"])
	}
	if snapshot.RequestsByHour["11"] != 1 {
		t.Fatalf("usage.requests_by_hour[11] = %d, want 1", snapshot.RequestsByHour["11"])
	}
	if snapshot.TokensByDay["2026-06-10"] != 17 {
		t.Fatalf("usage.tokens_by_day[2026-06-10] = %d, want 17", snapshot.TokensByDay["2026-06-10"])
	}

	apiSnapshot, ok := snapshot.APIs["panel-client-key"]
	if !ok {
		t.Fatalf("usage.apis missing panel-client-key: %#v", snapshot.APIs)
	}
	if apiSnapshot.TotalRequests != 1 || apiSnapshot.TotalTokens != 17 {
		t.Fatalf("api snapshot = %+v, want requests=1 tokens=17", apiSnapshot)
	}

	modelSnapshot, ok := apiSnapshot.Models["gpt-5.4"]
	if !ok {
		t.Fatalf("api.models missing gpt-5.4: %#v", apiSnapshot.Models)
	}
	if modelSnapshot.TotalRequests != 1 || modelSnapshot.TotalTokens != 17 {
		t.Fatalf("model snapshot = %+v, want requests=1 tokens=17", modelSnapshot)
	}
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("model.details len = %d, want 1", len(modelSnapshot.Details))
	}

	detail := modelSnapshot.Details[0]
	if detail.Source != "auths/openai.json" {
		t.Fatalf("detail.source = %q, want auths/openai.json", detail.Source)
	}
	if detail.AuthIndex != "1" {
		t.Fatalf("detail.auth_index = %q, want 1", detail.AuthIndex)
	}
	if detail.Alias != "panel-visible-model" {
		t.Fatalf("detail.alias = %q, want panel-visible-model", detail.Alias)
	}
	if detail.ReasoningEffort != "medium" {
		t.Fatalf("detail.reasoning_effort = %q, want medium", detail.ReasoningEffort)
	}
	if detail.LatencyMs != 2000 {
		t.Fatalf("detail.latency_ms = %d, want 2000", detail.LatencyMs)
	}
	if detail.Failed {
		t.Fatalf("detail.failed = true, want false")
	}
	if detail.Tokens.InputTokens != 5 ||
		detail.Tokens.OutputTokens != 7 ||
		detail.Tokens.ReasoningTokens != 3 ||
		detail.Tokens.CachedTokens != 2 ||
		detail.Tokens.TotalTokens != 17 {
		t.Fatalf("detail.tokens = %+v, want panel-compatible token breakdown", detail.Tokens)
	}
}
