package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"

	coreapi "github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestOAuthDaybreakBlueVirtualAliasRotation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, errorType := range []string{"", "permission_error", "invalid_request_error"} {
			t.Run(fmt.Sprintf("stream=%t/type=%s", stream, errorType), func(t *testing.T) {
				testOAuthDaybreakBlueVirtualAliasRotation(t, stream, errorType)
			})
		}
	}
}

func testOAuthDaybreakBlueVirtualAliasRotation(t *testing.T, stream bool, errorType string) {
	t.Helper()
	const (
		baseModel    = "gpt-5.6-sol"
		virtualModel = "gpt-5.6-sol-blue"
		firstToken   = "synthetic-blue-ineligible-token"
		secondToken  = "synthetic-blue-eligible-token"
	)
	type attempt struct {
		auth       string
		model      string
		blue       bool
		hasProgram bool
		path       string
	}
	var mu sync.Mutex
	var attempts []attempt
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		current := attempt{
			auth:       strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			model:      gjson.GetBytes(body, "model").String(),
			blue:       gjson.GetBytes(body, "access_programs.cyber").String() == "daybreak_blue",
			hasProgram: gjson.GetBytes(body, "access_programs").Exists(),
			path:       r.URL.Path,
		}
		mu.Lock()
		attempts = append(attempts, current)
		mu.Unlock()
		if current.auth == firstToken && current.blue {
			errorBody := map[string]string{
				"code":    "access_program_not_enabled",
				"message": "The requested access program is not enabled.",
			}
			if errorType != "" {
				errorBody["type"] = errorType
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": errorBody})
			return
		}
		program := ""
		if current.blue {
			program = `,"access_programs":{"cyber":"daybreak_blue"}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_rotation\",\"status\":\"completed\",\"model\":%q%s,\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", baseModel, program)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.CommercialMode = true
	cfg.DisableImageGeneration = internalconfig.DisableImageGenerationPassthrough
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.RequestRetry = 1
	cfg.MaxRetryCredentials = 2
	cfg.OAuthModelAlias = map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: baseModel, Alias: virtualModel, Fork: true}},
	}
	cfg.Payload.OverrideRaw = []internalconfig.PayloadRule{{
		Models: []internalconfig.PayloadModelRule{{Name: virtualModel, Protocol: "codex", Scope: "requested"}},
		Params: map[string]any{"access_programs": map[string]any{"cyber": "daybreak_blue"}},
	}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{cfg: cfg, coreManager: manager}
	var firstID string
	for i, token := range []string{firstToken, secondToken} {
		auth := &coreauth.Auth{
			ID:       fmt.Sprintf("daybreak-rotation-%t-%s-%d", stream, errorType, i),
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  upstream.URL,
				"priority":  fmt.Sprint(100 - i),
			},
			Metadata: map[string]any{"access_token": token},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		service.registerModelsForAuth(context.Background(), auth)
		service.ensureExecutorsForAuth(auth)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if i == 0 {
			firstID = auth.ID
		}
	}
	capture := &daybreakUsageCapture{records: make(chan usage.Record, 8)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &daybreakUsageCapture{}) })
	handler := coreapi.NewServer(cfg, manager, nil, "", coreapi.WithRequestLoggerFactory(nil)).Handler()
	post := func(model string) gjson.Result {
		t.Helper()
		body := fmt.Sprintf(`{"model":%q,"input":"OK","stream":%t}`, model, stream)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("response status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if !stream {
			return gjson.Parse(rec.Body.String())
		}
		for line := range strings.SplitSeq(rec.Body.String(), "\n") {
			data, ok := strings.CutPrefix(line, "data:")
			if ok && gjson.Get(data, "type").String() == "response.completed" {
				return gjson.Get(data, "response")
			}
		}
		t.Fatalf("stream has no response.completed: %s", rec.Body.String())
		return gjson.Result{}
	}

	blueResponse := post(virtualModel)
	if blueResponse.Get("model").String() != baseModel || blueResponse.Get("access_programs.cyber").String() != "daybreak_blue" {
		t.Fatalf("Blue response lost backing model or program: %s", blueResponse.Raw)
	}
	first, ok := manager.GetByID(firstID)
	if !ok || first == nil {
		t.Fatal("first credential disappeared after rotation")
	}
	if first.Failed != 1 || first.Unavailable || !first.NextRetryAfter.IsZero() {
		t.Fatalf("first credential after rejection: failed=%d unavailable=%t retry=%v", first.Failed, first.Unavailable, first.NextRetryAfter)
	}
	for model, state := range first.ModelStates {
		if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded) {
			t.Fatalf("model %q cooled after Blue rejection", model)
		}
	}
	plainResponse := post(baseModel)
	if plainResponse.Get("model").String() != baseModel || plainResponse.Get("access_programs").Exists() {
		t.Fatalf("plain response unexpectedly changed: %s", plainResponse.Raw)
	}
	for _, expected := range []struct {
		failed  bool
		program string
	}{{true, ""}, {false, "daybreak_blue"}, {false, ""}} {
		select {
		case record := <-capture.records:
			if record.Failed != expected.failed || record.ResponseCyberProgram != expected.program {
				t.Fatalf("usage failed/program=%t/%q, want %t/%q", record.Failed, record.ResponseCyberProgram, expected.failed, expected.program)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("usage record was not delivered")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []attempt{
		{auth: firstToken, model: baseModel, blue: true, hasProgram: true, path: "/responses"},
		{auth: secondToken, model: baseModel, blue: true, hasProgram: true, path: "/responses"},
		{auth: firstToken, model: baseModel, blue: false, path: "/responses"},
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempt count = %d, want %d", len(attempts), len(want))
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempt %d = %+v, want %+v", i, attempts[i], want[i])
		}
	}
}

type daybreakUsageCapture struct{ records chan usage.Record }

func (c *daybreakUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if !strings.HasPrefix(record.AuthID, "daybreak-rotation-") {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}
