package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCodexExecutorAbnormalReasoningRetry_NonStreaming(t *testing.T) {
	testCases := []struct {
		name       string
		cfg        *config.Config
		auth       *cliproxyauth.Auth
		model      string
		reasoning  int
		wantRetry  bool
		authIDs    []string
		authKind   string
		provider   string
		metadata   map[string]any
		attributes map[string]string
	}{
		{
			name:      "feature disabled",
			cfg:       &config.Config{},
			model:     "gpt-5.5",
			reasoning: 516,
		},
		{
			name:      "enabled matching oauth",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			wantRetry: true,
		},
		{
			name:      "normal reasoning token",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 128,
		},
		{
			name:      "observe only does not retry",
			cfg:       codexAbnormalReasoningRetryTestConfigWithAction(config.CodexAbnormalReasoningRetryActionObserveOnly),
			model:     "gpt-5.5",
			reasoning: 516,
		},
		{
			name:      "model mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.4",
			reasoning: 516,
		},
		{
			name:      "api key channel ignored",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			authKind:  "apikey",
			metadata:  map[string]any{},
		},
		{
			name:      "auth id whitelist mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig([]string{"other-auth"}, nil),
			model:     "gpt-5.5",
			reasoning: 516,
		},
		{
			name:      "provider mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			provider:  "openai",
		},
		{
			name:      "auth kind fallback oauth",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 1034,
			authKind:  " ",
			wantRetry: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newCodexAbnormalReasoningRetryServer(t, tc.model, tc.reasoning)
			defer server.Close()

			auth := tc.auth
			if auth == nil {
				auth = codexAbnormalReasoningRetryTestAuth(server.URL)
			}
			if tc.provider != "" {
				auth.Provider = tc.provider
			}
			if tc.authKind != "" {
				auth.Attributes["auth_kind"] = tc.authKind
			}
			if tc.metadata != nil {
				auth.Metadata = tc.metadata
			}
			for key, value := range tc.attributes {
				auth.Attributes[key] = value
			}

			executor := NewCodexExecutor(tc.cfg)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: []byte(`{"model":"` + tc.model + `","input":"hello"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       false,
			})

			if tc.wantRetry {
				assertRetryWithoutPenaltyError(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Execute error = %v, want nil", err)
			}
		})
	}
}

func TestCodexExecutorAbnormalReasoningRetry_MatchesRequestedModelMetadata(t *testing.T) {
	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.4-upstream", 516)
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil))
	_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4-upstream",
		Payload: []byte(`{"model":"gpt-5.4-upstream","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.5-client",
		},
	})
	assertRetryWithoutPenaltyError(t, err)
}

func TestCodexExecutorAbnormalReasoningRetry_ErrorCarriesHedgePolicyAndAuthID(t *testing.T) {
	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.5", 516)
	defer server.Close()

	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	hedgeDelayMS := 25
	requireDistinctAuth := false
	cfg.Codex.AbnormalReasoningRetry.HedgedRetry = config.CodexAbnormalReasoningHedgedRetryConfig{
		Enabled:             true,
		HedgeDelayMS:        &hedgeDelayMS,
		RequireDistinctAuth: &requireDistinctAuth,
	}
	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-policy"

	executor := NewCodexExecutor(cfg)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	assertRetryWithoutPenaltyError(t, err)

	var withPolicy interface {
		RetryWithoutPenaltyHedgePolicy() (bool, time.Duration, bool)
	}
	if !errors.As(err, &withPolicy) {
		t.Fatalf("error %T does not expose RetryWithoutPenaltyHedgePolicy", err)
	}
	enabled, delay, requireDistinct := withPolicy.RetryWithoutPenaltyHedgePolicy()
	if !enabled || delay != 25*time.Millisecond || requireDistinct {
		t.Fatalf("hedge policy = enabled:%t delay:%v requireDistinct:%t, want true/25ms/false", enabled, delay, requireDistinct)
	}
	var withAuthID interface {
		RetryWithoutPenaltyAuthID() string
	}
	if !errors.As(err, &withAuthID) {
		t.Fatalf("error %T does not expose RetryWithoutPenaltyAuthID", err)
	}
	if got := withAuthID.RetryWithoutPenaltyAuthID(); got != "codex-oauth-policy" {
		t.Fatalf("RetryWithoutPenaltyAuthID = %q, want codex-oauth-policy", got)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ReasoningEffortFilterUsesPayloadWithoutModelSuffix(t *testing.T) {
	testCases := []struct {
		name         string
		sourceFormat string
		payload      []byte
	}{
		{
			name:         "responses reasoning.effort",
			sourceFormat: "openai-response",
			payload:      []byte(`{"model":"gpt-5.5","input":"hello","reasoning":{"effort":"xhigh"}}`),
		},
		{
			name:         "chat reasoning_effort",
			sourceFormat: "openai",
			payload:      []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
			})

			server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.5", 516)
			defer server.Close()

			executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithEfforts([]string{"xhigh"}))
			_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
				Model:   "gpt-5.5",
				Payload: tc.payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.sourceFormat),
				Stream:       false,
			})
			assertRetryWithoutPenaltyError(t, err)

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5" && record.Detail.ReasoningTokens == 516
			})
			if record.ReasoningEffort != "xhigh" {
				t.Fatalf("record.ReasoningEffort = %q, want xhigh", record.ReasoningEffort)
			}
		})
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ReasoningEffortFilterSkipsMismatch(t *testing.T) {
	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.5", 516)
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithEfforts([]string{"xhigh"}))
	_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello","reasoning":{"effort":"high"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil when reasoning effort does not match", err)
	}
}

func TestCodexAbnormalReasoningRetryEffortCanonicalizationIsModelAware(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		filters       []string
		runtimeEffort string
		wantRetry     bool
	}{
		{name: "known ultra filter matches final max", model: "gpt-5.6-sol", filters: []string{"ultra"}, runtimeEffort: "max", wantRetry: true},
		{name: "known max filter matches final max", model: "gpt-5.6-terra", filters: []string{"max"}, runtimeEffort: "max", wantRetry: true},
		{name: "empty filter keeps any-effort semantics", model: "gpt-5.6-sol", filters: nil, runtimeEffort: "high", wantRetry: true},
		{name: "custom ultra remains literal", model: "custom-codex-ultra-retry", filters: []string{"ultra"}, runtimeEffort: "ultra", wantRetry: true},
		{name: "custom ultra filter does not alias max", model: "custom-codex-ultra-retry", filters: []string{"ultra"}, runtimeEffort: "max", wantRetry: false},
		{name: "custom max filter does not alias ultra", model: "custom-codex-ultra-retry", filters: []string{"max"}, runtimeEffort: "ultra", wantRetry: false},
		{name: "luna ultra filter does not alias max", model: "gpt-5.6-luna", filters: []string{"ultra"}, runtimeEffort: "max", wantRetry: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := codexAbnormalReasoningRetryTestConfigWithEfforts(tt.filters)
			cfg.Codex.AbnormalReasoningRetry.ModelContains = []string{tt.model}
			policy := newCodexAbnormalReasoningRetryPolicy(cfg, codexAbnormalReasoningRetryTestAuth(""), tt.model, tt.model)
			err := policy.RetryError(usage.Detail{ReasoningTokens: 516}, tt.runtimeEffort)
			if tt.wantRetry {
				assertRetryWithoutPenaltyError(t, err)
				return
			}
			if err != nil {
				t.Fatalf("RetryError() = %v, want nil", err)
			}
		})
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PayloadOverrideUltraUsesFinalMaxUsage(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-gpt56-ultra-usage-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-gpt56-ultra-usage-test", noopUsagePlugin{})
	})

	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.6-sol", 516)
	defer server.Close()

	cfg := codexAbnormalReasoningRetryTestConfigWithEfforts([]string{"ultra"})
	cfg.Codex.AbnormalReasoningRetry.ModelContains = []string{"gpt-5.6-sol"}
	cfg.Payload.Override = []config.PayloadRule{
		{
			Models: []config.PayloadModelRule{{Name: "gpt-5.6-sol"}},
			Params: map[string]any{"reasoning.effort": "ultra"},
		},
	}
	executor := NewCodexExecutor(cfg)
	_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"effort":"max"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	assertRetryWithoutPenaltyError(t, err)

	record := recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.6-sol" && record.Detail.ReasoningTokens == 516
	})
	if record.ReasoningEffort != "max" {
		t.Fatalf("record.ReasoningEffort = %q, want max", record.ReasoningEffort)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PublishesFailedUsageForInterceptedAttempt(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.5", 516)
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil))
	_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	assertRetryWithoutPenaltyError(t, err)

	record := recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5" && record.Detail.ReasoningTokens == 516
	})
	if !record.Failed {
		t.Fatalf("record.Failed = false, want true")
	}
	if record.Detail.InputTokens != 1 || record.Detail.OutputTokens != 2 || record.Detail.TotalTokens != 3 {
		t.Fatalf("record detail = %+v, want input=1 output=2 total=3", record.Detail)
	}
	if !strings.Contains(record.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode) {
		t.Fatalf("record failure body = %q, want usage failure code %q", record.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode)
	}
	if strings.Contains(record.Fail.Body, "codex_abnormal_reasoning_retry_exhausted") {
		t.Fatalf("record failure body = %q, want attempt reason not terminal exhausted code", record.Fail.Body)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ManagerRecordsAttemptLevelUsageAndDeliversRawClientUsage(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-nonaggregate"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 12 {
		t.Fatalf("client response usage.total_tokens = %d, want delivered total 12; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 5 {
		t.Fatalf("client response usage.input_tokens = %d, want delivered input 5; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 7 {
		t.Fatalf("client response usage.output_tokens = %d, want delivered output 7; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 128 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want delivered reasoning 128; payload=%s", got, resp.Payload)
	}

	records := recorder.waitForRecords(t, func(record usage.Record) bool {
		return record.AuthID == auth.ID && record.Model == "gpt-5.5"
	}, 2)
	var failedRecord, successRecord *usage.Record
	for i := range records {
		if records[i].Failed {
			failedRecord = &records[i]
		} else {
			successRecord = &records[i]
		}
	}
	if failedRecord == nil {
		t.Fatalf("records = %+v, want failed abnormal attempt record", records)
	}
	if successRecord == nil {
		t.Fatalf("records = %+v, want final success attempt record", records)
	}
	if failedRecord.Detail.TotalTokens != 3 || failedRecord.Detail.ReasoningTokens != 516 {
		t.Fatalf("failed record detail = %+v, want total=3 reasoning=516", failedRecord.Detail)
	}
	if !strings.Contains(failedRecord.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode) {
		t.Fatalf("failed record body = %q, want usage failure code %q", failedRecord.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode)
	}
	if successRecord.Detail.TotalTokens != 12 || successRecord.Detail.ReasoningTokens != 128 {
		t.Fatalf("success record detail = %+v, want final attempt total=12 reasoning=128", successRecord.Detail)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_BestNonSpecialKeepsShorterSuccess(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "special", 516, 1, 80, 81)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "non-special", 128, 5, 20, 25)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithDeliveryPolicy(config.CodexAbnormalReasoningRetryDeliveryPolicyBestNonSpecial)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-best-non-special"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if !bytes.Contains(resp.Payload, []byte("non-special")) || bytes.Contains(resp.Payload, []byte(`"text":"special"`)) {
		t.Fatalf("payload = %s, want non-special success", resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_MaxOutputCanReturnLongerSpecial(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "special", 516, 1, 80, 81)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "short-success", 128, 5, 20, 25)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithDeliveryPolicy(config.CodexAbnormalReasoningRetryDeliveryPolicyMaxOutput)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-max-output-special"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if !bytes.Contains(resp.Payload, []byte(`"text":"special"`)) || bytes.Contains(resp.Payload, []byte("short-success")) {
		t.Fatalf("payload = %s, want longer special fallback", resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ClientUsageAggregationSumSumsFields(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithAggregation(config.CodexAbnormalReasoningRetryClientUsageAggregationSum)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-sum-aggregate"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.total_tokens = %d, want summed total 15; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.input_tokens = %d, want summed input 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 9 {
		t.Fatalf("client response usage.output_tokens = %d, want summed output 9; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 644 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want summed reasoning 644; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorModelFallbackPreservesSourceAbnormalPolicyAndUsageAggregation(t *testing.T) {
	var sourceCalls, targetCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
		switch model {
		case "gpt-source":
			sourceCalls++
			if sourceCalls == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-source", 516, 1, 2, 3)))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"source quota","resets_in_seconds":60}}`))
		case "gpt-target":
			targetCalls++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-target", 128, 5, 7, 12)))
		default:
			t.Errorf("unexpected upstream model %q; body=%s", model, body)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	maxRetries := 1
	cfg := codexAbnormalReasoningRetryTestConfigWithAggregation(config.CodexAbnormalReasoningRetryClientUsageAggregationSum)
	cfg.Codex.AbnormalReasoningRetry.MaxRetries = &maxRetries
	cfg.Codex.AbnormalReasoningRetry.ModelContains = []string{"gpt-source"}
	cfg.Codex.ModelFallback = config.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []config.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target"}}},
	}

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewCodexExecutor(cfg))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-model-fallback-shared-policy"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-source",
		Payload: []byte(`{"model":"gpt-source","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want target success", errExecute)
	}
	if sourceCalls != 2 || targetCalls != 1 {
		t.Fatalf("upstream calls = source:%d target:%d, want source:2 target:1", sourceCalls, targetCalls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.total_tokens = %d, want shared source+target total 15; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.input_tokens = %d, want shared input 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 9 {
		t.Fatalf("client response usage.output_tokens = %d, want shared output 9; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 644 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want shared reasoning 644; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorModelFallbackPreflightUsesAfterAuthRewrittenReplayScope(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	if !internalcache.CacheCodexReasoningReplayItem("gpt-source", "prompt-cache:after-auth-rewritten", []byte(`{"type":"function_call","call_id":"call-after-auth","name":"tool","arguments":"{}"}`)) {
		t.Fatal("failed to cache replay test item")
	}
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"source quota","resets_in_seconds":60}}`))
	}))
	defer server.Close()

	cfg := &config.Config{Codex: config.CodexConfig{ModelFallback: config.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []config.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target"}}},
	}}}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewCodexExecutor(cfg))
	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-after-auth-replay"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	var interceptedModels []string
	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-source",
		Payload: []byte(`{"model":"gpt-source","messages":[{"role":"user","content":"continue"}],"max_tokens":32}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		RequestAfterAuthInterceptor: func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			interceptedModels = append(interceptedModels, request.Model)
			body := request.Body
			if request.Model == "gpt-source" {
				body, _ = sjson.SetBytes(body, "prompt_cache_key", "after-auth-rewritten")
			}
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{Body: body}
		},
	})
	var classified interface{ ModelFallbackReason() string }
	if !errors.As(errExecute, &classified) || classified == nil || classified.ModelFallbackReason() != config.CodexModelFallbackTriggerUsageLimit {
		t.Fatalf("Execute error = %v, want original typed usage-limit", errExecute)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want source only", upstreamCalls)
	}
	if len(interceptedModels) != 1 || interceptedModels[0] != "gpt-source" {
		t.Fatalf("after-auth interceptor models = %#v, want source only before fallback preflight", interceptedModels)
	}
}

func TestPatchCodexAbnormalReasoningClientUsageSumsCacheReadAndWrite(t *testing.T) {
	current := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}}`)
	previous := cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{Detail: usage.Detail{
		InputTokens:         50,
		OutputTokens:        10,
		CachedTokens:        5,
		CacheReadTokens:     5,
		CacheCreationTokens: 7,
		TotalTokens:         60,
	}}

	patched := patchCodexAbnormalReasoningClientUsageWithSnapshot(current, previous, config.CodexAbnormalReasoningRetryClientUsageAggregationSum)
	if got := gjson.GetBytes(patched, "response.usage.input_tokens_details.cached_tokens").Int(); got != 35 {
		t.Fatalf("cached_tokens = %d, want 35; payload=%s", got, patched)
	}
	if got := gjson.GetBytes(patched, "response.usage.input_tokens_details.cache_write_tokens").Int(); got != 47 {
		t.Fatalf("cache_write_tokens = %d, want 47; payload=%s", got, patched)
	}
}

func TestAddCodexUsageDetailPreservesUncachedInputKnowledge(t *testing.T) {
	first := usage.Detail{
		InputTokens:              10,
		TotalTokens:              10,
		UncachedInputTokens:      0,
		UncachedInputTokensKnown: true,
	}
	second := usage.Detail{
		InputTokens:              5,
		TotalTokens:              5,
		UncachedInputTokens:      5,
		UncachedInputTokensKnown: true,
	}

	total := addCodexUsageDetail(first, second)
	if !total.UncachedInputTokensKnown || total.UncachedInputTokens != 5 {
		t.Fatalf("added detail = %+v, want known uncached input 5", total)
	}

	mixed := addCodexUsageDetail(first, usage.Detail{InputTokens: 1, TotalTokens: 1})
	if mixed.UncachedInputTokensKnown {
		t.Fatalf("mixed detail = %+v, want uncached input unknown", mixed)
	}
}

func TestAddCodexUsageDetailRejectsInvalidKnownUncachedInputContribution(t *testing.T) {
	invalid := usage.Detail{
		InputTokens:              10,
		TotalTokens:              10,
		UncachedInputTokens:      11,
		UncachedInputTokensKnown: true,
	}
	valid := usage.Detail{
		InputTokens:              10,
		TotalTokens:              10,
		UncachedInputTokens:      9,
		UncachedInputTokensKnown: true,
	}

	total := addCodexUsageDetail(invalid, valid)
	if total.UncachedInputTokensKnown || total.UncachedInputTokens != 0 {
		t.Fatalf("added detail = %+v, want cleared unknown uncached input", total)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ClientUsageAggregationSumUsesCodexFallbackWhenPreviousTotalMissing(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsageNoTotal("gpt-5.5", 516, 1, 2)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithAggregation(config.CodexAbnormalReasoningRetryClientUsageAggregationSum)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-sum-missing-previous-total"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.total_tokens = %d, want summed Codex fallback total 15; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.input_tokens = %d, want summed input 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 9 {
		t.Fatalf("client response usage.output_tokens = %d, want summed output 9; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 644 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want summed reasoning 644; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ClientUsageAggregationSumWithDeliveredTotal(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithAggregation(config.CodexAbnormalReasoningRetryClientUsageAggregationSumWithDeliveredTotal)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-sum-delivered-total"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 12 {
		t.Fatalf("client response usage.total_tokens = %d, want delivered total 12; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.input_tokens = %d, want summed input 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 9 {
		t.Fatalf("client response usage.output_tokens = %d, want summed output 9; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 644 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want summed reasoning 644; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_DefaultMaxRetriesDeliversFinalUsageWithTwoAbnormalAttempts(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
		case 2:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 1034, 4, 6, 10)))
		default:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil)))
	manager.SetRetryConfig(2, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-default-two-abnormal"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 12 {
		t.Fatalf("client response usage.total_tokens = %d, want delivered total 12; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 5 {
		t.Fatalf("client response usage.input_tokens = %d, want delivered input 5; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 7 {
		t.Fatalf("client response usage.output_tokens = %d, want delivered output 7; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 128 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want delivered reasoning 128; payload=%s", got, resp.Payload)
	}

	records := recorder.waitForRecords(t, func(record usage.Record) bool {
		return record.AuthID == auth.ID && record.Model == "gpt-5.5"
	}, 3)
	var failedTotal, successTotal int
	var failedReasoning int64
	var successRecord *usage.Record
	for i := range records {
		if records[i].Failed {
			failedTotal++
			failedReasoning += records[i].Detail.ReasoningTokens
			continue
		}
		successTotal++
		successRecord = &records[i]
	}
	if failedTotal != 2 {
		t.Fatalf("failed records = %d, want 2; records=%+v", failedTotal, records)
	}
	if failedReasoning != 1550 {
		t.Fatalf("failed reasoning tokens = %d, want 1550", failedReasoning)
	}
	if successTotal != 1 || successRecord == nil {
		t.Fatalf("success records = %d, want 1; records=%+v", successTotal, records)
	}
	if successRecord.Detail.TotalTokens != 12 || successRecord.Detail.ReasoningTokens != 128 {
		t.Fatalf("success record detail = %+v, want final attempt total=12 reasoning=128", successRecord.Detail)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ManagerMaxRetriesIndependentFromRequestRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(1, "")))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-independent-retry"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 despite request-retry=0", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 12 {
		t.Fatalf("client response usage.total_tokens = %d, want delivered total 12; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughWhenExhaustedNonStreaming(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(0, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-pass-through"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("client response usage.total_tokens = %d, want delivered abnormal total 3; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 1 {
		t.Fatalf("client response usage.input_tokens = %d, want delivered abnormal input 1; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 2 {
		t.Fatalf("client response usage.output_tokens = %d, want delivered abnormal output 2; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 516 {
		t.Fatalf("client response reasoning_tokens = %d, want 516; payload=%s", got, resp.Payload)
	}

	record := recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == auth.ID && record.Model == "gpt-5.5" && record.Detail.ReasoningTokens == 516
	})
	if !record.Failed {
		t.Fatalf("record.Failed = false, want true")
	}
	if !strings.Contains(record.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode) {
		t.Fatalf("record failure body = %q, want usage failure code %q", record.Fail.Body, codexAbnormalReasoningRetryUsageFailureCode)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughDeliversSelectedFallbackUsageWhenExhausted(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
		default:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 1034, 4, 6, 10)))
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(1, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-pass-through-aggregate"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 10 {
		t.Fatalf("client response usage.total_tokens = %d, want selected fallback total 10; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 4 {
		t.Fatalf("client response usage.input_tokens = %d, want selected fallback input 4; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.output_tokens = %d, want selected fallback output 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 1034 {
		t.Fatalf("client response reasoning_tokens = %d, want selected fallback reasoning 1034; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughSumWithDeliveredTotalKeepsFallbackTotal(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
		default:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 1034, 4, 6, 10)))
		}
	}))
	defer server.Close()

	cfg := codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(1, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)
	cfg.Codex.AbnormalReasoningRetry.ClientUsageAggregation = config.CodexAbnormalReasoningRetryClientUsageAggregationSumWithDeliveredTotal

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(cfg))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-pass-through-sum-delivered-total"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 10 {
		t.Fatalf("client response usage.total_tokens = %d, want selected fallback total 10; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 5 {
		t.Fatalf("client response usage.input_tokens = %d, want summed input 5; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 8 {
		t.Fatalf("client response usage.output_tokens = %d, want summed output 8; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 1550 {
		t.Fatalf("client response reasoning_tokens = %d, want summed reasoning 1550; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughReturnsLongestNonStreamingFallback(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "trigger", 516, 1, 5, 6)))
		case 2:
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "long", 516, 1, 80, 81)))
		default:
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "short", 516, 1, 20, 21)))
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(2, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-pass-through-longest"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3", calls)
	}
	if !bytes.Contains(resp.Payload, []byte("long")) || bytes.Contains(resp.Payload, []byte("short")) {
		t.Fatalf("client response payload = %s, want longest fallback only", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 81 {
		t.Fatalf("client response usage.total_tokens = %d, want longest fallback total 81; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ManagerDeliversStreamingClientUsage(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 7, 12)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil)))
	manager.SetRetryConfig(1, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-stream-aggregate"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if !bytes.Contains(payload, []byte(`"total_tokens":12`)) {
		t.Fatalf("stream payload missing delivered total_tokens=12: %s", payload)
	}
	if !bytes.Contains(payload, []byte(`"reasoning_tokens":128`)) {
		t.Fatalf("stream payload missing delivered reasoning_tokens=128: %s", payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_QualityStreamDeliversWinnerUsageIntoChatCompletionsFormat(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
		case 2:
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"clean"}` + "\n\n"))
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 128, 5, 200, 205)))
		default:
			_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 2, 4, 6)))
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithQualityHedge(2)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-quality-chat-delivered"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 3 {
		t.Fatalf("upstream calls = %d, want trigger plus two quality lanes", gotCalls)
	}
	if !bytes.Contains(payload, []byte("clean")) {
		t.Fatalf("stream payload missing winner delta text: %s", payload)
	}
	for _, want := range []string{
		`"prompt_tokens":5`,
		`"completion_tokens":200`,
		`"total_tokens":205`,
		`"reasoning_tokens":128`,
	} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Fatalf("stream payload missing delivered chat usage %s: %s", want, payload)
		}
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughWhenExhaustedStreaming(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSEWithUsage("gpt-5.5", 516, 1, 2, 3)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(0, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-stream-pass-through"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil pass-through", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	if !bytes.Contains(payload, []byte("visible")) {
		t.Fatalf("stream payload missing buffered visible delta: %s", payload)
	}
	if !bytes.Contains(payload, []byte(`"total_tokens":3`)) {
		t.Fatalf("stream payload missing delivered abnormal total_tokens=3: %s", payload)
	}
	if !bytes.Contains(payload, []byte(`"reasoning_tokens":516`)) {
		t.Fatalf("stream payload missing delivered abnormal reasoning_tokens=516: %s", payload)
	}

	record := recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == auth.ID && record.Model == "gpt-5.5" && record.Detail.ReasoningTokens == 516
	})
	if !record.Failed {
		t.Fatalf("record.Failed = false, want true")
	}
}

func TestCodexExecutorAbnormalReasoningRetry_PassThroughReturnsLongestStreamingFallback(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"trigger"}` + "\n\n"))
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "trigger", 516, 1, 5, 6)))
		case 2:
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"long"}` + "\n\n"))
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "long", 516, 1, 80, 81)))
		default:
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"short"}` + "\n\n"))
			_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", "short", 516, 1, 20, 21)))
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(2, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)

	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-oauth-stream-pass-through-longest"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil pass-through", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3", calls)
	}
	if !bytes.Contains(payload, []byte("long")) || bytes.Contains(payload, []byte("short")) {
		t.Fatalf("stream payload = %s, want longest fallback only", payload)
	}
	if !bytes.Contains(payload, []byte(`"total_tokens":81`)) {
		t.Fatalf("stream payload missing longest fallback total_tokens=81: %s", payload)
	}
}

func TestCodexAbnormalReasoningRetryAuthKindNormalization(t *testing.T) {
	for _, raw := range []string{"apikey", "api-key", "api_key", " APIKEY "} {
		if got := normalizeCodexAbnormalReasoningRetryAuthKind(raw); got != "api_key" {
			t.Fatalf("normalizeCodexAbnormalReasoningRetryAuthKind(%q) = %q, want api_key", raw, got)
		}
	}
	if got := normalizeCodexAbnormalReasoningRetryAuthKind("OAuth"); got != "oauth" {
		t.Fatalf("normalizeCodexAbnormalReasoningRetryAuthKind(OAuth) = %q, want oauth", got)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferDropsAbnormalChunks(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", recorder)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("codex-abnormal-reasoning-retry-test", noopUsagePlugin{})
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"bad"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 1034)))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil))
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var payloads int
	var streamErr error
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			payloads++
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if payloads != 0 {
		t.Fatalf("payload chunks = %d, want 0", payloads)
	}
	assertRetryWithoutPenaltyError(t, streamErr)
	record := recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5" && record.Detail.ReasoningTokens == 1034
	})
	if !record.Failed {
		t.Fatalf("record.Failed = false, want true")
	}
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferDisabledDoesNotRetry(t *testing.T) {
	streamBuffer := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 1034)))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, &streamBuffer))
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var sawPayload bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		if bytes.Contains(chunk.Payload, []byte("visible")) || bytes.Contains(chunk.Payload, []byte("response.completed")) {
			sawPayload = true
		}
	}
	if !sawPayload {
		t.Fatal("expected visible streamed payload when stream-buffer=false")
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ObserveOnlyStreamingDoesNotBufferUntilCompleted(t *testing.T) {
	allowCompleted := make(chan struct{})
	var releaseCompleted sync.Once
	release := func() {
		releaseCompleted.Do(func() {
			close(allowCompleted)
		})
	}
	defer release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-allowCompleted:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 516)))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithAction(config.CodexAbnormalReasoningRetryActionObserveOnly))
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before first payload")
		}
		if chunk.Err != nil {
			t.Fatalf("first stream chunk error = %v, want nil", chunk.Err)
		}
		if !bytes.Contains(chunk.Payload, []byte("visible")) {
			t.Fatalf("first stream payload = %s, want visible delta before completed", chunk.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observe-only stream buffered first payload until response.completed")
	}

	release()
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
	}
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferMaxBytesAbnormalStillRetriesWithoutFallback(t *testing.T) {
	streamBufferMaxBytes := int64(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 1034)))
	}))
	defer server.Close()

	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.StreamBufferMaxBytes = &streamBufferMaxBytes
	executor := NewCodexExecutor(cfg)
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var payload []byte
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		payload = append(payload, chunk.Payload...)
	}
	if streamErr == nil {
		t.Fatal("stream error = nil, want retry-without-penalty error")
	}
	if len(payload) != 0 {
		t.Fatalf("stream payload = %s, want no fail-open payload", payload)
	}
	var retryErr interface {
		RetryWithoutPenalty() bool
	}
	if !errors.As(streamErr, &retryErr) || !retryErr.RetryWithoutPenalty() {
		t.Fatalf("stream error = %v, want retry-without-penalty error", streamErr)
	}
	var fallbackErr interface {
		RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool)
	}
	if errors.As(streamErr, &fallbackErr) {
		_, chunks, ok := fallbackErr.RetryWithoutPenaltyFallbackStreamChunks()
		if ok || len(chunks) != 0 {
			t.Fatalf("fallback chunks = %d, ok=%t; want no oversized fallback payload", len(chunks), ok)
		}
	}
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferMaxBytesNormalFailsClosed(t *testing.T) {
	streamBufferMaxBytes := int64(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 100)))
	}))
	defer server.Close()

	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.StreamBufferMaxBytes = &streamBufferMaxBytes
	executor := NewCodexExecutor(cfg)
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var payload []byte
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		payload = append(payload, chunk.Payload...)
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "stream buffer limit") {
		t.Fatalf("stream error = %v, want stream buffer limit error", streamErr)
	}
	if len(payload) != 0 {
		t.Fatalf("stream payload = %s, want no partial payload", payload)
	}
}

func codexAbnormalReasoningRetryTestConfig(authIDs []string, streamBuffer *bool) *config.Config {
	return &config.Config{
		Codex: config.CodexConfig{
			AbnormalReasoningRetry: config.CodexAbnormalReasoningRetryConfig{
				Enabled:      true,
				AuthIDs:      authIDs,
				StreamBuffer: streamBuffer,
			},
		},
	}
}

func codexAbnormalReasoningRetryTestConfigWithEfforts(efforts []string) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.ReasoningEfforts = efforts
	return cfg
}

func codexAbnormalReasoningRetryTestConfigWithAction(action string) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.Action = action
	return cfg
}

func codexAbnormalReasoningRetryTestConfigWithAggregation(aggregation string) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.ClientUsageAggregation = aggregation
	return cfg
}

func codexAbnormalReasoningRetryTestConfigWithDeliveryPolicy(deliveryPolicy string) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.DeliveryPolicy = deliveryPolicy
	return cfg
}

func codexAbnormalReasoningRetryTestConfigWithQualityHedge(maxRetries int) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.MaxRetries = &maxRetries
	hedgeDelayMS := 0
	requireDistinctAuth := false
	cfg.Codex.AbnormalReasoningRetry.HedgedRetry = config.CodexAbnormalReasoningHedgedRetryConfig{
		Enabled:             true,
		Mode:                config.CodexAbnormalReasoningHedgedRetryModeQuality,
		HedgeDelayMS:        &hedgeDelayMS,
		RequireDistinctAuth: &requireDistinctAuth,
	}
	return cfg
}

func codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(maxRetries int, exhaustedBehavior string) *config.Config {
	cfg := codexAbnormalReasoningRetryTestConfig(nil, nil)
	cfg.Codex.AbnormalReasoningRetry.MaxRetries = &maxRetries
	cfg.Codex.AbnormalReasoningRetry.ExhaustedBehavior = exhaustedBehavior
	return cfg
}

func codexAbnormalReasoningRetryTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-oauth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  baseURL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
			"email":        "test@example.com",
		},
	}
}

func newCodexAbnormalReasoningRetryServer(t *testing.T, model string, reasoning int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSE(model, reasoning)))
	}))
}

func codexCompletedSSE(model string, reasoning int) string {
	return codexCompletedSSEWithUsage(model, reasoning, 1, 2, 3)
}

func codexCompletedSSEWithUsage(model string, reasoning, input, output, total int) string {
	return codexCompletedSSEWithTextAndUsage(model, "ok", reasoning, input, output, total)
}

func codexCompletedSSEWithUsageNoTotal(model string, reasoning, input, output int) string {
	return `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"` + model + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":` + strconv.Itoa(input) + `,"output_tokens":` + strconv.Itoa(output) + `,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoning) + `}}}}` + "\n\n"
}

func codexCompletedSSEWithTextAndUsage(model, text string, reasoning, input, output, total int) string {
	return `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"` + model + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":` + strconv.Itoa(input) + `,"output_tokens":` + strconv.Itoa(output) + `,"total_tokens":` + strconv.Itoa(total) + `,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoning) + `}}}}` + "\n\n"
}

func assertRetryWithoutPenaltyError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want retry without penalty")
	}
	var retry interface {
		RetryWithoutPenalty() bool
	}
	if !errors.As(err, &retry) || !retry.RetryWithoutPenalty() {
		t.Fatalf("error %T does not implement RetryWithoutPenalty(): %v", err, err)
	}
}

type codexAbnormalReasoningRetryUsageRecorder struct {
	mu      sync.Mutex
	records []usage.Record
}

func (r *codexAbnormalReasoningRetryUsageRecorder) HandleUsage(_ context.Context, record usage.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
}

func (r *codexAbnormalReasoningRetryUsageRecorder) recordCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

func (r *codexAbnormalReasoningRetryUsageRecorder) waitForRecord(t *testing.T, match func(usage.Record) bool) usage.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, record := range r.records {
			if match(record) {
				r.mu.Unlock()
				return record
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("timed out waiting for usage record; records=%+v", r.records)
	return usage.Record{}
}

func (r *codexAbnormalReasoningRetryUsageRecorder) waitForRecords(t *testing.T, match func(usage.Record) bool, count int) []usage.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		var records []usage.Record
		for _, record := range r.records {
			if match(record) {
				records = append(records, record)
			}
		}
		r.mu.Unlock()
		if len(records) >= count {
			return records
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("timed out waiting for %d usage records; records=%+v", count, r.records)
	return nil
}

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, usage.Record) {}
