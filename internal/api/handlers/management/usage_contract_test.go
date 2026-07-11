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
	exportedJSON, err := json.Marshal(exported)
	if err != nil {
		t.Fatalf("marshal exported usage: %v", err)
	}
	if !bytes.Contains(exportedJSON, []byte(`"service_tier":"priority"`)) {
		t.Fatalf("exported usage missing service_tier: %s", exportedJSON)
	}
	var legacyDecoded struct {
		Version int `json:"version"`
		Usage   struct {
			TotalRequests int64 `json:"total_requests"`
			APIs          map[string]struct {
				Models map[string]struct {
					Details []struct {
						Source string           `json:"source"`
						Tokens usage.TokenStats `json:"tokens"`
					} `json:"details"`
				} `json:"models"`
			} `json:"apis"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(exportedJSON, &legacyDecoded); err != nil {
		t.Fatalf("legacy decoder rejected additive service_tier field: %v", err)
	}
	legacyDetails := legacyDecoded.Usage.APIs["panel-client-key"].Models["gpt-5.4"].Details
	if legacyDecoded.Version != 1 || legacyDecoded.Usage.TotalRequests != 1 || len(legacyDetails) != 1 || legacyDetails[0].Source != "auths/openai.json" || legacyDetails[0].Tokens.TotalTokens != 17 {
		t.Fatalf("legacy decoded export lost existing fields: version=%d usage=%+v details=%+v", legacyDecoded.Version, legacyDecoded.Usage, legacyDetails)
	}

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
		ServiceTier: "priority",
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
	if detail.ServiceTier != "priority" {
		t.Fatalf("detail.service_tier = %q, want priority", detail.ServiceTier)
	}
}

func TestUsageManagementImportLegacyExportKeepsServiceTierUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const legacyExport = `{
		"version": 1,
		"exported_at": "2026-06-10T12:00:00Z",
		"usage": {
			"total_requests": 1,
			"success_count": 1,
			"failure_count": 0,
			"total_tokens": 9,
			"apis": {
				"legacy-client-key": {
					"total_requests": 1,
					"total_tokens": 9,
					"models": {
						"gpt-5.4": {
							"total_requests": 1,
							"total_tokens": 9,
							"details": [{
								"timestamp": "2026-06-10T12:00:00Z",
								"latency_ms": 100,
								"source": "auths/legacy.json",
								"auth_index": "0",
								"tokens": {
									"input_tokens": 4,
									"output_tokens": 5,
									"reasoning_tokens": 0,
									"cached_tokens": 0,
									"total_tokens": 9
								},
								"failed": false
							}]
						}
					}
				}
			},
			"requests_by_day": {"2026-06-10": 1},
			"requests_by_hour": {"12": 1},
			"tokens_by_day": {"2026-06-10": 9},
			"tokens_by_hour": {"12": 9}
		}
	}`

	stats := usage.NewRequestStatistics()
	h := &Handler{}
	h.SetUsageStatistics(stats)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewBufferString(legacyExport))
	h.ImportUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy import status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var importResult struct {
		Added          int64 `json:"added"`
		Skipped        int64 `json:"skipped"`
		TotalRequests  int64 `json:"total_requests"`
		FailedRequests int64 `json:"failed_requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &importResult); err != nil {
		t.Fatalf("unmarshal legacy import response: %v body=%s", err, rec.Body.String())
	}
	if importResult.Added != 1 || importResult.Skipped != 0 || importResult.TotalRequests != 1 || importResult.FailedRequests != 0 {
		t.Fatalf("legacy import result = %+v, want added=1 skipped=0 total_requests=1 failed_requests=0", importResult)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["legacy-client-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("legacy details len = %d, want 1", len(details))
	}
	if details[0].ServiceTier != "" {
		t.Fatalf("legacy detail.service_tier = %q, want empty/unknown", details[0].ServiceTier)
	}
	if snapshot.TotalRequests != 1 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 0 || snapshot.TotalTokens != 9 {
		t.Fatalf("legacy snapshot totals = %+v, want requests=1 success=1 failure=0 tokens=9", snapshot)
	}
	if snapshot.RequestsByDay["2026-06-10"] != 1 || snapshot.RequestsByHour["12"] != 1 || snapshot.TokensByDay["2026-06-10"] != 9 || snapshot.TokensByHour["12"] != 9 {
		t.Fatalf("legacy snapshot buckets = requests/day:%v requests/hour:%v tokens/day:%v tokens/hour:%v", snapshot.RequestsByDay, snapshot.RequestsByHour, snapshot.TokensByDay, snapshot.TokensByHour)
	}

	reexported := exportUsageStatistics(t, h)
	if reexported.Version != 1 {
		t.Fatalf("legacy re-export version = %d, want 1", reexported.Version)
	}
	reexportedJSON, err := json.Marshal(reexported)
	if err != nil {
		t.Fatalf("marshal legacy re-export: %v", err)
	}
	if bytes.Contains(reexportedJSON, []byte(`"service_tier"`)) {
		t.Fatalf("legacy re-export fabricated service_tier: %s", reexportedJSON)
	}
}

func TestUsageManagementImportDeduplicatesLegacyAndCanonicalCacheCreation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	legacy := usageCacheCreationSnapshot(timestamp, usage.TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheCreationTokens: 1024,
		TotalTokens:         1210,
	})
	canonical := usageCacheCreationSnapshot(timestamp, usage.TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CacheCreationTokens: 1024,
		TotalTokens:         2234,
	})

	tests := []struct {
		name            string
		first           usage.StatisticsSnapshot
		second          usage.StatisticsSnapshot
		wantTotalTokens int64
	}{
		{
			name:            "legacy then canonical",
			first:           legacy,
			second:          canonical,
			wantTotalTokens: 1210,
		},
		{
			name:            "canonical then legacy",
			first:           canonical,
			second:          legacy,
			wantTotalTokens: 2234,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := usage.NewRequestStatistics()
			h := &Handler{}
			h.SetUsageStatistics(stats)

			firstResult := importUsageStatistics(t, h, tt.first)
			if firstResult.Added != 1 || firstResult.Skipped != 0 || firstResult.TotalRequests != 1 {
				t.Fatalf("first import result = %+v, want added=1 skipped=0 total_requests=1", firstResult)
			}

			secondResult := importUsageStatistics(t, h, tt.second)
			if secondResult.Added != 0 || secondResult.Skipped != 1 || secondResult.TotalRequests != 1 {
				t.Fatalf("second import result = %+v, want added=0 skipped=1 total_requests=1", secondResult)
			}

			snapshot := stats.Snapshot()
			model := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"]
			if snapshot.TotalRequests != 1 || snapshot.TotalTokens != tt.wantTotalTokens || len(model.Details) != 1 {
				t.Fatalf("snapshot after imports = requests:%d tokens:%d details:%d, want 1/%d/1", snapshot.TotalRequests, snapshot.TotalTokens, len(model.Details), tt.wantTotalTokens)
			}
			if snapshot.RequestsByDay["2026-07-11"] != 1 || snapshot.RequestsByHour["09"] != 1 {
				t.Fatalf("request buckets after imports = day:%v hour:%v, want one request", snapshot.RequestsByDay, snapshot.RequestsByHour)
			}
			if snapshot.TokensByDay["2026-07-11"] != tt.wantTotalTokens || snapshot.TokensByHour["09"] != tt.wantTotalTokens {
				t.Fatalf("token buckets after imports = day:%v hour:%v, want %d", snapshot.TokensByDay, snapshot.TokensByHour, tt.wantTotalTokens)
			}
		})
	}
}

func usageCacheCreationSnapshot(timestamp time.Time, tokens usage.TokenStats) usage.StatisticsSnapshot {
	return usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"cache-key": {
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.6-sol": {
						Details: []usage.RequestDetail{{
							Timestamp: timestamp,
							Source:    "auths/codex.json",
							AuthIndex: "0",
							Tokens:    tokens,
						}},
					},
				},
			},
		},
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
		ServiceTier:     "priority",
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
	if detail.ServiceTier != "priority" {
		t.Fatalf("detail.service_tier = %q, want priority", detail.ServiceTier)
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
