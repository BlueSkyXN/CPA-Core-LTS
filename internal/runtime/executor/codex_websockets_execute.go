package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
	reporter.EnableSemanticTiming(to.String())
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, false)

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = sanitizeCodexUnsupportedReasoningSummary(body, baseModel)
	body = helps.SetBoolIfDifferent(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex websockets executor", body)
	body = normalizeCodexWebsocketParallelToolCalls(body, opts.Headers)
	multiAgentV2Conflict := helps.HasCodexMultiAgentV2NamespaceConflict(body)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, baseModel)
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
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg, opts.Headers)
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
	sessionLocked := false
	unlockSession := func() {
		if sess != nil && sessionLocked {
			sess.reqMu.Unlock()
			sessionLocked = false
		}
	}
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		sess.reqMu.Lock()
		sessionLocked = true
		defer unlockSession()
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
	var connection codexWebsocketConnectionRef
	var closer *websocketConnectionCloser
	var respHS *http.Response
	var errDial error
	dialCtx := ctx
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		connection, closer = existingCodexWebsocketSessionConnection(sess, connectionKey)
		if connection.conn == nil {
			return resp, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
		}
	} else {
		dialCtx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
		connection, closer, respHS, errDial = e.ensureUpstreamConn(dialCtx, auth, sess, connectionKey, wsHeaders)
	}
	if errDial != nil {
		status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHS, errDial, "dial")
		if status == http.StatusUpgradeRequired {
			if opts.ExecutionLifecycle != nil || cliproxyexecutor.DownstreamWebsocket(ctx) {
				if cliproxyexecutor.UpstreamAttempted(dialCtx) {
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
				}
				return resp, handshakeErr
			}
			return e.CodexExecutor.Execute(ctx, auth, req, opts)
		}
		if cliproxyexecutor.UpstreamAttempted(dialCtx) {
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
		}
		return resp, handshakeErr
	}
	if errBind := sess.bindExecutionLifecycle(opts, connection.conn, closer, req.Model); errBind != nil {
		unlockSession()
		closeWebsocketAfterBindFailure(sess, connection.conn, closer)
		return resp, errBind
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTiming()
	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
		defer func() {
			reason := "completed"
			if err != nil {
				reason = "error"
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, reason, err)
			if errClose := closer.Close(); errClose != nil {
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
	restoreMultiAgentV2 := !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(connection))

	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if errSend := writeCodexWebsocketMessage(sess, connection.conn, wsReqBody); errSend != nil {
		errSend = mapCodexWebsocketWriteError(sess, connection.conn, errSend)
		if sess != nil {
			if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
				e.invalidateUpstreamConnWithoutDisconnectNotify(sess, connection, "send_error", errSend)
				if !shouldRetryCodexWebsocketSend(errSend) {
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
					return resp, errSend
				}
				return resp, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
			}
			e.invalidateUpstreamConn(sess, connection, "send_error", errSend)
			if !shouldRetryCodexWebsocketSend(errSend) {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
				return resp, errSend
			}

			// Retry once with a fresh websocket connection. This is mainly to handle
			// upstream closing the socket between sequential requests within the same
			// execution session.
			connectionRetry, closerRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
			if errDialRetry == nil && connectionRetry.conn != nil {
				if errBind := sess.bindExecutionLifecycle(opts, connectionRetry.conn, closerRetry, req.Model); errBind != nil {
					sess.clearActiveConnection(connection, readCh)
					unlockSession()
					closeWebsocketAfterBindFailure(sess, connectionRetry.conn, closerRetry)
					return resp, errBind
				}
				closer = closerRetry
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
				reporter.BeginResponseTiming()
				retrySignal, errActive := sess.setActiveConnection(connectionRetry, readCh)
				if errActive != nil {
					return resp, errActive
				}
				connection = connectionRetry
				requestSignal = retrySignal
				restoreMultiAgentV2 = !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(connection))
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if errSendRetry := writeCodexWebsocketMessage(sess, connection.conn, wsReqBodyRetry); errSendRetry == nil {
					wsReqBody = wsReqBodyRetry
				} else {
					errSendRetry = mapCodexWebsocketWriteError(sess, connection.conn, errSendRetry)
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

	if optimizeMultiAgentV2 || multiAgentV2Conflict {
		sess.setMultiAgentV2Optimized(connection, optimizeMultiAgentV2 && !multiAgentV2Conflict)
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
		reporter.ObserveTimingMessage(to.String(), payload)
		observeCodexTokenEvent(reporter, payload)
		payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
		helps.AppendCodexAPIWebsocketResponse(ctx, e.cfg, payload)
		helps.EmitWebSocketResponseEvent(ctx, opts, auth, e.Identifier(), req.Model, payload)
		payload = helps.RestoreCodexMultiAgentV2Response(payload, restoreMultiAgentV2)

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
			if sess != nil {
				unlockSession()
				e.invalidateUpstreamConn(sess, connection, "terminal_failure", streamErr)
			}
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
		case "response.completed", "response.done", "response.incomplete":
			payload = patchCodexCompletedOutput(payload, outputItemsByIndex, outputItemsFallback)
			if eventType != "response.incomplete" {
				cacheCodexReasoningReplayFromCompleted(replayScope, payload)
			}
			if detail, ok := helps.ParseCodexUsage(payload); ok {
				reporter.Publish(ctx, detail)
			} else {
				reporter.EnsurePublished(ctx)
			}
			var param any
			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, clientBody, clientPayload, &param)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				out = helps.EnsureResponsesUsageDetails(out)
			}
			resp = cliproxyexecutor.Response{Payload: out}
			return resp, nil
		}
	}
}
