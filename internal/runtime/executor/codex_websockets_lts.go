package executor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type codexWebsocketConnectionKey struct {
	useSIWC               bool
	authID                string
	wsURL                 string
	proxyURL              string
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

// The caller holds activeMu. CancelCauseFunc treats a nil cause as context.Canceled.
func (a *codexWebsocketActive) cancel(cause error) {
	if a.signal != nil {
		a.signal.cancel(cause)
	}
	*a = codexWebsocketActive{}
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
	s.activeMu.Lock()
	if s.terminalConn == connection.conn && s.terminalErr != nil {
		err := s.terminalErr
		s.activeMu.Unlock()
		s.connMu.Unlock()
		return nil, err
	}
	if connection.conn == nil || s.conn != connection.conn || s.connGen != connection.generation {
		s.activeMu.Unlock()
		s.connMu.Unlock()
		return nil, fmt.Errorf("codex websockets executor: connection generation is no longer current")
	}
	signal := newCodexWebsocketRequestSignal()
	if s.active.signal != nil {
		s.active.signal.cancel(context.Canceled)
	}
	s.active = codexWebsocketActive{connection: connection, ch: ch, signal: signal}
	s.activeResponseID = ""
	s.activeResponseTerminal = false
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
		s.active.cancel(context.Canceled)
	}
	s.activeMu.Unlock()
}

// A detached socket may finish cleanup after its replacement has started a turn.
func (s *codexWebsocketSession) cancelReadForConnection(connection codexWebsocketConnectionRef, cause error) {
	s.activeMu.Lock()
	if s.active.connection == connection {
		s.active.cancel(cause)
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) cancelActiveConnection(cause error) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	s.active.cancel(cause)
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) activeConnection() (codexWebsocketActive, bool) {
	if s == nil {
		return codexWebsocketActive{}, false
	}
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.active.connection.conn == nil || s.active.ch == nil || s.active.signal == nil {
		return codexWebsocketActive{}, false
	}
	return s.active, true
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
		s.active.cancel(event.err)
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
		s.connCfg = nil
		s.hasConnCfg = false
		s.multiAgentV2OptimizedGen = 0
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
func newCodexWebsocketHandshakeStatusErr(status int, body []byte, headers http.Header, modelLevelCooling ...bool) error {
	modelScoped := len(modelLevelCooling) > 0 && modelLevelCooling[0]
	classified := newCodexStatusErrWithHeadersAndCooling(status, headers, body, modelScoped)
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
		// Upstream records the stage-scoped dial error alongside the upgrade
		// rejection (recordWebsocketDialFailure / recordWebsocketDialRetryFailure),
		// so a rejected reconnect still leaves an "Error: dial_retry:" line.
		helps.RecordAPIWebsocketError(ctx, cfg, stage, dialErr)
		return resp.StatusCode, newCodexWebsocketHandshakeStatusErr(resp.StatusCode, body, resp.Header, cfg != nil && cfg.Codex.ModelLevelCooling)
	}
	if dialErr == nil {
		dialErr = fmt.Errorf("codex websockets executor: websocket %s failed without a connection or handshake response", stage)
	}
	if stage == "dial_retry" && cfg != nil && !cfg.CommercialMode && !cfg.RequestLog {
		// Upstream keeps a transport-level reconnect failure auditable by
		// recording the pending upgrade request first, which opens the paired
		// response attempt ("=== API RESPONSE 2 ===") before the error lands.
		helps.RecordAPIRequest(ctx, cfg, websocketUpgradeRequestLog(requestLog))
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

func newXAIWebsocketConnectionKey(authID string, wsURL string, proxyURL string) codexWebsocketConnectionKey {
	key := newCodexWebsocketConnectionKey(authID, wsURL, "", [sha256.Size]byte{})
	key.proxyURL = strings.TrimSpace(proxyURL)
	return key
}

func (s *codexWebsocketSession) effectiveConnKeyLocked() codexWebsocketConnectionKey {
	if s == nil {
		return codexWebsocketConnectionKey{}
	}
	if s.connKey != (codexWebsocketConnectionKey{}) {
		return s.connKey
	}
	if s.authID == "" && s.wsURL == "" && s.proxyURL == "" && !s.useSIWC {
		return codexWebsocketConnectionKey{}
	}
	return codexWebsocketConnectionKey{
		useSIWC:  s.useSIWC,
		authID:   strings.TrimSpace(s.authID),
		wsURL:    strings.TrimSpace(s.wsURL),
		proxyURL: strings.TrimSpace(s.proxyURL),
	}
}

func websocketSessionTargetChanged(sess *codexWebsocketSession, authID string, wsURL string, proxyURL string, useSIWC ...bool) bool {
	if sess == nil {
		return false
	}
	sess.connMu.Lock()
	defer sess.connMu.Unlock()
	current := sess.effectiveConnKeyLocked()
	if current.authID == "" && current.wsURL == "" {
		return false
	}
	wanted := newCodexWebsocketConnectionKey(authID, wsURL, current.baseModel, current.overrideHeaderProfile)
	wanted.proxyURL = strings.TrimSpace(proxyURL)
	wanted.useSIWC = current.useSIWC
	if len(useSIWC) > 0 {
		wanted.useSIWC = useSIWC[0]
	}
	return current != wanted
}

func existingCodexWebsocketSessionConnection(sess *codexWebsocketSession, wantedKey codexWebsocketConnectionKey) (codexWebsocketConnectionRef, *websocketConnectionCloser) {
	if sess == nil {
		return codexWebsocketConnectionRef{}, nil
	}
	sess.connMu.Lock()
	connection := codexWebsocketConnectionRef{conn: sess.conn, generation: sess.connGen}
	closer := sess.connCloser
	matches := connection.conn != nil && closer != nil && sess.effectiveConnKeyLocked() == wantedKey
	sess.connMu.Unlock()
	if !matches || sess.upstreamDisconnectError(connection.conn) != nil {
		return codexWebsocketConnectionRef{}, nil
	}
	return connection, closer
}

func optimizeCodexWebsocketMultiAgentV2Doc(ctx context.Context, doc *ojson.Doc, headers http.Header, cfg *config.Config, auth *cliproxyauth.Auth, isCompat bool) ([]byte, bool, bool, error) {
	multiAgentV2Conflict := helps.HasCodexMultiAgentV2NamespaceConflictDoc(doc)
	body, optimizeMultiAgentV2, err := optimizeCodexMultiAgentV2Doc(ctx, doc, headers, cfg, auth, isCompat)
	if err != nil {
		return nil, false, false, err
	}
	return body, optimizeMultiAgentV2, multiAgentV2Conflict, nil
}

func prepareCodexWebsocketMultiAgentV2Doc(ctx context.Context, doc *ojson.Doc, headers http.Header, cfg *config.Config, auth *cliproxyauth.Auth, isCompat bool) ([]byte, multiagentv2.PlaintextToolProvenance, bool, bool, error) {
	multiAgentV2Conflict := helps.HasCodexMultiAgentV2NamespaceConflictDoc(doc)
	body, plaintextTools := multiagentv2.PreparePlaintextTools(ctx, headers, doc.Bytes(), cfg)
	optimizedBody, optimizeMultiAgentV2, err := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, headers, body, cfg, auth, isCompat)
	if err != nil {
		return nil, multiagentv2.PlaintextToolProvenance{}, false, false, err
	}
	*doc = *ojson.NewDoc(optimizedBody)
	return doc.Bytes(), plaintextTools, optimizeMultiAgentV2, multiAgentV2Conflict, nil
}

func prepareCodexWebsocketPromptCacheDoc(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, doc *ojson.Doc, headers http.Header, affinityActive bool, nativeAPI bool) (codexCacheBase, http.Header, error) {
	if affinityActive {
		base, body, errPromptCache := oauthCacheBase(ctx, from, req, doc.Bytes(), codexAffinityHeaders(ctx, headers))
		if errPromptCache != nil {
			return codexCacheBase{}, nil, errPromptCache
		}
		*doc = *ojson.NewDoc(body)
		return base, make(http.Header), nil
	}
	if nativeAPI {
		return codexCacheBase{}, make(http.Header), nil
	}
	wsHeaders, _, errPromptCache := applyCodexPromptCacheHeadersWithContextDoc(ctx, from, req, doc, headers)
	if errPromptCache != nil {
		return codexCacheBase{}, nil, errPromptCache
	}
	return codexCacheBase{}, wsHeaders, nil
}

func (e *CodexWebsocketsExecutor) prepareCodexWebsocketUpstreamIdentity(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	from sdktranslator.Format,
	httpURL string,
	originalPayloadSource []byte,
	body []byte,
	incomingHeaders http.Header,
	wsHeaders http.Header,
	base codexCacheBase,
	affinityActive bool,
	nativeAPI bool,
) ([]byte, http.Header, codexIdentityConfuseState, *codexAffinityGuard, error) {
	var identityState codexIdentityConfuseState
	upstreamBody := body
	if nativeAPI {
		observeNativeCacheIntent(ctx, body)
	} else {
		var err error
		upstreamBody, identityState, err = prepareCodexOutboundMetadata(ctx, e.cfg, auth, originalPayloadSource, body, incomingHeaders)
		if err != nil {
			return nil, nil, codexIdentityConfuseState{}, nil, err
		}
	}
	if affinityActive {
		upstreamBody = helps.SanitizeCodexInputItemIDs(upstreamBody)
		upstreamBody, wsHeaders = e.adaptOAuthCache(ctx, auth, req, from, httpURL, originalPayloadSource, upstreamBody, incomingHeaders, base, &identityState)
	}
	return upstreamBody, wsHeaders, identityState, newCodexAffinityGuard(&identityState), nil
}

func finalizeCodexWebsocketHeaders(
	ctx context.Context,
	wsHeaders http.Header,
	auth *cliproxyauth.Auth,
	apiKey string,
	cfg *config.Config,
	nativeRequest bool,
	upstreamModel string,
	upstreamBody []byte,
	clientHeaders http.Header,
	modelHeaderProfile codexModelHeaderProfile,
	identityState *codexIdentityConfuseState,
) http.Header {
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, cfg, nativeRequest, clientHeaders)
	applyCodexRoutingHint(ctx, wsHeaders, auth, upstreamModel, upstreamBody, clientHeaders)
	applyFinalCodexClientHeaders(wsHeaders, modelHeaderProfile, auth)
	applyCodexOutboundMetadataHeaders(wsHeaders, identityState)
	identityState.verifyAffinityHeader(wsHeaders)
	return wsHeaders
}

func closePreparedCodexWebsocketAffinities(preparedRequests []*codexWebsocketPrepared) {
	for _, prepared := range preparedRequests {
		if prepared != nil {
			prepared.identityState.affinity.close()
		}
	}
}
