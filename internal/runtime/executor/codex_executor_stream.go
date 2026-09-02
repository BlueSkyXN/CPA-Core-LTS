package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
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
		if !isRetryWithoutPenaltyError(err) && !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()
	defer func() { err = withCodexReasoningReplayScope(err, replayScope) }()

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	isGrokClient := grokbuild.IsGrokClientContext(ctx, opts.Headers)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true, helps.APIKeyModelIsCompat(req))

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	abnormalRetry := newCodexAbnormalReasoningRetryPolicy(e.cfg, auth, requestedModel, req.Model, baseModel)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = sanitizeCodexUnsupportedReasoningSummary(body, baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	reasoningSummaryDelivery := gjson.GetBytes(body, "stream_options.reasoning_summary_delivery")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	if reasoningSummaryDelivery.Exists() {
		body, _ = sjson.SetBytes(body, "stream_options.reasoning_summary_delivery", reasoningSummaryDelivery.Value())
	}
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, baseModel)
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
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	reporter.SetOutboundServiceTier(upstreamBody)
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg, opts.Headers)
	applyFinalCodexClientHeaders(httpReq.Header, modelHeaderProfile, auth)
	applyCodexOutboundMetadataHeaders(httpReq.Header, &identityState)
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
	httpResp, err := httpClient.Do(httpReq)
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
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
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
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, int(scannerMaxTokenBytes))
	closeResponseBody := func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}

	var bootstrapLines [][]byte
	if e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering {
		bootstrapReleased := false
		handshakeEvents := 0
		for scanner.Scan() {
			rawLine := bytes.Clone(scanner.Bytes())
			bootstrapLines = append(bootstrapLines, rawLine)
			line := applyCodexIdentityConfuseResponsePayload(rawLine, identityState)
			if _, transformed := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); transformed {
				handshakeEvents++
				if handshakeEvents >= codexBootstrapMaxBufferedEvents {
					helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap buffer limit %d reached, releasing stream without overload probing", codexBootstrapMaxBufferedEvents)
					bootstrapReleased = true
					break
				}
				continue
			}
			if !bytes.HasPrefix(line, dataTag) {
				continue
			}

			data := bytes.TrimSpace(line[5:])
			data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
			observeCodexTokenEvent(reporter, data)
			if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
				if isCodexOverloadBootstrapFailure(terminalBody) {
					for _, bufferedLine := range bootstrapLines {
						loggedLine := applyCodexIdentityConfuseResponsePayload(bufferedLine, identityState)
						helps.AppendAPIResponseChunk(ctx, e.cfg, loggedLine)
					}
					closeResponseBody()
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						return nil, errClearReplay
					}
					overloadErr := newCodexBootstrapOverloadErr(terminalBody)
					helps.RecordAPIResponseError(ctx, e.cfg, overloadErr)
					reporter.PublishFailure(ctx, overloadErr)
					helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d buffered handshake events, failing over", handshakeEvents)
					return nil, overloadErr
				}
				bootstrapReleased = true
				break
			}

			eventType := gjson.GetBytes(data, "type").String()
			if !isCodexHandshakeMetadataEvent(eventType) {
				bootstrapReleased = true
				break
			}
			handshakeEvents++
			if handshakeEvents >= codexBootstrapMaxBufferedEvents {
				helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap buffer limit %d reached, releasing stream without overload probing", codexBootstrapMaxBufferedEvents)
				bootstrapReleased = true
				break
			}
		}
		if !bootstrapReleased {
			closeResponseBody()
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
	out := make(chan cliproxyexecutor.StreamChunk)
	streamUsage := &cliproxyexecutor.RetryWithoutPenaltyStreamUsage{}
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
	go func() {
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
			line := applyCodexIdentityConfuseResponsePayload(rawLine, identityState)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			translatedLine := bytes.Clone(line)
			flushAfterLine := false
			var completedData []byte
			var usageDetail usage.Detail
			var usageDetailOK bool
			var retryErr error
			terminalSuccess := false
			cacheReasoningReplay := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(translatedLine, isGrokClient); ok {
				translatedLine = transformed
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				observeCodexTokenEvent(reporter, data)
				translatedLine = append([]byte("data: "), data...)
				eventType := gjson.GetBytes(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
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
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsage(data); ok {
						usageDetail = detail
						usageDetailOK = true
						if buffering {
							retryErr = abnormalRetry.RetryError(detail, reporter.ReasoningEffort())
						} else if abnormalRetry.ObserveOnly() {
							_ = abnormalRetry.RetryError(detail, reporter.ReasoningEffort())
						}
					}
					completedData = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					outputItemsByIndex = nil
					outputItemsFallback = nil
					outputItemsBytes = 0
					data = patchCodexAbnormalReasoningClientUsage(completedData, opts.Metadata, abnormalRetry.clientUsageAggregation)
					cacheReasoningReplay = eventType == "response.completed" || eventType == "response.done"
					translatedLine = append([]byte("data: "), data...)
					flushAfterLine = buffering
				}
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
					cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
				}
			}
			if bufferLimitExceeded && len(completedData) > 0 {
				helps.RecordAPIResponseError(ctx, e.cfg, bufferLimitErr)
				emitError(bufferLimitErr)
				return
			}
			for i := range chunks {
				if ok := emitChunk(cliproxyexecutor.StreamChunk{Payload: chunks[i]}); !ok {
					return
				}
			}
			if flushAfterLine {
				if ok := flushBuffered(); !ok {
					return
				}
			}
			if terminalSuccess {
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
