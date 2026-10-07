package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// registerCodexToolErrorTranslator registers a response converter that reports
// a tool-input failure whenever the completed payload contains marker.
func registerCodexToolErrorTranslator(t *testing.T, marker string) sdktranslator.Format {
	t.Helper()
	format := sdktranslator.Format("test-abnormal-" + uuid.NewString())
	sdktranslator.Register(format, sdktranslator.FormatCodex, nil, sdktranslator.ResponseTransform{
		NonStream: func(_ context.Context, _ string, _, _, raw []byte, param *any) []byte {
			if bytes.Contains(raw, []byte(marker)) {
				state := &translatorcommon.ApplyPatchErrorState{}
				state.SetToolInputError(errors.New("synthetic conversion error"))
				*param = state
			}
			return append([]byte(nil), raw...)
		},
	})
	t.Cleanup(func() { sdktranslator.Unregister(format, sdktranslator.FormatCodex) })
	return format
}

func runCodexAbnormalPassThrough(t *testing.T, maxRetries int, format sdktranslator.Format, texts []string) (cliproxyexecutor.Response, error, int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(texts) {
			n = len(texts) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSEWithTextAndUsage("gpt-5.5", texts[n], 516, 1, 5+n, 6+n)))
	}))
	t.Cleanup(server.Close)

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(maxRetries, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)))
	manager.SetRetryConfig(0, 0, 0)
	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	auth.ID = "codex-abnormal-translation-" + uuid.NewString()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), ResponseFormat: format})
	return resp, err, calls.Load()
}

func TestCodexAbnormalRetryPassThroughNeverDeliversFailedTranslation(t *testing.T) {
	for _, maxRetries := range []int{0, 2} {
		format := registerCodexToolErrorTranslator(t, "broken")
		resp, err, calls := runCodexAbnormalPassThrough(t, maxRetries, format, []string{"broken"})
		if err == nil {
			t.Fatalf("max-retries=%d: expected an error, got success payload=%q", maxRetries, resp.Payload)
		}
		if len(resp.Payload) != 0 {
			t.Fatalf("max-retries=%d: failed translation leaked payload %q", maxRetries, resp.Payload)
		}
		if want := int32(maxRetries + 1); calls != want {
			t.Fatalf("max-retries=%d: upstream calls=%d, want %d", maxRetries, calls, want)
		}
	}
}

func TestCodexAbnormalRetryPassThroughSkipsUntranslatableCandidates(t *testing.T) {
	format := registerCodexToolErrorTranslator(t, "broken")
	resp, err, calls := runCodexAbnormalPassThrough(t, 2, format, []string{"valid", "broken-but-longer", "broken"})
	if err != nil {
		t.Fatalf("expected the validated fallback, got error %v", err)
	}
	if calls != 3 {
		t.Fatalf("upstream calls=%d, want 3", calls)
	}
	if !bytes.Contains(resp.Payload, []byte("valid")) || bytes.Contains(resp.Payload, []byte("broken")) {
		t.Fatalf("delivered payload=%s, want the only translatable candidate", resp.Payload)
	}
}
