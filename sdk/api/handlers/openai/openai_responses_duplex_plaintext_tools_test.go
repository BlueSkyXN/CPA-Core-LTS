package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Exercise the handler and real Codex executor across ordinary and duplex turns.
func TestResponsesWebsocketPlaintextToolsWithCodexExecutor(t *testing.T) {
	for _, steering := range []bool{false, true} {
		for _, transition := range []string{"add", "redeclare_without_encryption", "inherit", "append", "inline_replace", "upstream_encrypted", "spawn_namespace", "branch_restore", "branch_revoke"} {
			t.Run(fmt.Sprintf("steering=%t/%s", steering, transition), func(t *testing.T) {
				turnCount := 2
				if strings.HasPrefix(transition, "branch_") {
					turnCount = 3
				}
				functionName := "send_message"
				toolsDeclaration := collaborationPlaintextTestTools
				if transition == "spawn_namespace" {
					functionName = "spawn_agent"
					toolsDeclaration = strings.ReplaceAll(toolsDeclaration, "send_message", functionName)
				}
				var connections atomic.Int32
				upstreamDone := make(chan struct{}, 4)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					connections.Add(1)
					defer func() { upstreamDone <- struct{}{} }()
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
					for turn := 1; turn <= turnCount; turn++ {
						_, request, err := conn.ReadMessage()
						if err != nil {
							t.Errorf("upstream turn %d: %v", turn, err)
							return
						}
						marker := gjson.GetBytes(request, `tools.#(name=="collaboration").tools.0.parameters.properties.message.encrypted`)
						if marker.Type == gjson.True {
							t.Error("encryption marker was not removed before dispatch")
						}
						if turn == 2 && transition == "append" && steering && gjson.GetBytes(request, "previous_response_id").String() != "review-r1" {
							t.Error("append did not resolve its implicit parent")
						}
						if turn == 3 && (gjson.GetBytes(request, "previous_response_id").String() != "review-r1" || gjson.GetBytes(request, "tools").Exists()) {
							t.Errorf("branch must continue the first response without redeclaring tools: %s", request)
						}
						id := fmt.Sprintf("review-r%d", turn)
						item := `{"id":"review-msg-1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]}`
						if turn == 2 || transition != "add" {
							namespace := "collaboration"
							if transition == "spawn_namespace" {
								namespace = "collaboration-optimize"
							}
							extra := ""
							if transition == "upstream_encrypted" {
								extra = `,"encrypted_function_args":["message"]`
							}
							item = fmt.Sprintf(`{"id":"review-fc-%d","type":"function_call","call_id":"review-call-%d","namespace":%q,"name":%q,"arguments":"{\"message\":\"synthetic delegated task\"}"%s}`, turn, turn, namespace, functionName, extra)
						}
						for _, event := range []string{
							fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, id),
							fmt.Sprintf(`{"type":"response.output_item.done","response_id":%q,"output_index":0,"item":%s}`, id, item),
							fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[%s],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`, id, item),
						} {
							if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
								t.Errorf("upstream write: %v", err)
								return
							}
						}
					}
					_, _, _ = conn.ReadMessage()
				}))
				defer upstream.Close()
				cfg := &config.Config{}
				cfg.DisableImageGeneration = config.DisableImageGenerationPassthrough
				cfg.Client.Codex.OptimizeMultiAgentV2 = true
				cfg.Codex.ResponseSteering = steering
				cfg.CodexResponseSteering = steering
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				authID := "review-duplex-" + strings.ReplaceAll(t.Name(), "/", "-")
				_, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "synthetic-review-key", "base_url": upstream.URL, "websockets": "true"}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "review-duplex-model"}})
				defer registry.GetGlobalRegistry().UnregisterClient(authID)
				handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.GET("/v1/responses", handler.ResponsesWebsocket)
				downstream := httptest.NewServer(router)
				defer downstream.Close()
				client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", http.Header{"User-Agent": []string{"Codex Desktop/0.163.0"}})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
				for turn := 1; turn <= turnCount; turn++ {
					toolsField := `,"tools":` + toolsDeclaration
					input := `[{"role":"user","content":"synthetic turn"}]`
					if transition == "add" && turn == 1 {
						toolsField = `,"tools":[]`
					}
					parent := ""
					kind := "response.create"
					if turn == 2 {
						parent = `,"previous_response_id":"review-r1"`
						if transition != "add" {
							input = `[{"type":"function_call_output","call_id":"review-call-1","output":"ok"}]`
						}
						switch transition {
						case "redeclare_without_encryption":
							toolsField = strings.ReplaceAll(toolsField, `"encrypted":true`, `"encrypted":false`)
						case "inherit", "upstream_encrypted":
							toolsField = ""
						case "append":
							kind, toolsField = "response.append", ""
							if steering {
								parent = ""
							}
						case "inline_replace":
							toolsField = ""
							input = strings.TrimSuffix(input, "]") + `,{"type":"additional_tools","tools":` + strings.ReplaceAll(collaborationPlaintextTestTools, `"encrypted":true`, `"encrypted":false`) + `}]`
						}
					}
					if (transition == "branch_restore" && turn == 2) || (transition == "branch_revoke" && turn == 1) {
						toolsField = strings.ReplaceAll(toolsField, `"encrypted":true`, `"encrypted":false`)
					}
					if turn == 3 {
						toolsField = ""
						parent = `,"previous_response_id":"review-r1"`
						input = `[{"type":"function_call_output","call_id":"review-call-1","output":"continue the first response"}]`
					}
					request := fmt.Sprintf(`{"type":%q,"model":"review-duplex-model","input":%s%s%s}`, kind, input, toolsField, parent)
					if err := client.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
						t.Fatal(err)
					}
					for {
						_, payload, err := client.ReadMessage()
						if err != nil {
							t.Fatalf("downstream turn %d: %v", turn, err)
						}
						typ := gjson.GetBytes(payload, "type").String()
						if typ == "error" || typ == "response.failed" {
							t.Fatalf("unexpected protocol error: %s", payload)
						}
						if typ != "response.completed" && typ != "response.output_item.done" {
							continue
						}
						path := "response.output.0"
						if typ == "response.output_item.done" {
							path = "item"
						}
						item := gjson.GetBytes(payload, path)
						if (turn == 2 || transition != "add") && item.Get("type").String() != "function_call" {
							t.Errorf("turn %d %s: function call disappeared", turn, typ)
						}
						if item.Get("type").String() == "function_call" {
							want := "[]"
							if turn == 2 && (transition == "redeclare_without_encryption" || transition == "inline_replace") {
								want = ""
							}
							if transition == "upstream_encrypted" {
								want = `["message"]`
							}
							if (transition == "branch_restore" && turn == 2) || (transition == "branch_revoke" && (turn == 1 || turn == 3)) {
								want = ""
							}
							if got := item.Get("encrypted_function_args").Raw; got != want {
								t.Errorf("turn %d %s: plaintext marker=%q want=%q", turn, typ, got, want)
							}
							if item.Get("arguments").String() != `{"message":"synthetic delegated task"}` || item.Get("call_id").String() != fmt.Sprintf("review-call-%d", turn) {
								t.Errorf("turn %d: function arguments or identity changed", turn)
							}
							if item.Get("namespace").String() != "collaboration" || item.Get("name").String() != functionName {
								t.Errorf("turn %d: client tool namespace or name was not restored", turn)
							}
						}
						if typ == "response.completed" {
							break
						}
					}
				}
				_ = client.Close()
				select {
				case <-upstreamDone:
				case <-time.After(3 * time.Second):
					t.Fatal("upstream cleanup stalled")
				}
				if connections.Load() != 1 {
					t.Errorf("expected one persistent connection, got %d", connections.Load())
				}
			})
		}
	}
}
