// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements a Codex executor that uses the Responses API WebSocket transport.
package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/proxy"
)

const (
	codexResponsesWebsocketBetaHeaderValue = "responses_websockets=2026-02-06"
	codexResponsesWebsocketIdleTimeout     = 5 * time.Minute
	codexResponsesWebsocketHandshakeTO     = 30 * time.Second
)

// CodexWebsocketsExecutor executes Codex Responses requests using a WebSocket transport.
//
// It preserves the existing CodexExecutor HTTP implementation as a fallback for endpoints
// not available over WebSocket (e.g. /responses/compact) and for websocket upgrade failures.
type CodexWebsocketsExecutor struct {
	*CodexExecutor

	store *codexWebsocketSessionStore
}

type codexWebsocketSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*codexWebsocketSession
}

var globalCodexWebsocketSessionStore = &codexWebsocketSessionStore{
	sessions: make(map[string]*codexWebsocketSession),
}

type codexWebsocketSession struct {
	sessionID string

	reqMu sync.Mutex

	connMu  sync.Mutex
	conn    *websocket.Conn
	connGen uint64
	connKey codexWebsocketConnectionKey
	wsURL   string
	authID  string
	closed  bool

	writeMu sync.Mutex

	activeMu     sync.Mutex
	active       codexWebsocketActive
	activeConn   *websocket.Conn
	activeCh     chan codexWebsocketRead
	activeDone   <-chan struct{}
	activeCancel context.CancelFunc

	readerConn *websocket.Conn
	readerGen  uint64

	upstreamDisconnectOnce sync.Once
	upstreamDisconnectCh   chan error
}

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

func NewCodexWebsocketsExecutor(cfg *config.Config) *CodexWebsocketsExecutor {
	return &CodexWebsocketsExecutor{
		CodexExecutor: NewCodexExecutor(cfg),
		store:         globalCodexWebsocketSessionStore,
	}
}

type codexWebsocketRead struct {
	connection codexWebsocketConnectionRef
	conn       *websocket.Conn
	msgType    int
	payload    []byte
	err        error
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

// setActive and clearActive retain the legacy active-channel contract used by
// the independent xAI websocket session store. Codex sessions use the
// generation-aware variants above.
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

func (s *codexWebsocketSession) isCurrentConnection(connection codexWebsocketConnectionRef) bool {
	if s == nil || connection.conn == nil {
		return false
	}
	s.connMu.Lock()
	current := s.conn == connection.conn && s.connGen == connection.generation
	s.connMu.Unlock()
	return current
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

func (s *codexWebsocketSession) configureConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	conn.SetPingHandler(func(appData string) error {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		// Reply pongs from the same write lock to avoid concurrent writes.
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
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

func detachMismatchedWebsocketSessionConn(sess *codexWebsocketSession, authID string, wsURL string) (*websocket.Conn, string, string) {
	if sess == nil {
		return nil, "", ""
	}

	sess.connMu.Lock()
	conn := sess.conn
	if conn == nil || (strings.TrimSpace(sess.authID) == strings.TrimSpace(authID) && strings.TrimSpace(sess.wsURL) == strings.TrimSpace(wsURL)) {
		sess.connMu.Unlock()
		return nil, "", ""
	}

	previousAuthID := sess.authID
	previousWSURL := sess.wsURL
	sess.conn = nil
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
	return conn, previousAuthID, previousWSURL
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

func (e *CodexWebsocketsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Alt == "responses/compact" {
		return e.CodexExecutor.executeCompact(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)
	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	var replayScope codexReasoningReplayScope
	defer func() {
		if !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()
	defer func() { err = withCodexReasoningReplayScope(err, replayScope) }()

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = sanitizeCodexUnsupportedReasoningSummary(body, baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex websockets executor", body)
	body = normalizeCodexWebsocketParallelToolCalls(body, opts.Headers)
	var skipReplay bool
	body, replayScope, skipReplay, err = prepareCodexModelFallbackBody(ctx, from, req, opts, body)
	if err != nil {
		return resp, err
	}
	if !skipReplay {
		var errReplay error
		body, replayScope, errReplay = applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
		if errReplay != nil {
			return resp, errReplay
		}
	}

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return resp, err
	}

	body, wsHeaders, errPromptCache := applyCodexPromptCacheHeadersWithContext(ctx, from, req, body, opts.Headers)
	if errPromptCache != nil {
		return resp, errPromptCache
	}
	replayScope = codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return resp, err
	}
	clientBody := body
	var identityState codexIdentityConfuseState
	upstreamBody, identityState, err := prepareCodexOutboundMetadata(ctx, e.cfg, auth, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())
	reporter.SetOutboundServiceTier(upstreamBody)
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg)
	applyFinalCodexClientHeaders(wsHeaders, modelHeaderProfile, auth)
	applyCodexOutboundMetadataHeaders(wsHeaders, &identityState)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		sess.reqMu.Lock()
		defer sess.reqMu.Unlock()
	}

	wsReqBody := buildCodexWebsocketRequestBody(upstreamBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	connectionKey := newCodexWebsocketConnectionKey(authID, wsURL, baseModel, modelHeaderProfile.digest)
	connection, respHS, errDial := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
	if errDial != nil {
		status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHS, errDial, "dial")
		if status == http.StatusUpgradeRequired {
			return e.CodexExecutor.Execute(ctx, auth, req, opts)
		}
		return resp, handshakeErr
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTTFT()
	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
		defer func() {
			reason := "completed"
			if err != nil {
				reason = "error"
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, reason, err)
			if errClose := connection.conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}()
	}

	var readCh chan codexWebsocketRead
	var requestSignal *codexWebsocketRequestSignal
	if sess != nil {
		readCh = make(chan codexWebsocketRead, 4096)
		var errActive error
		requestSignal, errActive = sess.setActiveConnection(connection, readCh)
		if errActive != nil {
			return resp, errActive
		}
		defer func() { sess.clearActiveConnection(connection, readCh) }()
	}

	if errSend := writeCodexWebsocketMessage(sess, connection.conn, wsReqBody); errSend != nil {
		if sess != nil {
			e.invalidateUpstreamConn(sess, connection, "send_error", errSend)

			// Retry once with a fresh websocket connection. This is mainly to handle
			// upstream closing the socket between sequential requests within the same
			// execution session.
			connectionRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
			if errDialRetry == nil && connectionRetry.conn != nil {
				wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      wsReqBodyRetry,
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				reporter.StartResponseTTFT()
				retrySignal, errActive := sess.setActiveConnection(connectionRetry, readCh)
				if errActive != nil {
					return resp, errActive
				}
				connection = connectionRetry
				requestSignal = retrySignal
				if errSendRetry := writeCodexWebsocketMessage(sess, connection.conn, wsReqBodyRetry); errSendRetry == nil {
					wsReqBody = wsReqBodyRetry
				} else {
					e.invalidateUpstreamConn(sess, connection, "send_error", errSendRetry)
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
					return resp, errSendRetry
				}
			} else {
				status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHSRetry, errDialRetry, "dial_retry")
				if status == http.StatusUpgradeRequired {
					return e.CodexExecutor.Execute(ctx, auth, req, opts)
				}
				return resp, handshakeErr
			}
		} else {
			helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
			return resp, errSend
		}
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for {
		if ctx != nil && ctx.Err() != nil {
			return resp, ctx.Err()
		}
		msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, connection, readCh, requestSignal)
		if errRead != nil {
			mappedErr := mapCodexWebsocketReadError(errRead)
			helps.RecordAPIWebsocketError(ctx, e.cfg, "read", mappedErr)
			return resp, mappedErr
		}
		if msgType != websocket.TextMessage {
			if msgType == websocket.BinaryMessage {
				err = fmt.Errorf("codex websockets executor: unexpected binary message")
				if sess != nil {
					e.invalidateUpstreamConn(sess, connection, "unexpected_binary", err)
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", err)
				return resp, err
			}
			continue
		}

		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 {
			continue
		}
		reporter.MarkFirstResponseByte()
		payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
		helps.AppendAPIWebsocketResponse(ctx, e.cfg, payload)

		if wsErr, ok := parseCodexWebsocketError(payload); ok {
			if sess != nil {
				e.invalidateUpstreamConn(sess, connection, "upstream_error", wsErr)
			}
			if errClearReplay := clearCodexReasoningReplayOnWebsocketError(ctx, replayScope, payload); errClearReplay != nil {
				return resp, errClearReplay
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
			return resp, wsErr
		}
		if streamErr, terminalBody, ok := codexTerminalFailureErr(payload); ok {
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
				return resp, errClearReplay
			}
			return resp, streamErr
		}

		payload = normalizeCodexWebsocketCompletion(payload)
		eventType := gjson.GetBytes(payload, "type").String()
		switch eventType {
		case "response.output_item.done":
			collectCodexOutputItemDone(payload, outputItemsByIndex, &outputItemsFallback)
		case "response.completed":
			payload = patchCodexCompletedOutput(payload, outputItemsByIndex, outputItemsFallback)
			cacheCodexReasoningReplayFromCompleted(replayScope, payload)
			if detail, ok := helps.ParseCodexUsage(payload); ok {
				reporter.Publish(ctx, detail)
			}
			var param any
			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, clientBody, clientPayload, &param)
			resp = cliproxyexecutor.Response{Payload: out}
			return resp, nil
		}
	}
}

func (e *CodexWebsocketsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("Executing Codex Websockets stream request with auth ID: %s, model: %s", auth.ID, req.Model)
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)
	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	var replayScope codexReasoningReplayScope
	defer func() {
		if !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()
	defer func() { err = withCodexReasoningReplayScope(err, replayScope) }()

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = sanitizeCodexUnsupportedReasoningSummary(body, baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex websockets executor", body)
	body = normalizeCodexWebsocketParallelToolCalls(body, opts.Headers)
	var skipReplay bool
	body, replayScope, skipReplay, err = prepareCodexModelFallbackBody(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	if !skipReplay {
		var errReplay error
		body, replayScope, errReplay = applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
		if errReplay != nil {
			return nil, errReplay
		}
	}

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return nil, err
	}

	body, wsHeaders, errPromptCache := applyCodexPromptCacheHeadersWithContext(ctx, from, req, body, opts.Headers)
	if errPromptCache != nil {
		return nil, errPromptCache
	}
	replayScope = codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return nil, err
	}
	clientBody := body
	var identityState codexIdentityConfuseState
	upstreamBody, identityState, err := prepareCodexOutboundMetadata(ctx, e.cfg, auth, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())
	reporter.SetOutboundServiceTier(upstreamBody)
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg)
	applyFinalCodexClientHeaders(wsHeaders, modelHeaderProfile, auth)
	applyCodexOutboundMetadataHeaders(wsHeaders, &identityState)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		if sess != nil {
			sess.reqMu.Lock()
		}
	}

	wsReqBody := buildCodexWebsocketRequestBody(upstreamBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	connectionKey := newCodexWebsocketConnectionKey(authID, wsURL, baseModel, modelHeaderProfile.digest)
	connection, respHS, errDial := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
	var upstreamHeaders http.Header
	if respHS != nil {
		upstreamHeaders = respHS.Header.Clone()
	}
	if errDial != nil {
		status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHS, errDial, "dial")
		if sess != nil {
			sess.reqMu.Unlock()
		}
		if status == http.StatusUpgradeRequired {
			return e.CodexExecutor.ExecuteStream(ctx, auth, req, opts)
		}
		return nil, handshakeErr
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTTFT()

	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
	}

	var readCh chan codexWebsocketRead
	var requestSignal *codexWebsocketRequestSignal
	if sess != nil {
		readCh = make(chan codexWebsocketRead, 4096)
		var errActive error
		requestSignal, errActive = sess.setActiveConnection(connection, readCh)
		if errActive != nil {
			sess.reqMu.Unlock()
			return nil, errActive
		}
	}

	if errSend := writeCodexWebsocketMessage(sess, connection.conn, wsReqBody); errSend != nil {
		helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
		if sess != nil {
			e.invalidateUpstreamConn(sess, connection, "send_error", errSend)

			// Retry once with a new websocket connection for the same execution session.
			connectionRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
			if errDialRetry != nil || connectionRetry.conn == nil {
				status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHSRetry, errDialRetry, "dial_retry")
				sess.clearActiveConnection(connection, readCh)
				sess.reqMu.Unlock()
				if status == http.StatusUpgradeRequired {
					return e.CodexExecutor.ExecuteStream(ctx, auth, req, opts)
				}
				return nil, handshakeErr
			}
			wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
			helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
				URL:       wsURL,
				Method:    "WEBSOCKET",
				Headers:   wsHeaders.Clone(),
				Body:      wsReqBodyRetry,
				Provider:  e.Identifier(),
				AuthID:    authID,
				AuthLabel: authLabel,
				AuthType:  authType,
				AuthValue: authValue,
			})
			recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
			reporter.StartResponseTTFT()
			retrySignal, errActive := sess.setActiveConnection(connectionRetry, readCh)
			if errActive != nil {
				sess.clearActiveConnection(connection, readCh)
				sess.reqMu.Unlock()
				return nil, errActive
			}
			connection = connectionRetry
			requestSignal = retrySignal
			if errSendRetry := writeCodexWebsocketMessage(sess, connection.conn, wsReqBodyRetry); errSendRetry != nil {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
				e.invalidateUpstreamConn(sess, connection, "send_error", errSendRetry)
				sess.clearActiveConnection(connection, readCh)
				sess.reqMu.Unlock()
				return nil, errSendRetry
			}
			wsReqBody = wsReqBodyRetry
		} else {
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "send_error", errSend)
			if errClose := connection.conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
			return nil, errSend
		}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		terminateReason := "completed"
		var terminateErr error

		defer close(out)
		defer func() {
			if sess != nil {
				sess.clearActiveConnection(connection, readCh)
				sess.reqMu.Unlock()
				return
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, terminateReason, terminateErr)
			if errClose := connection.conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}()

		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil {
				chunk.Err = withCodexReasoningReplayScope(chunk.Err, replayScope)
			}
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for {
			if ctx != nil && ctx.Err() != nil {
				terminateReason = "context_done"
				terminateErr = ctx.Err()
				_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
				return
			}
			msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, connection, readCh, requestSignal)
			if errRead != nil {
				if sess != nil && ctx != nil && ctx.Err() != nil {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
					return
				}
				mappedErr := mapCodexWebsocketReadError(errRead)
				terminateReason = "read_error"
				terminateErr = mappedErr
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read", mappedErr)
				reporter.PublishFailure(ctx, mappedErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: mappedErr})
				return
			}
			if msgType != websocket.TextMessage {
				if msgType == websocket.BinaryMessage {
					err = fmt.Errorf("codex websockets executor: unexpected binary message")
					terminateReason = "unexpected_binary"
					terminateErr = err
					helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", err)
					reporter.PublishFailure(ctx, err)
					if sess != nil {
						e.invalidateUpstreamConn(sess, connection, "unexpected_binary", err)
					}
					_ = send(cliproxyexecutor.StreamChunk{Err: err})
					return
				}
				continue
			}

			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				continue
			}
			reporter.MarkFirstResponseByte()
			payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
			helps.AppendAPIWebsocketResponse(ctx, e.cfg, payload)

			if wsErr, ok := parseCodexWebsocketError(payload); ok {
				terminateReason = "upstream_error"
				terminateErr = wsErr
				if sess != nil {
					e.invalidateUpstreamConn(sess, connection, "upstream_error", wsErr)
				}
				if errClearReplay := clearCodexReasoningReplayOnWebsocketError(ctx, replayScope, payload); errClearReplay != nil {
					terminateErr = errClearReplay
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					_ = send(cliproxyexecutor.StreamChunk{Err: errClearReplay})
					return
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx, wsErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: wsErr})
				return
			}
			if streamErr, terminalBody, ok := codexTerminalFailureErr(payload); ok {
				terminateReason = "upstream_error"
				terminateErr = streamErr
				if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
					terminateErr = errClearReplay
					helps.RecordAPIWebsocketError(ctx, e.cfg, "replay_clear_error", errClearReplay)
					reporter.PublishFailure(ctx, errClearReplay)
					_ = send(cliproxyexecutor.StreamChunk{Err: errClearReplay})
					return
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", streamErr)
				reporter.PublishFailure(ctx, streamErr)
				_ = send(cliproxyexecutor.StreamChunk{Err: streamErr})
				return
			}

			eventType := gjson.GetBytes(payload, "type").String()
			isTerminalEvent := eventType == "response.completed" || eventType == "response.done" || eventType == "error"
			if eventType == "response.output_item.done" {
				collectCodexOutputItemDone(payload, outputItemsByIndex, &outputItemsFallback)
			}
			completedPayload := payload
			if eventType == "response.completed" || eventType == "response.done" {
				completedPayload = normalizeCodexWebsocketCompletion(completedPayload)
				completedPayload = patchCodexCompletedOutput(completedPayload, outputItemsByIndex, outputItemsFallback)
				cacheCodexReasoningReplayFromCompleted(replayScope, completedPayload)
				if detail, ok := helps.ParseCodexUsage(completedPayload); ok {
					reporter.Publish(ctx, detail)
				}
			}

			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			if cliproxyexecutor.DownstreamWebsocket(ctx) {
				if !send(cliproxyexecutor.StreamChunk{Payload: clientPayload}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
				if isTerminalEvent {
					return
				}
				continue
			}

			payload = normalizeCodexWebsocketCompletion(payload)
			if eventType == "response.completed" || eventType == "response.done" {
				payload = completedPayload
			}
			eventType = gjson.GetBytes(payload, "type").String()
			clientPayload = applyCodexIdentityExposeResponsePayload(payload, identityState)
			line := encodeCodexWebsocketAsSSE(clientPayload)
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, originalPayload, clientBody, line, &param)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
			}
			if eventType == "response.completed" || eventType == "response.done" {
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
}

func (e *CodexWebsocketsExecutor) dialCodexWebsocket(ctx context.Context, auth *cliproxyauth.Auth, wsURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := newProxyAwareWebsocketDialer(e.cfg, auth)
	dialer.HandshakeTimeout = codexResponsesWebsocketHandshakeTO
	dialer.EnableCompression = true
	if ctx == nil {
		ctx = context.Background()
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if conn != nil {
		// Avoid gorilla/websocket flate tail validation issues on some upstreams/Go versions.
		// Negotiating permessage-deflate is fine; we just don't compress outbound messages.
		conn.EnableWriteCompression(false)
	}
	return conn, resp, err
}

func writeCodexWebsocketMessage(sess *codexWebsocketSession, conn *websocket.Conn, payload []byte) error {
	if sess != nil {
		return sess.writeMessage(conn, websocket.TextMessage, payload)
	}
	if conn == nil {
		return fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func mapCodexWebsocketReadError(err error) error {
	if err == nil {
		return nil
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		return statusErr{code: http.StatusRequestEntityTooLarge, msg: `{"error":{"message":"upstream websocket message too big","type":"invalid_request_error","code":"message_too_big"}}`}
	}
	return err
}

func normalizeCodexWebsocketParallelToolCalls(body []byte, headers http.Header) []byte {
	if !isCodexResponsesLiteRequest(body, headers) {
		return body
	}
	body, _ = sjson.SetBytes(body, "parallel_tool_calls", false)
	return body
}

func buildCodexWebsocketRequestBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}

	// Match codex-rs websocket v2 semantics: every request is `response.create`.
	// Incremental follow-up turns continue on the same websocket using
	// `previous_response_id` + incremental `input`, not `response.append`.
	body = helps.SanitizeCodexInputItemIDs(body)
	wsReqBody, errSet := sjson.SetBytes(bytes.Clone(body), "type", "response.create")
	if errSet == nil && len(wsReqBody) > 0 {
		return wsReqBody
	}
	fallback := bytes.Clone(body)
	fallback, _ = sjson.SetBytes(fallback, "type", "response.create")
	return fallback
}

func readCodexWebsocketMessage(ctx context.Context, sess *codexWebsocketSession, connection codexWebsocketConnectionRef, readCh chan codexWebsocketRead, requestSignal *codexWebsocketRequestSignal) (int, []byte, error) {
	if sess == nil {
		if connection.conn == nil {
			return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
		}
		_ = connection.conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
		msgType, payload, errRead := connection.conn.ReadMessage()
		return msgType, payload, errRead
	}
	if connection.conn == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	if readCh == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: session read channel is nil")
	}
	if requestSignal == nil || requestSignal.ctx == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: session request signal is nil")
	}
	for {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-requestSignal.ctx.Done():
			cause := context.Cause(requestSignal.ctx)
			if cause == nil {
				cause = context.Canceled
			}
			return 0, nil, cause
		case ev, ok := <-readCh:
			if !ok {
				return 0, nil, fmt.Errorf("codex websockets executor: session read channel closed")
			}
			if ev.connection != connection {
				continue
			}
			if ev.err != nil {
				return 0, nil, ev.err
			}
			return ev.msgType, ev.payload, nil
		}
	}
}

func newProxyAwareWebsocketDialer(cfg *config.Config, auth *cliproxyauth.Auth) *websocket.Dialer {
	dialer := &websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  codexResponsesWebsocketHandshakeTO,
		EnableCompression: true,
		NetDialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL == "" {
		return dialer
	}

	setting, errParse := proxyutil.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("codex websockets executor: %v", errParse)
		return dialer
	}

	switch setting.Mode {
	case proxyutil.ModeDirect:
		dialer.Proxy = nil
		return dialer
	case proxyutil.ModeProxy:
	default:
		return dialer
	}

	switch setting.URL.Scheme {
	case "socks5", "socks5h":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("codex websockets executor: create SOCKS5 dialer failed: %v", errSOCKS5)
			return dialer
		}
		dialer.Proxy = nil
		dialer.NetDialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
	case "http", "https":
		dialer.Proxy = http.ProxyURL(setting.URL)
	default:
		log.Errorf("codex websockets executor: unsupported proxy scheme: %s", setting.URL.Scheme)
	}

	return dialer
}

func buildCodexResponsesWebsocketURL(httpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("codex websockets executor: unsupported responses websocket URL scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("codex websockets executor: responses websocket URL host is empty")
	}
	return parsed.String(), nil
}

func applyCodexPromptCacheHeaders(from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte) ([]byte, http.Header) {
	body, headers, _ := applyCodexPromptCacheHeadersWithContext(context.Background(), from, req, rawJSON)
	return body, headers
}

func applyCodexPromptCacheHeadersWithContext(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte, headerSets ...http.Header) ([]byte, http.Header, error) {
	headers := http.Header{}
	if len(rawJSON) == 0 {
		return rawJSON, headers, nil
	}

	var requestHeaders http.Header
	if len(headerSets) > 0 {
		requestHeaders = headerSets[0]
	}
	var cache helps.CodexCache
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
		if modelName == "" {
			modelName = thinking.ParseSuffix(req.Model).ModelName
		}
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, requestHeaders)
		if errCache != nil {
			return nil, nil, errCache
		}
		if ok {
			cache = cached
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAI) {
		if promptCacheKey := strings.TrimSpace(gjson.GetBytes(req.Payload, "prompt_cache_key").String()); promptCacheKey != "" {
			cache.ID = promptCacheKey
		} else if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
		setHeaderCasePreserved(headers, "session_id", cache.ID)
		headers.Set("Conversation_id", cache.ID)
	}

	return rawJSON, headers, nil
}

func applyCodexWebsocketHeaders(ctx context.Context, headers http.Header, auth *cliproxyauth.Auth, token string, cfg *config.Config) http.Header {
	if headers == nil {
		headers = http.Header{}
	}
	if strings.TrimSpace(token) != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	var ginHeaders http.Header
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header.Clone()
	}

	isAPIKey := codexAuthUsesAPIKey(auth)
	cfgUserAgent, cfgBetaFeatures := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithPriority(headers, ginHeaders, "x-codex-beta-features", cfgBetaFeatures, "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-state", "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-metadata", "")
	misc.EnsureHeader(headers, ginHeaders, "x-client-request-id", "")
	misc.EnsureHeader(headers, ginHeaders, "x-responsesapi-include-timing-metrics", "")
	misc.EnsureHeader(headers, ginHeaders, "Version", "")
	if isAPIKey {
		ensureHeaderWithPriority(headers, ginHeaders, "User-Agent", "", "")
	} else {
		ensureHeaderWithConfigPrecedence(headers, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)
	}

	betaHeader := strings.TrimSpace(headers.Get("OpenAI-Beta"))
	if betaHeader == "" && ginHeaders != nil {
		betaHeader = strings.TrimSpace(ginHeaders.Get("OpenAI-Beta"))
	}
	if betaHeader == "" || !strings.Contains(betaHeader, "responses_websockets=") {
		betaHeader = codexResponsesWebsocketBetaHeaderValue
	}
	headers.Set("OpenAI-Beta", betaHeader)
	sessionFallback := ""
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") {
		sessionFallback = uuid.NewString()
	}
	ensureCodexWebsocketSessionHeader(headers, ginHeaders, sessionFallback)
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		headers.Set("Originator", originator)
	} else if !isAPIKey {
		headers.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				if trimmed := strings.TrimSpace(accountID); trimmed != "" {
					setHeaderCasePreserved(headers, "ChatGPT-Account-ID", trimmed)
				}
			}
		}
	}

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, attrs)

	return headers
}

func ensureCodexWebsocketSessionHeader(target http.Header, source http.Header, fallbackValue string) {
	if target == nil {
		return
	}
	sessionID := codexSessionHeaderValue(target)
	if sessionID == "" {
		sessionID = codexSessionHeaderValue(source)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackValue)
	}
	if sessionID != "" {
		setHeaderCasePreserved(target, "session_id", sessionID)
	}
	deleteHeaderCaseInsensitive(target, "Session-Id")
}

func codexSessionHeaderValue(headers http.Header) string {
	for _, key := range []string{"Session-Id", "Session_id", "session_id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, key)); value != "" {
			return value
		}
	}
	return ""
}

func codexAuthUsesAPIKey(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

func ensureHeaderCasePreserved(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(headerValueCaseInsensitive(target, key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(headerValueCaseInsensitive(source, key)); val != "" {
			setHeaderCasePreserved(target, key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		setHeaderCasePreserved(target, key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		setHeaderCasePreserved(target, key, val)
	}
}

func setHeaderCasePreserved(headers http.Header, key string, value string) {
	if headers == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	deleteHeaderCaseInsensitive(headers, key)
	headers[key] = []string{value}
}

func setCodexSessionHeaderCasePreserved(headers http.Header, fallbackKey string, value string) {
	if headers == nil {
		return
	}
	fallbackKey = strings.TrimSpace(fallbackKey)
	value = strings.TrimSpace(value)
	if fallbackKey == "" || value == "" {
		return
	}

	selectedKey := ""
	if _, ok := headers[fallbackKey]; ok && codexSessionHeaderKeyUsesUnderscore(fallbackKey) {
		selectedKey = fallbackKey
	} else {
		for existingKey := range headers {
			if codexSessionHeaderKeyUsesUnderscore(existingKey) {
				selectedKey = existingKey
				break
			}
		}
	}
	if selectedKey == "" {
		selectedKey = fallbackKey
	}
	for existingKey := range headers {
		if codexSessionHeaderKey(existingKey) && existingKey != selectedKey {
			delete(headers, existingKey)
		}
	}
	headers[selectedKey] = []string{value}
}

func codexSessionHeaderKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return normalized == "session_id" || normalized == "session-id"
}

func codexSessionHeaderKeyUsesUnderscore(key string) bool {
	return strings.ToLower(strings.TrimSpace(key)) == "session_id"
}

func headerValueCaseInsensitive(headers http.Header, key string) string {
	key = strings.TrimSpace(key)
	if headers == nil || key == "" {
		return ""
	}
	if val := strings.TrimSpace(headers.Get(key)); val != "" {
		return val
	}
	for existingKey, values := range headers {
		if !strings.EqualFold(existingKey, key) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func deleteHeaderCaseInsensitive(headers http.Header, key string) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
		}
	}
}

func codexHeaderDefaults(cfg *config.Config, auth *cliproxyauth.Auth) (string, string) {
	if cfg == nil || auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return "", ""
		}
	}
	return strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent), strings.TrimSpace(cfg.CodexHeaderDefaults.BetaFeatures)
}

func ensureHeaderWithPriority(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}

func ensureHeaderWithConfigPrecedence(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}

type statusErrWithHeaders struct {
	statusErr
	headers http.Header
}

// Websocket upgrade failures are still upstream Codex status responses. Keep
// both the typed classification (usage_limit/capacity can therefore enter the
// pre-payload fallback path) and retry headers for downstream error shaping.
func newCodexWebsocketHandshakeStatusErr(status int, body []byte, headers http.Header) error {
	classified := newCodexStatusErr(status, body)
	return statusErrWithHeaders{statusErr: classified, headers: headers.Clone()}
}

func (e statusErrWithHeaders) Headers() http.Header {
	if e.headers == nil {
		return nil
	}
	return e.headers.Clone()
}

func parseCodexWebsocketError(payload []byte) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	status := int(gjson.GetBytes(payload, "status").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, "status_code").Int())
	}
	if status <= 0 {
		return nil, false
	}

	out := buildCodexWebsocketErrorPayload(payload, status)
	headers := parseCodexWebsocketErrorHeaders(payload)
	statusError := newCodexStatusErr(status, out)
	if retryAfter := parseCodexRetryAfter(status, out, time.Now()); retryAfter != nil {
		statusError.retryAfter = retryAfter
	} else if isCodexWebsocketConnectionLimitError(payload) {
		retryAfter := time.Duration(0)
		statusError.retryAfter = &retryAfter
	}
	return statusErrWithHeaders{
		statusErr: statusError,
		headers:   headers,
	}, true
}

func clearCodexReasoningReplayOnWebsocketError(ctx context.Context, scope codexReasoningReplayScope, payload []byte) error {
	status := int(gjson.GetBytes(payload, "status").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, "status_code").Int())
	}
	if status <= 0 {
		return nil
	}
	return clearCodexReasoningReplayOnInvalidSignature(ctx, scope, status, buildCodexWebsocketErrorPayload(payload, status))
}

func buildCodexWebsocketErrorPayload(payload []byte, status int) []byte {
	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, "status", status)

	if bodyNode := gjson.GetBytes(payload, "body"); bodyNode.Exists() {
		out, _ = sjson.SetRawBytes(out, "body", []byte(bodyNode.Raw))
		if bodyErrorNode := bodyNode.Get("error"); bodyErrorNode.Exists() {
			out, _ = sjson.SetRawBytes(out, "error", []byte(bodyErrorNode.Raw))
			return out
		}
	}

	if errNode := gjson.GetBytes(payload, "error"); errNode.Exists() {
		out, _ = sjson.SetRawBytes(out, "error", []byte(errNode.Raw))
		return out
	}

	out, _ = sjson.SetBytes(out, "error.type", "server_error")
	out, _ = sjson.SetBytes(out, "error.message", http.StatusText(status))
	return out
}

func isCodexWebsocketConnectionLimitError(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, path := range []string{"error.code", "error.type", "body.error.code", "body.error.type", "code", "error"} {
		if strings.TrimSpace(gjson.GetBytes(payload, path).String()) == "websocket_connection_limit_reached" {
			return true
		}
	}
	return false
}

func parseCodexWebsocketErrorHeaders(payload []byte) http.Header {
	headersNode := gjson.GetBytes(payload, "headers")
	if !headersNode.Exists() || !headersNode.IsObject() {
		return nil
	}
	mapped := make(http.Header)
	headersNode.ForEach(func(key, value gjson.Result) bool {
		name := strings.TrimSpace(key.String())
		if name == "" {
			return true
		}
		switch value.Type {
		case gjson.String:
			if v := strings.TrimSpace(value.String()); v != "" {
				mapped.Set(name, v)
			}
		case gjson.Number, gjson.True, gjson.False:
			if v := strings.TrimSpace(value.Raw); v != "" {
				mapped.Set(name, v)
			}
		default:
		}
		return true
	})
	if len(mapped) == 0 {
		return nil
	}
	return mapped
}

func normalizeCodexWebsocketCompletion(payload []byte) []byte {
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.done" {
		updated, err := sjson.SetBytes(payload, "type", "response.completed")
		if err == nil && len(updated) > 0 {
			return updated
		}
	}
	return payload
}

func encodeCodexWebsocketAsSSE(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	line := make([]byte, 0, len("data: ")+len(payload))
	line = append(line, []byte("data: ")...)
	line = append(line, payload...)
	return line
}

func websocketUpgradeRequestLog(info helps.UpstreamRequestLog) helps.UpstreamRequestLog {
	upgradeInfo := info
	upgradeInfo.URL = helps.WebsocketUpgradeRequestURL(info.URL)
	upgradeInfo.Method = http.MethodGet
	upgradeInfo.Body = nil
	upgradeInfo.Headers = info.Headers.Clone()
	if upgradeInfo.Headers == nil {
		upgradeInfo.Headers = make(http.Header)
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Connection")) == "" {
		upgradeInfo.Headers.Set("Connection", "Upgrade")
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Upgrade")) == "" {
		upgradeInfo.Headers.Set("Upgrade", "websocket")
	}
	return upgradeInfo
}

func recordAPIWebsocketHandshake(ctx context.Context, cfg *config.Config, resp *http.Response) {
	if resp == nil {
		return
	}
	helps.RecordAPIWebsocketHandshake(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
}

func websocketHandshakeBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
	if len(body) == 0 {
		return nil
	}
	return body
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

func closeHTTPResponseBody(resp *http.Response, logPrefix string) {
	if resp == nil || resp.Body == nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("%s: %v", logPrefix, errClose)
	}
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

func newCodexWebsocketConnectionKey(authID string, wsURL string, baseModel string, overrideHeaderProfile [sha256.Size]byte) codexWebsocketConnectionKey {
	return codexWebsocketConnectionKey{
		authID:                strings.TrimSpace(authID),
		wsURL:                 strings.TrimSpace(wsURL),
		baseModel:             strings.TrimSpace(baseModel),
		overrideHeaderProfile: overrideHeaderProfile,
	}
}

func (e *CodexWebsocketsExecutor) ensureUpstreamConn(ctx context.Context, auth *cliproxyauth.Auth, sess *codexWebsocketSession, wantedKey codexWebsocketConnectionKey, headers http.Header) (codexWebsocketConnectionRef, *http.Response, error) {
	if sess == nil {
		conn, resp, errDial := e.dialCodexWebsocket(ctx, auth, wantedKey.wsURL, headers)
		return codexWebsocketConnectionRef{conn: conn}, resp, errDial
	}

	sess.connMu.Lock()
	if sess.closed {
		sess.connMu.Unlock()
		return codexWebsocketConnectionRef{}, nil, fmt.Errorf("codex websockets executor: session is closed")
	}
	connection := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
	reader := codexWebsocketConnectionRef{conn: sess.readerConn, generation: sess.readerGen}
	if connection.conn != nil && sess.connKey == wantedKey {
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
		return connection, nil, nil
	}
	if connection.conn != nil {
		sess.conn = nil
		if reader == connection {
			sess.readerConn = nil
			sess.readerGen = 0
		}
	}
	sess.connKey = codexWebsocketConnectionKey{}
	sess.connMu.Unlock()

	if connection.conn != nil {
		if errClose := connection.conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
	}

	conn, resp, errDial := e.dialCodexWebsocket(ctx, auth, wantedKey.wsURL, headers)
	if errDial != nil {
		return codexWebsocketConnectionRef{}, resp, errDial
	}

	sess.connMu.Lock()
	if sess.closed {
		sess.connMu.Unlock()
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		return codexWebsocketConnectionRef{}, nil, fmt.Errorf("codex websockets executor: session closed while dialing")
	}
	if sess.conn != nil {
		previous := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
		previousKey := sess.connKey
		sess.connMu.Unlock()
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		if previousKey == wantedKey {
			return previous, nil, nil
		}
		return codexWebsocketConnectionRef{}, nil, fmt.Errorf("codex websockets executor: session connection profile changed while dialing")
	}
	sess.connGen++
	if sess.connGen == 0 {
		sess.connGen++
	}
	connection = codexWebsocketConnectionRef{conn: conn, generation: sess.connGen}
	sess.conn = conn
	sess.connKey = wantedKey
	sess.wsURL = wantedKey.wsURL
	sess.authID = wantedKey.authID
	sess.readerConn = conn
	sess.readerGen = connection.generation
	sess.connMu.Unlock()

	sess.configureConn(conn)
	go e.readUpstreamLoop(sess, connection)
	logCodexWebsocketConnected(sess.sessionID, wantedKey.authID, wantedKey.wsURL)
	return connection, resp, nil
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
	sess.conn = nil
	sess.connKey = codexWebsocketConnectionKey{}
	if sess.readerConn == connection.conn && sess.readerGen == connection.generation {
		sess.readerConn = nil
		sess.readerGen = 0
	}
	sess.connMu.Unlock()

	logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, err)
	sess.notifyUpstreamDisconnect(err)
	if errClose := connection.conn.Close(); errClose != nil {
		log.Errorf("codex websockets executor: close websocket error: %v", errClose)
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
		// Executor replacement can happen during hot reload (config/credential changes).
		// Do not force-close upstream websocket sessions here, otherwise in-flight
		// downstream websocket requests get interrupted.
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
	if connection.conn == nil {
		sess.cancelActiveConnection(closeErr)
		return
	}

	if delivered := sess.dispatchRead(connection, codexWebsocketRead{
		err: closeErr,
	}, true); !delivered {
		sess.cancelActiveConnection(closeErr)
	}

	logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, nil)
	if errClose := connection.conn.Close(); errClose != nil {
		log.Errorf("codex websockets executor: close websocket error: %v", errClose)
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

// CodexAutoExecutor routes Codex requests to the websocket transport only when:
//  1. The downstream transport is websocket, and
//  2. The selected auth enables websockets.
//
// For non-websocket downstream requests, it always uses the legacy HTTP implementation.
type CodexAutoExecutor struct {
	httpExec *CodexExecutor
	wsExec   *CodexWebsocketsExecutor
}

func NewCodexAutoExecutor(cfg *config.Config) *CodexAutoExecutor {
	return &CodexAutoExecutor{
		httpExec: NewCodexExecutor(cfg),
		wsExec:   NewCodexWebsocketsExecutor(cfg),
	}
}

func (e *CodexAutoExecutor) Identifier() string { return "codex" }

func (e *CodexAutoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.httpExec == nil {
		return nil
	}
	return e.httpExec.PrepareRequest(req, auth)
}

func (e *CodexAutoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.HttpRequest(ctx, auth, req)
}

func (e *CodexAutoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex auto executor: executor is nil")
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) && codexWebsocketsEnabled(auth) {
		return e.wsExec.Execute(ctx, auth, req, opts)
	}
	return e.httpExec.Execute(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return nil, fmt.Errorf("codex auto executor: executor is nil")
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) && codexWebsocketsEnabled(auth) {
		return e.wsExec.ExecuteStream(ctx, auth, req, opts)
	}
	return e.httpExec.ExecuteStream(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.Refresh(ctx, auth)
}

func (e *CodexAutoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.CountTokens(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) CloseExecutionSession(sessionID string) {
	if e == nil || e.wsExec == nil {
		return
	}
	e.wsExec.CloseExecutionSession(sessionID)
}

func (e *CodexAutoExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	if e == nil || e.wsExec == nil {
		return nil
	}
	return e.wsExec.UpstreamDisconnectChan(sessionID)
}

func codexWebsocketsEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}
