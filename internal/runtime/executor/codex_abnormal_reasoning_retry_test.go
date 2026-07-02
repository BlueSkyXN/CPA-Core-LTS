package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
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

func TestCodexExecutorAbnormalReasoningRetry_ManagerRecordsAttemptLevelUsageAndAggregatesClientUsage(t *testing.T) {
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
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.total_tokens = %d, want abnormal plus final total 15; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 6 {
		t.Fatalf("client response usage.input_tokens = %d, want abnormal plus final input 6; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 9 {
		t.Fatalf("client response usage.output_tokens = %d, want abnormal plus final output 9; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 644 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want abnormal plus final reasoning 644; payload=%s", got, resp.Payload)
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

func TestCodexExecutorAbnormalReasoningRetry_DefaultMaxRetriesAggregatesTwoAbnormalAttempts(t *testing.T) {
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
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 25 {
		t.Fatalf("client response usage.total_tokens = %d, want abnormal attempts plus final total 25; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 10 {
		t.Fatalf("client response usage.input_tokens = %d, want abnormal attempts plus final input 10; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.output_tokens = %d, want abnormal attempts plus final output 15; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 1678 {
		t.Fatalf("client response usage.reasoning_tokens = %d, want abnormal attempts plus final reasoning 1678; payload=%s", got, resp.Payload)
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
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 15 {
		t.Fatalf("client response usage.total_tokens = %d, want abnormal plus final total 15; payload=%s", got, resp.Payload)
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

func TestCodexExecutorAbnormalReasoningRetry_PassThroughAggregatesDiscardedUsageWhenExhausted(t *testing.T) {
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
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 13 {
		t.Fatalf("client response usage.total_tokens = %d, want discarded plus delivered total 13; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens_details.reasoning_tokens").Int(); got != 1550 {
		t.Fatalf("client response reasoning_tokens = %d, want discarded plus delivered reasoning 1550; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_ManagerAggregatesStreamingClientUsage(t *testing.T) {
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
	if !bytes.Contains(payload, []byte(`"total_tokens":15`)) {
		t.Fatalf("stream payload missing aggregated total_tokens=15: %s", payload)
	}
	if !bytes.Contains(payload, []byte(`"reasoning_tokens":644`)) {
		t.Fatalf("stream payload missing aggregated reasoning_tokens=644: %s", payload)
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

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferMaxBytesFlushesAndDisablesRetry(t *testing.T) {
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
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil because buffer cap disables retry guard", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if !bytes.Contains(payload, []byte("visible")) {
		t.Fatalf("stream payload missing flushed visible delta: %s", payload)
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
	return `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"` + model + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":` + strconv.Itoa(input) + `,"output_tokens":` + strconv.Itoa(output) + `,"total_tokens":` + strconv.Itoa(total) + `,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoning) + `}}}}` + "\n\n"
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
