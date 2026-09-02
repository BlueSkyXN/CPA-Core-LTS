package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	getPayload, getJSON := readUsageStatisticsResponse(t, h)
	requirePanelUsageShape(t, getPayload.Usage)
	requireCanonicalReasoningEffortJSON(t, getJSON, "GET usage response")
	if getPayload.FailedRequests != 0 {
		t.Fatalf("failed_requests = %d, want 0", getPayload.FailedRequests)
	}

	exported := exportUsageStatistics(t, h)
	if exported.Version != 3 {
		t.Fatalf("export version = %d, want 3", exported.Version)
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
	if !bytes.Contains(exportedJSON, []byte(`"request_service_tier":"priority"`)) {
		t.Fatalf("exported usage missing request_service_tier: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"outbound_service_tier":"priority"`)) {
		t.Fatalf("exported usage missing outbound_service_tier: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"response_service_tier":"standard"`)) {
		t.Fatalf("exported usage missing response_service_tier: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"effective_service_tier":"standard"`)) {
		t.Fatalf("exported usage missing effective_service_tier: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"generate":false`)) {
		t.Fatalf("exported usage missing explicit generate=false: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"ttfb_ms":500`)) {
		t.Fatalf("exported usage missing ttfb_ms: %s", exportedJSON)
	}
	if !bytes.Contains(exportedJSON, []byte(`"timing_version":1`)) ||
		!bytes.Contains(exportedJSON, []byte(`"ttft_ms":900`)) ||
		!bytes.Contains(exportedJSON, []byte(`"ttfa_ms":1500`)) {
		t.Fatalf("exported usage missing canonical semantic timing: %s", exportedJSON)
	}
	requireCanonicalReasoningEffortJSON(t, exportedJSON, "exported usage")
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
		t.Fatalf("legacy decoder rejected additive service-tier fields: %v", err)
	}
	legacyDetails := legacyDecoded.Usage.APIs["panel-client-key"].Models["gpt-5.4"].Details
	if legacyDecoded.Version != 3 || legacyDecoded.Usage.TotalRequests != 1 || len(legacyDetails) != 1 || legacyDetails[0].Source != "auths/openai.json" || legacyDetails[0].Tokens.TotalTokens != 17 {
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
	if importResult.SchemaVersion != 3 || importResult.MigratedFromVersion != 0 || importResult.Migration != "" {
		t.Fatalf("canonical import receipt = %+v, want schema_version=3 without migration fields", importResult)
	}
	reimported := exportUsageStatistics(t, importHandler)
	requirePanelUsageShape(t, reimported.Usage)
	reimportedJSON, err := json.Marshal(reimported)
	if err != nil {
		t.Fatalf("marshal re-exported usage: %v", err)
	}
	requireCanonicalReasoningEffortJSON(t, reimportedJSON, "usage re-exported after import")
}

func TestUsageManagementTimingV3MatrixAndAtomicRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := map[string]any{
		"version": 3,
		"usage": map[string]any{
			"apis": map[string]any{
				"timing-client": map[string]any{
					"models": map[string]any{
						"gpt-5.6-sol": map[string]any{
							"details": []any{map[string]any{
								"timestamp":      "2026-09-02T12:00:00Z",
								"latency_ms":     2000,
								"timing_version": 1,
								"ttfb_ms":        500,
								"ttft_ms":        900,
								"ttfa_ms":        1500,
								"tokens": map[string]any{
									"input_tokens":          10,
									"output_tokens":         20,
									"reasoning_tokens":      5,
									"cached_tokens":         0,
									"cache_read_tokens":     0,
									"cache_creation_tokens": 0,
									"total_tokens":          30,
								},
							}},
						},
					},
				},
			},
		},
	}
	validPayload, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid v3 payload: %v", err)
	}
	stats := usage.NewRequestStatistics()
	h := &Handler{}
	h.SetUsageStatistics(stats)
	validResult := performUsageImport(h, validPayload)
	if validResult.Code != http.StatusOK {
		t.Fatalf("valid v3 import status = %d body=%s", validResult.Code, validResult.Body.String())
	}
	var receipt struct {
		SchemaVersion       int      `json:"schema_version"`
		MigratedFromVersion int      `json:"migrated_from_version"`
		Migrations          []string `json:"migrations"`
	}
	if err := json.Unmarshal(validResult.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode valid v3 receipt: %v", err)
	}
	if receipt.SchemaVersion != 3 || receipt.MigratedFromVersion != 0 || len(receipt.Migrations) != 0 {
		t.Fatalf("valid v3 receipt = %+v, want direct v3 receipt", receipt)
	}

	for _, version := range []int{1, 2} {
		legacy := map[string]any{}
		if err := json.Unmarshal(validPayload, &legacy); err != nil {
			t.Fatalf("clone valid payload: %v", err)
		}
		legacy["version"] = version
		legacyUsage := legacy["usage"].(map[string]any)
		legacyAPI := legacyUsage["apis"].(map[string]any)
		legacyClient := legacyAPI["timing-client"].(map[string]any)
		legacyModel := legacyClient["models"].(map[string]any)
		legacyDetail := legacyModel["gpt-5.6-sol"].(map[string]any)["details"].([]any)[0].(map[string]any)
		before := stats.Snapshot()
		result := performUsageImport(h, mustMarshalUsagePayload(t, legacy))
		if result.Code != http.StatusBadRequest {
			t.Fatalf("legacy v%d semantic timing status = %d body=%s", version, result.Code, result.Body.String())
		}
		var errorBody struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &errorBody); err != nil {
			t.Fatalf("decode legacy v%d error: %v", version, err)
		}
		wantCode := usageCodeV1TimingAmbiguous
		if version == 2 {
			wantCode = usageCodeV2TimingAmbiguous
		}
		if errorBody.Code != wantCode {
			t.Fatalf("legacy v%d error code = %q, want %q", version, errorBody.Code, wantCode)
		}
		if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
			t.Fatalf("legacy v%d rejection mutated snapshot: before=%+v after=%+v", version, before, after)
		}
		delete(legacyDetail, "timing_version")
		delete(legacyDetail, "ttft_ms")
		delete(legacyDetail, "ttfa_ms")
		legacyDetail["latency_ms"] = -1
		negativeLatencyResult := performUsageImport(h, mustMarshalUsagePayload(t, legacy))
		if negativeLatencyResult.Code != http.StatusBadRequest {
			t.Fatalf("legacy v%d negative latency status = %d body=%s", version, negativeLatencyResult.Code, negativeLatencyResult.Body.String())
		}
		var negativeLatencyError struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(negativeLatencyResult.Body.Bytes(), &negativeLatencyError); err != nil {
			t.Fatalf("decode legacy v%d negative latency error: %v", version, err)
		}
		if negativeLatencyError.Code != usageCodeShapeInvalid {
			t.Fatalf("legacy v%d negative latency error code = %q, want %q", version, negativeLatencyError.Code, usageCodeShapeInvalid)
		}
		legacyDetail["latency_ms"] = 2000
		migratedResult := performUsageImport(h, mustMarshalUsagePayload(t, legacy))
		if migratedResult.Code != http.StatusOK {
			t.Fatalf("legacy v%d token/timing-compatible import status = %d body=%s", version, migratedResult.Code, migratedResult.Body.String())
		}
		var migratedReceipt struct {
			SchemaVersion       int      `json:"schema_version"`
			MigratedFromVersion int      `json:"migrated_from_version"`
			Migrations          []string `json:"migrations"`
		}
		if err := json.Unmarshal(migratedResult.Body.Bytes(), &migratedReceipt); err != nil {
			t.Fatalf("decode legacy v%d migration receipt: %v", version, err)
		}
		wantMigrations := []string{usageV2TimingMigrationName}
		if version == 1 {
			wantMigrations = []string{usageV1MigrationName, usageV2TimingMigrationName}
		}
		if migratedReceipt.SchemaVersion != 3 || migratedReceipt.MigratedFromVersion != version || !reflect.DeepEqual(migratedReceipt.Migrations, wantMigrations) {
			t.Fatalf("legacy v%d migration receipt = %+v, want schema_version=3 migrated_from_version=%d migrations=%v", version, migratedReceipt, version, wantMigrations)
		}
	}

	invalid := map[string]any{}
	if err := json.Unmarshal(validPayload, &invalid); err != nil {
		t.Fatalf("clone valid payload for invalid v3: %v", err)
	}
	invalidUsage := invalid["usage"].(map[string]any)
	invalidAPI := invalidUsage["apis"].(map[string]any)
	invalidClient := invalidAPI["timing-client"].(map[string]any)
	invalidModel := invalidClient["models"].(map[string]any)
	invalidDetail := invalidModel["gpt-5.6-sol"].(map[string]any)["details"].([]any)[0].(map[string]any)
	invalidDetail["ttfa_ms"] = 2500
	before := stats.Snapshot()
	invalidResult := performUsageImport(h, mustMarshalUsagePayload(t, invalid))
	if invalidResult.Code != http.StatusBadRequest {
		t.Fatalf("invalid v3 timing status = %d body=%s", invalidResult.Code, invalidResult.Body.String())
	}
	var invalidError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(invalidResult.Body.Bytes(), &invalidError); err != nil {
		t.Fatalf("decode invalid v3 timing error: %v", err)
	}
	if invalidError.Code != usageCodeV3TimingInvalid {
		t.Fatalf("invalid v3 timing error code = %q, want %q", invalidError.Code, usageCodeV3TimingInvalid)
	}
	if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid v3 timing rejection mutated snapshot: before=%+v after=%+v", before, after)
	}
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
	payload, _ := readUsageStatisticsResponse(t, h)
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

func TestUsageManagementProducerSnapshotsAlwaysRoundTripAsCanonicalV3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() { usage.SetStatisticsEnabled(prevEnabled) })

	tests := []struct {
		name        string
		records     []coreusage.Record
		wantTotals  []int64
		wantOverall int64
	}{
		{
			name: "OpenAI reasoning is an output subset",
			records: []coreusage.Record{{
				Provider: "openai", Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5},
			}},
			wantTotals:  []int64{120},
			wantOverall: 120,
		},
		{
			name: "explicit total below minimum is repaired",
			records: []coreusage.Record{{
				Provider: "openai", Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 2, TotalTokens: 1},
			}},
			wantTotals:  []int64{12},
			wantOverall: 12,
		},
		{
			name: "unrepresentable detail becomes canonical zero vector",
			records: []coreusage.Record{{
				Provider: "openai", Detail: coreusage.Detail{InputTokens: math.MaxInt64, OutputTokens: 1},
			}},
			wantTotals: []int64{0},
		},
		{
			name: "record aggregate overflow preserves request with zero tokens",
			records: []coreusage.Record{
				{Provider: "openai", Detail: coreusage.Detail{InputTokens: math.MaxInt64, TotalTokens: math.MaxInt64}},
				{Provider: "openai", Detail: coreusage.Detail{InputTokens: 1, TotalTokens: 1}},
			},
			wantTotals:  []int64{math.MaxInt64, 0},
			wantOverall: math.MaxInt64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := usage.NewRequestStatistics()
			for index, record := range tt.records {
				record.APIKey = "producer-key"
				record.Model = "producer-model"
				record.RequestedAt = time.Date(2026, 7, 22, 12, index, 0, 0, time.UTC)
				stats.Record(context.Background(), record)
			}

			h := &Handler{}
			h.SetUsageStatistics(stats)
			exported := exportUsageStatistics(t, h)
			model := exported.Usage.APIs["producer-key"].Models["producer-model"]
			if exported.Version != 3 || len(model.Details) != len(tt.wantTotals) {
				t.Fatalf("export = version:%d details:%d, want version 3 and %d details", exported.Version, len(model.Details), len(tt.wantTotals))
			}
			if exported.Usage.TotalTokens != tt.wantOverall {
				t.Fatalf("export total_tokens = %d, want %d", exported.Usage.TotalTokens, tt.wantOverall)
			}
			for index, wantTotal := range tt.wantTotals {
				if got := model.Details[index].Tokens; got.TotalTokens != wantTotal {
					t.Fatalf("detail %d tokens = %+v, want total_tokens=%d", index, got, wantTotal)
				}
			}

			fresh := usage.NewRequestStatistics()
			freshHandler := &Handler{}
			freshHandler.SetUsageStatistics(fresh)
			receipt := importUsageStatistics(t, freshHandler, exported.Usage)
			if receipt.Added != int64(len(tt.wantTotals)) || receipt.Skipped != 0 || receipt.SchemaVersion != 3 {
				t.Fatalf("roundtrip receipt = %+v, want all producer details imported as canonical v3", receipt)
			}
			if got := fresh.Snapshot(); !reflect.DeepEqual(got, exported.Usage) {
				t.Fatalf("roundtrip snapshot mismatch: exported=%+v imported=%+v", exported.Usage, got)
			}
		})
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
								"billing_basis": "api-token-usd",
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
		Added               int64    `json:"added"`
		Skipped             int64    `json:"skipped"`
		TotalRequests       int64    `json:"total_requests"`
		FailedRequests      int64    `json:"failed_requests"`
		SchemaVersion       int      `json:"schema_version"`
		MigratedFromVersion int      `json:"migrated_from_version"`
		Migrations          []string `json:"migrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &importResult); err != nil {
		t.Fatalf("unmarshal legacy import response: %v body=%s", err, rec.Body.String())
	}
	if importResult.Added != 1 || importResult.Skipped != 0 || importResult.TotalRequests != 1 || importResult.FailedRequests != 0 {
		t.Fatalf("legacy import result = %+v, want added=1 skipped=0 total_requests=1 failed_requests=0", importResult)
	}
	if importResult.SchemaVersion != 3 || importResult.MigratedFromVersion != 1 || !reflect.DeepEqual(importResult.Migrations, []string{"v1_uncached_input_tokens_to_v2", "v2_timing_contract_to_v3"}) {
		t.Fatalf("legacy import migration receipt = %+v, want v1-to-v3 receipt", importResult)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["legacy-client-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("legacy details len = %d, want 1", len(details))
	}
	if details[0].ServiceTier != "" {
		t.Fatalf("legacy detail.service_tier = %q, want empty/unknown", details[0].ServiceTier)
	}
	if details[0].RequestServiceTier != "" || details[0].OutboundServiceTier != "" || details[0].ResponseServiceTier != "" || details[0].EffectiveServiceTier != "" {
		t.Fatalf("legacy detail service tiers = request:%q outbound:%q response:%q effective:%q, want empty/unknown", details[0].RequestServiceTier, details[0].OutboundServiceTier, details[0].ResponseServiceTier, details[0].EffectiveServiceTier)
	}
	if !details[0].Generate {
		t.Fatalf("legacy detail.generate = false, want backward-compatible true")
	}
	if snapshot.TotalRequests != 1 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 0 || snapshot.TotalTokens != 9 {
		t.Fatalf("legacy snapshot totals = %+v, want requests=1 success=1 failure=0 tokens=9", snapshot)
	}
	if snapshot.RequestsByDay["2026-06-10"] != 1 || snapshot.RequestsByHour["12"] != 1 || snapshot.TokensByDay["2026-06-10"] != 9 || snapshot.TokensByHour["12"] != 9 {
		t.Fatalf("legacy snapshot buckets = requests/day:%v requests/hour:%v tokens/day:%v tokens/hour:%v", snapshot.RequestsByDay, snapshot.RequestsByHour, snapshot.TokensByDay, snapshot.TokensByHour)
	}

	reexported := exportUsageStatistics(t, h)
	if reexported.Version != 3 {
		t.Fatalf("legacy re-export version = %d, want 3", reexported.Version)
	}
	reexportedJSON, err := json.Marshal(reexported)
	if err != nil {
		t.Fatalf("marshal legacy re-export: %v", err)
	}
	if bytes.Contains(reexportedJSON, []byte(`"service_tier"`)) {
		t.Fatalf("legacy re-export fabricated service_tier: %s", reexportedJSON)
	}
	if !bytes.Contains(reexportedJSON, []byte(`"generate":true`)) {
		t.Fatalf("legacy re-export missing normalized generate=true: %s", reexportedJSON)
	}
	if bytes.Contains(reexportedJSON, []byte(`"request_service_tier"`)) || bytes.Contains(reexportedJSON, []byte(`"outbound_service_tier"`)) || bytes.Contains(reexportedJSON, []byte(`"response_service_tier"`)) || bytes.Contains(reexportedJSON, []byte(`"effective_service_tier"`)) {
		t.Fatalf("legacy re-export fabricated explicit service-tier fields: %s", reexportedJSON)
	}
	if bytes.Contains(reexportedJSON, []byte(`"uncached_input_tokens"`)) {
		t.Fatalf("legacy re-export fabricated uncached_input_tokens: %s", reexportedJSON)
	}
	if bytes.Contains(reexportedJSON, []byte(`"billing_basis"`)) {
		t.Fatalf("legacy re-export retained removed billing_basis: %s", reexportedJSON)
	}
}

func TestUsageManagementImportMigratesReleasedV1UncachedInputTokensAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const legacyExport = `{
		"version": 1,
		"usage": {
			"apis": {
				"legacy-client-key": {
					"models": {
					"claude-sonnet": {
						"details": [{
							"timestamp": "2026-07-21T11:59:00Z",
				"tokens": {
					"input_tokens": 10,
					"output_tokens": 1,
					"reasoning_tokens": 0,
					"cached_tokens": 0,
					"total_tokens": 11
							},
							"failed": false
						}, {
							"timestamp": "2026-07-21T12:00:00Z",
					"tokens": {
						"input_tokens": 3085,
						"output_tokens": 253,
						"reasoning_tokens": 0,
						"cached_tokens": 7,
						"cache_read_tokens": 7,
									"cache_creation_tokens": 19514,
									"uncached_input_tokens": 3085,
									"total_tokens": 22859
								},
								"failed": false
							}]
						}
					}
				}
			}
		}
	}`

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "existing-client-key",
		Model:       "existing-model",
		RequestedAt: time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
	})
	wantSnapshot := stats.Snapshot()
	h := &Handler{}
	h.SetUsageStatistics(stats)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/usage/import",
		bytes.NewBufferString(legacyExport),
	)
	h.ImportUsageStatistics(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("legacy token-contract import status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Added               int64    `json:"added"`
		SchemaVersion       int      `json:"schema_version"`
		MigratedFromVersion int      `json:"migrated_from_version"`
		Migrations          []string `json:"migrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal legacy token-contract migration receipt: %v body=%s", err, rec.Body.String())
	}
	if response.Added != 2 || response.SchemaVersion != 3 || response.MigratedFromVersion != 1 || !reflect.DeepEqual(response.Migrations, []string{"v1_uncached_input_tokens_to_v2", "v2_timing_contract_to_v3"}) {
		t.Fatalf("legacy token-contract migration receipt = %+v", response)
	}
	snapshot := stats.Snapshot()
	if reflect.DeepEqual(snapshot, wantSnapshot) || snapshot.TotalRequests != wantSnapshot.TotalRequests+2 {
		t.Fatalf("migrated legacy import snapshot = %+v, want two added details", snapshot)
	}
	details := snapshot.APIs["legacy-client-key"].Models["claude-sonnet"].Details
	if len(details) != 2 {
		t.Fatalf("migrated legacy detail count = %d, want 2", len(details))
	}
	if details[0].Tokens.InputTokens != 10 || details[0].Tokens.CacheReadTokens != 0 || details[0].Tokens.CacheCreationTokens != 0 {
		t.Fatalf("markerless no-cache v1 migration = %+v", details[0].Tokens)
	}
	if details[1].Tokens.InputTokens != 22606 || details[1].Tokens.CachedTokens != 7 || details[1].Tokens.CacheReadTokens != 7 || details[1].Tokens.CacheCreationTokens != 19514 {
		t.Fatalf("marker-bearing cached v1 migration = %+v", details[1].Tokens)
	}

	beforeDuplicate := stats.Snapshot()
	repeat := performUsageImport(h, []byte(legacyExport))
	if repeat.Code != http.StatusOK {
		t.Fatalf("duplicate v1 import status = %d, want %d body=%s", repeat.Code, http.StatusOK, repeat.Body.String())
	}
	var repeatResult struct {
		Added         int64 `json:"added"`
		Skipped       int64 `json:"skipped"`
		TotalRequests int64 `json:"total_requests"`
		SchemaVersion int   `json:"schema_version"`
	}
	if err := json.Unmarshal(repeat.Body.Bytes(), &repeatResult); err != nil {
		t.Fatalf("unmarshal duplicate v1 receipt: %v body=%s", err, repeat.Body.String())
	}
	if repeatResult.Added != 0 || repeatResult.Skipped != 2 || repeatResult.TotalRequests != 3 || repeatResult.SchemaVersion != 3 {
		t.Fatalf("duplicate v1 import result = %+v, want added=0 skipped=2 total_requests=3 schema_version=3", repeatResult)
	}
	if afterDuplicate := stats.Snapshot(); !reflect.DeepEqual(afterDuplicate, beforeDuplicate) {
		t.Fatalf("duplicate v1 import mutated statistics: before=%+v after=%+v", beforeDuplicate, afterDuplicate)
	}

	canonical := exportUsageStatistics(t, h)
	canonicalResult := importUsageStatistics(t, h, canonical.Usage)
	if canonicalResult.Added != 0 || canonicalResult.Skipped != 3 || canonicalResult.TotalRequests != 3 {
		t.Fatalf("canonical re-import after v1 migration = %+v, want added=0 skipped=3 total_requests=3", canonicalResult)
	}
	if afterCanonical := stats.Snapshot(); !reflect.DeepEqual(afterCanonical, beforeDuplicate) {
		t.Fatalf("canonical re-import after v1 migration mutated statistics: before=%+v after=%+v", beforeDuplicate, afterCanonical)
	}
}

func TestUsageManagementImportRejectsAmbiguousMarkerlessV1CacheTokensAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const payload = `{
		"version": 1,
		"usage": {"apis": {"legacy-client-key": {"models": {"claude-sonnet": {"details": [{
			"timestamp": "2026-07-21T12:00:00Z",
			"tokens": {
				"input_tokens": 1200,
				"output_tokens": 10,
				"reasoning_tokens": 0,
				"cached_tokens": 1024,
				"cache_creation_tokens": 1024,
				"total_tokens": 1210
			}
		}]}}}}}
	}`

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "existing-client-key", Model: "existing-model", RequestedAt: time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
	})
	wantSnapshot := stats.Snapshot()
	h := &Handler{}
	h.SetUsageStatistics(stats)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewBufferString(payload))
	h.ImportUsageStatistics(ginCtx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous v1 import status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var response struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal ambiguous v1 rejection: %v body=%s", err, rec.Body.String())
	}
	if response.Code != "usage_v1_cache_semantics_ambiguous" {
		t.Fatalf("ambiguous v1 code = %q", response.Code)
	}
	if snapshot := stats.Snapshot(); !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("ambiguous v1 import mutated statistics: got=%+v want=%+v", snapshot, wantSnapshot)
	}
}

func TestUsageManagementImportDistinguishesMissingAndExplicitZeroV2TokenFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	missingFields := []string{"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "total_tokens"}
	complete := map[string]int64{
		"input_tokens": 0, "output_tokens": 0, "reasoning_tokens": 0, "cached_tokens": 0, "total_tokens": 0,
	}

	for _, missing := range missingFields {
		t.Run("missing_"+missing, func(t *testing.T) {
			tokens := make(map[string]int64, len(complete)-1)
			for key, value := range complete {
				if key != missing {
					tokens[key] = value
				}
			}
			payload := usageImportFixtureJSON(t, 2, tokens)
			stats, h, before := seededUsageImportHandler()
			rec := performUsageImport(h, payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("missing %s status = %d, want %d body=%s", missing, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var response struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal missing-field rejection: %v", err)
			}
			if response.Code != "usage_v2_token_contract_invalid" {
				t.Fatalf("missing %s code = %q", missing, response.Code)
			}
			if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("missing %s import mutated statistics: before=%+v after=%+v", missing, before, after)
			}
		})
	}

	t.Run("explicit_zero", func(t *testing.T) {
		_, h, _ := seededUsageImportHandler()
		rec := performUsageImport(h, usageImportFixtureJSON(t, 2, complete))
		if rec.Code != http.StatusOK {
			t.Fatalf("explicit-zero v2 status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

func TestUsageManagementImportReturnsStableSchemaErrorCodesAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		payload  string
		wantCode string
	}{
		{
			name:     "invalid JSON",
			payload:  `{"version":2`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "invalid null root",
			payload:  `null`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "invalid array root",
			payload:  `[]`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "invalid version type",
			payload:  `{"version":null,"usage":{"apis":{}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "unversioned payload",
			payload:  `{"usage":{"apis":{}}}`,
			wantCode: "usage_version_unsupported",
		},
		{
			name:     "unsupported version",
			payload:  `{"version":0,"usage":{"apis":{}}}`,
			wantCode: "usage_version_unsupported",
		},
		{
			name: "case-colliding version alias",
			payload: `{"version":2,"Version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":1,"uncached_input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}
			}]}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "case-colliding usage alias",
			payload: `{"version":2,"usage":{"apis":{}},"Usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}
			}]}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "duplicate exact usage field",
			payload: `{"version":2,
				"usage":{"apis":{"client":{"models":{"model":{"details":[{"timestamp":"2026-07-21T12:00:00Z","tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}}]}}}}},
				"usage":{"apis":{}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "case-colliding details alias",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{
				"details":[],
				"Details":[{"timestamp":"2026-07-21T12:00:00Z","tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}}]
			}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "case-colliding tokens alias",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0},
				"Tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}
			}]}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "missing APIs",
			payload:  `{"version":2,"usage":{}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null API container",
			payload:  `{"version":2,"usage":{"apis":{"client":null}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null models container",
			payload:  `{"version":2,"usage":{"apis":{"client":{"models":null}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null details container",
			payload:  `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":null}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null detail entry",
			payload:  `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[null]}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null model container",
			payload:  `{"version":2,"usage":{"apis":{"client":{"models":{"model":null}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "missing details container",
			payload:  `{"version":2,"usage":{"apis":{"client":{"models":{"model":{}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "null aggregate map",
			payload:  `{"version":2,"usage":{"apis":{},"requests_by_day":null}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name:     "blank API identity",
			payload:  `{"version":2,"usage":{"apis":{"  ":{"models":{}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "invalid nested typed shape",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":17,
				"tokens":{"input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}
			}]}}}}}}`,
			wantCode: "usage_shape_invalid",
		},
		{
			name: "missing released v1 required field",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy optional reasoning is null",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"output_tokens":1,"reasoning_tokens":null,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy optional cached field is mistyped",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"output_tokens":1,"cached_tokens":"0","total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy optional cache read is negative",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"output_tokens":1,"cache_read_tokens":-1,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy optional cache creation is fractional",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"output_tokens":1,"cache_creation_tokens":0.5,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy marker exceeds released input",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"uncached_input_tokens":4,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy marker is null",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"uncached_input_tokens":null,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy marker is non-integral",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"uncached_input_tokens":1.5,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy marker is negative",
			payload: `{"version":1,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":3,"uncached_input_tokens":-1,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":4}
			}]}}}}}}`,
			wantCode: "usage_v1_token_contract_invalid",
		},
		{
			name: "legacy field in canonical payload",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":0,"uncached_input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical mandatory field is null",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":null,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical tokens object is empty",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical tokens object is missing",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z"
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical mandatory field has wrong type",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":"0","output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":0}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical cache relationship overflows int64",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":9223372036854775807,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":9223372036854775807,"cache_read_tokens":9223372036854775807,"cache_creation_tokens":1,"total_tokens":9223372036854775807}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "canonical minimum total relationship overflows int64",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":9223372036854775807,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":9223372036854775807}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
		{
			name: "invalid canonical relationships",
			payload: `{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{
				"timestamp":"2026-07-21T12:00:00Z",
				"tokens":{"input_tokens":10,"output_tokens":2,"reasoning_tokens":0,"cached_tokens":9,"cache_read_tokens":9,"cache_creation_tokens":2,"total_tokens":12}
			}]}}}}}}`,
			wantCode: "usage_v2_token_contract_invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats, h, before := seededUsageImportHandler()
			rec := performUsageImport(h, []byte(tt.payload))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var response struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal schema rejection: %v body=%s", err, rec.Body.String())
			}
			if response.Error == "" || response.Code != tt.wantCode {
				t.Fatalf("schema rejection = %+v, want code %q", response, tt.wantCode)
			}
			if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("schema rejection mutated statistics: before=%+v after=%+v", before, after)
			}
		})
	}

}

func TestUsageManagementImportMapsUnavailableAndReadFailureToShapeInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		handler *Handler
		body    io.Reader
	}{
		{
			name: "usage store unavailable",
			body: bytes.NewBufferString(`{"version":2,"usage":{"apis":{}}}`),
		},
		{
			name: "request body read failure",
			handler: func() *Handler {
				h := &Handler{}
				h.SetUsageStatistics(usage.NewRequestStatistics())
				return h
			}(),
			body: failingUsageImportReader{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(rec)
			ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", tt.body)
			tt.handler.ImportUsageStatistics(ginCtx)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var response struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("unmarshal rejection: %v body=%s", err, rec.Body.String())
			}
			if response.Code != "usage_shape_invalid" {
				t.Fatalf("code = %q, want usage_shape_invalid", response.Code)
			}
		})
	}
}

func TestUsageManagementImportRejectsAggregateOverflowAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	h := &Handler{}
	h.SetUsageStatistics(stats)

	first := usageImportFixtureJSON(t, 2, map[string]int64{
		"input_tokens": math.MaxInt64, "output_tokens": 0, "reasoning_tokens": 0, "cached_tokens": 0, "total_tokens": math.MaxInt64,
	})
	if rec := performUsageImport(h, first); rec.Code != http.StatusOK {
		t.Fatalf("seed max-token import status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	before := stats.Snapshot()

	second := usageImportFixtureJSONAt(t, 2, map[string]int64{
		"input_tokens": 1, "output_tokens": 0, "reasoning_tokens": 0, "cached_tokens": 0, "total_tokens": 1,
	}, "2026-07-21T12:01:00Z")
	rec := performUsageImport(h, second)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("overflow import status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var response struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal overflow rejection: %v", err)
	}
	if response.Code != "usage_aggregate_overflow" {
		t.Fatalf("overflow code = %q", response.Code)
	}
	if after := stats.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("overflow import mutated statistics: before=%+v after=%+v", before, after)
	}
}

func TestUsageManagementImportPreservesUncertainTimestampForDeterministicReimport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name      string
		timestamp string
	}{
		{name: "missing timestamp"},
		{name: "null timestamp", timestamp: `,"timestamp":null`},
		{name: "Go zero timestamp", timestamp: `,"timestamp":"0001-01-01T00:00:00Z"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{"tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}` + tt.timestamp + `}]}}}}}}`)
			stats := usage.NewRequestStatistics()
			h := &Handler{}
			h.SetUsageStatistics(stats)

			first := performUsageImport(h, payload)
			if first.Code != http.StatusOK {
				t.Fatalf("first uncertain-timestamp import status = %d, want %d body=%s", first.Code, http.StatusOK, first.Body.String())
			}
			second := performUsageImport(h, payload)
			if second.Code != http.StatusOK {
				t.Fatalf("second uncertain-timestamp import status = %d, want %d body=%s", second.Code, http.StatusOK, second.Body.String())
			}
			var receipt struct {
				Added   int64 `json:"added"`
				Skipped int64 `json:"skipped"`
			}
			if err := json.Unmarshal(second.Body.Bytes(), &receipt); err != nil {
				t.Fatalf("unmarshal repeat receipt: %v body=%s", err, second.Body.String())
			}
			if receipt.Added != 0 || receipt.Skipped != 1 {
				t.Fatalf("repeat receipt = %+v, want added=0 skipped=1", receipt)
			}
			detail := stats.Snapshot().APIs["client"].Models["model"].Details[0]
			if !detail.Timestamp.IsZero() {
				t.Fatalf("stored timestamp = %s, want Go zero time preserved", detail.Timestamp.Format(time.RFC3339Nano))
			}
		})
	}

	t.Run("missing null and Go zero share one uncertain identity", func(t *testing.T) {
		payloads := [][]byte{
			[]byte(`{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{"tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}}]}}}}}}`),
			[]byte(`{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{"timestamp":null,"tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}}]}}}}}}`),
			[]byte(`{"version":2,"usage":{"apis":{"client":{"models":{"model":{"details":[{"timestamp":"0001-01-01T00:00:00Z","tokens":{"input_tokens":1,"output_tokens":0,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":1}}]}}}}}}`),
		}
		stats := usage.NewRequestStatistics()
		h := &Handler{}
		h.SetUsageStatistics(stats)
		for index, payload := range payloads {
			rec := performUsageImport(h, payload)
			if rec.Code != http.StatusOK {
				t.Fatalf("uncertain form %d status = %d, want %d body=%s", index, rec.Code, http.StatusOK, rec.Body.String())
			}
			var receipt struct {
				Added   int64 `json:"added"`
				Skipped int64 `json:"skipped"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
				t.Fatalf("unmarshal uncertain form %d receipt: %v", index, err)
			}
			if index == 0 && (receipt.Added != 1 || receipt.Skipped != 0) {
				t.Fatalf("first uncertain receipt = %+v, want added=1 skipped=0", receipt)
			}
			if index > 0 && (receipt.Added != 0 || receipt.Skipped != 1) {
				t.Fatalf("uncertain replay %d receipt = %+v, want added=0 skipped=1", index, receipt)
			}
		}
	})
}

func TestUsageManagementImportKeepsDifferentCacheCreationTokenShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	timestamp := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	firstShape := usageCacheCreationSnapshot(timestamp, usage.TokenStats{
		InputTokens:         1200,
		OutputTokens:        10,
		CacheCreationTokens: 1024,
		TotalTokens:         1210,
	})
	secondShape := usageCacheCreationSnapshot(timestamp, usage.TokenStats{
		InputTokens:         2224,
		OutputTokens:        10,
		CachedTokens:        1024,
		CacheReadTokens:     1024,
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
			name:            "first canonical token shape then second",
			first:           firstShape,
			second:          secondShape,
			wantTotalTokens: 3444,
		},
		{
			name:            "second canonical token shape then first",
			first:           secondShape,
			second:          firstShape,
			wantTotalTokens: 3444,
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
			if secondResult.Added != 1 || secondResult.Skipped != 0 || secondResult.TotalRequests != 2 {
				t.Fatalf("second import result = %+v, want added=1 skipped=0 total_requests=2", secondResult)
			}

			snapshot := stats.Snapshot()
			model := snapshot.APIs["cache-key"].Models["gpt-5.6-sol"]
			if snapshot.TotalRequests != 2 || snapshot.TotalTokens != tt.wantTotalTokens || len(model.Details) != 2 {
				t.Fatalf("snapshot after imports = requests:%d tokens:%d details:%d, want 2/%d/2", snapshot.TotalRequests, snapshot.TotalTokens, len(model.Details), tt.wantTotalTokens)
			}
			if snapshot.RequestsByDay["2026-07-11"] != 2 || snapshot.RequestsByHour["09"] != 2 {
				t.Fatalf("request buckets after imports = day:%v hour:%v, want two requests", snapshot.RequestsByDay, snapshot.RequestsByHour)
			}
			if snapshot.TokensByDay["2026-07-11"] != tt.wantTotalTokens || snapshot.TokensByHour["09"] != tt.wantTotalTokens {
				t.Fatalf("token buckets after imports = day:%v hour:%v, want %d", snapshot.TokensByDay, snapshot.TokensByHour, tt.wantTotalTokens)
			}
		})
	}
}

type failingUsageImportReader struct{}

func (failingUsageImportReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func usageCacheCreationSnapshot(timestamp time.Time, tokens usage.TokenStats) usage.StatisticsSnapshot {
	return usage.StatisticsSnapshot{
		RequestsByDay:  map[string]int64{},
		RequestsByHour: map[string]int64{},
		TokensByDay:    map[string]int64{},
		TokensByHour:   map[string]int64{},
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
		APIKey:               "panel-client-key",
		Provider:             "openai",
		Model:                "gpt-5.4",
		Alias:                "panel-visible-model",
		Source:               "auths/openai.json",
		AuthIndex:            "1",
		ReasoningEffort:      "medium",
		ServiceTier:          "priority",
		RequestServiceTier:   "priority",
		OutboundServiceTier:  "priority",
		ResponseServiceTier:  "standard",
		EffectiveServiceTier: "standard",
		Generate:             coreusage.GenerateFlag(false),
		RequestedAt:          time.Date(2026, 6, 10, 11, 30, 0, 0, time.UTC),
		Latency:              2 * time.Second,
		TimingVersion:        1,
		TTFB:                 500 * time.Millisecond,
		TTFT:                 900 * time.Millisecond,
		TTFA:                 1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     5,
			OutputTokens:    7,
			ReasoningTokens: 3,
			CachedTokens:    2,
			TotalTokens:     17,
		},
	})
}

func readUsageStatisticsResponse(t *testing.T, h *Handler) (struct {
	Usage          usage.StatisticsSnapshot `json:"usage"`
	FailedRequests int64                    `json:"failed_requests"`
}, []byte) {
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
	return payload, rec.Body.Bytes()
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
	Added               int64    `json:"added"`
	Skipped             int64    `json:"skipped"`
	TotalRequests       int64    `json:"total_requests"`
	FailedRequests      int64    `json:"failed_requests"`
	SchemaVersion       int      `json:"schema_version"`
	MigratedFromVersion int      `json:"migrated_from_version"`
	Migration           string   `json:"migration"`
	Migrations          []string `json:"migrations"`
} {
	t.Helper()

	body, err := json.Marshal(usageImportPayload{Version: 3, Usage: snapshot})
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
		Added               int64    `json:"added"`
		Skipped             int64    `json:"skipped"`
		TotalRequests       int64    `json:"total_requests"`
		FailedRequests      int64    `json:"failed_requests"`
		SchemaVersion       int      `json:"schema_version"`
		MigratedFromVersion int      `json:"migrated_from_version"`
		Migration           string   `json:"migration"`
		Migrations          []string `json:"migrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal import response: %v body=%s", err, rec.Body.String())
	}
	return payload
}

func usageImportFixtureJSON(t *testing.T, version int, tokens map[string]int64) []byte {
	return usageImportFixtureJSONAt(t, version, tokens, "2026-07-21T12:00:00Z")
}

func usageImportFixtureJSONAt(t *testing.T, version int, tokens map[string]int64, timestamp string) []byte {
	t.Helper()
	payload := map[string]any{
		"version": version,
		"usage": map[string]any{
			"apis": map[string]any{
				"import-client": map[string]any{
					"models": map[string]any{
						"gpt-5.6-sol": map[string]any{
							"details": []any{map[string]any{
								"timestamp":  timestamp,
								"source":     "auths/import.json",
								"auth_index": "0",
								"tokens":     tokens,
								"failed":     false,
							}},
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal usage import fixture: %v", err)
	}
	return data
}

func seededUsageImportHandler() (*usage.RequestStatistics, *Handler, usage.StatisticsSnapshot) {
	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey: "existing-client-key", Model: "existing-model", RequestedAt: time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
	})
	h := &Handler{}
	h.SetUsageStatistics(stats)
	return stats, h, stats.Snapshot()
}

func performUsageImport(h *Handler, payload []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(payload))
	h.ImportUsageStatistics(ginCtx)
	return rec
}

func mustMarshalUsagePayload(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal usage payload: %v", err)
	}
	return data
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
	if detail.RequestServiceTier != "priority" {
		t.Fatalf("detail.request_service_tier = %q, want priority", detail.RequestServiceTier)
	}
	if detail.OutboundServiceTier != "priority" {
		t.Fatalf("detail.outbound_service_tier = %q, want priority", detail.OutboundServiceTier)
	}
	if detail.ResponseServiceTier != "standard" {
		t.Fatalf("detail.response_service_tier = %q, want standard", detail.ResponseServiceTier)
	}
	if detail.EffectiveServiceTier != "standard" {
		t.Fatalf("detail.effective_service_tier = %q, want standard", detail.EffectiveServiceTier)
	}
	if detail.Generate {
		t.Fatalf("detail.generate = true, want false")
	}
	if detail.LatencyMs != 2000 {
		t.Fatalf("detail.latency_ms = %d, want 2000", detail.LatencyMs)
	}
	if detail.TTFBMs != 500 {
		t.Fatalf("detail.ttfb_ms = %d, want 500", detail.TTFBMs)
	}
	if detail.TimingVersion != 1 || detail.TTFTMs != 900 || detail.TTFAMs != 1500 {
		t.Fatalf("detail semantic timing = version:%d ttft:%d ttfa:%d, want 1/900/1500", detail.TimingVersion, detail.TTFTMs, detail.TTFAMs)
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

func requireCanonicalReasoningEffortJSON(t *testing.T, payload []byte, surface string) {
	t.Helper()

	if !bytes.Contains(payload, []byte(`"reasoning_effort":"medium"`)) {
		t.Fatalf("%s missing canonical reasoning_effort: %s", surface, payload)
	}
	if bytes.Contains(payload, []byte(`"thinking"`)) {
		t.Fatalf("%s contains non-canonical thinking field: %s", surface, payload)
	}
}
