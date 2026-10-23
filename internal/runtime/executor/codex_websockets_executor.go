// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements a Codex executor that uses the Responses API WebSocket transport.
package executor

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// CodexWebsocketsExecutor executes Codex Responses requests using a WebSocket transport.
//
// It preserves the existing CodexExecutor HTTP implementation as a fallback for endpoints
// not available over WebSocket (e.g. /responses/compact) and for websocket upgrade failures.
type CodexWebsocketsExecutor struct {
	*CodexExecutor

	store *codexWebsocketSessionStore
}

func NewCodexWebsocketsExecutor(cfg *config.Config) *CodexWebsocketsExecutor {
	return &CodexWebsocketsExecutor{
		CodexExecutor: NewCodexExecutor(cfg),
		store:         globalCodexWebsocketSessionStore,
	}
}

// CodexAutoExecutor routes Codex requests to the websocket transport when the selected auth enables
// websockets and either:
//  1. The downstream transport is websocket, or
//  2. codex.outbound.transport selects websocket for an HTTP request (always, or in auto mode once
//     the payload reaches the configured threshold).
//
// Otherwise it uses the HTTP implementation. The default outbound transport, follow, keeps HTTP
// downstream requests on HTTP.
type CodexAutoExecutor struct {
	httpExec *CodexExecutor
	wsExec   *CodexWebsocketsExecutor
}

func NewCodexAutoExecutor(cfg *config.Config, previous ...*CodexAutoExecutor) *CodexAutoExecutor {
	httpExec := NewCodexExecutor(cfg)
	if len(previous) > 0 && previous[0] != nil && previous[0].httpExec != nil {
		old := previous[0].httpExec
		httpExec.affinity = old.affinityStore()
		httpExec.affinityGeneration = old.affinityGeneration
		if codexAffinityStrategy(old.cfg) != codexAffinityStrategy(cfg) {
			httpExec.affinityGeneration = old.affinity.resetGeneration()
		}
	}
	wsExec := NewCodexWebsocketsExecutor(cfg)
	wsExec.CodexExecutor = httpExec
	return &CodexAutoExecutor{httpExec: httpExec, wsExec: wsExec}
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
	if e.useUpstreamWebsocket(ctx, auth, req, opts) {
		return e.wsExec.Execute(ctx, auth, req, opts)
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return cliproxyexecutor.Response{}, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
	}
	return e.httpExec.Execute(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return nil, fmt.Errorf("codex auto executor: executor is nil")
	}
	if e.useUpstreamWebsocket(ctx, auth, req, opts) {
		return e.wsExec.ExecuteStream(ctx, auth, req, opts)
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return nil, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
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

// useUpstreamWebsocket reports whether a request should use the upstream websocket transport.
// Websocket sessions keep following the credential's websockets attribute. HTTP requests use it
// only when codex.outbound.transport selects websocket, always or in auto mode once the payload
// reaches the threshold. Compact requests stay on HTTP: the websocket executor delegates them to
// HTTP anyway.
func (e *CodexAutoExecutor) useUpstreamWebsocket(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	if !codexWebsocketsEnabled(auth) {
		return false
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) {
		return true
	}
	if opts.Alt == "responses/compact" || cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return false
	}
	var outbound config.CodexOutboundConfig
	if e != nil && e.httpExec != nil && e.httpExec.cfg != nil {
		outbound = e.httpExec.cfg.Codex.Outbound
	}
	mode := outbound.EffectiveTransport()
	size := len(req.Payload)
	if n := len(opts.OriginalRequest); n > size {
		size = n
	}
	switch mode {
	case config.CodexOutboundTransportWebsocket:
	case config.CodexOutboundTransportAuto:
		if size < outbound.EffectiveWebsocketThresholdBytes() {
			return false
		}
	default:
		return false
	}
	log.Debugf("codex outbound: sending HTTP request over upstream websocket (mode=%s, payload=%d bytes)", mode, size)
	return true
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

// SupportsApplyPatch requires both selectable transports to support the tool.
func (e *CodexAutoExecutor) SupportsApplyPatch() bool {
	return e != nil && e.httpExec != nil && e.wsExec != nil && e.httpExec.SupportsApplyPatch() && e.wsExec.SupportsApplyPatch()
}

// InterruptExecutionSession forwards a response.interrupt control frame on the
// current upstream socket. It does not dial, replay, or select another credential.
func (e *CodexAutoExecutor) InterruptExecutionSession(ctx context.Context, sessionID string, payload []byte) error {
	if e == nil || e.wsExec == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	return e.wsExec.InterruptExecutionSession(ctx, sessionID, payload)
}

// InterruptExecutionSession writes the original interrupt payload to the session
// socket captured for this execution. Payload rules and response.create defaults
// must not rewrite response_id, mode, or extension fields.
func (e *CodexWebsocketsExecutor) InterruptExecutionSession(ctx context.Context, sessionID string, payload []byte) error {
	if e == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sess := store.sessions[sessionID]
	store.mu.Unlock()
	if sess == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sess.connMu.Lock()
	conn := sess.conn
	connection := codexWebsocketConnectionRef{conn: conn, generation: sess.connGen}
	authID := sess.authID
	wsURL := sess.wsURL
	sess.connMu.Unlock()
	if conn == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	// A retained socket from an earlier turn is not the current upstream.
	// HTTP turns must fall through to local cancellation instead.
	if _, _, active := sess.activeFor(connection); !active {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	if !cliproxyexecutor.WebsocketAuthEnabled(ctx, authID) {
		return fmt.Errorf("websocket credential is no longer enabled")
	}
	if errWrite := writeCodexWebsocketMessage(sess, conn, payload); errWrite != nil {
		return errWrite
	}
	log.Infof("codex websockets: request forwarded session=%s auth=%s url=%s event=response.interrupt", sessionID, authID, wsURL)
	return nil
}
