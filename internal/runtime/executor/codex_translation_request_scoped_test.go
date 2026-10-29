package executor

import (
	"context"
	"errors"
	"fmt"
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

// A local conversion failure after a completed upstream response must neither
// cool down the healthy credential nor replay the paid call on another one.
func TestCodexTranslationFailureDoesNotRotateOrCooldown(t *testing.T) {
	for _, outcome := range []string{"empty", "tool_error"} {
		t.Run(outcome, func(t *testing.T) {
			alias := uuid.NewString()
			format := sdktranslator.Format("test-scoped-" + alias)
			sdktranslator.Register(format, sdktranslator.FormatCodex, nil, sdktranslator.ResponseTransform{
				NonStream: func(_ context.Context, _ string, _, _, _ []byte, param *any) []byte {
					if outcome == "empty" {
						return nil
					}
					state := &translatorcommon.ApplyPatchErrorState{}
					state.SetToolInputError(errors.New("synthetic conversion error"))
					*param = state
					return []byte(`{"id":"synthetic","output":[]}`)
				},
			})
			t.Cleanup(func() { sdktranslator.Unregister(format, sdktranslator.FormatCodex) })

			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":3,\"total_tokens\":8}}}\n\n")
			}))
			t.Cleanup(server.Close)

			exec := NewCodexExecutor(&config.Config{})
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(exec)
			model := "scoped-" + alias
			var ids []string
			for i := 0; i < 2; i++ {
				auth := codexAPIKeyTestAuth(server.URL)
				auth.ID = fmt.Sprintf("scoped-%s-%d", alias, i)
				auth.Status = cliproxyauth.StatusActive
				if _, err := manager.Register(t.Context(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, exec.Identifier(), []*registry.ModelInfo{{ID: model}})
				id := auth.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				ids = append(ids, auth.ID)
			}

			_, err := manager.Execute(t.Context(), []string{"codex"}, cliproxyexecutor.Request{
				Model: model, Payload: []byte(`{"model":"` + model + `","input":"synthetic"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: format})
			if err == nil {
				t.Fatal("expected translation failure")
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("upstream calls=%d, want 1 (no credential rotation)", got)
			}
			for _, id := range ids {
				auth, ok := manager.GetByID(id)
				if !ok {
					t.Fatalf("auth %s missing", id)
				}
				if auth.Unavailable {
					t.Fatalf("auth %s became unavailable", id)
				}
				for name, state := range auth.ModelStates {
					if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
						t.Fatalf("auth %s model %s cooled down: %+v", id, name, state)
					}
				}
			}
		})
	}
}
