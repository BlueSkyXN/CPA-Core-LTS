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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageTokenAccountingImportPreservesRawCountersAndRejectsContradictions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() { usage.SetStatisticsEnabled(enabled) })
	source := usage.NewRequestStatistics()
	source.Record(context.Background(), coreusage.Record{APIKey: "accounting-test", Model: "gemini", Provider: "gemini", RequestedAt: time.Now(), Latency: time.Second, Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 40, TotalTokens: 150, TokenBreakdown: coreusage.NewSeparateReasoningTokenBreakdown(10, 0, 0, 100, 40, 150)}})
	sourceHandler := &Handler{}
	sourceHandler.SetUsageStatistics(source)
	raw, err := json.Marshal(exportUsageStatistics(t, sourceHandler))
	if err != nil {
		t.Fatal(err)
	}
	target := usage.NewRequestStatistics()
	h := &Handler{}
	h.SetUsageStatistics(target)
	call := func(data []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(data))
		h.ImportUsageStatistics(c)
		return w
	}
	if w := call(raw); w.Code != http.StatusOK {
		t.Fatalf("valid import: %d %s", w.Code, w.Body.String())
	}
	d := target.Snapshot().APIs["accounting-test"].Models["gemini"].Details[0]
	if d.Tokens.OutputTokens != 100 || d.TokenBreakdown == nil || d.TokenBreakdown.Output.TotalTokens != 140 || d.TokenBreakdown.Output.NonReasoningTokens != 100 {
		t.Fatalf("lost accounting: %+v", d)
	}
	before, _ := json.Marshal(target.Snapshot())
	for _, mutate := range []func(map[string]any){
		func(d map[string]any) { d["token_breakdown"] = nil },
		func(d map[string]any) { delete(d["token_breakdown"].(map[string]any), "input") },
		func(d map[string]any) { d["token_breakdown"].(map[string]any)["quality"] = "guess" },
		func(d map[string]any) {
			d["token_breakdown"].(map[string]any)["output"].(map[string]any)["total_tokens"] = 141
		},
		func(d map[string]any) { d["tokens"].(map[string]any)["output_tokens"] = 60 },
		func(d map[string]any) { d["tokens"].(map[string]any)["input_tokens"] = 20 },
		func(d map[string]any) {
			d["tokens"].(map[string]any)["cache_read_tokens"] = 1
			d["tokens"].(map[string]any)["cached_tokens"] = 1
		},
	} {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		item := value["usage"].(map[string]any)["apis"].(map[string]any)["accounting-test"].(map[string]any)["models"].(map[string]any)["gemini"].(map[string]any)["details"].([]any)[0].(map[string]any)
		mutate(item)
		data, _ := json.Marshal(value)
		w := call(data)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("usage_v3_token_contract_invalid")) {
			t.Fatalf("invalid import: %d %s", w.Code, w.Body.String())
		}
		after, _ := json.Marshal(target.Snapshot())
		if !bytes.Equal(before, after) {
			t.Fatal("rejected import mutated usage")
		}
	}
}
