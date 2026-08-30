package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxTurnDuration = 10 * time.Minute

func (r *pluginRuntime) execute(raw []byte) (pluginapi.ExecutorResponse, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_request", "Qoder executor request is invalid", http.StatusBadRequest, false)
	}
	if req.Stream {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_request", "Qoder non-stream executor received a streaming request", http.StatusBadRequest, false)
	}
	session, errStart := r.startTurn(req.ExecutorRequest)
	if errStart != nil {
		return pluginapi.ExecutorResponse{}, errStart
	}
	defer r.completeTurn(session, req.RequestID)
	projection := newEventProjection(req.RequestID, req.Model)
	ctx, cancel := context.WithTimeout(context.Background(), maxTurnDuration)
	defer cancel()
	for {
		event, errEvent := session.client.readEvent(ctx)
		if errEvent != nil {
			r.dropSession(session)
			return pluginapi.ExecutorResponse{}, newPluginCallError("runner_lost", "Qoder runner event stream was lost", http.StatusBadGateway, true)
		}
		if errConsume := projection.consume(event); errConsume != nil {
			r.dropSession(session)
			return pluginapi.ExecutorResponse{}, newPluginCallError("runner_protocol_error", errConsume.Error(), http.StatusBadGateway, true)
		}
		if event.IsTerminal() {
			return projection.nonStreamResponse()
		}
	}
}

func (r *pluginRuntime) executeStream(raw []byte) (rpcStreamResponse, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return rpcStreamResponse{}, newPluginCallError("invalid_request", "Qoder executor request is invalid", http.StatusBadRequest, false)
	}
	if !req.Stream || strings.TrimSpace(req.StreamID) == "" {
		return rpcStreamResponse{}, newPluginCallError("invalid_stream", "Qoder stream requires stream=true and a stream ID", http.StatusBadRequest, false)
	}
	session, errStart := r.startTurn(req.ExecutorRequest)
	if errStart != nil {
		return rpcStreamResponse{}, errStart
	}
	go r.forwardEvents(session, req)
	return rpcStreamResponse{Headers: http.Header{"Content-Type": {"text/event-stream"}}}, nil
}

func (r *pluginRuntime) startTurn(req pluginapi.ExecutorRequest) (*runnerSession, error) {
	if req.RequestID = strings.TrimSpace(req.RequestID); req.RequestID == "" {
		return nil, newPluginCallError("invalid_request", "Qoder request_id is required", http.StatusBadRequest, false)
	}
	if req.Format != "" && req.Format != "chat-completions" && req.Format != "openai" {
		return nil, newPluginCallError("unsupported_format", "Qoder executor accepts host-translated Chat Completions payloads only", http.StatusBadRequest, false)
	}
	if errModel := validateCanonicalModel(req.Model); errModel != nil {
		return nil, errModel
	}
	prompt, errPrompt := promptFromChat(req.Payload)
	if errPrompt != nil {
		return nil, errPrompt
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return nil, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusUnauthorized, false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.loadedConfig().RequestTimeout)
	defer cancel()
	session, errSession := r.acquireSession(ctx, req, auth)
	if errSession != nil {
		return nil, errSession
	}
	turnID := req.RequestID
	if value, ok := req.Metadata["turn_id"].(string); ok && strings.TrimSpace(value) != "" {
		turnID = strings.TrimSpace(value)
	}
	cfg := r.loadedConfig()
	params := map[string]any{
		"request_id":           req.RequestID,
		"execution_session_id": session.executionSessionID,
		"turn_id":              turnID,
		"provider":             pluginIdentifier,
		"auth_id":              req.AuthID,
		"auth_index":           req.AuthIndex,
		"prompt":               prompt,
		"model":                req.Model,
		"auth":                 auth.runnerAuth(),
		"permission_policy": map[string]any{
			"default": cfg.PermissionDefault,
			"rules":   cfg.PermissionRules,
		},
	}
	if errCall := session.client.call(ctx, "start", params, nil); errCall != nil {
		r.completeTurn(session, req.RequestID)
		r.dropSession(session)
		return nil, errCall
	}
	return session, nil
}

func (r *pluginRuntime) forwardEvents(session *runnerSession, req rpcExecutorRequest) {
	projection := newEventProjection(req.RequestID, req.Model)
	errorMessage := ""
	errorCode := ""
	errorRetryable := false
	errorHTTPStatus := 0
	downstreamClosed := false
	defer func() {
		closePluginStream(r.caller, req.StreamID, errorMessage, errorCode, errorRetryable, errorHTTPStatus)
		r.completeTurn(session, req.RequestID)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), maxTurnDuration)
	defer cancel()
	for {
		event, errEvent := session.client.readEvent(ctx)
		if errEvent != nil {
			errorMessage = "Qoder runner event stream was lost"
			errorCode = "connection_lifecycle"
			errorRetryable = true
			r.dropSession(session)
			return
		}
		chunk, terminal, errProject := projection.streamChunk(event)
		if errProject != nil {
			errorMessage = errProject.Error()
			if callErr, ok := errProject.(*pluginCallError); ok {
				errorCode = callErr.code
				errorRetryable = callErr.retryable
				errorHTTPStatus = callErr.statusCode
				if callErr.code == "runner_lost" || callErr.code == "session_closed" {
					errorCode = "connection_lifecycle"
					errorHTTPStatus = 0
				}
			}
			r.dropSession(session)
			return
		}
		if len(chunk) > 0 && !downstreamClosed {
			if errEmit := emitPluginStream(r.caller, req.StreamID, chunk); errEmit != nil {
				errorMessage = "Qoder downstream stream closed"
				_ = r.cancelExecution(pluginapi.CancelExecutionRequest{
					RequestID: req.RequestID, ExecutionSessionID: session.executionSessionID,
					Reason: pluginapi.ExecutionCancelReasonDownstreamDisconnected,
				})
				downstreamClosed = true
			}
		}
		if terminal {
			return
		}
	}
}

func (r *pluginRuntime) dropSession(session *runnerSession) {
	if session == nil {
		return
	}
	r.mu.Lock()
	if r.sessions[session.key] == session {
		delete(r.sessions, session.key)
	}
	if session.currentRequestID != "" {
		delete(r.requestSession, session.currentRequestID)
	}
	r.mu.Unlock()
	session.client.shutdown()
}
