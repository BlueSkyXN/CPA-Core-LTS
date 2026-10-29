package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketHTTPRejectsMismatchedParentWithoutLosingHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &homeResponsesWebsocketExecutor{provider: "fixture-http"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "fixture-parent-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	const model = "fixture-parent-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	turn := func(raw string) []byte {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := turn(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":"first"}]}`, model))
	if gjson.GetBytes(first, "type").String() != "response.completed" {
		t.Fatal("initial turn failed")
	}
	rejected := turn(`{"type":"response.create","previous_response_id":"other-connection","input":[{"role":"user","content":"must-not-survive"}]}`)
	if gjson.GetBytes(rejected, "error.code").String() != "previous_response_not_found" || executor.calls.Load() != 1 {
		t.Fatal("unknown parent dispatched model")
	}
	recovered := turn(`{"type":"response.create","previous_response_id":"home-response","input":[{"role":"user","content":"next"}]}`)
	if gjson.GetBytes(recovered, "type").String() != "response.completed" || executor.calls.Load() != 2 {
		t.Fatal("valid parent did not recover")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	last := string(executor.payloads[1])
	if strings.Contains(last, "must-not-survive") || !strings.Contains(last, "first") || !strings.Contains(last, "next") {
		t.Fatal("rejected turn changed accepted history")
	}
}
