package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImage(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return resp, errSIWC
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
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := e.resolveCodexModelIsCompat(auth, req, baseModel)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	abnormalRetry := newCodexAbnormalReasoningRetryPolicy(e.cfg, auth, requestedModel, req.Model, baseModel)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, false, isCompat)
	if err != nil {
		return resp, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	doc := ojson.NewDoc(body)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return resp, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts, auth.AuthKind()))
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", true)
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("generate")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
	hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
	ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	upstreamModel := baseModel
	if !useSIWC {
		normalizeCodexInstructionsDoc(doc, helps.IsNativeCodexRequest(req.Payload, opts))
		if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
			ensureImageGenerationToolDoc(doc, baseModel, auth, opts.Headers)
			if !codexAuthUsesAPIKey(auth) {
				normalizeCodexImageGenerationToolsDoc(doc)
				var errHostedModel error
				upstreamModel, errHostedModel = applyCodexHostedImageGenerationModelDoc(e.cfg, doc, baseModel, req.Model)
				if errHostedModel != nil {
					return resp, errHostedModel
				}
			}
		}
	} else {
		applyCodexSIWCRequestBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	if upstreamModel != baseModel {
		reporter.SetUpstreamModel(upstreamModel)
	}
	normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
	helps.NormalizeCodexToolSchemasDoc(doc)
	optimizedBody, optimizeMultiAgentV2, err := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, doc.Bytes(), e.cfg, auth, isCompat)
	if err != nil {
		return resp, err
	}
	*doc = *ojson.NewDoc(optimizedBody)
	body = doc.Bytes()
	var skipReplay bool
	body, replayScope, skipReplay, err = prepareCodexModelFallbackBody(ctx, from, req, opts, body)
	if err != nil {
		return resp, err
	}
	// injectedReplay records whether this attempt inserted CPA-owned replay
	// items into the upstream body — the injection helper's changed outcome, not
	// mere cache presence: a present entry can be filtered out entirely. Only an
	// actual injection may be retried without it on a signature rejection; the
	// removed content is proxy-owned, never client ciphertext. A self-heal retry
	// skips the injection itself but still resolves the scope so a successful
	// response stores fresh replay content.
	selfHealRetry := req.Metadata != nil && req.Metadata[cliproxyexecutor.CodexReplaySelfHealMetadataKey] == true
	injectedReplay := false
	if selfHealRetry {
		replayScope = resolveCodexReasoningReplayScopeOnly(ctx, from, req, opts, body)
	} else if !skipReplay {
		var errReplay error
		var replayResult applyCodexReasoningReplayCacheResult
		body, replayResult, errReplay = applyCodexReasoningReplayCacheWithResult(ctx, from, req, opts, body)
		if errReplay != nil {
			return resp, errReplay
		}
		replayScope = replayResult.scope
		injectedReplay = replayResult.injected
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, "codex executor", body, isCompat, targetModel, baseURL)
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return resp, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return resp, err
	}
	reporter.SetOutboundServiceTier(upstreamBody)
	body = upstreamBody
	reporter.SetTranslatedReasoningEffort(upstreamBody, to.String())
	prepareHeaders := func(request *http.Request) {
		applyCodexHeaders(request, auth, apiKey, true, e.cfg, opts.Headers)
		applyCodexRoutingHint(ctx, request.Header, auth, upstreamModel, upstreamBody, opts.Headers)
		applyFinalCodexClientHeaders(request.Header, modelHeaderProfile, auth)
		applyCodexOutboundMetadataHeaders(request.Header, &identityState)
		alignCodexContentEncoding(request)
	}
	prepareHeaders(httpReq)
	identityState.verifyAffinityHeader(httpReq.Header)
	defer identityState.affinity.close()
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
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, nil)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		b = applyCodexIdentityConfuseResponsePayload(b, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, b); errClearReplay != nil {
			return resp, errClearReplay
		}
		if shouldSelfHealReplayRetry(replayScope, injectedReplay, httpResp.StatusCode, b) {
			firstErr := newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
			resp, err = e.retryOnceWithoutReplay(ctx, reporter, firstErr, auth, req, opts)
			return resp, err
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
		return resp, err
	}
	data, errRead := io.ReadAll(httpResp.Body)
	upstreamData := applyCodexIdentityConfuseResponsePayload(data, identityState)
	helps.AppendAPIResponseChunk(ctx, e.cfg, upstreamData)

	events := helps.NewCodexSSEScanner(ctx, bytes.NewReader(upstreamData), len(upstreamData)+1)
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	sawOutputDelta := false
	for events.Scan() {
		line := events.Bytes()
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		eventData := bytes.TrimSpace(line[5:])
		eventData = helps.RestoreCodexMultiAgentV2Response(eventData, optimizeMultiAgentV2)
		reporter.ObserveResponseModel(eventData)
		eventType := ojson.GetBytesNoCopy(eventData, "type").String()

		if helps.HasMeaningfulCodexOutputDelta(eventData) {
			sawOutputDelta = true
		}

		if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(eventData, e.modelLevelCooling()); ok {
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
				return resp, errClearReplay
			}
			if shouldSelfHealReplayRetry(replayScope, injectedReplay, streamErr.StatusCode(), terminalBody) {
				resp, err = e.retryOnceWithoutReplay(ctx, reporter, streamErr, auth, req, opts)
				return resp, err
			}
			err = streamErr
			return resp, err
		}

		if eventType == "response.output_item.done" {
			itemResult := ojson.GetBytesNoCopy(eventData, "item")
			if !itemResult.Exists() || itemResult.Type != ojson.JSON {
				continue
			}
			outputIndexResult := ojson.GetBytesNoCopy(eventData, "output_index")
			if outputIndexResult.Exists() {
				outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
			} else {
				outputItemsFallback = append(outputItemsFallback, []byte(itemResult.Raw))
			}
			continue
		}

		if eventType != "response.completed" && eventType != "response.incomplete" {
			continue
		}

		if helps.IsCodexTerminalEmptyIncomplete(eventData, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
			err = newCodexEmptyIncompleteStreamError()
			detail, _ := helps.ParseCodexUsage(eventData)
			reporter.PublishFailureWithDetail(ctx, detail, err)
			return resp, err
		}

		completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
		var completedUsage usage.Detail
		var completedUsageOK bool
		var responseFinalizer cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer
		if detail, ok := helps.ParseCodexUsage(eventData); ok {
			completedUsage = detail
			completedUsageOK = true
			responseFinalizer = cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer(func(base cliproxyexecutor.Response, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot) cliproxyexecutor.Response {
				finalCompletedData := patchCodexAbnormalReasoningClientUsageWithSnapshot(completedData, previous, abnormalRetry.clientUsageAggregation)
				finalCompletedData = applyCodexIdentityExposeResponsePayload(finalCompletedData, identityState)
				// base already carries a validated translation; keep it rather than
				// delivering an empty or partially converted aggregate.
				if out, ok := translateCodexNonStreamChecked(ctx, to, responseFormat, req.Model, originalPayload, body, finalCompletedData); ok {
					base.Payload = out
				}
				return base
			})
			if errRetry := abnormalRetry.RetryError(detail, reporter.ReasoningEffort()); errRetry != nil {
				clientCompletedData := patchCodexAbnormalReasoningClientUsage(completedData, opts.Metadata, abnormalRetry.clientUsageAggregation)
				clientCompletedData = applyCodexIdentityExposeResponsePayload(clientCompletedData, identityState)
				// Only a validated translation may become a pass-through fallback;
				// otherwise exhaustion must surface an error, not an empty success.
				if out, ok := translateCodexNonStreamChecked(ctx, to, responseFormat, req.Model, originalPayload, body, clientCompletedData); ok {
					fallbackResp := cliproxyexecutor.Response{
						Payload:  out,
						Headers:  httpResp.Header.Clone(),
						Metadata: codexAbnormalReasoningRetryResponseMetadata(detail, responseFinalizer, abnormalRetry),
					}
					if errWithFallback := abnormalRetry.RetryErrorWithFallbackResponse(detail, reporter.ReasoningEffort(), fallbackResp); errWithFallback != nil {
						errRetry = errWithFallback
					}
				}
				reporter.PublishFailureWithDetail(ctx, detail, errRetry)
				// The image tool ran upstream regardless of the reasoning retry.
				publishCodexImageToolUsage(ctx, reporter, body, eventData)
				err = errRetry
				return resp, err
			}
		}

		if eventType == "response.completed" {
			identityState.affinity.complete(completedData)
			cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
		}

		clientCompletedData := patchCodexAbnormalReasoningClientUsage(completedData, opts.Metadata, abnormalRetry.clientUsageAggregation)
		clientCompletedData = applyCodexIdentityExposeResponsePayload(clientCompletedData, identityState)
		out, translated := translateCodexNonStreamChecked(ctx, to, responseFormat, req.Model, originalPayload, body, clientCompletedData)
		if !translated {
			err = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			reporter.PublishFailureWithDetail(ctx, completedUsage, err)
			publishCodexImageToolUsage(ctx, reporter, body, eventData)
			return cliproxyexecutor.Response{}, err
		}
		if detail, ok := helps.ParseCodexUsageWithOutputItems(completedData, outputItemsByIndex, outputItemsFallback); ok {
			reporter.Publish(ctx, detail)
		} else if completedUsageOK {
			reporter.Publish(ctx, completedUsage)
		} else {
			// A completed response without usage is still a successful request.
			reporter.EnsurePublished(ctx)
		}
		publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, completedData, outputItemsByIndex, outputItemsFallback)
		if responseFormat == sdktranslator.FormatOpenAIResponse {
			out = helps.EnsureResponsesUsageDetails(out)
		}
		resp = cliproxyexecutor.Response{Payload: out, Incomplete: eventType == "response.incomplete", Headers: httpResp.Header.Clone()}
		if completedUsageOK {
			resp.Metadata = codexAbnormalReasoningRetryResponseMetadata(completedUsage, responseFinalizer, abnormalRetry)
		}
		return resp, nil
	}
	if errRead != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errCtx)
			err = errCtx
			return resp, err
		}
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
	}
	err = newCodexIncompleteStreamError()
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	modelHeaderProfile := resolveCodexModelHeaderProfile(baseModel)

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return resp, errSIWC
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer func() {
		if !isRetryWithoutPenaltyError(err) && !isCodexModelFallbackBlockedError(err) {
			reporter.TrackFailure(ctx, &err)
		}
	}()

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := e.resolveCodexModelIsCompat(auth, req, baseModel)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, false, isCompat)
	if err != nil {
		return resp, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)

	doc := ojson.NewDoc(body)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return resp, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts, auth.AuthKind()))
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	_ = doc.Delete("stream")
	if !codexUseSIWCForRequest(ctx, auth, opts.Headers) {
		normalizeCodexInstructionsDoc(doc, helps.IsNativeCodexRequest(req.Payload, opts))
	} else {
		instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
		hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
		applyCodexSIWCCompactBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex executor", doc, isCompat, resolveResponsesSignatureTargetModel(baseModel, req, opts), baseURL)
	normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
	helps.NormalizeCodexToolSchemasDoc(doc)
	optimizedBody, optimizeMultiAgentV2, err := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, doc.Bytes(), e.cfg, auth, isCompat)
	if err != nil {
		return resp, err
	}
	*doc = *ojson.NewDoc(optimizedBody)
	body = doc.Bytes()
	body, err = thinking.NormalizeCodexReasoningEffortForWire(body, baseModel)
	if err != nil {
		return resp, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return resp, err
	}
	reporter.SetOutboundServiceTier(upstreamBody)
	reporter.SetTranslatedReasoningEffort(upstreamBody, to.String())
	prepareHeaders := func(request *http.Request) {
		applyCodexHeaders(request, auth, apiKey, false, e.cfg, opts.Headers)
		applyCodexRoutingHint(ctx, request.Header, auth, baseModel, upstreamBody, opts.Headers)
		applyFinalCodexClientHeaders(request.Header, modelHeaderProfile, auth)
		applyCodexOutboundMetadataHeaders(request.Header, &identityState)
		alignCodexContentEncoding(request)
	}
	prepareHeaders(httpReq)
	httpReq, _ = prepareCodexOutboundHTTPRequest(e.cfg, auth, url, httpReq, upstreamBody, false)
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
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, nil)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		b = applyCodexIdentityConfuseResponsePayload(b, identityState)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	upstreamData := applyCodexIdentityConfuseResponsePayload(data, identityState)
	helps.AppendAPIResponseChunk(ctx, e.cfg, upstreamData)
	upstreamData = helps.RestoreCodexMultiAgentV2Response(upstreamData, optimizeMultiAgentV2)
	var param any
	clientData := applyCodexIdentityExposeResponsePayload(upstreamData, identityState)
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, clientData, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		err = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		reporter.PublishFailureWithDetail(ctx, helps.ParseOpenAIUsage(upstreamData), err)
		return cliproxyexecutor.Response{}, err
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(upstreamData))
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// translateCodexNonStreamChecked converts a completed Codex response and
// reports whether the result is deliverable. Empty output or a translator
// tool-input failure means the conversion must not be treated as success.
func translateCodexNonStreamChecked(ctx context.Context, from, to sdktranslator.Format, model string, originalPayload, body, completed []byte) ([]byte, bool) {
	var param any
	out := sdktranslator.TranslateNonStream(ctx, from, to, model, originalPayload, body, completed, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return nil, false
	}
	return out, true
}
