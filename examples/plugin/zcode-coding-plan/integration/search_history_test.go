package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
)

func TestZcodeResponsesWebsocketSearchHistoryAfterModelSwitch(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	prior := http.DefaultTransport
	http.DefaultTransport = blockedContextNetwork{}
	t.Cleanup(func() { http.DefaultTransport = prior })
	fixture := newV2Fixture(t, "glm-5.3", "glm-5.4")
	transport := &contextCountTransport{base: fixture.transport}
	fixture.manager.SetRoundTripperProvider(transport)
	fixture.cfg.Client.Codex.WebSearchResultCache.Enabled = true
	fixture.host.ApplyConfig(context.Background(), fixture.cfg)
	handlerDone := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { handlerDone <- struct{}{} }()
		fixture.router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	for _, target := range []string{"glm-5.3", "glm-5.4"} {
		t.Run(target, func(t *testing.T) {
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = conn.Close()
				select {
				case <-handlerDone:
				case <-time.After(5 * time.Second):
					t.Error("WebSocket handler did not finish before plugin cleanup")
				}
			})
			turn := func(request string) string {
				t.Helper()
				if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
					t.Fatal(err)
				}
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				for {
					_, payload, err := conn.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					event := ojson.ParseBytes(payload)
					switch event.Get("type").String() {
					case "response.completed":
						return event.Get("response.id").String()
					case "response.failed", "error":
						t.Fatalf("unexpected terminal event: %s", payload)
					}
				}
			}
			parent := turn(`{"type":"response.create","model":"glm-5.3","input":[{"role":"user","content":"Find a source"}],"tools":[{"type":"web_search"}]}`)
			if parent == "" {
				t.Fatal("first turn did not return a response ID")
			}
			turn(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":%q,"input":[{"role":"user","content":"Use the source from above"}]}`, target, parent))

			transport.mu.Lock()
			wire := ojson.ParseBytes(transport.generationBody)
			transport.mu.Unlock()
			if wire.Get("model").String() != target {
				t.Fatal("follow-up did not dispatch to the requested model")
			}
			var nativeResult, readableHistory bool
			for _, message := range wire.Get("messages").Array() {
				for _, block := range message.Get("content").Array() {
					switch block.Get("type").String() {
					case "web_search_tool_result":
						nativeResult = block.Get("content.0.refer").String() == "ref_1"
						if target == "glm-5.4" {
							t.Fatal("GLM-5.4 received a native search result")
						}
					case "text":
						text := block.Get("text").String()
						readableHistory = readableHistory || strings.Contains(text, "Fixture\nhttps://example.com/\nretained search body")
					}
				}
			}
			if target == "glm-5.3" && !nativeResult || target == "glm-5.4" && !readableHistory {
				t.Fatal("follow-up lost the earlier search hit")
			}
		})
	}
}
