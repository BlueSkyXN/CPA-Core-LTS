package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebsocketActivationAfterUpstreamDisconnect(t *testing.T) {
	tests := []struct {
		name     string
		readLoop func(*codexWebsocketSession, *websocket.Conn)
	}{
		{
			name: "codex",
			readLoop: func(sess *codexWebsocketSession, conn *websocket.Conn) {
				NewCodexWebsocketsExecutor(nil).readUpstreamLoop(sess, codexWebsocketConnectionRef{conn: conn, generation: sess.connGen})
			},
		},
		{
			name: "xai",
			readLoop: func(sess *codexWebsocketSession, conn *websocket.Conn) {
				NewXAIWebsocketsExecutor(nil).readUpstreamLoop(sess, codexWebsocketConnectionRef{conn: conn, generation: sess.connGen})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverClosed := make(chan struct{})
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					return
				}
				_ = conn.Close()
				close(serverClosed)
			}))
			defer server.Close()

			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			defer func() { _ = conn.Close() }()

			sess := &codexWebsocketSession{
				conn:       conn,
				connGen:    1,
				connCloser: newWebsocketConnectionCloser(conn),
				readerConn: conn,
				readerGen:  1,
			}
			connection := codexWebsocketConnectionRef{conn: conn, generation: 1}
			sess.configureConn(conn)
			go test.readLoop(sess, conn)

			<-serverClosed
			deadline := time.NewTimer(time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer deadline.Stop()
			defer ticker.Stop()
			for {
				sess.connMu.Lock()
				invalidated := sess.conn == nil
				sess.connMu.Unlock()
				if invalidated {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("upstream disconnect was not processed")
				}
			}

			activationDone := make(chan error, 1)
			go func() {
				_, errActive := sess.setActiveConnection(connection, make(chan codexWebsocketRead, 1))
				activationDone <- errActive
			}()
			select {
			case errActive := <-activationDone:
				if errActive == nil {
					t.Fatal("expected terminal upstream error on activation after disconnect, got nil")
				}
			case <-time.After(time.Second):
				t.Fatal("activation after upstream disconnect blocked forever")
			}
		})
	}
}
