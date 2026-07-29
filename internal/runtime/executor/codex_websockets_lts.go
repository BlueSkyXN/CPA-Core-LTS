package executor

import (
	"context"
	"crypto/sha256"

	"fmt"

	"net/http"

	"strings"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type codexWebsocketConnectionKey struct {
	authID                string
	wsURL                 string
	baseModel             string
	overrideHeaderProfile [sha256.Size]byte
}

type codexWebsocketConnectionRef struct {
	conn       *websocket.Conn
	generation uint64
}

type codexWebsocketRequestSignal struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func newCodexWebsocketRequestSignal() *codexWebsocketRequestSignal {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &codexWebsocketRequestSignal{ctx: ctx, cancel: cancel}
}

type codexWebsocketActive struct {
	connection codexWebsocketConnectionRef
	ch         chan codexWebsocketRead
	signal     *codexWebsocketRequestSignal
}

func (s *codexWebsocketSession) setActiveConnection(connection codexWebsocketConnectionRef, ch chan codexWebsocketRead) (*codexWebsocketRequestSignal, error) {
	if s == nil {
		return nil, fmt.Errorf("codex websockets executor: session is nil")
	}
	s.connMu.Lock()
	if s.closed {
		s.connMu.Unlock()
		return nil, fmt.Errorf("codex websockets executor: session is closed")
	}
	if connection.conn == nil || s.conn != connection.conn || s.connGen != connection.generation {
		s.connMu.Unlock()
		return nil, fmt.Errorf("codex websockets executor: connection generation is no longer current")
	}
	signal := newCodexWebsocketRequestSignal()
	s.activeMu.Lock()
	if s.active.signal != nil {
		s.active.signal.cancel(context.Canceled)
	}
	s.active = codexWebsocketActive{connection: connection, ch: ch, signal: signal}
	s.activeMu.Unlock()
	s.connMu.Unlock()
	return signal, nil
}

func (s *codexWebsocketSession) clearActiveConnection(connection codexWebsocketConnectionRef, ch chan codexWebsocketRead) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	if s.active.connection == connection && s.active.ch == ch {
		if s.active.signal != nil {
			s.active.signal.cancel(context.Canceled)
		}
		s.active = codexWebsocketActive{}
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) cancelActiveConnection(cause error) {
	if s == nil {
		return
	}
	if cause == nil {
		cause = context.Canceled
	}
	s.activeMu.Lock()
	if s.active.signal != nil {
		s.active.signal.cancel(cause)
	}
	s.active = codexWebsocketActive{}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) activeFor(connection codexWebsocketConnectionRef) (chan codexWebsocketRead, *codexWebsocketRequestSignal, bool) {
	if s == nil {
		return nil, nil, false
	}
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.active.connection != connection || s.active.ch == nil || s.active.signal == nil {
		return nil, nil, false
	}
	return s.active.ch, s.active.signal, true
}

func (s *codexWebsocketSession) dispatchRead(connection codexWebsocketConnectionRef, event codexWebsocketRead, terminal bool) bool {
	event.connection = connection
	if terminal {
		s.activeMu.Lock()
		defer s.activeMu.Unlock()
		if s.active.connection != connection || s.active.ch == nil || s.active.signal == nil {
			return false
		}
		select {
		case s.active.ch <- event:
		default:
		}
		cause := event.err
		if cause == nil {
			cause = context.Canceled
		}
		s.active.signal.cancel(cause)
		s.active = codexWebsocketActive{}
		return true
	}

	ch, signal, ok := s.activeFor(connection)
	if !ok {
		return false
	}
	select {
	case ch <- event:
	case <-signal.ctx.Done():
		return false
	}
	return true
}

func (s *codexWebsocketSession) isCurrentConnection(connection codexWebsocketConnectionRef) bool {
	if s == nil || connection.conn == nil {
		return false
	}
	s.connMu.Lock()
	current := s.conn == connection.conn && s.connGen == connection.generation
	s.connMu.Unlock()
	return current
}

func (s *codexWebsocketSession) detachConnectionRef(connection codexWebsocketConnectionRef, lifecycle cliproxyexecutor.ExecutionLifecycle) *websocketConnectionCloser {
	if s == nil || connection.conn == nil {
		return nil
	}
	s.connMu.Lock()
	var closer *websocketConnectionCloser
	matched := s.conn == connection.conn && s.connGen == connection.generation
	if matched {
		closer = s.connCloser
		s.conn = nil
		s.connCloser = nil
		s.connKey = codexWebsocketConnectionKey{}
		s.connGen++
		if s.connGen == 0 {
			s.connGen++
		}
		if s.readerConn == connection.conn && s.readerGen == connection.generation {
			s.readerConn = nil
			s.readerGen = 0
		}
	}
	if ((lifecycle == nil && matched) || (lifecycle != nil && s.lifecycle == lifecycle)) && s.lifecycleGen == connection.generation {
		s.lifecycle = nil
		s.lifecycleModel = ""
		s.lifecycleGen = 0
	}
	s.connMu.Unlock()
	return closer
}

// Websocket upgrade failures are still upstream Codex status responses. Keep
// both the typed classification (usage_limit/capacity can therefore enter the
// pre-payload fallback path) and retry headers for downstream error shaping.
func newCodexWebsocketHandshakeStatusErr(status int, body []byte, headers http.Header) error {
	classified := newCodexStatusErr(status, body)
	return statusErrWithHeaders{statusErr: classified, headers: headers.Clone()}
}

// codexWebsocketHandshakeFailure keeps initial and reconnect upgrade failures
// on the same classification path. In particular, a typed Codex quota or
// capacity response before any payload remains eligible for model fallback,
// while a 426 can still downgrade to the HTTP executor at the caller.
func codexWebsocketHandshakeFailure(ctx context.Context, cfg *config.Config, requestLog helps.UpstreamRequestLog, resp *http.Response, dialErr error, stage string) (int, error) {
	body := websocketHandshakeBody(resp)
	if resp != nil && resp.StatusCode > 0 {
		helps.RecordAPIWebsocketUpgradeRejection(ctx, cfg, websocketUpgradeRequestLog(requestLog), resp.StatusCode, resp.Header.Clone(), body)
		return resp.StatusCode, newCodexWebsocketHandshakeStatusErr(resp.StatusCode, body, resp.Header)
	}
	if dialErr == nil {
		dialErr = fmt.Errorf("codex websockets executor: websocket %s failed without a connection or handshake response", stage)
	}
	helps.RecordAPIWebsocketError(ctx, cfg, stage, dialErr)
	return 0, dialErr
}

func newCodexWebsocketConnectionKey(authID string, wsURL string, baseModel string, overrideHeaderProfile [sha256.Size]byte) codexWebsocketConnectionKey {
	return codexWebsocketConnectionKey{
		authID:                strings.TrimSpace(authID),
		wsURL:                 strings.TrimSpace(wsURL),
		baseModel:             strings.TrimSpace(baseModel),
		overrideHeaderProfile: overrideHeaderProfile,
	}
}

func existingCodexWebsocketSessionConnection(sess *codexWebsocketSession, wantedKey codexWebsocketConnectionKey) (codexWebsocketConnectionRef, *websocketConnectionCloser) {
	if sess == nil {
		return codexWebsocketConnectionRef{}, nil
	}
	sess.connMu.Lock()
	connection := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
	closer := sess.connCloser
	matches := connection.conn != nil && closer != nil && sess.connKey == wantedKey
	sess.connMu.Unlock()
	if !matches || sess.upstreamDisconnectError(connection.conn) != nil {
		return codexWebsocketConnectionRef{}, nil
	}
	return connection, closer
}
