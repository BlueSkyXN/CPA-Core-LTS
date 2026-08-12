package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketHandshakeFailureReleasesExecutionSession(t *testing.T) {
	tests := []struct {
		name            string
		handshakeStatus int
	}{
		{name: "upgrade-required-fallback", handshakeStatus: http.StatusUpgradeRequired},
		{name: "upstream-status-error", handshakeStatus: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if websocket.IsWebSocketUpgrade(r) {
					w.WriteHeader(tt.handshakeStatus)
					_, _ = w.Write([]byte(`{"error":{"message":"websocket rejected"}}`))
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"http fallback stopped"}}`))
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			auth := &cliproxyauth.Auth{
				ID:         "auth-handshake-" + tt.name,
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL},
			}
			sessionID := "session-handshake-" + tt.name
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, _ := exec.ExecuteStream(ctx, auth, req, opts)
			if result != nil {
				for range result.Chunks {
				}
			}

			sess := exec.getOrCreateSession(sessionID)
			acquired := make(chan struct{})
			go func() {
				sess.reqMu.Lock()
				close(acquired)
				sess.reqMu.Unlock()
			}()
			select {
			case <-acquired:
			case <-time.After(2 * time.Second):
				t.Fatal("websocket handshake failure left the execution session locked")
			}
			exec.CloseExecutionSession(sessionID)
		})
	}
}

func TestCodexWebsocketReconnectHandshakePreservesTypedQuotaError(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upgradeAttempts atomic.Int32
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Path; got != "/responses" {
					t.Errorf("request path = %q, want /responses", got)
				}
				if upgradeAttempts.Add(1) == 1 {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Errorf("upgrade stale websocket: %v", err)
						return
					}
					defer func() { _ = conn.Close() }()
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "9")
				w.Header().Set("X-Request-ID", "req-reconnect-quota")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"quota","resets_in_seconds":7}}`))
			}))
			defer server.Close()

			wsURL, err := buildCodexResponsesWebsocketURL(strings.TrimSuffix(server.URL, "/") + "/responses")
			if err != nil {
				t.Fatalf("build websocket URL: %v", err)
			}
			staleConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial stale websocket: %v", err)
			}
			if errClose := staleConn.Close(); errClose != nil {
				t.Fatalf("close stale websocket: %v", errClose)
			}

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{
				ID:         "auth-reconnect-quota-" + tt.name,
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL},
			}
			const model = "gpt-5.4"
			sessionID := "session-reconnect-quota-" + tt.name
			sess := exec.getOrCreateSession(sessionID)
			connectionKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, model, resolveCodexModelHeaderProfile(model).digest)
			sess.connMu.Lock()
			sess.conn = staleConn
			sess.connGen = 1
			sess.connKey = connectionKey
			sess.wsURL = wsURL
			sess.authID = auth.ID
			sess.readerConn = staleConn
			sess.readerGen = 1
			sess.connMu.Unlock()
			defer exec.CloseExecutionSession(sessionID)

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var executeErr error
			if tt.stream {
				result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
				if result != nil {
					t.Fatalf("ExecuteStream() result = %#v, want nil on reconnect handshake rejection", result)
				}
				executeErr = errStream
			} else {
				_, executeErr = exec.Execute(ctx, auth, req, opts)
			}
			if executeErr == nil {
				t.Fatal("reconnect handshake error = nil, want typed usage-limit error")
			}
			classified, ok := executeErr.(interface{ ModelFallbackReason() string })
			if !ok || classified.ModelFallbackReason() != config.CodexModelFallbackTriggerUsageLimit {
				t.Fatalf("fallback reason = %T %v, want %q", executeErr, executeErr, config.CodexModelFallbackTriggerUsageLimit)
			}
			retryable, ok := executeErr.(interface{ RetryAfter() *time.Duration })
			if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
				t.Fatalf("RetryAfter = %v, want 7s", retryable.RetryAfter())
			}
			withHeaders, ok := executeErr.(interface{ Headers() http.Header })
			if !ok {
				t.Fatalf("reconnect error type %T does not expose headers", executeErr)
			}
			if got := withHeaders.Headers().Get("X-Request-ID"); got != "req-reconnect-quota" {
				t.Fatalf("X-Request-ID = %q, want req-reconnect-quota", got)
			}
			if got := upgradeAttempts.Load(); got != 2 {
				t.Fatalf("upgrade attempts = %d, want stale connection plus one reconnect", got)
			}
		})
	}
}

func TestCodexWebsocketStaleReaderCannotCloseNewActiveChannel(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverReady := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		serverReady <- struct{}{}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	oldConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	select {
	case <-serverReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket server")
	}

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sess := &codexWebsocketSession{sessionID: "stale-reader-generation"}
	oldConnection := codexWebsocketConnectionRef{conn: oldConn, generation: 1}
	newConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 2}

	sess.connMu.Lock()
	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	sess.readerConn = oldConnection.conn
	sess.readerGen = oldConnection.generation
	sess.connMu.Unlock()

	readerDone := make(chan struct{})
	go func() {
		exec.readUpstreamLoop(sess, oldConnection)
		close(readerDone)
	}()

	readCh := make(chan codexWebsocketRead, 4)
	sess.connMu.Lock()
	sess.conn = newConnection.conn
	sess.connGen = newConnection.generation
	sess.readerConn = newConnection.conn
	sess.readerGen = newConnection.generation
	sess.connMu.Unlock()
	requestSignal, errActive := sess.setActiveConnection(newConnection, readCh)
	if errActive != nil {
		t.Fatalf("install new active connection: %v", errActive)
	}
	if requestSignal == nil {
		t.Fatal("install new active connection returned a nil request signal")
	}

	if errClose := oldConn.Close(); errClose != nil {
		t.Fatalf("close old websocket: %v", errClose)
	}
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stale reader did not exit")
	}

	if activeCh, _, ok := sess.activeFor(newConnection); !ok || activeCh != readCh {
		t.Fatal("stale reader cleared the new connection active channel")
	}
	select {
	case event, ok := <-readCh:
		if !ok {
			t.Fatal("stale reader closed the new connection active channel")
		}
		t.Fatalf("stale reader delivered an event to the new connection: %#v", event)
	default:
	}

	sess.clearActiveConnection(newConnection, readCh)
	sess.connMu.Lock()
	sess.conn = nil
	sess.readerConn = nil
	sess.connMu.Unlock()
}

func TestCodexWebsocketMultiAgentV2StateIsGenerationScoped(t *testing.T) {
	conn := &websocket.Conn{}
	first := codexWebsocketConnectionRef{conn: conn, generation: 1}
	second := codexWebsocketConnectionRef{conn: conn, generation: 2}
	sess := &codexWebsocketSession{conn: conn, connGen: first.generation}

	sess.setMultiAgentV2Optimized(first, true)
	if !sess.isMultiAgentV2Optimized(first) {
		t.Fatal("first generation did not retain Multi-Agent v2 optimization state")
	}

	sess.connMu.Lock()
	sess.connGen = second.generation
	sess.connMu.Unlock()
	if sess.isMultiAgentV2Optimized(second) {
		t.Fatal("new connection generation inherited stale Multi-Agent v2 optimization state")
	}

	sess.connMu.Lock()
	sess.multiAgentV2OptimizedGen = first.generation
	sess.connMu.Unlock()
	sess.setMultiAgentV2Optimized(first, false)
	if sess.multiAgentV2OptimizedGen != first.generation {
		t.Fatalf("stale generation cleared optimization state: got %d, want %d", sess.multiAgentV2OptimizedGen, first.generation)
	}
	sess.setMultiAgentV2Optimized(second, false)
	if sess.multiAgentV2OptimizedGen != 0 {
		t.Fatalf("current generation did not clear optimization state: got %d", sess.multiAgentV2OptimizedGen)
	}
}

func TestCodexWebsocketGenerationRetryRebindUsesCurrentConnection(t *testing.T) {
	sess := &codexWebsocketSession{sessionID: "retry-generation-rebind"}
	oldConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 1}
	newConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 2}
	readCh := make(chan codexWebsocketRead, 4)

	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	_, errActive := sess.setActiveConnection(oldConnection, readCh)
	if errActive != nil {
		t.Fatalf("install initial retry connection: %v", errActive)
	}
	sess.connMu.Lock()
	sess.conn = newConnection.conn
	sess.connGen = newConnection.generation
	sess.connMu.Unlock()
	requestSignal, errActive := sess.setActiveConnection(newConnection, readCh)
	if errActive != nil {
		t.Fatalf("rebind retry connection: %v", errActive)
	}

	if delivered := sess.dispatchRead(oldConnection, codexWebsocketRead{err: errors.New("stale retry error")}, true); delivered {
		t.Fatal("stale connection delivered an error after retry rebind")
	}
	wantPayload := []byte(`{"type":"response.completed"}`)
	if delivered := sess.dispatchRead(newConnection, codexWebsocketRead{msgType: websocket.TextMessage, payload: wantPayload}, false); !delivered {
		t.Fatal("current retry connection did not deliver its response")
	}

	msgType, payload, errRead := readCodexWebsocketMessage(context.Background(), sess, newConnection, readCh, requestSignal)
	if errRead != nil {
		t.Fatalf("read current retry response: %v", errRead)
	}
	if msgType != websocket.TextMessage || !bytes.Equal(payload, wantPayload) {
		t.Fatalf("retry response = type %d payload %s, want type %d payload %s", msgType, payload, websocket.TextMessage, wantPayload)
	}
	sess.clearActiveConnection(newConnection, readCh)
}

func TestCodexWebsocketGenerationRetryRebindRejectedAfterSessionClose(t *testing.T) {
	sess := &codexWebsocketSession{sessionID: "retry-rebind-after-close"}
	oldConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 11}
	retryConnection := codexWebsocketConnectionRef{conn: &websocket.Conn{}, generation: 12}
	readCh := make(chan codexWebsocketRead, 1)

	sess.conn = oldConnection.conn
	sess.connGen = oldConnection.generation
	_, errActive := sess.setActiveConnection(oldConnection, readCh)
	if errActive != nil {
		t.Fatalf("install initial retry active connection: %v", errActive)
	}

	sess.connMu.Lock()
	sess.conn = nil
	sess.connGen = retryConnection.generation + 1
	sess.closed = true
	sess.connMu.Unlock()
	sess.cancelActiveConnection(errors.New("test session closed"))

	if _, errActive := sess.setActiveConnection(retryConnection, readCh); errActive == nil {
		t.Fatal("closed session accepted a retry generation rebind")
	}
	if _, _, active := sess.activeFor(oldConnection); active {
		t.Fatal("closed session retained the pre-retry active connection")
	}
	if _, _, active := sess.activeFor(retryConnection); active {
		t.Fatal("closed session installed the retry active connection")
	}
}

func TestCodexWebsocketGenerationSessionCloseCancelsActiveRequest(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	sess := &codexWebsocketSession{
		sessionID:  "generation-session-close",
		conn:       conn,
		connGen:    7,
		readerConn: conn,
		readerGen:  7,
		wsURL:      wsURL,
		authID:     "auth-session-close",
	}
	connection := codexWebsocketConnectionRef{conn: conn, generation: 7}
	readCh := make(chan codexWebsocketRead, 1)
	requestSignal, errActive := sess.setActiveConnection(connection, readCh)
	if errActive != nil {
		t.Fatalf("install session close active connection: %v", errActive)
	}

	closeCodexWebsocketSession(sess, "test_close")

	_, _, errRead := readCodexWebsocketMessage(context.Background(), sess, connection, readCh, requestSignal)
	if errRead == nil || !strings.Contains(errRead.Error(), "test_close") {
		t.Fatalf("session close read error = %v, want generation-scoped close error", errRead)
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("session close left the active request installed")
	}
	sess.connMu.Lock()
	currentConn := sess.conn
	currentGeneration := sess.connGen
	sess.connMu.Unlock()
	if currentConn != nil || currentGeneration == connection.generation {
		t.Fatalf("session close state = conn %p generation %d, want nil and a new generation", currentConn, currentGeneration)
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	connectionKey := newCodexWebsocketConnectionKey("auth-session-close", wsURL, "gpt-5.4", codexModelHeaderProfile{}.digest)
	if _, _, _, errEnsure := exec.ensureUpstreamConn(context.Background(), nil, sess, connectionKey, http.Header{}); errEnsure == nil {
		t.Fatal("closed session accepted a replacement connection")
	}
	lateReadCh := make(chan codexWebsocketRead, 1)
	if _, errActive := sess.setActiveConnection(connection, lateReadCh); errActive == nil {
		t.Fatal("closed session accepted an active request after connection setup")
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("closed session retained a late active request")
	}
	select {
	case event, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel after delivering the error")
		}
		if event.connection != connection || event.err == nil || !strings.Contains(event.err.Error(), "test_close") {
			t.Fatalf("queued session close event = %#v, want generation-scoped close error", event)
		}
	default:
	}
	select {
	case _, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel")
		}
		t.Fatal("session close delivered more than one terminal event")
	default:
	}
}

func TestCodexWebsocketGenerationSessionCloseDoesNotBlockOnFullActiveChannel(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	connection := codexWebsocketConnectionRef{conn: conn, generation: 17}
	sess := &codexWebsocketSession{
		sessionID:  "generation-session-close-full-channel",
		conn:       conn,
		connGen:    connection.generation,
		readerConn: conn,
		readerGen:  connection.generation,
		wsURL:      wsURL,
		authID:     "auth-session-close-full-channel",
	}
	readCh := make(chan codexWebsocketRead, 1)
	readCh <- codexWebsocketRead{connection: connection, msgType: websocket.TextMessage, payload: []byte(`{"type":"response.output_text.delta"}`)}
	requestSignal, errActive := sess.setActiveConnection(connection, readCh)
	if errActive != nil {
		t.Fatalf("install full-channel active connection: %v", errActive)
	}

	closed := make(chan struct{})
	go func() {
		closeCodexWebsocketSession(sess, "full_channel_close")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session close blocked while the request-owned channel was full")
	}
	select {
	case <-requestSignal.ctx.Done():
		if cause := context.Cause(requestSignal.ctx); cause == nil || !strings.Contains(cause.Error(), "full_channel_close") {
			t.Fatalf("request cancellation cause = %v, want full_channel_close", cause)
		}
	default:
		t.Fatal("session close did not cancel the request signal")
	}
	if _, _, active := sess.activeFor(connection); active {
		t.Fatal("session close left the full-channel request active")
	}
	select {
	case _, ok := <-readCh:
		if !ok {
			t.Fatal("session close closed the request-owned channel")
		}
	default:
		t.Fatal("session close unexpectedly drained the request-owned channel")
	}
}

func TestCodexWebsocketsEnsureUpstreamConnRedialsForLunaHeaderProfile(t *testing.T) {
	const (
		normalModel = "gpt-5.4"
		lunaModel   = "gpt-5.6-luna"
		normalUA    = "codex-tui/test-normal"
		lunaUA      = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- r.UserAgent()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-model-header-profile-change"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-1"}

	normalHeaders := http.Header{"User-Agent": []string{normalUA}}
	normalProfile := resolveCodexModelHeaderProfile(normalModel)
	normalKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, normalModel, normalProfile.digest)
	firstConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, normalKey, normalHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(normal) error = %v", err)
	}

	lunaHeaders := http.Header{"User-Agent": []string{normalUA}}
	lunaProfile := resolveCodexModelHeaderProfile(lunaModel)
	applyModelHeaderOverrides(lunaHeaders, lunaProfile)
	lunaKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, lunaModel, lunaProfile.digest)
	secondConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, lunaKey, lunaHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(luna) error = %v", err)
	}
	if secondConn == firstConn {
		t.Fatal("Luna header profile reused the normal-model websocket; want a fresh connection")
	}

	for i, wantUA := range []string{normalUA, lunaUA} {
		select {
		case gotUA := <-handshakes:
			if gotUA != wantUA {
				t.Fatalf("handshake %d User-Agent = %q, want %q", i+1, gotUA, wantUA)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for handshake %d", i+1)
		}
	}

	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("planned connection profile change signaled upstream disconnect: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCodexWebsocketsEnsureUpstreamConnUsesAppliedHeaderProfileSnapshot(t *testing.T) {
	const (
		modelID  = "test-codex-websocket-profile-snapshot"
		clientID = "test-codex-websocket-profile-snapshot-client"
	)

	reg := registry.GetGlobalRegistry()
	registerProfile := func(userAgent string) {
		reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
			ID: modelID,
			Config: &registry.ModelConfig{OverrideHeader: map[string]string{
				"user-agent": userAgent,
			}},
		}})
	}
	registerProfile("snapshot-a/1.0")
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	profileA := resolveCodexModelHeaderProfile(modelID)
	headersA := http.Header{}
	applyModelHeaderOverrides(headersA, profileA)

	registerProfile("snapshot-b/1.0")

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- r.UserAgent()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-applied-header-profile-snapshot"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-profile-snapshot"}

	keyA := newCodexWebsocketConnectionKey(auth.ID, wsURL, modelID, profileA.digest)
	firstConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, keyA, headersA)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(snapshot A) error = %v", err)
	}

	profileB := resolveCodexModelHeaderProfile(modelID)
	headersB := http.Header{}
	applyModelHeaderOverrides(headersB, profileB)
	keyB := newCodexWebsocketConnectionKey(auth.ID, wsURL, modelID, profileB.digest)
	secondConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, keyB, headersB)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(snapshot B) error = %v", err)
	}
	if secondConn == firstConn {
		t.Fatal("updated applied profile reused the snapshot A websocket; want redial")
	}

	for index, wantUserAgent := range []string{"snapshot-a/1.0", "snapshot-b/1.0"} {
		select {
		case gotUserAgent := <-handshakes:
			if gotUserAgent != wantUserAgent {
				t.Fatalf("handshake %d User-Agent = %q, want %q", index+1, gotUserAgent, wantUserAgent)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for handshake %d", index+1)
		}
	}
}

func TestCodexWebsocketsEnsureUpstreamConnReusesMatchingConnectionKey(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handshakes := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- struct{}{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-matching-connection-key"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	headers := http.Header{
		"User-Agent":          []string{"codex-tui/test"},
		"X-Client-Request-Id": []string{"request-1"},
	}

	profile := resolveCodexModelHeaderProfile("gpt-5.4")
	connectionKey := newCodexWebsocketConnectionKey(auth.ID, wsURL, "gpt-5.4", profile.digest)
	firstConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, connectionKey, headers)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(first) error = %v", err)
	}
	secondHeaders := headers.Clone()
	secondHeaders.Set("X-Client-Request-Id", "request-2")
	secondConn, _, _, err := exec.ensureUpstreamConn(context.Background(), auth, sess, connectionKey, secondHeaders)
	if err != nil {
		t.Fatalf("ensureUpstreamConn(second) error = %v", err)
	}
	if secondConn != firstConn {
		t.Fatal("matching connection key redialed; want websocket reuse")
	}

	select {
	case <-handshakes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial websocket handshake")
	}
	select {
	case <-handshakes:
		t.Fatal("matching connection key created a second websocket handshake")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestApplyCodexPromptCacheHeadersOpenAIChatPreservesExplicitKey(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"tenant:explicit"}`),
	}

	body, headers := applyCodexPromptCacheHeaders("openai", req, []byte(`{"model":"gpt-5.6-sol"}`))
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "tenant:explicit" {
		t.Fatalf("prompt_cache_key = %q, want explicit client key; body=%s", got, body)
	}
	if got := headers["Session-Id"]; len(got) != 1 || got[0] != "tenant:explicit" {
		t.Fatalf("Session-Id = %#v, want [tenant:explicit]", got)
	}
	if got := headers.Get("Conversation_id"); got != "tenant:explicit" {
		t.Fatalf("Conversation_id = %q, want tenant:explicit", got)
	}
}

func TestApplyCodexPromptCacheHeadersOpenAIChatUsesStableAPIKeyFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", "test-api-key")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol"}`)}

	firstBody, firstHeaders, err := applyCodexPromptCacheHeadersWithContext(ctx, sdktranslator.FromString("openai"), req, []byte(`{"model":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("first prompt cache headers: %v", err)
	}
	secondBody, secondHeaders, err := applyCodexPromptCacheHeadersWithContext(ctx, sdktranslator.FromString("openai"), req, []byte(`{"model":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("second prompt cache headers: %v", err)
	}

	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" || secondKey != firstKey {
		t.Fatalf("stable fallback keys = (%q, %q), want same non-empty key", firstKey, secondKey)
	}
	if got := firstHeaders.Get("Conversation_id"); got != firstKey {
		t.Fatalf("first Conversation_id = %q, want %q", got, firstKey)
	}
	if got := secondHeaders.Get("Conversation_id"); got != secondKey {
		t.Fatalf("second Conversation_id = %q, want %q", got, secondKey)
	}
}

func TestApplyCodexWebsocketHeadersCanonicalMetadataBypassesLegacyIdentityConfuse(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex: config.CodexConfig{
			IdentityConfuse: true,
			ClientMetadata: config.CodexClientMetadataConfig{
				Mode:            config.CodexClientMetadataModeRepair,
				WorkspacePolicy: config.CodexClientMetadataWorkspacePolicyDrop,
			},
		},
	}
	auth := &cliproxyauth.Auth{ID: "auth-ws-1", Provider: "codex", Metadata: map[string]any{"account_id": "acct-ws-1"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"input":"hello"}`),
	}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"installation_id\":\"install-ws-1\",\"session_id\":\"thread-ws-1\",\"thread_id\":\"thread-ws-1\",\"turn_id\":\"turn-ws-1\",\"window_id\":\"thread-ws-1:2\",\"request_kind\":\"turn\",\"workspaces\":{\"/private/project\":{\"associated_remote_urls\":{\"origin\":\"https://token@example.com/org/repo.git\"}}}}","thread_id":"wrong-thread","x-codex-window-id":"wrong:0"}}`)
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	ctx := contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"thread_id":"header-conflict"}`,
		"X-Client-Request-Id":   "client-request-1",
	})

	upstreamBody, state, err := prepareCodexOutboundMetadata(ctx, cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexOutboundMetadataHeaders(headers, &state)

	if state.enabled || !state.clientMetadata.CanonicalPresent {
		t.Fatalf("canonical websocket state unexpectedly used legacy identity mapping: %+v", state)
	}
	metadata := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-turn-metadata").String()
	if strings.Contains(metadata, `"workspaces"`) || strings.Contains(metadata, "token@example.com") {
		t.Fatalf("drop policy did not remove websocket workspace metadata: %s", metadata)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.thread_id").String(); got != "thread-ws-1" {
		t.Fatalf("flat thread_id = %q", got)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-window-id").String(); got != "thread-ws-1:2" {
		t.Fatalf("flat window_id = %q", got)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != "thread-ws-1:2" {
		t.Fatalf("X-Codex-Window-Id = %q", got)
	}
	if got := codexSessionHeaderValue(headers); got != "thread-ws-1" {
		t.Fatalf("session_id = %q, want canonical session", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != state.clientMetadata.TurnMetadata {
		t.Fatal("websocket canonical header does not match normalized body metadata")
	}
}

func TestApplyCodexWebsocketHeadersOffModeProjectsCanonicalSessionWithoutMutatingBody(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}
	auth := &cliproxyauth.Auth{ID: "auth-ws-off", Provider: "codex"}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-ws-session\",\"thread_id\":\"off-ws-session\"}","thread_id":"legacy-conflict"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawBody}
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	headers.Set("User-Agent", "Codex Desktop (Mac OS)")

	upstreamBody, state, err := prepareCodexOutboundMetadata(context.Background(), cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	if !bytes.Equal(upstreamBody, rawBody) {
		t.Fatalf("off mode mutated websocket body: got %s want %s", upstreamBody, rawBody)
	}
	headers = applyCodexWebsocketHeaders(context.Background(), headers, auth, "oauth-token", cfg)
	if fallback := codexSessionHeaderValue(headers); fallback == "" || fallback == "off-ws-session" {
		t.Fatalf("expected pre-projection random fallback, got %q", fallback)
	}
	applyCodexOutboundMetadataHeaders(headers, &state)
	if got := codexSessionHeaderValue(headers); got != "off-ws-session" {
		t.Fatalf("session_id = %q, want off-ws-session", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("off mode rebuilt X-Codex-Turn-Metadata = %q", got)
	}
}

func TestApplyCodexWebsocketHeadersOffModeBodyCanonicalSuppressesConflictingDirectHeader(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}
	auth := &cliproxyauth.Auth{ID: "auth-ws-off-conflict", Provider: "codex"}
	rawBody := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-ws-body-session\"}"}}`)
	direct := `{"request_kind":"turn","session_id":"off-ws-header-session"}`
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawBody}
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, rawBody)
	ctx := contextWithGinHeaders(map[string]string{"X-Codex-Turn-Metadata": direct})

	upstreamBody, state, err := prepareCodexOutboundMetadata(ctx, cfg, auth, req.Payload, body, nil)
	if err != nil {
		t.Fatalf("prepareCodexOutboundMetadata() error = %v", err)
	}
	if !bytes.Equal(upstreamBody, rawBody) {
		t.Fatalf("off mode mutated websocket body: got %s want %s", upstreamBody, rawBody)
	}
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexOutboundMetadataHeaders(headers, &state)
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("conflicting direct canonical header survived body precedence: %q", got)
	}
	if got := codexSessionHeaderValue(headers); got != "off-ws-body-session" {
		t.Fatalf("session_id = %q, want off-ws-body-session", got)
	}
}

func TestCodexWebsocketHandshakeStatusClassifiesFallbackAndPreservesHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Retry-After", "9")
	headers.Set("X-Request-ID", "req-handshake")
	err := newCodexWebsocketHandshakeStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"usage_limit_reached","message":"quota","resets_in_seconds":7}}`), headers)
	classified, ok := err.(interface{ ModelFallbackReason() string })
	if !ok || classified.ModelFallbackReason() != config.CodexModelFallbackTriggerUsageLimit {
		t.Fatalf("fallback reason = %#v, want usage-limit", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
		t.Fatalf("RetryAfter = %#v, want resets_in_seconds", err)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("X-Request-ID") != "req-handshake" {
		t.Fatalf("headers = %#v, want preserved upgrade headers", err)
	}
	transient := newCodexWebsocketHandshakeStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_error","message":"transient"}}`), nil)
	if got := transient.(interface{ ModelFallbackReason() string }).ModelFallbackReason(); got != "" {
		t.Fatalf("bare/transient 429 fallback reason = %q, want empty", got)
	}
}
