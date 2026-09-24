package main

import (
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// direct 请求只记录在途状态；对话历史仍由客户端逐次提供。
type nativeExecution struct {
	identity  executionIdentity
	requestID string
	mu        sync.Mutex
	upstream  string
	canceled  bool
	done      chan struct{}
	once      sync.Once
}

func (r *pluginRuntime) registerNative(req rpcExecutorRequest, auth qoderAuth) (*nativeExecution, error) {
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.HostCallbackID) == "" || r.caller == nil {
		return nil, newPluginCallError("invalid_request", "Qoder direct requires request_id and a host callback context", 400, false)
	}
	key := executionSessionKey(req.ExecutorRequest, auth)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return nil, newPluginCallError("plugin_quiescing", "Qoder plugin is quiescing", 503, true)
	}
	if r.nativeActive[req.RequestID] != nil {
		return nil, newPluginCallError("duplicate_request", "Qoder request is already active", 409, false)
	}
	if r.nativeSessions[key] != nil {
		return nil, newPluginCallError("turn_conflict", "Qoder execution session already has an active turn", 409, true)
	}
	exec := &nativeExecution{requestID: req.RequestID, done: make(chan struct{}), identity: executionIdentity{
		key: key, authID: req.AuthID, authIndex: req.AuthIndex, callerScope: req.CallerScope,
		workspaceIdentity: req.WorkspaceIdentity, executionSessionID: effectiveExecutionSessionID(req.ExecutorRequest),
	}}
	r.nativeActive[req.RequestID], r.nativeSessions[key] = exec, exec
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

func (r *pluginRuntime) releaseNative(e *nativeExecution) {
	e.once.Do(func() {
		e.closeUpstream(r.caller, false)
		r.mu.Lock()
		if r.nativeActive[e.requestID] == e {
			delete(r.nativeActive, e.requestID)
		}
		if r.nativeSessions[e.identity.key] == e {
			delete(r.nativeSessions, e.identity.key)
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
			active = append(active, e)
		}
	}
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
	return newPluginCallError("connection_lifecycle", "Qoder direct request was canceled", 0, true)
}

func (r *pluginRuntime) nativeReadiness(req pluginapi.ReadinessRequest, cfg pluginConfig) pluginapi.ReadinessResponse {
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	protocol := pluginapi.ReadinessStateReady
	message := "native direct HTTPS is configured; remote acceptance is checked on execution"
	if !accepting || cfg.DirectEndpoint == "" || validateDirectURL(cfg.DirectEndpoint, "direct_endpoint") != nil {
		protocol, message = pluginapi.ReadinessStateNotReady, "direct endpoint is unavailable or plugin is quiescing"
	}
	authState := pluginapi.ReadinessStateUnknown
	authMessage := "selected credential was not supplied"
	if len(req.StorageJSON) > 0 {
		auth, err := parseStoredAuth(req.StorageJSON)
		if err != nil || (auth.isPAT() && cfg.DirectTokenMode != "bearer" && cfg.OpenAPIEndpoint == "") {
			authState, authMessage = pluginapi.ReadinessStateNotReady, "selected direct credential or token exchange configuration is invalid"
		} else {
			authState, authMessage = pluginapi.ReadinessStateReady, "selected credential is configured; no inference probe was made"
		}
	}
	ready := protocol == pluginapi.ReadinessStateReady
	if req.Purpose == pluginapi.ReadinessPurposeAdmission || len(req.StorageJSON) > 0 || req.AuthID != "" || req.AuthIndex != "" {
		ready = ready && authState == pluginapi.ReadinessStateReady
	}
	capabilities := []string{"chat_completions", "stream", "cancel", "close", "direct_openai"}
	if !isQoderCosyInferenceEndpoint(cfg.DirectEndpoint) {
		capabilities = append(capabilities, "client_tools")
	}
	return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: ready, Generation: pluginVersion,
		Capabilities: capabilities,
		Checks: []pluginapi.ReadinessCheck{
			{Level: pluginapi.ReadinessLevelPluginInstalled, State: pluginapi.ReadinessStateReady, Version: pluginVersion},
			{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Version: "native-go", Message: "external runner, Node and Qoder SDK are not required"},
			{Level: pluginapi.ReadinessLevelProtocolReady, State: protocol, Message: message},
			{Level: pluginapi.ReadinessLevelAuthReady, State: authState, Message: authMessage},
			{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnsupported, Message: "conversation history is supplied by the client"},
		},
	}
}
