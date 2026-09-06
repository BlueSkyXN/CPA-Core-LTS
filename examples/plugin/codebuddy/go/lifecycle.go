package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const shutdownWait = 2 * time.Second

type pluginRuntime struct {
	mu           sync.Mutex
	caller       hostCaller
	config       pluginConfig
	accepting    bool
	active       map[string]*activeExecution
	catalogCache map[string]codeBuddyCatalogCacheEntry
	summaryCache map[string]codeBuddySummaryCacheEntry
}

type activeExecution struct {
	mu               sync.Mutex
	requestID        string
	pluginStreamID   string
	upstreamStreamID string
	cancelRequested  bool
	upstreamClosed   bool
	terminalOnce     sync.Once
	doneOnce         sync.Once
	done             chan struct{}
}

func newPluginRuntime(caller hostCaller) *pluginRuntime {
	return &pluginRuntime{
		caller:       caller,
		config:       defaultPluginConfig(),
		accepting:    true,
		active:       make(map[string]*activeExecution),
		catalogCache: make(map[string]codeBuddyCatalogCacheEntry),
		summaryCache: make(map[string]codeBuddySummaryCacheEntry),
	}
}

func (r *pluginRuntime) configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return newPluginCallError("invalid_config", "CodeBuddy plugin configuration is invalid", http.StatusBadRequest, false)
		}
	}
	cfg, errConfig := decodePluginConfig(req.ConfigYAML)
	if errConfig != nil {
		return newPluginCallError("invalid_config", errConfig.Error(), http.StatusBadRequest, false)
	}
	r.mu.Lock()
	r.config = cfg
	r.catalogCache = make(map[string]codeBuddyCatalogCacheEntry)
	r.summaryCache = make(map[string]codeBuddySummaryCacheEntry)
	r.accepting = true
	r.mu.Unlock()
	return nil
}

func (r *pluginRuntime) loadedConfig() pluginConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config
}

func (r *pluginRuntime) registerExecution(requestID, streamID string) (*activeExecution, error) {
	requestID = strings.TrimSpace(requestID)
	streamID = strings.TrimSpace(streamID)
	if requestID == "" || streamID == "" {
		return nil, newPluginCallError("invalid_stream", "CodeBuddy stream requires request_id and stream_id", http.StatusBadRequest, false)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return nil, newPluginCallError("plugin_quiescing", "CodeBuddy plugin is quiescing", http.StatusServiceUnavailable, true)
	}
	if _, exists := r.active[requestID]; exists {
		return nil, newPluginCallError("duplicate_request", "CodeBuddy request is already active", http.StatusConflict, false)
	}
	execution := &activeExecution{requestID: requestID, pluginStreamID: streamID, done: make(chan struct{})}
	r.active[requestID] = execution
	return execution, nil
}

func (r *pluginRuntime) releaseExecution(execution *activeExecution) {
	if execution == nil {
		return
	}
	r.mu.Lock()
	if current := r.active[execution.requestID]; current == execution {
		delete(r.active, execution.requestID)
	}
	r.mu.Unlock()
}

func (r *pluginRuntime) cancel(requestID string) {
	r.mu.Lock()
	execution := r.active[strings.TrimSpace(requestID)]
	r.mu.Unlock()
	if execution != nil {
		execution.cancel(r.caller)
	}
}

func (execution *activeExecution) bindUpstream(caller hostCaller, streamID string) bool {
	execution.mu.Lock()
	execution.upstreamStreamID = strings.TrimSpace(streamID)
	shouldClose := execution.cancelRequested && !execution.upstreamClosed && execution.upstreamStreamID != ""
	if shouldClose {
		execution.upstreamClosed = true
	}
	execution.mu.Unlock()
	if shouldClose {
		closeHostHTTPStream(caller, streamID)
	}
	return shouldClose
}

func (execution *activeExecution) cancel(caller hostCaller) {
	execution.mu.Lock()
	execution.cancelRequested = true
	streamID := execution.upstreamStreamID
	shouldClose := streamID != "" && !execution.upstreamClosed
	if shouldClose {
		execution.upstreamClosed = true
	}
	execution.mu.Unlock()
	if shouldClose {
		closeHostHTTPStream(caller, streamID)
	}
}

func (execution *activeExecution) closeUpstream(caller hostCaller) {
	execution.mu.Lock()
	streamID := execution.upstreamStreamID
	shouldClose := streamID != "" && !execution.upstreamClosed
	if shouldClose {
		execution.upstreamClosed = true
	}
	execution.mu.Unlock()
	if shouldClose {
		closeHostHTTPStream(caller, streamID)
	}
}

func (execution *activeExecution) canceled() bool {
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return execution.cancelRequested
}

func (execution *activeExecution) upstreamID() string {
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return execution.upstreamStreamID
}

func (execution *activeExecution) finish(caller hostCaller, errorMessage, errorCode string, retryable bool, httpStatus int) {
	execution.terminalOnce.Do(func() {
		closePluginStream(caller, execution.pluginStreamID, errorMessage, errorCode, retryable, httpStatus)
	})
	execution.signalDone()
}

func (execution *activeExecution) signalDone() {
	execution.doneOnce.Do(func() { close(execution.done) })
}

func (r *pluginRuntime) quiesce() {
	r.mu.Lock()
	r.accepting = false
	active := make([]*activeExecution, 0, len(r.active))
	for _, execution := range r.active {
		active = append(active, execution)
	}
	r.mu.Unlock()
	for _, execution := range active {
		execution.cancel(r.caller)
	}
}

func (r *pluginRuntime) activeExecutions() []*activeExecution {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := make([]*activeExecution, 0, len(r.active))
	for _, execution := range r.active {
		active = append(active, execution)
	}
	return active
}

func waitForExecutions(active []*activeExecution) {
	if len(active) == 0 {
		return
	}
	deadline := time.NewTimer(shutdownWait)
	defer deadline.Stop()
	for _, execution := range active {
		select {
		case <-execution.done:
		case <-deadline.C:
			return
		}
	}
}

func (r *pluginRuntime) quiesceAndWait() {
	r.quiesce()
	waitForExecutions(r.activeExecutions())
}

func (r *pluginRuntime) shutdown() {
	r.quiesceAndWait()
	r.mu.Lock()
	r.catalogCache = make(map[string]codeBuddyCatalogCacheEntry)
	r.summaryCache = make(map[string]codeBuddySummaryCacheEntry)
	r.mu.Unlock()
}

func (r *pluginRuntime) readiness(req pluginapi.ReadinessRequest) pluginapi.ReadinessResponse {
	cfg := r.loadedConfig()
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	protocolState := pluginapi.ReadinessStateReady
	protocolMessage := "direct HTTPS streaming protocol is configured"
	if !accepting {
		protocolState = pluginapi.ReadinessStateNotReady
		protocolMessage = "plugin is quiescing"
	} else if errEndpoint := validateEndpoint(cfg.Endpoint); errEndpoint != nil {
		protocolState = pluginapi.ReadinessStateNotReady
		protocolMessage = "endpoint configuration is invalid"
	}
	authState := pluginapi.ReadinessStateUnknown
	authMessage := "selected credential was not supplied"
	if len(req.StorageJSON) > 0 {
		if _, errAuth := parseStoredAuth(req.StorageJSON); errAuth != nil {
			authState = pluginapi.ReadinessStateNotReady
			authMessage = "selected CodeBuddy credential is invalid"
		} else {
			authState = pluginapi.ReadinessStateReady
			authMessage = "selected CodeBuddy credential is configured"
		}
	}
	checks := []pluginapi.ReadinessCheck{
		{Level: pluginapi.ReadinessLevelPluginInstalled, State: pluginapi.ReadinessStateReady, Version: pluginVersion},
		{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Version: "direct-https", Message: "external runner is not required for CodeBuddy G1"},
		{Level: pluginapi.ReadinessLevelProtocolReady, State: protocolState, Message: protocolMessage},
		{Level: pluginapi.ReadinessLevelAuthReady, State: authState, Message: authMessage},
		{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnsupported, Message: "CodeBuddy G1 does not expose native sessions"},
	}
	ready := protocolState == pluginapi.ReadinessStateReady
	if strings.TrimSpace(req.AuthID) != "" || strings.TrimSpace(req.AuthIndex) != "" || len(req.StorageJSON) > 0 {
		ready = ready && authState == pluginapi.ReadinessStateReady
	}
	return pluginapi.ReadinessResponse{
		Provider:     pluginIdentifier,
		Ready:        ready,
		Generation:   "g1-direct-https",
		Capabilities: []string{"chat_completions", "stream", "cancel"},
		Checks:       checks,
	}
}
