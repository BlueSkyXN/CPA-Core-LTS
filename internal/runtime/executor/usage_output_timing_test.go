package executor

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCodexWebsocketTTFBIncludesColdHandshake(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		for _, p := range []string{`{"type":"response.created","response":{"id":"audit","output":[]}}`, `{"type":"response.output_text.delta","delta":"answer"}`, `{"type":"response.completed","response":{"id":"audit","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`} {
			if err = conn.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	e.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	sink := newTimingUsageSink()
	id := t.Name()
	result, err := e.ExecuteStream(ctx, &auth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"api_key": "audit-dummy", "base_url": server.URL, "websockets": "true"}}, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}, core.Options{SourceFormat: tr.FormatCodex, Stream: true, Metadata: map[string]any{core.ExecutionSessionMetadataKey: id}})
	if err != nil {
		t.Fatal(err)
	}
	drainTimingStream(t, result)
	r := waitTimingUsageRecord(t, sink, id)
	t.Logf("handshake_delay=120ms latency=%v ttfb=%v ttfa=%v", r.Latency, r.TTFB, r.TTFA)
	if r.Failed || r.Latency < 120*time.Millisecond || r.TTFB < 120*time.Millisecond {
		t.Fatal("cold handshake missing from attempt timing")
	}
}
