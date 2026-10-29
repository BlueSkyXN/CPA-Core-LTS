package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexWebsocketProxyChangeFencesConnectionGeneration(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(&config.Config{})
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "proxy-generation", Provider: "codex"}
	sess := executor.getOrCreateSession("proxy-generation")
	t.Cleanup(func() { executor.CloseExecutionSession("proxy-generation") })
	key := newCodexWebsocketConnectionKey(auth.ID, "ws"+strings.TrimPrefix(server.URL, "http"), "gpt-5.4", [32]byte{})
	first, _, _, err := executor.ensureUpstreamConn(context.Background(), auth, sess, key, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	active := make(chan codexWebsocketRead, 1)
	signal, err := sess.setActiveConnection(first, active)
	if err != nil {
		t.Fatal(err)
	}

	ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "direct")
	changed := key
	changed.proxyURL = "direct"
	if found, _ := existingCodexWebsocketSessionConnection(sess, changed); found.conn != nil {
		t.Fatal("required replay reused the connection across a proxy profile change")
	}
	second, _, _, err := executor.ensureUpstreamConn(ctx, auth, sess, key, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if second.conn == first.conn || second.generation == first.generation {
		t.Fatal("proxy profile change did not replace the connection generation")
	}
	if signal.ctx.Err() == nil || sess.isCurrentConnection(first) {
		t.Fatal("old proxy generation remains active")
	}
	if sess.dispatchRead(first, codexWebsocketRead{payload: []byte("stale")}, false) {
		t.Fatal("old proxy generation delivered a frame")
	}
	reused, _, _, err := executor.ensureUpstreamConn(ctx, auth, sess, key, http.Header{})
	if err != nil || reused != second {
		t.Fatalf("same proxy profile not reused: err=%v", err)
	}
	if found, _ := existingCodexWebsocketSessionConnection(sess, changed); found != second {
		t.Fatal("required replay cannot find the current proxy generation")
	}
}
