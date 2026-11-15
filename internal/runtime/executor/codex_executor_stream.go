package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
			return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
		}
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)
	isCompat, errExpand := e.expandCodexResponsesCompaction(auth, &req, &opts, baseModel)
	if errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if e.shouldCompactCompatResponsesTrigger(isCompat, req, opts) {
		return e.executeCodexResponsesCompactionStream(ctx, auth, req, opts)
	}
	if errPrev := rejectSyntheticCompactionPreviousResponseID(ctx, req.Payload); errPrev != nil {
		return nil, errPrev
	}

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return nil, errSIWC
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	var replayScope codexReasoningReplayScope
	defer func() {
		if !isRetryWithoutPenaltyError(err) && !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()
	defer func() { err = withCodexReasoningReplayScope(err, replayScope) }()

	affinityReq := codexAffinityRequestMetadata(req, opts)
	affinityActive := codexAffinityEnabled(e.cfg, auth) || e.hasFrozenAffinity(ctx, auth, affinityReq)
	if affinityActive {
		req = affinityReq
	}
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	preserveNativeOutput := helps.IsNativeCodexRequest(req.Payload, opts)
	isGrokClient := grokbuild.IsGrokClientContext(ctx, opts.Headers)
	to := sdktranslator.FromString("codex")
	reporter.EnableSemanticTiming(to.String())
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, true, isCompat)
	if err != nil {
		return nil, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	doc := ojson.NewDoc(body)
	normalizeCodexOutputBudgetDoc(doc)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	abnormalRetry := newCodexAbnormalReasoningRetryPolicy(e.cfg, auth, requestedModel, req.Model, baseModel)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return nil, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts, auth))
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("generate")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	reasoningSummaryDelivery := doc.Get("stream_options.reasoning_summary_delivery")
	hasReasoningSummaryDelivery := reasoningSummaryDelivery.Exists()
	var reasoningSummaryDeliveryVal any
	if hasReasoningSummaryDelivery {
		reasoningSummaryDeliveryVal = reasoningSummaryDelivery.Value()
	}
	_ = doc.Delete("stream_options")
	if hasReasoningSummaryDelivery {
		_ = doc.Set("stream_options.reasoning_summary_delivery", reasoningSummaryDeliveryVal)
	}
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
	hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
	ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	if !useSIWC {
		normalizeCodexInstructionsDoc(doc, preserveNativeOutput)
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
	} else {
		applyCodexSIWCRequestBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
	helps.NormalizeCodexToolSchemasDoc(doc)
	optimizedBody, optimizeMultiAgentV2, err := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, doc.Bytes(), e.cfg, auth, isCompat)
	if err != nil {
		return nil, err
	}
	*doc = *ojson.NewDoc(optimizedBody)
	body = doc.Bytes()
	var skipReplay bool
	body, replayScope, skipReplay, err = prepareCodexModelFallbackBody(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	// injectedReplay mirrors the non-stream path: the injection helper's changed
	// outcome, not cache presence, gates the self-heal. A self-heal retry skips
	// the injection itself but still resolves the scope so a successful response
	// stores fresh replay content.
	selfHealRetry := req.Metadata != nil && req.Metadata[cliproxyexecutor.CodexReplaySelfHealMetadataKey] == true
	injectedReplay := false
	if selfHealRetry {
		replayScope = resolveCodexReasoningReplayScopeOnly(ctx, from, req, opts, body)
	} else if !skipReplay {
		var errReplay error
		var replayResult applyCodexReasoningReplayCacheResult
		body, replayResult, errReplay = applyCodexReasoningReplayCacheWithResult(ctx, from, req, opts, body)
		if errReplay != nil {
			return nil, errReplay
		}
		replayScope = replayResult.scope
		injectedReplay = replayResult.injected
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, "codex executor", body, isCompat, targetModel, baseURL)
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	reporter.SetOutboundServiceTier(upstreamBody)
	body = upstreamBody
	upstreamModel := strings.TrimSpace(ojson.GetBytesNoCopy(upstreamBody, "model").String())
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(doc.Get("model").String())
	}
	if upstreamModel != "" && upstreamModel != baseModel {
		reporter.SetUpstreamModel(upstreamModel)
	}
	reporter.SetTranslatedReasoningEffort(upstreamBody, to.String())
	prepareHeaders := func(request *http.Request) {
		applyCodexHeaders(request, auth, apiKey, true, e.cfg, opts.Headers)
		applyCodexRoutingHint(ctx, request.Header, auth, upstreamModel, upstreamBody, opts.Headers)
		applyFinalCodexClientHeaders(request.Header, modelHeaderProfile, auth)
		applyCodexOutboundMetadataHeaders(request.Header, &identityState)
		alignCodexContentEncoding(request)
	}
	prepareHeaders(httpReq)
	// Validate the final wire payload: payload rules applied in cacheHelper may
	// strip or filter the hosted image tool before this point.
	if bytes.Contains(upstreamBody, []byte(`"image_generation"`)) {
		if errValidate := helps.ValidateCodexHostedImageGenerationDoc(ojson.NewDoc(upstreamBody), httpReq.Header, e.cfg, !useSIWC && !codexAuthUsesAPIKey(auth)); errValidate != nil {
			return nil, errValidate
		}
	}
	identityState.verifyAffinityHeader(httpReq.Header)
	affinityTransferred := false
	defer func() {
		if !affinityTransferred {
			identityState.affinity.close()
		}
	}()
	httpReq, _ = prepareCodexOutboundHTTPRequest(e.cfg, auth, url, httpReq, upstreamBody, true)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	buffering := e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering
	var bootstrapTimeout time.Duration
	var bootstrapStart time.Time
	if buffering {
		bootstrapTimeout = e.cfg.Codex.StreamBootstrapTimeoutDuration()
	}
	// Start the bootstrap clock at response headers, before decoding may read body bytes.
	responseReady := func(response *http.Response) {
		if buffering && response.StatusCode >= 200 && response.StatusCode < 300 {
			bootstrapStart = nowCodexBootstrap()
		}
	}
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, responseReady)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		data = applyCodexIdentityConfuseResponsePayload(data, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		if shouldSelfHealReplayRetry(replayScope, injectedReplay, httpResp.StatusCode, data) {
			firstErr := newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, data, e.modelLevelCooling())
			return e.retryStreamOnceWithoutReplay(ctx, reporter, firstErr, auth, req, opts)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, data, e.modelLevelCooling())
		return nil, err
	}
	abnormalStreamBuffering := abnormalRetry.StreamBuffer()
	bufferMaxBytes := abnormalRetry.StreamBufferMaxBytes()
	scannerMaxTokenBytes := int64(52_428_800) // 50 MiB compatibility ceiling.
	if abnormalStreamBuffering && bufferMaxBytes > 0 && bufferMaxBytes < scannerMaxTokenBytes {
		scannerMaxTokenBytes = bufferMaxBytes + 1
		if scannerMaxTokenBytes < 64<<10 {
			scannerMaxTokenBytes = 64 << 10
		}
	}
	var bufferedChunks [][]byte
	var initialChunks [][]byte
	immediateTerminal := false
	immediateIncomplete := false
	// bootstrapTerminalErr holds a non-overload terminal failure seen while buffering. It is
	// delivered as an in-stream chunk after the buffered handshake so downstream behaviour stays
	// identical to the unbuffered path instead of silently turning into a credential failover.
	var bootstrapTerminalErr error

	closeResponseBody := func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}
	scanner := helps.NewCodexSSEScanner(ctx, httpResp.Body, int(scannerMaxTokenBytes))
	scanner.SetOnLine(func(line []byte) {
		helps.AppendAPIResponseChunk(ctx, e.cfg, applyCodexIdentityConfuseResponsePayload(line, identityState))
	})

	sawOutputDelta := false
	var bootstrapLines [][]byte
	if buffering {
		bootstrapClaudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var bootstrapParam any
		bootstrapReleased := false
		bufferedFrames := 0
		bufferedBytes := 0
		budgetExhausted := false
		scanner.SetBudgetChecker(func(_ []byte, heldLines, heldBytes int) bool {
			budgetExhausted = (bootstrapTimeout > 0 && nowCodexBootstrap().Sub(bootstrapStart) >= bootstrapTimeout) || bufferedFrames+heldLines >= codexBootstrapMaxBufferedFrames || bufferedBytes+heldBytes >= codexBootstrapMaxBufferedBytes
			return budgetExhausted
		})
		for scanner.Scan() {
			if budgetExhausted {
				bootstrapReleased = true
				break
			}

			rawLine := bytes.Clone(scanner.Bytes())
			bootstrapLines = append(bootstrapLines, rawLine)
			line := applyCodexIdentityConfuseResponsePayload(rawLine, identityState)
			var translatedLine []byte
			bufferable := false
			if transformedLine, transformed := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); transformed {
				translatedLine = transformedLine
				bufferable = true
			} else if !bytes.HasPrefix(line, dataTag) {
				translatedLine = bytes.Clone(line)
				// SSE comments, event/id/retry lines and blank separators are held
				// with the data frame they describe and spend the same budgets.
				bufferable = true
			} else {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				translatedLine = append([]byte("data: "), data...)
				eventType := ojson.GetBytesNoCopy(data, "type").String()
				reporter.SetUpstreamModel(helps.CodexUpstreamResponseModel(data))
				observeCodexTokenEvent(reporter, data)
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					if isCodexOverloadBootstrapFailure(terminalBody) {
						timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
						timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
						if !timeoutReached {
							closeResponseBody()
							if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
								helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
								reporter.PublishFailure(ctx, errClearReplay)
								return nil, errClearReplay
							}
							overloadErr := newCodexBootstrapOverloadErr(terminalBody)
							helps.RecordAPIResponseError(ctx, e.cfg, overloadErr)
							reporter.PublishFailure(ctx, overloadErr)
							helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d buffered frames, failing over", bufferedFrames)
							return nil, overloadErr
						}
						helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d frames / %v, time budget exhausted; delivering in-stream", bufferedFrames, timeSinceStart)
					}
					// Bootstrap holds only handshake frames and has delivered
					// nothing to the client, so a signature rejection of a
					// CPA-injected replay can still be retried before the
					// stream is released. The poisoned entry must be cleared
					// first; a delete failure surfaces instead of retrying
					// with a known-poisoned cache left behind.
					if shouldSelfHealReplayRetry(replayScope, injectedReplay, streamErr.StatusCode(), terminalBody) {
						closeResponseBody()
						if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
							helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
							reporter.PublishFailure(ctx, errClearReplay)
							return nil, errClearReplay
						}
						return e.retryStreamOnceWithoutReplay(ctx, reporter, streamErr, auth, req, opts)
					}
					// Only overload/capacity rejections are converted into a request-level
					// failover above. Other terminal failures keep their ordinary in-stream
					// delivery, so the downstream observes the same result as an unbuffered
					// stream and an unrelated credential is not burned.
					bootstrapReleased = true
					break
				}

				// Bootstrap has only retained non-generated frames; any prior output
				// would already have released this stream to the delivery path.
				if helps.IsCodexTerminalEmptyIncomplete(data, 0, false) {
					closeResponseBody()
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					detail, _ := helps.ParseCodexUsage(data)
					reporter.PublishFailureWithDetail(ctx, detail, streamErr)
					return nil, streamErr
				}
				bufferable = isCodexBootstrapBufferableEvent(eventType, data)
				if !bufferable {
					bootstrapReleased = true
					break
				}
			}

			translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
			translatedChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &bootstrapParam, bootstrapClaudeInputTokens)
			frameBytes := scanner.RawBytes()
			for i := range translatedChunks {
				frameBytes += len(translatedChunks[i])
			}
			timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
			timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
			if !timeoutReached && bufferedFrames+scanner.HeldLines()+scanner.RawLines() <= codexBootstrapMaxBufferedFrames && bufferedBytes+scanner.HeldBytes()+frameBytes <= codexBootstrapMaxBufferedBytes {
				bufferedFrames += scanner.RawLines()
				bufferedBytes += frameBytes
				continue
			}
			exhausted := "frame budget"
			if timeoutReached {
				exhausted = "time budget"
			} else if bufferedFrames < codexBootstrapMaxBufferedFrames {
				exhausted = "byte budget"
			}
			helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap %s exhausted after %d frames / %d bytes / %v, releasing stream without overload probing", exhausted, bufferedFrames, bufferedBytes, timeSinceStart)
			bootstrapReleased = true
			break
		}
		scanner.SetBudgetChecker(nil)
		if !bootstrapReleased {
			closeResponseBody()
			if len(bootstrapLines) == 0 && ctx.Err() == nil {
				emptyErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before first payload"}
				helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
				reporter.PublishFailure(ctx, emptyErr)
				return nil, emptyErr
			}
			if errScan := scanner.Err(); errScan != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				return nil, errScan
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			streamErr := newCodexIncompleteStreamError()
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			return nil, streamErr
		}
	}

	chanCapacity := len(bufferedChunks) + len(initialChunks)
	if bootstrapTerminalErr != nil || (immediateIncomplete && len(initialChunks) == 0) {
		chanCapacity++
	}
	out := make(chan cliproxyexecutor.StreamChunk, chanCapacity)
	streamUsage := &cliproxyexecutor.RetryWithoutPenaltyStreamUsage{}
	for _, chunk := range bufferedChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	for _, chunk := range initialChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk, Incomplete: immediateIncomplete}
	}
	if immediateIncomplete && len(initialChunks) == 0 {
		out <- cliproxyexecutor.StreamChunk{Incomplete: true}
	}
	if bootstrapTerminalErr != nil {
		// Buffered handshake payloads are flushed first so the conductor observes a committed
		// stream and delivers this failure in-stream, exactly as the unbuffered path would.
		out <- cliproxyexecutor.StreamChunk{Err: bootstrapTerminalErr}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}
	if immediateTerminal {
		closeResponseBody()
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}

	emittedCount := 0
	for _, chunk := range bufferedChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	for _, chunk := range initialChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	var qualityRecorder *codexAbnormalReasoningRetryStreamRecorder
	if abnormalStreamBuffering {
		qualityRecorder = newCodexAbnormalReasoningRetryStreamRecorder(bufferMaxBytes)
	}
	streamFinalizer := cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer(func(headers http.Header, chunks []cliproxyexecutor.StreamChunk, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot) *cliproxyexecutor.StreamResult {
		if result := finalizeCodexAbnormalReasoningRetryStreamFromRaw(ctx, to, responseFormat, req.Model, originalPayload, body, identityState, qualityRecorder, headers, previous, abnormalRetry.clientUsageAggregation); result != nil {
			return result
		}
		return finalizeCodexAbnormalReasoningRetryStream(headers, chunks, previous, abnormalRetry.clientUsageAggregation)
	})
	affinityTransferred = true
	go func() {
		defer identityState.affinity.close()
		defer close(out)
		defer closeResponseBody()
		buffering := abnormalStreamBuffering
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		var outputItemsBytes int64
		var bufferedBytes int64
		var bufferedChunks []cliproxyexecutor.StreamChunk
		var bufferLimitErr error
		bufferLimitExceeded := false
		reconstructionCapExceeded := false
		var flushBuffered func() bool
		exceedBufferLimit := func() {
			if bufferLimitExceeded {
				return
			}
			bufferLimitErr = newCodexStreamBufferLimitError(bufferMaxBytes)
			log.WithField("stream_buffer_max_bytes", bufferMaxBytes).Warn("codex abnormal reasoning retry stream buffer exceeded; failing closed for current stream")
			bufferLimitExceeded = true
			bufferedChunks = nil
			bufferedBytes = 0
			outputItemsByIndex = nil
			outputItemsFallback = nil
			outputItemsBytes = 0
			qualityRecorder.drop()
		}
		emitChunk := func(chunk cliproxyexecutor.StreamChunk) bool {
			if buffering {
				if bufferLimitExceeded {
					return true
				}
				if len(chunk.Payload) > 0 {
					chunk.Payload = bytes.Clone(chunk.Payload)
				}
				if bufferMaxBytes > 0 && bufferedBytes+outputItemsBytes+int64(len(chunk.Payload)) > bufferMaxBytes {
					exceedBufferLimit()
					return true
				}
				bufferedChunks = append(bufferedChunks, chunk)
				bufferedBytes += int64(len(chunk.Payload))
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		flushBuffered = func() bool {
			if bufferLimitExceeded {
				return true
			}
			buffering = false
			for i := range bufferedChunks {
				select {
				case out <- bufferedChunks[i]:
				case <-ctx.Done():
					return false
				}
			}
			bufferedChunks = nil
			bufferedBytes = 0
			return true
		}
		emitError := func(err error) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: withCodexReasoningReplayScope(err, replayScope)}:
			case <-ctx.Done():
			}
		}
		rawLinesSeen := 0
		bootstrapLineIndex := 0
		nextLine := func() ([]byte, bool) {
			if bootstrapLineIndex < len(bootstrapLines) {
				line := bootstrapLines[bootstrapLineIndex]
				bootstrapLineIndex++
				return line, true
			}
			if scanner.Scan() {
				return scanner.Bytes(), true
			}
			return nil, false
		}
		for {
			rawLine, ok := nextLine()
			if !ok {
				break
			}
			rawLinesSeen++
			line := applyCodexIdentityConfuseResponsePayload(rawLine, identityState)
			var translatedLine []byte
			flushAfterLine := false
			var completedData []byte
			var usageDetail usage.Detail
			var usageDetailOK bool
			var retryErr error
			terminalSuccess := false
			cacheReasoningReplay := false
			isIncompleteEvent := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); ok {
				translatedLine = transformed
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				reporter.SetUpstreamModel(helps.CodexUpstreamResponseModel(data))
				observeCodexTokenEvent(reporter, data)
				translatedLine = append([]byte("data: "), data...)
				eventType := ojson.GetBytesNoCopy(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						emitError(errClearReplay)
						return
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					emitError(streamErr)
					return
				}
				if helps.HasMeaningfulCodexOutputDelta(data) {
					sawOutputDelta = true
				}
				if helps.IsCodexTerminalEmptyIncomplete(data, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					detail, _ := helps.ParseCodexUsage(data)
					reporter.PublishFailureWithDetail(ctx, detail, streamErr)
					emitError(streamErr)
					return
				}
				switch eventType {
				case "response.output_item.done":
					if !bufferLimitExceeded && !reconstructionCapExceeded {
						candidateBytes := outputItemsBytes + int64(len(data))
						if buffering {
							candidateBytes += bufferedBytes
						}
						if bufferMaxBytes > 0 && candidateBytes > bufferMaxBytes {
							if buffering {
								exceedBufferLimit()
							} else {
								// Deltas have already reached the client. Drop only optional
								// completion reconstruction state instead of turning the
								// delivered response into a terminal buffer-limit error.
								reconstructionCapExceeded = true
								outputItemsByIndex = nil
								outputItemsFallback = nil
								outputItemsBytes = 0
							}
						} else {
							collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
							outputItemsBytes += int64(len(data))
						}
					}
				case "response.completed", "response.incomplete", "response.done":
					terminalSuccess = true
					isIncompleteEvent = eventType == "response.incomplete"
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsageWithOutputItems(data, outputItemsByIndex, outputItemsFallback); ok {
						usageDetail = detail
						usageDetailOK = true
						if buffering {
							retryErr = abnormalRetry.RetryError(detail, reporter.ReasoningEffort())
						} else if abnormalRetry.ObserveOnly() {
							_ = abnormalRetry.RetryError(detail, reporter.ReasoningEffort())
						}
					}
					completedData = data
					if !preserveNativeOutput {
						completedData = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					}
					outputItemsByIndex = nil
					outputItemsFallback = nil
					outputItemsBytes = 0
					data = patchCodexAbnormalReasoningClientUsage(completedData, opts.Metadata, abnormalRetry.clientUsageAggregation)
					cacheReasoningReplay = eventType == "response.completed" || eventType == "response.done"
					translatedLine = append([]byte("data: "), data...)
					flushAfterLine = buffering
				}
			} else {
				translatedLine = bytes.Clone(line)
			}

			translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
			if len(completedData) > 0 {
				qualityRecorder.recordCompleted(completedData)
			} else {
				qualityRecorder.recordLine(translatedLine)
			}
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			if retryErr != nil {
				var fallbackChunks []cliproxyexecutor.StreamChunk
				if !bufferLimitExceeded {
					fallbackChunks = cloneCodexAbnormalReasoningRetryStreamChunks(bufferedChunks)
					for i := range chunks {
						fallbackChunks = append(fallbackChunks, cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunks[i])})
					}
				}
				if usageDetailOK {
					if errWithFallback := abnormalRetry.RetryErrorWithFallbackStreamChunksAndFinalizer(usageDetail, reporter.ReasoningEffort(), httpResp.Header.Clone(), fallbackChunks, streamFinalizer); errWithFallback != nil {
						retryErr = errWithFallback
					}
					reporter.PublishFailureWithDetail(ctx, usageDetail, retryErr)
					if len(completedData) > 0 {
						publishCodexImageToolUsage(ctx, reporter, body, completedData)
					}
				}
				bufferedChunks = nil
				emitError(retryErr)
				return
			}
			if usageDetailOK {
				streamUsage.Detail = normalizeCodexUsageDetail(usageDetail)
				streamUsage.HedgeScore = usageDetail.OutputTokens
				streamUsage.CandidatePolicy = codexAbnormalReasoningRetryCandidatePolicy(usageDetail, abnormalRetry.deliveryPolicy, abnormalRetry.fallbackPolicy, cliproxyexecutor.RetryWithoutPenaltyCandidateKindNonSpecial)
				streamUsage.OK = true
				if bufferLimitExceeded {
					reporter.PublishFailureWithDetail(ctx, usageDetail, bufferLimitErr)
				} else {
					reporter.Publish(ctx, usageDetail)
				}
			}
			if len(completedData) > 0 {
				publishCodexImageToolUsage(ctx, reporter, body, completedData)
				if cacheReasoningReplay {
					if !bufferLimitExceeded {
						identityState.affinity.complete(completedData)
					}
					cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
				}
			}
			if bufferLimitExceeded && len(completedData) > 0 {
				helps.RecordAPIResponseError(ctx, e.cfg, bufferLimitErr)
				emitError(bufferLimitErr)
				return
			}
			for i := range chunks {
				if ok := emitChunk(cliproxyexecutor.StreamChunk{Payload: chunks[i], Incomplete: isIncompleteEvent}); !ok {
					return
				}
				if len(chunks[i]) > 0 {
					emittedCount++
				}
			}
			if flushAfterLine {
				if ok := flushBuffered(); !ok {
					return
				}
			}
			if terminalSuccess {
				if isIncompleteEvent && len(chunks) == 0 {
					select {
					case out <- cliproxyexecutor.StreamChunk{Incomplete: true}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			if ctx.Err() != nil {
				return
			}
			if buffering && bufferMaxBytes > 0 && strings.Contains(errScan.Error(), "token too long") {
				exceedBufferLimit()
				helps.RecordAPIResponseError(ctx, e.cfg, bufferLimitErr)
				reporter.PublishFailure(ctx, bufferLimitErr)
				emitError(bufferLimitErr)
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		}
		if bufferLimitExceeded {
			helps.RecordAPIResponseError(ctx, e.cfg, bufferLimitErr)
			reporter.PublishFailure(ctx, bufferLimitErr)
			emitError(bufferLimitErr)
			return
		}
		if rawLinesSeen == 0 {
			emptyErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before first payload"}
			helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
			reporter.PublishFailure(ctx, emptyErr)
			emitError(emptyErr)
			return
		}
		streamErr := newCodexIncompleteStreamError()
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		emitError(streamErr)
	}()
	return &cliproxyexecutor.StreamResult{
		Headers:  httpResp.Header.Clone(),
		Chunks:   out,
		Metadata: codexAbnormalReasoningRetryStreamMetadata(streamUsage, streamFinalizer),
	}, nil
}
