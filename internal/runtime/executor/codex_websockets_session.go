package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type codexWebsocketSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*codexWebsocketSession
}

var globalCodexWebsocketSessionStore = &codexWebsocketSessionStore{
	sessions: make(map[string]*codexWebsocketSession),
}

type websocketConnectionCloser struct {
	conn *websocket.Conn
	once sync.Once
	err  error
}

func newWebsocketConnectionCloser(conn *websocket.Conn) *websocketConnectionCloser {
	if conn == nil {
		return nil
	}
	return &websocketConnectionCloser{conn: conn}
}

func (c *websocketConnectionCloser) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.once.Do(func() {
		c.err = c.conn.Close()
	})
	return c.err
}

type codexWebsocketSession struct {
	sessionID string

	reqMu sync.Mutex

	connMu          sync.Mutex
	conn            *websocket.Conn
	connCloser      *websocketConnectionCloser
	connGen         uint64
	connKey         codexWebsocketConnectionKey
	wsURL           string
	authID          string
	closed          bool
	lifecycleBindMu sync.Mutex
	lifecycle       cliproxyexecutor.ExecutionLifecycle
	lifecycleModel  string
	lifecycleGen    uint64

	writeMu sync.Mutex

	activeMu     sync.Mutex
	active       codexWebsocketActive
	activeConn   *websocket.Conn
	activeCh     chan codexWebsocketRead
	activeDone   <-chan struct{}
	activeCancel context.CancelFunc

	readerConn *websocket.Conn
	readerGen  uint64

	upstreamDisconnectOnce    sync.Once
	upstreamDisconnectCh      chan error
	upstreamDisconnectErrMu   sync.RWMutex
	upstreamDisconnectErrConn *websocket.Conn
	upstreamDisconnectErr     error
}

type codexWebsocketRead struct {
	connection codexWebsocketConnectionRef
	conn       *websocket.Conn
	msgType    int
	payload    []byte
	err        error
}

func (s *codexWebsocketSession) setActive(conn *websocket.Conn, ch chan codexWebsocketRead) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	if s.activeCancel != nil {
		s.activeCancel()
		s.activeCancel = nil
		s.activeDone = nil
	}
	s.activeConn = conn
	s.activeCh = ch
	if conn != nil && ch != nil {
		activeCtx, activeCancel := context.WithCancel(context.Background())
		s.activeDone = activeCtx.Done()
		s.activeCancel = activeCancel
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) activate(conn *websocket.Conn) chan codexWebsocketRead {
	if s == nil || conn == nil {
		return nil
	}
	ch := make(chan codexWebsocketRead, 4096)
	s.setActive(conn, ch)
	return ch
}

func (s *codexWebsocketSession) activeForConn(conn *websocket.Conn) (chan codexWebsocketRead, <-chan struct{}) {
	if s == nil || conn == nil {
		return nil, nil
	}
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeConn != conn {
		return nil, nil
	}
	return s.activeCh, s.activeDone
}

func clearRetryActiveState(sess *codexWebsocketSession, conn *websocket.Conn, ch chan codexWebsocketRead) bool {
	if sess == nil {
		return false
	}
	return sess.clearActive(conn, ch)
}

func (s *codexWebsocketSession) clearActive(conn *websocket.Conn, ch chan codexWebsocketRead) bool {
	if s == nil {
		return false
	}
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeConn != conn || s.activeCh != ch {
		return false
	}
	s.activeConn = nil
	s.activeCh = nil
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.activeCancel = nil
	s.activeDone = nil
	return true
}

func (s *codexWebsocketSession) writeMessage(conn *websocket.Conn, msgType int, payload []byte) error {
	if s == nil {
		return fmt.Errorf("codex websockets executor: session is nil")
	}
	if conn == nil {
		return fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(msgType, payload)
}

// sendTerminalWebsocketRead reports whether it invalidated a full channel's connection before waiting.
func sendTerminalWebsocketRead(ch chan<- codexWebsocketRead, done <-chan struct{}, event codexWebsocketRead, invalidate func()) bool {
	select {
	case ch <- event:
		return false
	case <-done:
		return false
	default:
	}

	invalidated := invalidate != nil
	if invalidated {
		invalidate()
	}
	select {
	case ch <- event:
	case <-done:
	}
	return invalidated
}

func (s *codexWebsocketSession) configureConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.resetUpstreamDisconnectError(conn)
	conn.SetPingHandler(func(appData string) error {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		// Reply pongs from the same write lock to avoid concurrent writes.
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
	defaultCloseHandler := conn.CloseHandler()
	conn.SetCloseHandler(func(code int, text string) error {
		s.setUpstreamDisconnectError(conn, &websocket.CloseError{Code: code, Text: text})
		return defaultCloseHandler(code, text)
	})
}

func (s *codexWebsocketSession) bindExecutionLifecycle(opts cliproxyexecutor.Options, conn *websocket.Conn, closer *websocketConnectionCloser, model string) error {
	if closer == nil && conn != nil {
		closer = newWebsocketConnectionCloser(conn)
	}
	if closer == nil {
		return fmt.Errorf("codex websockets executor: websocket connection closer is nil")
	}
	if s == nil {
		return cliproxyexecutor.BindExecutionResource(opts, closer)
	}
	lifecycle := opts.ExecutionLifecycle
	if lifecycle == nil || conn == nil {
		return nil
	}

	s.lifecycleBindMu.Lock()
	defer s.lifecycleBindMu.Unlock()

	s.connMu.Lock()
	if s.conn == conn && s.connCloser == nil {
		s.connCloser = closer
	}
	connection := codexWebsocketConnectionRef{conn: s.conn, generation: s.connGen}
	alreadyBound := connection.conn == conn && s.connCloser == closer && s.lifecycle == lifecycle && s.lifecycleGen == connection.generation
	ownedByAnotherLifecycle := connection.conn == conn && s.lifecycle != nil && s.lifecycle != lifecycle
	s.connMu.Unlock()
	if alreadyBound {
		return nil
	}
	if ownedByAnotherLifecycle {
		return fmt.Errorf("codex websockets executor: websocket connection is already owned by another lifecycle")
	}

	if errBind := lifecycle.Bind(func() error {
		return s.closeBoundConnection(connection, closer, lifecycle)
	}); errBind != nil {
		return errBind
	}
	if retained, ok := lifecycle.(interface{ Retain() }); ok {
		retained.Retain()
	}

	s.connMu.Lock()
	if s.conn != connection.conn || s.connGen != connection.generation || s.connCloser != closer {
		s.connMu.Unlock()
		return fmt.Errorf("codex websockets executor: websocket connection closed during lifecycle bind")
	}
	previous := s.lifecycle
	s.lifecycle = lifecycle
	s.lifecycleModel = strings.TrimSpace(model)
	s.lifecycleGen = connection.generation
	s.connMu.Unlock()
	if previous != nil && previous != lifecycle {
		previous.End("target_replaced")
	}
	return nil
}

func (s *codexWebsocketSession) closeBoundConnection(connection codexWebsocketConnectionRef, closer *websocketConnectionCloser, lifecycle cliproxyexecutor.ExecutionLifecycle) error {
	if s == nil || connection.conn == nil {
		return nil
	}
	s.detachConnectionRef(connection, lifecycle)
	errClose := closer.Close()
	go lifecycle.End("connection_closed")
	return errClose
}

func (s *codexWebsocketSession) detachConnection(conn *websocket.Conn, lifecycle cliproxyexecutor.ExecutionLifecycle) *websocketConnectionCloser {
	if s == nil || conn == nil {
		return nil
	}
	s.connMu.Lock()
	connection := codexWebsocketConnectionRef{conn: conn, generation: s.connGen}
	s.connMu.Unlock()
	return s.detachConnectionRef(connection, lifecycle)
}

func closeWebsocketAfterBindFailure(sess *codexWebsocketSession, conn *websocket.Conn, closer *websocketConnectionCloser) {
	if conn == nil || closer == nil {
		return
	}
	if sess != nil {
		sess.detachConnection(conn, nil)
	}
	if errClose := closer.Close(); errClose != nil {
		log.Errorf("websockets executor: close lifecycle bind failure connection error: %v", errClose)
	}
}

func websocketSessionTargetChanged(sess *codexWebsocketSession, authID string, wsURL string) bool {
	if sess == nil {
		return false
	}

	sess.connMu.Lock()
	defer sess.connMu.Unlock()
	if strings.TrimSpace(sess.authID) == "" && strings.TrimSpace(sess.wsURL) == "" {
		return false
	}
	return strings.TrimSpace(sess.authID) != strings.TrimSpace(authID) || strings.TrimSpace(sess.wsURL) != strings.TrimSpace(wsURL)
}

func existingWebsocketSessionConn(sess *codexWebsocketSession, authID string, wsURL string) (*websocket.Conn, *websocketConnectionCloser) {
	if sess == nil {
		return nil, nil
	}
	sess.connMu.Lock()
	conn := sess.conn
	closer := sess.connCloser
	matches := conn != nil && closer != nil &&
		strings.TrimSpace(sess.authID) == strings.TrimSpace(authID) &&
		strings.TrimSpace(sess.wsURL) == strings.TrimSpace(wsURL)
	sess.connMu.Unlock()
	if !matches || sess.upstreamDisconnectError(conn) != nil {
		return nil, nil
	}
	return conn, closer
}

func detachMismatchedWebsocketSessionConn(sess *codexWebsocketSession, authID string, wsURL string) (*websocket.Conn, *websocketConnectionCloser, string, string, cliproxyexecutor.ExecutionLifecycle) {
	if sess == nil {
		return nil, nil, "", "", nil
	}

	sess.connMu.Lock()
	conn := sess.conn
	if conn == nil || (strings.TrimSpace(sess.authID) == strings.TrimSpace(authID) && strings.TrimSpace(sess.wsURL) == strings.TrimSpace(wsURL)) {
		sess.connMu.Unlock()
		return nil, nil, "", "", nil
	}

	previousAuthID := sess.authID
	previousWSURL := sess.wsURL
	lifecycle := sess.lifecycle
	closer := sess.connCloser
	sess.lifecycle = nil
	sess.lifecycleModel = ""
	sess.lifecycleGen = 0
	sess.conn = nil
	sess.connCloser = nil
	sess.connKey = codexWebsocketConnectionKey{}
	sess.connGen++
	if sess.connGen == 0 {
		sess.connGen++
	}
	if sess.readerConn == conn {
		sess.readerConn = nil
		sess.readerGen = 0
	}
	sess.connMu.Unlock()

	if ch, _ := sess.activeForConn(conn); ch != nil {
		sess.clearActive(conn, ch)
	}
	return conn, closer, previousAuthID, previousWSURL, lifecycle
}

func (s *codexWebsocketSession) resetUpstreamDisconnectError(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.upstreamDisconnectErrMu.Lock()
	s.upstreamDisconnectErrConn = conn
	s.upstreamDisconnectErr = nil
	s.upstreamDisconnectErrMu.Unlock()
}

func (s *codexWebsocketSession) setUpstreamDisconnectError(conn *websocket.Conn, err error) {
	if s == nil || conn == nil || err == nil {
		return
	}
	s.upstreamDisconnectErrMu.Lock()
	if s.upstreamDisconnectErrConn == conn && s.upstreamDisconnectErr == nil {
		s.upstreamDisconnectErr = err
	}
	s.upstreamDisconnectErrMu.Unlock()
}

func (s *codexWebsocketSession) upstreamDisconnectError(conn *websocket.Conn) error {
	if s == nil || conn == nil {
		return nil
	}
	s.upstreamDisconnectErrMu.RLock()
	defer s.upstreamDisconnectErrMu.RUnlock()
	if s.upstreamDisconnectErrConn != conn {
		return nil
	}
	return s.upstreamDisconnectErr
}

func (s *codexWebsocketSession) notifyUpstreamDisconnect(err error) {
	if s == nil {
		return
	}
	s.upstreamDisconnectOnce.Do(func() {
		if s.upstreamDisconnectCh == nil {
			return
		}
		select {
		case s.upstreamDisconnectCh <- err:
		default:
		}
		close(s.upstreamDisconnectCh)
	})
}

func executionSessionIDFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func (e *CodexWebsocketsExecutor) getOrCreateSession(sessionID string) *codexWebsocketSession {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if e == nil {
		return nil
	}
	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions == nil {
		store.sessions = make(map[string]*codexWebsocketSession)
	}
	if sess, ok := store.sessions[sessionID]; ok && sess != nil {
		return sess
	}
	sess := &codexWebsocketSession{
		sessionID:            sessionID,
		upstreamDisconnectCh: make(chan error, 1),
	}
	store.sessions[sessionID] = sess
	return sess
}

func (e *CodexWebsocketsExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	sess := e.getOrCreateSession(sessionID)
	if sess == nil {
		return nil
	}
	return sess.upstreamDisconnectCh
}

func (e *CodexWebsocketsExecutor) ensureUpstreamConn(ctx context.Context, auth *cliproxyauth.Auth, sess *codexWebsocketSession, wantedKey codexWebsocketConnectionKey, headers http.Header) (codexWebsocketConnectionRef, *websocketConnectionCloser, *http.Response, error) {
	if sess == nil {
		conn, closer, resp, errDial := e.dialCodexWebsocket(ctx, auth, wantedKey.wsURL, headers)
		return codexWebsocketConnectionRef{conn: conn}, closer, resp, errDial
	}

	sess.connMu.Lock()
	if sess.closed {
		sess.connMu.Unlock()
		return codexWebsocketConnectionRef{}, nil, nil, fmt.Errorf("codex websockets executor: session is closed")
	}
	connection := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
	reader := codexWebsocketConnectionRef{conn: sess.readerConn, generation: sess.readerGen}
	if connection.conn != nil && sess.connKey == wantedKey {
		closer := sess.connCloser
		startReader := reader != connection
		if startReader {
			sess.readerConn = connection.conn
			sess.readerGen = connection.generation
		}
		sess.connMu.Unlock()
		if startReader {
			sess.configureConn(connection.conn)
			go e.readUpstreamLoop(sess, connection)
		}
		return connection, closer, nil, nil
	}
	oldCloser := sess.connCloser
	oldLifecycle := sess.lifecycle
	oldAuthID := sess.authID
	oldWSURL := sess.wsURL
	if connection.conn != nil {
		sess.conn = nil
		sess.connCloser = nil
		sess.lifecycle = nil
		sess.lifecycleModel = ""
		sess.lifecycleGen = 0
		if reader == connection {
			sess.readerConn = nil
			sess.readerGen = 0
		}
		sess.connGen++
		if sess.connGen == 0 {
			sess.connGen++
		}
	}
	sess.connKey = codexWebsocketConnectionKey{}
	sess.connMu.Unlock()

	if connection.conn != nil {
		sess.cancelActiveConnection(fmt.Errorf("codex websockets executor: target changed"))
		logCodexWebsocketDisconnected(sess.sessionID, oldAuthID, oldWSURL, "target_changed", nil)
		if oldCloser != nil {
			if errClose := oldCloser.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close stale websocket error: %v", errClose)
			}
		} else if errClose := connection.conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		if oldLifecycle != nil {
			oldLifecycle.End("target_changed")
		}
	}

	conn, closer, resp, errDial := e.dialCodexWebsocket(ctx, auth, wantedKey.wsURL, headers)
	if errDial != nil {
		return codexWebsocketConnectionRef{}, closer, resp, errDial
	}

	sess.connMu.Lock()
	if sess.closed {
		sess.connMu.Unlock()
		if errClose := closer.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		return codexWebsocketConnectionRef{}, nil, nil, fmt.Errorf("codex websockets executor: session closed while dialing")
	}
	if sess.conn != nil {
		previous := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
		previousCloser := sess.connCloser
		previousKey := sess.connKey
		sess.connMu.Unlock()
		if errClose := closer.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		if previousKey == wantedKey {
			return previous, previousCloser, nil, nil
		}
		return codexWebsocketConnectionRef{}, nil, nil, fmt.Errorf("codex websockets executor: session connection profile changed while dialing")
	}
	sess.connGen++
	if sess.connGen == 0 {
		sess.connGen++
	}
	connection = codexWebsocketConnectionRef{conn: conn, generation: sess.connGen}
	sess.conn = conn
	sess.connCloser = closer
	sess.connKey = wantedKey
	sess.wsURL = wantedKey.wsURL
	sess.authID = wantedKey.authID
	sess.readerConn = conn
	sess.readerGen = connection.generation
	sess.connMu.Unlock()

	sess.configureConn(conn)
	go e.readUpstreamLoop(sess, connection)
	logCodexWebsocketConnected(sess.sessionID, wantedKey.authID, wantedKey.wsURL)
	return connection, closer, resp, nil
}

func (e *CodexWebsocketsExecutor) readUpstreamLoop(sess *codexWebsocketSession, connection codexWebsocketConnectionRef) {
	if e == nil || sess == nil || connection.conn == nil {
		return
	}
	for {
		_ = connection.conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
		msgType, payload, errRead := connection.conn.ReadMessage()
		if !sess.isCurrentConnection(connection) {
			return
		}
		if errRead != nil {
			sess.dispatchRead(connection, codexWebsocketRead{err: errRead}, true)
			e.invalidateUpstreamConn(sess, connection, "upstream_disconnected", errRead)
			return
		}

		if msgType != websocket.TextMessage {
			if msgType == websocket.BinaryMessage {
				errBinary := fmt.Errorf("codex websockets executor: unexpected binary message")
				sess.dispatchRead(connection, codexWebsocketRead{err: errBinary}, true)
				e.invalidateUpstreamConn(sess, connection, "unexpected_binary", errBinary)
				return
			}
			continue
		}

		sess.dispatchRead(connection, codexWebsocketRead{msgType: msgType, payload: payload}, false)
	}
}

func (e *CodexWebsocketsExecutor) invalidateUpstreamConn(sess *codexWebsocketSession, connection codexWebsocketConnectionRef, reason string, err error) {
	e.invalidateUpstreamConnWithNotify(sess, connection, reason, err, true)
}

func (e *CodexWebsocketsExecutor) invalidateUpstreamConnWithoutDisconnectNotify(sess *codexWebsocketSession, connection codexWebsocketConnectionRef, reason string, err error) {
	e.invalidateUpstreamConnWithNotify(sess, connection, reason, err, false)
}

func (e *CodexWebsocketsExecutor) invalidateUpstreamConnWithNotify(sess *codexWebsocketSession, connection codexWebsocketConnectionRef, reason string, err error, notify bool) {
	if sess == nil || connection.conn == nil {
		return
	}

	sess.connMu.Lock()
	current := sess.conn
	authID := sess.authID
	wsURL := sess.wsURL
	sessionID := sess.sessionID
	if current == nil || current != connection.conn || sess.connGen != connection.generation {
		sess.connMu.Unlock()
		return
	}
	lifecycle := sess.lifecycle
	closer := sess.connCloser
	sess.lifecycle = nil
	sess.lifecycleModel = ""
	sess.lifecycleGen = 0
	sess.conn = nil
	sess.connCloser = nil
	sess.connKey = codexWebsocketConnectionKey{}
	if sess.readerConn == connection.conn && sess.readerGen == connection.generation {
		sess.readerConn = nil
		sess.readerGen = 0
	}
	sess.connMu.Unlock()

	logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, err)
	if notify {
		sess.notifyUpstreamDisconnect(err)
	}
	if closer != nil {
		if errClose := closer.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
	} else if errClose := connection.conn.Close(); errClose != nil {
		log.Errorf("codex websockets executor: close websocket error: %v", errClose)
	}
	if lifecycle != nil {
		lifecycle.End(reason)
	}
}

func (e *CodexWebsocketsExecutor) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil {
		return
	}
	if sessionID == "" {
		return
	}
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		e.closeAllExecutionSessions("executor_shutdown")
		return
	}

	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sess := store.sessions[sessionID]
	delete(store.sessions, sessionID)
	store.mu.Unlock()

	e.closeExecutionSession(sess, "session_closed")
}

func (e *CodexWebsocketsExecutor) closeAllExecutionSessions(reason string) {
	if e == nil {
		return
	}

	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sessions := make([]*codexWebsocketSession, 0, len(store.sessions))
	for sessionID, sess := range store.sessions {
		delete(store.sessions, sessionID)
		if sess != nil {
			sessions = append(sessions, sess)
		}
	}
	store.mu.Unlock()

	for i := range sessions {
		e.closeExecutionSession(sessions[i], reason)
	}
}

func (e *CodexWebsocketsExecutor) closeExecutionSession(sess *codexWebsocketSession, reason string) {
	closeCodexWebsocketSession(sess, reason)
}

func closeCodexWebsocketSession(sess *codexWebsocketSession, reason string) {
	if sess == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "session_closed"
	}

	sess.connMu.Lock()
	connection := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
	authID := sess.authID
	wsURL := sess.wsURL
	lifecycle := sess.lifecycle
	closer := sess.connCloser
	sess.lifecycle = nil
	sess.lifecycleModel = ""
	sess.lifecycleGen = 0
	sess.connCloser = nil
	sessionID := sess.sessionID
	sess.closed = true
	sess.conn = nil
	sess.connKey = codexWebsocketConnectionKey{}
	sess.connGen++
	if sess.connGen == 0 {
		sess.connGen++
	}
	if sess.readerConn == connection.conn && sess.readerGen == connection.generation {
		sess.readerConn = nil
		sess.readerGen = 0
	}
	sess.connMu.Unlock()

	closeErr := fmt.Errorf("codex websockets executor: session closed: %s", reason)
	if connection.conn != nil {
		if delivered := sess.dispatchRead(connection, codexWebsocketRead{
			err: closeErr,
		}, true); !delivered {
			sess.cancelActiveConnection(closeErr)
		}

		logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, nil)
		if closer != nil {
			if errClose := closer.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		} else if errClose := connection.conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
	} else {
		sess.cancelActiveConnection(closeErr)
	}

	if lifecycle != nil {
		lifecycle.End(reason)
	}
}

func logCodexWebsocketConnected(sessionID string, authID string, wsURL string) {
	log.Infof("codex websockets: upstream connected session=%s auth=%s url=%s", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL))
}

func logCodexWebsocketDisconnected(sessionID string, authID string, wsURL string, reason string, err error) {
	if err != nil {
		log.Infof("codex websockets: upstream disconnected session=%s auth=%s url=%s reason=%s err=%v", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL), strings.TrimSpace(reason), err)
		return
	}
	log.Infof("codex websockets: upstream disconnected session=%s auth=%s url=%s reason=%s", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL), strings.TrimSpace(reason))
}

// CloseCodexWebsocketSessionsForAuthID closes all active Codex upstream websocket sessions
// associated with the supplied auth ID.
func CloseCodexWebsocketSessionsForAuthID(authID string, reason string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "auth_removed"
	}

	store := globalCodexWebsocketSessionStore
	if store == nil {
		return
	}

	type sessionItem struct {
		sessionID string
		sess      *codexWebsocketSession
	}

	store.mu.Lock()
	items := make([]sessionItem, 0, len(store.sessions))
	for sessionID, sess := range store.sessions {
		items = append(items, sessionItem{sessionID: sessionID, sess: sess})
	}
	store.mu.Unlock()

	matches := make([]sessionItem, 0)
	for i := range items {
		sess := items[i].sess
		if sess == nil {
			continue
		}
		sess.connMu.Lock()
		sessAuthID := strings.TrimSpace(sess.authID)
		sess.connMu.Unlock()
		if sessAuthID == authID {
			matches = append(matches, items[i])
		}
	}
	if len(matches) == 0 {
		return
	}

	toClose := make([]*codexWebsocketSession, 0, len(matches))
	store.mu.Lock()
	for i := range matches {
		current, ok := store.sessions[matches[i].sessionID]
		if !ok || current == nil || current != matches[i].sess {
			continue
		}
		delete(store.sessions, matches[i].sessionID)
		toClose = append(toClose, current)
	}
	store.mu.Unlock()

	for i := range toClose {
		closeCodexWebsocketSession(toClose[i], reason)
	}
}
