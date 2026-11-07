package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"net/http"
	"strings"
	"testing"
	"time"

	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const copilotToolResponse = `{"choices":[{"index":0,"message":{"role":"assistant","content":"checking","reasoning_opaque":"fixture-opaque-state","tool_calls":[{"id":"call-fixture","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"reasoning_tokens":2,"total_tokens":16}}`

func TestNativeReasoningSurvivesResponsesToolRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := 0
			runtime, host := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					calls++
					w.Header().Set("Content-Type", "text/event-stream")
					if calls == 1 {
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", copilotToolResponse)
						return
					}
					var body struct {
						Messages []map[string]any `json:"messages"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					found := false
					for _, message := range body.Messages {
						if message["role"] == "assistant" && message["tool_calls"] != nil {
							found = message["reasoning_opaque"] == "fixture-opaque-state"
						}
					}
					if !found {
						t.Error("tool continuation lost opaque state")
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Paris result\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			})
			req := copilotReq("tools", stream)
			var outputs string
			if stream {
				if _, err := runtime.executeNativeStream(req); err != nil {
					t.Fatal(err)
				}
				if closed := waitCopilotClose(t, host); closed.Error != "" {
					t.Fatalf("close = %+v", closed)
				}
				var param any
				frames := append(append([][]byte(nil), host.emitted...), []byte("data: [DONE]"))
				for _, frame := range frames {
					for _, event := range responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), req.Model, req.Payload, req.Payload, frame, &param) {
						for _, line := range strings.Split(string(event), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							data := ojson.Parse(strings.TrimPrefix(line, "data: "))
							if data.Get("type").String() == "response.completed" {
								outputs = data.Get("response.output").Raw
							}
						}
					}
				}
			} else {
				response, err := runtime.executeNative(req)
				if err != nil {
					t.Fatal(err)
				}
				if ojson.GetBytes(response.Payload, "choices.0.message.reasoning_opaque").String() != "fixture-opaque-state" {
					t.Fatal("Chat response lost opaque state")
				}
				var param any
				translated := responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), req.Model, req.Payload, req.Payload, response.Payload, &param)
				outputs = ojson.GetBytes(translated, "output").Raw
			}
			if outputs == "" {
				t.Fatal("Responses output missing")
			}
			items := []json.RawMessage{json.RawMessage(`{"role":"user","content":"lookup Paris"}`)}
			var returned []json.RawMessage
			if err := json.Unmarshal([]byte(outputs), &returned); err != nil {
				t.Fatal(err)
			}
			items = append(items, returned...)
			items = append(items, json.RawMessage(`{"type":"function_call_output","call_id":"call-fixture","output":"fixture result"}`))
			follow, _ := json.Marshal(map[string]any{"model": req.Model, "input": items})
			payload, err := responses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(req.Model, follow, false)
			if err != nil {
				t.Fatal(err)
			}
			req.RequestID, req.Stream, req.Payload = "follow", false, payload
			response, err := runtime.executeNative(req)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(response.Payload), "Paris result") || calls != 2 {
				t.Fatal("tool round trip did not finish")
			}
			assertNativeReleased(t, runtime, host)
		})
	}
}

func TestNativeReasoningReplayScopeAndLifetime(t *testing.T) {
	runtime := newPluginRuntime(nil)
	req := copilotReq("first", false)
	auth, err := parseStoredAuth(req.StorageJSON)
	if err != nil {
		t.Fatal(err)
	}
	execution := func(req rpcExecutorRequest) *nativeExecution {
		auth, err := parseStoredAuth(req.StorageJSON)
		if err != nil {
			t.Fatal(err)
		}
		return &nativeExecution{identity: executionIdentity{key: executionSessionKey(req.ExecutorRequest, auth), authID: req.AuthID, authIndex: req.AuthIndex, executionSessionID: req.ExecutionSessionID, callerScope: req.CallerScope, workspaceIdentity: req.WorkspaceIdentity}, generation: runtime.generation}
	}
	exec := execution(req)
	decoder := newNativeDecoder(req.RequestID, req.Model, nil)
	if _, err := decoder.data([]byte(copilotToolResponse)); err != nil {
		t.Fatal(err)
	}
	runtime.rememberNativeReasoning(exec, req.Model, decoder.projection)
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call-fixture","type":"function","function":{"name":"lookup","arguments":"{ \"city\": \"Paris\" }"}}]}]}`)
	hasOpaque := func(out []byte) bool {
		return ojson.GetBytes(out, "messages.0.reasoning_opaque").String() == "fixture-opaque-state"
	}
	if !hasOpaque(runtime.restoreNativeReasoning(exec, req.Model, body)) {
		t.Fatal("matching call did not replay")
	}
	for name, change := range map[string]func(*rpcExecutorRequest){
		"auth":       func(r *rpcExecutorRequest) { r.AuthID = "other" },
		"auth_index": func(r *rpcExecutorRequest) { r.AuthIndex = "other" },
		"caller":     func(r *rpcExecutorRequest) { r.CallerScope = "other" },
		"workspace":  func(r *rpcExecutorRequest) { r.WorkspaceIdentity = "other" },
		"session":    func(r *rpcExecutorRequest) { r.ExecutionSessionID = "other" },
		"credential": func(r *rpcExecutorRequest) {
			a := auth
			a.GitHubToken = "fixture-other-token"
			r.StorageJSON, _ = json.Marshal(a)
		},
		"model": func(r *rpcExecutorRequest) { r.Model = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			other := req
			change(&other)
			if hasOpaque(runtime.restoreNativeReasoning(execution(other), other.Model, body)) {
				t.Fatal("opaque crossed scope")
			}
		})
	}
	if hasOpaque(runtime.restoreNativeReasoning(exec, req.Model, []byte(strings.ReplaceAll(string(body), "Paris", "Rome")))) {
		t.Fatal("changed arguments reused opaque")
	}
	key := nativeReasoningKey(exec, req.Model, decoder.projection.toolCallPayload())
	replay := runtime.nativeReasoning[key]
	replay.created = time.Now().Add(-2 * nativeReasoningTTL)
	runtime.nativeReasoning[key] = replay
	if hasOpaque(runtime.restoreNativeReasoning(exec, req.Model, body)) {
		t.Fatal("expired opaque replayed")
	}
	runtime.rememberNativeReasoning(exec, req.Model, decoder.projection)
	if err := runtime.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeSession, ExecutionSessionID: req.ExecutionSessionID, CallerScope: req.CallerScope}); err != nil {
		t.Fatal(err)
	}
	if hasOpaque(runtime.restoreNativeReasoning(exec, req.Model, body)) {
		t.Fatal("closed session kept opaque")
	}
	runtime.rememberNativeReasoning(exec, req.Model, decoder.projection)
	if err := runtime.configure(nil); err != nil {
		t.Fatal(err)
	}
	runtime.rememberNativeReasoning(exec, req.Model, decoder.projection)
	if len(runtime.nativeReasoning) != 0 {
		t.Fatal("old generation repopulated opaque cache")
	}
}

func TestNativeReasoningReplayBounded(t *testing.T) {
	runtime := newPluginRuntime(nil)
	exec := &nativeExecution{identity: executionIdentity{key: "fixture-scope"}}
	for i := 0; i < nativeReasoningMaxEntries+10; i++ {
		decoder := newNativeDecoder("fixture", "model", nil)
		if _, err := decoder.data([]byte(strings.ReplaceAll(copilotToolResponse, "call-fixture", fmt.Sprintf("call-%d", i)))); err != nil {
			t.Fatal(err)
		}
		runtime.rememberNativeReasoning(exec, "model", decoder.projection)
	}
	if len(runtime.nativeReasoning) != nativeReasoningMaxEntries {
		t.Fatal("replay count is not bounded")
	}
	decoder := newNativeDecoder("fixture", "model", nil)
	if _, err := decoder.data([]byte(copilotToolResponse)); err != nil {
		t.Fatal(err)
	}
	decoder.projection.reasoningOpaque = strings.Repeat("x", nativeReasoningMaxBytes+1)
	runtime.rememberNativeReasoning(exec, "model", decoder.projection)
	if _, ok := runtime.nativeReasoning[nativeReasoningKey(exec, "model", decoder.projection.toolCallPayload())]; ok {
		t.Fatal("oversized opaque retained")
	}
}
