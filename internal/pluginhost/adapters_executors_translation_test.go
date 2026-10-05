package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A plugin response that the host cannot convert must be reported as a
// request-scoped failure with its upstream usage, never as a success.
func TestExecutorAdapterExecuteTranslationFailureIsRequestScoped(t *testing.T) {
	for _, outcome := range []string{"success", "empty", "tool_error"} {
		t.Run(outcome, func(t *testing.T) {
			provider := "plugin-provider"
			plugin := newTestUsageCapturePlugin(provider)
			registerTestUsagePlugin(t, "test-adapter-translation-"+outcome, plugin)

			format := sdktranslator.Format("test-plugin-" + strings.ReplaceAll(uuid.NewString(), "-", ""))
			sdktranslator.Register(format, sdktranslator.FormatOpenAI, nil, sdktranslator.ResponseTransform{
				NonStream: func(_ context.Context, _ string, _, _, raw []byte, param *any) []byte {
					switch outcome {
					case "empty":
						return nil
					case "tool_error":
						state := &translatorcommon.ApplyPatchErrorState{}
						state.SetToolInputError(errors.New("synthetic conversion error"))
						*param = state
					}
					return append([]byte(nil), raw...)
				},
			})
			t.Cleanup(func() { sdktranslator.Unregister(format, sdktranslator.FormatOpenAI) })

			record := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin"})
			host := newHostWithRecords(record)
			exec := &fakeExecutor{
				identifier: provider,
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{
						Payload: []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
						Headers: http.Header{"Content-Type": []string{"application/json"}},
					}, nil
				},
			}
			adapter := newExecutorAdapterForRecordForTest(host, record, exec,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
			)
			auth := &coreauth.Auth{ID: "auth-" + outcome, Provider: provider, FileName: "auth.json", Attributes: map[string]string{"type": "oauth"}}
			resp, err := adapter.Execute(context.Background(), auth, coreexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
			}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: format})

			rec := plugin.waitRecord(t, time.Second)
			if rec.Detail.TotalTokens != 30 {
				t.Fatalf("usage detail=%+v, want upstream tokens preserved", rec.Detail)
			}
			if outcome == "success" {
				if err != nil || len(resp.Payload) == 0 || rec.Failed {
					t.Fatalf("success: err=%v bytes=%d failed=%t", err, len(resp.Payload), rec.Failed)
				}
				return
			}
			if err == nil || len(resp.Payload) != 0 {
				t.Fatalf("expected conversion failure, got err=%v bytes=%d", err, len(resp.Payload))
			}
			if scoped, ok := errors.AsType[coreexecutor.RequestScopedError](err); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("conversion failure must be request-scoped: %v", err)
			}
			if !rec.Failed {
				t.Fatal("conversion failure was recorded as a successful request")
			}
			plugin.assertNoRecord(t, 50*time.Millisecond)
		})
	}
}
