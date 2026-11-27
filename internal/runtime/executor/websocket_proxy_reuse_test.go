package executor

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebsocketSessionIsolatesReusableConnectionByProxy(t *testing.T) {
	conn := &websocket.Conn{}
	closer := newWebsocketConnectionCloser(conn)
	sess := &codexWebsocketSession{
		authID:     "auth-1",
		wsURL:      "wss://upstream.example/v1",
		proxyURL:   "http://proxy-a.example:8081",
		conn:       conn,
		connGen:    1,
		connCloser: closer,
		connKey:    newXAIWebsocketConnectionKey("auth-1", "wss://upstream.example/v1", "http://proxy-a.example:8081"),
	}

	if got, _ := existingCodexWebsocketSessionConnection(sess, newXAIWebsocketConnectionKey("auth-1", sess.wsURL, "http://proxy-b.example:8082")); got.conn != nil {
		t.Fatal("reused websocket after proxy_url changed")
	}
	if got, _ := existingCodexWebsocketSessionConnection(sess, newXAIWebsocketConnectionKey("auth-1", sess.wsURL, "")); got.conn != nil {
		t.Fatal("reused proxied websocket after proxy override was removed")
	}
	if got, _ := existingCodexWebsocketSessionConnection(sess, newXAIWebsocketConnectionKey("auth-1", sess.wsURL, sess.proxyURL)); got.conn == nil {
		t.Fatal("did not reuse websocket for the same proxy")
	}
	if !websocketSessionTargetChanged(sess, "auth-1", sess.wsURL, "http://proxy-b.example:8082") {
		t.Fatal("proxy change was not treated as a websocket target change")
	}
	if !websocketSessionTargetChanged(sess, "auth-1", sess.wsURL, "") {
		t.Fatal("removing the proxy override was not treated as a websocket target change")
	}

	detachedCloser := sess.detachConnectionRef(codexWebsocketConnectionRef{conn: conn, generation: 1}, nil)
	if detachedCloser != closer {
		t.Fatal("detaching generational connection did not return the owned closer")
	}
	if sess.conn != nil {
		t.Fatal("detached websocket remained attached")
	}
}
