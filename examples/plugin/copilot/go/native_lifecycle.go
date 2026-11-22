package main

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// nativeExecution 只记录在途状态；对话历史仍由客户端逐次提供。
type nativeExecution struct {
	identity     executionIdentity
	requestID    string
	model        string
	tools        map[int]nativeToolIdentity // 发给客户端的工具片段快照，由 pluginRuntime.mu 保护。
	toolBatchKey string                     // 仅在工具批次收齐且参数已确认时设置。
	replayClosed bool                       // 会话关闭后，旧执行不能再登记待续调的状态。
	config       pluginConfig
	mu           sync.Mutex
	upstream     string
	canceled     bool
	done         chan struct{}
	once         sync.Once
	open         func() (hostHTTPStreamResponse, error)
	generation   uint64
}

func (r *pluginRuntime) registerNative(req rpcExecutorRequest, auth copilotAuth) (*nativeExecution, error) {
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.HostCallbackID) == "" || r.caller == nil {
		return nil, newPluginCallError("invalid_request", "Copilot execution requires request_id and a host callback context", http.StatusBadRequest, false)
	}
	key := executionSessionKey(req.ExecutorRequest, auth)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return nil, newPluginCallError("plugin_quiescing", "Copilot plugin is quiescing", http.StatusServiceUnavailable, true)
	}
	if r.nativeActive[req.RequestID] != nil {
		return nil, newPluginCallError("duplicate_request", "Copilot request is already active", http.StatusConflict, false)
	}
	exec := &nativeExecution{requestID: req.RequestID, model: req.Model, done: make(chan struct{}), config: r.config, generation: r.generation, identity: executionIdentity{
		key: key, authID: req.AuthID, authIndex: req.AuthIndex, callerScope: req.CallerScope,
		workspaceIdentity: req.WorkspaceIdentity, executionSessionID: effectiveExecutionSessionID(req.ExecutorRequest),
	}}
	r.nativeActive[req.RequestID] = exec
	return exec, nil
}

func (e *nativeExecution) bind(caller hostCaller, id string) bool {
	e.mu.Lock()
	canceled := e.canceled
	if !canceled {
		e.upstream = id
	}
	e.mu.Unlock()
	if canceled {
		closeHostHTTPStream(caller, id)
	}
	return !canceled
}

func (e *nativeExecution) closeUpstream(caller hostCaller, cancel bool) {
	e.mu.Lock()
	if cancel {
		e.canceled = true
	}
	id := e.upstream
	e.upstream = ""
	e.mu.Unlock()
	closeHostHTTPStream(caller, id)
}

func (e *nativeExecution) isCanceled() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.canceled }

func (r *pluginRuntime) releaseNative(e *nativeExecution) { r.finishNative(e, false) }

func (r *pluginRuntime) finishNative(e *nativeExecution, interrupted bool) {
	e.once.Do(func() {
		e.closeUpstream(r.caller, false)
		r.mu.Lock()
		if r.nativeActive[e.requestID] == e {
			if interrupted || e.isCanceled() {
				r.interruptNativeToolsLocked(e)
			}
			delete(r.nativeActive, e.requestID)
		}
		r.mu.Unlock()
		close(e.done)
	})
}

func (r *pluginRuntime) cancelNative(req pluginapi.CancelExecutionRequest) {
	r.mu.Lock()
	e := r.nativeActive[strings.TrimSpace(req.RequestID)]
	r.mu.Unlock()
	if e != nil && cancelMatches(&e.identity, req) {
		e.closeUpstream(r.caller, true)
	}
}

func (r *pluginRuntime) closeNative(req pluginapi.CloseExecutionSessionRequest) {
	if req.Provider != "" && req.Provider != pluginIdentifier {
		return
	}
	r.mu.Lock()
	var active []*nativeExecution
	for _, e := range r.nativeActive {
		if closeMatches(&e.identity, req) {
			e.replayClosed = true
			active = append(active, e)
		}
	}
	for key, replay := range r.nativeReasoning {
		if closeMatches(&replay.identity, req) {
			delete(r.nativeReasoning, key)
		}
	}
	r.interruptedTools = slices.DeleteFunc(r.interruptedTools, func(batch interruptedToolBatch) bool { return closeMatches(&batch.identity, req) })
	r.mu.Unlock()
	for _, e := range active {
		e.closeUpstream(r.caller, true)
	}
}

func (r *pluginRuntime) waitNative() {
	r.mu.Lock()
	var active []*nativeExecution
	for _, e := range r.nativeActive {
		active = append(active, e)
	}
	r.mu.Unlock()
	deadline := time.NewTimer(shutdownWait)
	defer deadline.Stop()
	for _, e := range active {
		select {
		case <-e.done:
		case <-deadline.C:
			return
		}
	}
}

func nativeCanceled() error {
	return newPluginCallError("request_cancelled", "Copilot request was canceled", 0, true)
}

func (r *pluginRuntime) nativeReadiness(req pluginapi.ReadinessRequest, cfg pluginConfig) pluginapi.ReadinessResponse {
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	protocol := pluginapi.ReadinessStateReady
	message := "Copilot API endpoint is configured; live acceptance is verified on execution"
	if !accepting || cfg.CopilotAPIEndpoint == "" || validateDirectURL(cfg.CopilotAPIEndpoint, "copilot_api_endpoint") != nil {
		protocol, message = pluginapi.ReadinessStateNotReady, "Copilot API endpoint is unavailable or the plugin is quiescing"
	}
	authState := pluginapi.ReadinessStateUnknown
	authMessage := "selected credential was not supplied"
	if len(req.StorageJSON) > 0 {
		if _, err := parseStoredAuth(req.StorageJSON); err != nil {
			authState, authMessage = pluginapi.ReadinessStateNotReady, "selected Copilot credential is invalid"
		} else {
			authState, authMessage = pluginapi.ReadinessStateReady, "selected credential is configured; no inference probe was made"
		}
	}
	ready := protocol == pluginapi.ReadinessStateReady
	if req.Purpose == pluginapi.ReadinessPurposeAdmission || len(req.StorageJSON) > 0 || req.AuthID != "" || req.AuthIndex != "" {
		ready = ready && authState == pluginapi.ReadinessStateReady
	}
	return pluginapi.ReadinessResponse{
		Provider: pluginIdentifier, Ready: ready, Generation: pluginVersion,
		Capabilities: []string{"chat_completions", "stream", "cancel", "close", "oauth_device_login"},
		Checks: []pluginapi.ReadinessCheck{
			{Level: pluginapi.ReadinessLevelPluginInstalled, State: pluginapi.ReadinessStateReady, Version: pluginVersion},
			{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Version: "native-go", Message: "no external runner or CLI is required"},
			{Level: pluginapi.ReadinessLevelProtocolReady, State: protocol, Message: message},
			{Level: pluginapi.ReadinessLevelAuthReady, State: authState, Message: authMessage},
			{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnsupported, Message: "conversation history is supplied by the client"},
		},
	}
}
