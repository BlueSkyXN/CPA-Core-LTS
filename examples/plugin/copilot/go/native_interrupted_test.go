package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeInterruptedToolReplay(t *testing.T) {
	for _, how := range []string{"eof", "cancel", "partial", "close", "reconfigure"} {
		t.Run(how, func(t *testing.T) {
			wire := make(chan string, 4)
			r, h := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					w.Header().Set("Content-Type", "text/event-stream")
					switch req.Header.Get("X-Request-Id") {
					case "A":
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", copilotToolResponse)
					case "B":
						frame := strings.ReplaceAll(copilotToolResponse, "fixture-opaque-state", "unfinished-state")
						if how == "partial" {
							frame = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-fixture","function":{"name":"lookup","arguments":"{"}}]}}]}`
						}
						fmt.Fprintf(w, "data: %s\n\n", frame)
					default:
						var raw json.RawMessage
						if err := json.NewDecoder(req.Body).Decode(&raw); err != nil {
							t.Error(err)
						}
						wire <- ojson.GetBytes(raw, "messages.0.reasoning_opaque").String()
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", matrixFinalAnswer)
					}
				}
			})
			if _, err := r.executeNative(copilotReq("A", false)); err != nil {
				t.Fatal(err)
			}
			gate := make(chan struct{})
			seen := make(chan string, 1)
			unblock := func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
			}
			defer unblock()
			r.caller = toolEmitGate{h, seen, map[string]chan struct{}{"call-fixture": gate}}
			if _, err := r.executeNativeStream(copilotReq("B", true)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-seen:
			case <-time.After(3 * time.Second):
				t.Fatal("no published tool")
			}
			if how == "cancel" {
				r.cancelNative(pluginapi.CancelExecutionRequest{RequestID: "B", ExecutionSessionID: "session"})
				_, err := r.executeNative(copilotToolFollow("while-cancelling", "call-fixture", ""))
				if err == nil {
					t.Error("cancellation exposed old state before release")
				}
			} else if how == "reconfigure" {
				cfg := r.loadedConfig()
				raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: fmt.Appendf(nil, "github_api_endpoint: %s\ncopilot_api_endpoint: %s\n", cfg.GitHubAPIEndpoint, cfg.CopilotAPIEndpoint)})
				if err := r.configure(raw); err != nil {
					t.Fatal(err)
				}
			} else if how == "close" {
				if err := r.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeSession, ExecutionSessionID: "session"}); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			if c := waitCopilotClose(t, h); c.Error == "" {
				t.Fatal("interrupted stream reported success")
			}
			_, err := r.executeNative(copilotToolFollow("after", "call-fixture", ""))
			if how == "close" || how == "reconfigure" {
				if err != nil || <-wire != "" {
					t.Fatal("closed session or old generation repopulated replay")
				}
				return
			}
			expectCopilotConflict(t, err, "tool_continuation_ambiguous", false)
			select {
			case <-wire:
				t.Fatal("interrupted continuation reached inference")
			default:
			}
			if _, err := r.executeNative(copilotToolFollow("supplied", "call-fixture", "fixture-opaque-state")); err != nil || <-wire != "fixture-opaque-state" {
				t.Fatal("explicit state no longer usable")
			}
			if _, err := r.executeNative(copilotToolFollow("unrelated", "other-id", "")); err != nil || <-wire != "" {
				t.Fatal("failed batch blocked an unrelated tool")
			}
			other := copilotToolFollow("other-scope", "call-fixture", "")
			other.ExecutionSessionID = "other-session"
			if _, err := r.executeNative(other); err != nil || <-wire != "" {
				t.Fatal("failed batch crossed session scope")
			}
			if _, err := r.executeNative(copilotReq("A", false)); err != nil {
				t.Fatal(err)
			}
			_, err = r.executeNative(copilotToolFollow("after-later-success", "call-fixture", ""))
			expectCopilotConflict(t, err, "tool_continuation_ambiguous", false)
		})
	}
}

func TestNativeDistinctCompletedToolBatches(t *testing.T) {
	r := newPluginRuntime(&copilotFixtureHost{})
	req := copilotReq("B", true)
	auth, _ := parseStoredAuth(req.StorageJSON)
	exec, err := r.registerNative(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer r.releaseNative(exec)
	a := newNativeDecoder("A", req.Model, nil)
	if _, err := a.data([]byte(matrixToolAnswer)); err != nil {
		t.Fatal(err)
	}
	r.rememberNativeReasoning(exec, req.Model, a.projection)
	b := newNativeDecoder("B", req.Model, func([]byte) error { return nil })
	b.beforeTool = func(i int, call nativeToolIdentity) { r.trackNativeTool(exec, i, call) }
	b.toolsFinished = func(count int) { r.finishNativeTools(exec, count) }
	if _, err := b.data([]byte(strings.Replace(matrixToolAnswer, `\"n\":2`, `\"n\":3`, 1))); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*nativeDecoder{a, b} {
		msgs := []any{map[string]any{"role": "assistant", "tool_calls": d.projection.toolCallPayload()}, map[string]any{"role": "tool", "tool_call_id": "call-A", "content": "a"}, map[string]any{"role": "tool", "tool_call_id": "call-B", "content": "b"}}
		raw, _ := json.Marshal(map[string]any{"messages": msgs})
		out, err := r.restoreNativeReasoning(exec, req.Model, raw, nil)
		if d == b {
			expectCopilotConflict(t, err, "tool_continuation_pending", true)
		} else if err != nil || ojson.GetBytes(out, "messages.0.reasoning_opaque").String() != "fixture-matrix-state" {
			t.Fatalf("completed A blocked by distinct batch B: %v", err)
		}
	}
}
