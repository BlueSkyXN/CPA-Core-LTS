package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func (e *CodexWebsocketsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	if opts.Alt == "responses/compact" {
		return e.CodexExecutor.executeCompact(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)
	isCompat, errExpand := e.expandCodexResponsesCompaction(auth, &req, &opts, baseModel)
	if errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if e.shouldCompactCompatResponsesTrigger(isCompat, req, opts) {
		return e.CodexExecutor.executeCodexResponsesCompaction(ctx, auth, req, opts)
	}
	if errPrev := rejectSyntheticCompactionPreviousResponseID(ctx, req.Payload); errPrev != nil {
		return resp, errPrev
	}
	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return resp, errSIWC
	}
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	if baseURL == "" {
		if useSIWC {
			baseURL = defaultOpenAISIWCBaseURL
		} else {
			baseURL = "https://chatgpt.com/backend-api/codex"
		}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	ctx = reporter.DiagnosticsContext(ctx)
	var replayScope codexReasoningReplayScope
	defer func() {
		if !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()
	defer func() { err = withCodexReasoningReplayScope(err, replayScope) }()

	req, affinityActive := e.prepareCodexAffinityRequest(ctx, auth, req, opts)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	nativeRequest := helps.IsNativeCodexRequest(req.Payload, opts)
	nativeAPI := nativeOpenAIResponses(auth, baseURL)
	to := sdktranslator.FromString("codex")
	reporter.EnableSemanticTiming(to.String())
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, codexRequestTarget(from, nativeAPI), targetModel, originalPayload, req.Payload, false, isCompat)
	if err != nil {
		return resp, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	doc := ojson.NewDoc(body)
	if !nativeAPI {
		normalizeCodexOutputBudgetDoc(doc)
	}
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return resp, err
	}

	instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
	hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, "codex-websockets", baseModel, to.String(), "", originalTranslated, req, opts, auth))
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", true)
	if !nativeAPI {
		_ = doc.Delete("prompt_cache_retention")
		_ = doc.Delete("safety_identifier")
		ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
		if !useSIWC {
			normalizeCodexInstructionsDoc(doc, nativeRequest)
			if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
				checkModel := req.Model
				if strings.TrimSpace(checkModel) == "" {
					checkModel = baseModel
				}
				ensureImageGenerationToolDoc(doc, checkModel, auth, opts.Headers)
				if !codexAuthUsesAPIKey(auth) {
					normalizeCodexImageGenerationToolsDoc(doc)
				}
			}
		}
		normalizeCodexWebsocketParallelToolCallsDoc(doc, opts.Headers)
		helps.NormalizeCodexToolSchemasDoc(doc)
	}
	var optimizeMultiAgentV2, multiAgentV2Conflict bool
	body, optimizeMultiAgentV2, multiAgentV2Conflict, err = optimizeCodexWebsocketMultiAgentV2Doc(ctx, doc, opts.Headers, e.cfg, auth, isCompat)
	if err != nil {
		return resp, err
	}
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
		*doc = *ojson.NewDoc(body)
	}
	sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex websockets executor", doc, isCompat, targetModel, baseURL)

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return resp, err
	}

	base, wsHeaders, errPromptCache := prepareCodexWebsocketPromptCacheDoc(ctx, from, req, doc, opts.Headers, affinityActive, nativeAPI)
	if errPromptCache != nil {
		return resp, errPromptCache
	}
	helps.SanitizeCodexInputItemIDsDoc(doc)
	if useSIWC {
		applyCodexSIWCWebsocketBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	isEphemeralSession := false
	sessionLocked := false
	unlockSession := func() {
		if sess != nil && sessionLocked {
			sess.reqMu.Unlock()
			sessionLocked = false
		}
	}
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		if sess != nil {
			sess.reqMu.Lock()
			sessionLocked = true
			defer unlockSession()
		}
	} else {
		isEphemeralSession = true
		sess = newEphemeralCodexWebsocketSession()
	}

	helps.SanitizeCodexInputItemIDsDoc(doc)
	sessionInstructions, errSession := e.prepareSessionInstructions(ctx, doc, auth, req, opts, sess, wsURL, useSIWC)
	if errSession != nil {
		return resp, errSession
	}
	body = doc.Bytes()
	body, err = finalizeCodexPayloadForWire(ctx, body, baseModel)
	if err != nil {
		return resp, err
	}
	replayScope = codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	clientBody := body
	upstreamBody, wsHeaders, identityState, affinityGuard, err := e.prepareCodexWebsocketUpstreamIdentity(ctx, auth, req, from, httpURL, originalPayloadSource, body, opts.Headers, wsHeaders, base, affinityActive, nativeAPI)
	if err != nil {
		return resp, err
	}
	defer affinityGuard.Close()
	if errFinal := sessionInstructions.ValidateFinal(upstreamBody, from); errFinal != nil {
		return resp, errFinal
	}
	upstreamModel := strings.TrimSpace(ojson.GetBytesNoCopy(upstreamBody, "model").String())
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(doc.Get("model").String())
	}
	if upstreamModel != "" && upstreamModel != baseModel {
		reporter.SetUpstreamModel(upstreamModel)
	}
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())
	reporter.SetOutboundServiceTier(upstreamBody)
	wsHeaders = finalizeCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg, nativeRequest, upstreamModel, upstreamBody, opts.Headers, modelHeaderProfile, &identityState)
	// Validate the final wire payload: payload rules applied during the wire
	// finalize may strip or filter the hosted image tool before this point.
	if bytes.Contains(upstreamBody, []byte(`"image_generation"`)) {
		if errValidate := helps.ValidateCodexHostedImageGenerationDoc(ojson.NewDoc(upstreamBody), wsHeaders, e.cfg, !useSIWC && !codexAuthUsesAPIKey(auth)); errValidate != nil {
			return resp, errValidate
		}
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	wsReqBody := frameCodexWebsocketRequestBody(upstreamBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      sessionInstructions.LogBody(wsReqBody),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	connectionKey := newCodexWebsocketConnectionKey(authID, wsURL, baseModel, modelHeaderProfile.digest)
	connectionKey.useSIWC = useSIWC
	connectionKey.proxyURL = executionProxyURL(ctx, sess.effectiveConfig(authID, e.cfg), auth)
	var connection codexWebsocketConnectionRef
	var closer *websocketConnectionCloser
	var respHS *http.Response
	var errDial error
	// Include cold connection setup, as the HTTP attempt timer does.
	reporter.StartResponseTiming()
	dialCtx := ctx
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) || sessionInstructions.ConnectionBound() {
		connection, closer = existingCodexWebsocketSessionConnection(sess, connectionKey)
		if connection.conn == nil {
			if errConn := sessionInstructions.OnConnection(nil); errConn != nil {
				return resp, errConn
			}
			return resp, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
		}
	} else {
		dialCtx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
		connection, closer, respHS, errDial = e.ensureUpstreamConn(dialCtx, auth, sess, connectionKey, wsHeaders)
	}
	if errDial != nil {
		status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHS, errDial, "dial")
		if sess != nil {
			sess.cancelActiveConnection(handshakeErr)
		}
		unlockSession()
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
	if errConn := sessionInstructions.OnConnection(connection.conn); errConn != nil {
		return resp, errConn
	}
	actualSessionHeader, actualSessionHeaderKnown := sess.affinityHeader(connection)
	if errBind := sess.bindExecutionLifecycle(opts, connection.conn, closer, req.Model); errBind != nil {
		unlockSession()
		closeWebsocketAfterBindFailure(sess, connection.conn, closer)
		return resp, errBind
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	if isEphemeralSession {
		defer func() {
			reason := "completed"
			if err != nil {
				reason = "error"
			}
			closeCodexWebsocketSession(sess, reason)
		}()
	}

	var readCh chan codexWebsocketRead
	var requestSignal *codexWebsocketRequestSignal
	releaseActiveConnection := func() {
		if sess != nil && readCh != nil {
			sess.clearActiveConnection(connection, readCh)
		}
	}
	if sess != nil {
		readCh = make(chan codexWebsocketRead, 4096)
		var errActive error
		requestSignal, errActive = sess.setActiveConnection(connection, readCh)
		if errActive != nil {
			return resp, errActive
		}
		defer func() { releaseActiveConnection() }()
	}
	restoreMultiAgentV2 := !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(connection))

	sessionReceipt := sess.sessionInstructions.Sent(connection.conn, sessionInstructions)
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	reporter.ObserveUpstreamWebsocketRequestTo(wsReqBody, wsURL, actualSessionHeader)
	if errSend := writeCodexWebsocketMessage(sess, connection.conn, wsReqBody); errSend != nil {
		reporter.ObserveWebsocketSendFailure()
		errSend = mapCodexWebsocketWriteError(sess, connection.conn, errSend)
		if sess != nil && !isEphemeralSession {
			if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
				e.invalidateUpstreamConnWithoutDisconnectNotify(sess, connection, "send_error", errSend)
				if !shouldRetryCodexWebsocketSend(errSend) {
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
					return resp, errSend
				}
				return resp, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
			e.invalidateUpstreamConn(sess, connection, "send_error", errSend)
			if !shouldRetryCodexWebsocketSend(errSend) {
				return resp, errSend
			}

			if errConn := sessionInstructions.OnConnection(nil); errConn != nil {
				return resp, errConn
			}

			// Retry once with a fresh websocket connection. This is mainly to handle
			// upstream closing the socket between sequential requests within the same
			// execution session.
			reporter.BeginResponseTiming()
			connectionRetry, closerRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, connectionKey, wsHeaders)
			if errDialRetry == nil && connectionRetry.conn != nil {
				if errBind := sess.bindExecutionLifecycle(opts, connectionRetry.conn, closerRetry, req.Model); errBind != nil {
					releaseActiveConnection()
					unlockSession()
					closeWebsocketAfterBindFailure(sess, connectionRetry.conn, closerRetry)
					return resp, errBind
				}
				closer = closerRetry
				wsReqBodyRetry := frameCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      sessionInstructions.LogBody(wsReqBodyRetry),
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				retrySignal, errActive := sess.setActiveConnection(connectionRetry, readCh)
				if errActive != nil {
					return resp, errActive
				}
				connection = connectionRetry
				actualSessionHeader, actualSessionHeaderKnown = sess.affinityHeader(connection)
				requestSignal = retrySignal
				restoreMultiAgentV2 = !multiAgentV2Conflict && (optimizeMultiAgentV2 || sess.isMultiAgentV2Optimized(connection))
				sessionReceipt = sess.sessionInstructions.Sent(connection.conn, sessionInstructions)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				reporter.ObserveUpstreamWebsocketRequestTo(wsReqBodyRetry, wsURL, actualSessionHeader)
				if errSendRetry := writeCodexWebsocketMessage(sess, connection.conn, wsReqBodyRetry); errSendRetry == nil {
					wsReqBody = wsReqBodyRetry
				} else {
					reporter.ObserveWebsocketSendFailure()
					errSendRetry = mapCodexWebsocketWriteError(sess, connection.conn, errSendRetry)
					e.invalidateUpstreamConn(sess, connection, "send_error", errSendRetry)
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
					return resp, errSendRetry
				}
			} else {
				status, handshakeErr := codexWebsocketHandshakeFailure(ctx, e.cfg, wsReqLog, respHSRetry, errDialRetry, "dial_retry")
				releaseActiveConnection()
				unlockSession()
				if status == http.StatusUpgradeRequired {
					return e.CodexExecutor.Execute(ctx, auth, req, opts)
				}
				return resp, handshakeErr
			}
		} else {
			if sess != nil {
				e.invalidateUpstreamConn(sess, connection, "send_error", errSend)
				releaseActiveConnection()
				if isEphemeralSession {
					closeCodexWebsocketSession(sess, "send_error")
				}
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
			return resp, errSend
		}
	}

	identityState.affinity.verifySessionHeader(actualSessionHeader, actualSessionHeaderKnown)

	if optimizeMultiAgentV2 || multiAgentV2Conflict {
		sess.setMultiAgentV2Optimized(connection, optimizeMultiAgentV2 && !multiAgentV2Conflict)
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	sawOutputDelta := false
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
		reporter.ObserveUpstreamResponseBytes(len(payload))
		reporter.ObserveWebsocketDiagnostics(payload)
		reporter.ObserveResponseModel(payload)
		reporter.ObserveTimingMessage(to.String(), payload)
		observeCodexTokenEvent(reporter, payload)
		payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
		helps.AppendCodexAPIWebsocketResponse(ctx, e.cfg, payload)
		helps.EmitWebSocketResponseEvent(ctx, opts, auth, e.Identifier(), req.Model, payload)
		payload = helps.RestoreCodexMultiAgentV2Response(payload, restoreMultiAgentV2)
		reporter.SetUpstreamModel(helps.CodexUpstreamResponseModel(payload))

		if wsErr, ok := parseCodexWebsocketErrorWithCooling(payload, e.modelLevelCooling()); ok {
			if sess != nil {
				e.invalidateUpstreamConn(sess, connection, "upstream_error", wsErr)
			}
			if errClearReplay := clearCodexReasoningReplayOnWebsocketError(ctx, replayScope, payload); errClearReplay != nil {
				return resp, errClearReplay
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
			return resp, wsErr
		}
		if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(payload, e.modelLevelCooling()); ok {
			if sess != nil {
				unlockSession()
				e.invalidateUpstreamConn(sess, connection, "terminal_failure", streamErr)
			}
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
				return resp, errClearReplay
			}
			return resp, streamErr
		}

		sessionReceipt.Observe(payload)
		payload = normalizeCodexWebsocketCompletion(payload)
		eventType := ojson.GetBytesNoCopy(payload, "type").String()
		if helps.HasMeaningfulCodexOutputDelta(payload) {
			sawOutputDelta = true
		}
		switch eventType {
		case "response.output_item.done":
			collectCodexOutputItemDone(payload, outputItemsByIndex, &outputItemsFallback)
		case "response.completed", "response.done", "response.incomplete":
			if helps.IsCodexTerminalEmptyIncomplete(payload, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
				if sess != nil {
					e.invalidateUpstreamConn(sess, connection, "terminal_empty_incomplete", nil)
					unlockSession()
				}
				streamErr := newCodexEmptyIncompleteStreamError()
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				detail, _ := helps.ParseCodexUsage(payload)
				reporter.PublishFailureWithDetail(ctx, detail, streamErr)
				return resp, streamErr
			}
			payload = patchCodexCompletedOutput(payload, outputItemsByIndex, outputItemsFallback)
			if eventType != "response.incomplete" {
				identityState.affinity.complete(payload)
				cacheCodexReasoningReplayFromCompleted(replayScope, payload)
			}
			detail, hasUsage := helps.ParseCodexUsageWithOutputItems(payload, outputItemsByIndex, outputItemsFallback)
			var param any
			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, clientBody, clientPayload, &param)
			if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
				err = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				reporter.PublishFailureWithDetail(ctx, detail, err)
				return cliproxyexecutor.Response{}, err
			}
			if hasUsage {
				reporter.Publish(ctx, detail)
			} else {
				reporter.EnsurePublished(ctx)
			}
			publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, payload, outputItemsByIndex, outputItemsFallback)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				out = helps.EnsureResponsesUsageDetails(out)
			}
			resp = cliproxyexecutor.Response{Payload: out, Incomplete: eventType == "response.incomplete"}
			return resp, nil
		}
	}
}
