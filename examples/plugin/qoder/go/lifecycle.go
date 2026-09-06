package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const shutdownWait = 3 * time.Second

type pluginRuntime struct {
	mu             sync.Mutex
	caller         hostCaller
	config         pluginConfig
	accepting      bool
	sessions       map[string]*runnerSession
	requestSession map[string]*runnerSession
	modelCache     map[string]cachedModels
	summaryCache   map[string]qoderSummaryCacheEntry
	tokenCache     map[string]qoderTokenCacheEntry
	runnerExtraEnv map[string]string
}

type runnerSession struct {
	key                string
	client             *runnerClient
	authID             string
	authIndex          string
	executionSessionID string
	callerScope        string
	workspaceIdentity  string
	currentRequestID   string
	ephemeral          bool
}

type runnerReadiness struct {
	Ready  bool                       `json:"ready"`
	Checks []pluginapi.ReadinessCheck `json:"checks"`
}

func newPluginRuntime(caller hostCaller) *pluginRuntime {
	return &pluginRuntime{
		caller: caller, config: defaultPluginConfig(), accepting: true,
		sessions: make(map[string]*runnerSession), requestSession: make(map[string]*runnerSession),
		modelCache:   make(map[string]cachedModels),
		summaryCache: make(map[string]qoderSummaryCacheEntry),
		tokenCache:   make(map[string]qoderTokenCacheEntry),
	}
}

func (r *pluginRuntime) configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return newPluginCallError("invalid_config", "Qoder plugin configuration is invalid", http.StatusBadRequest, false)
		}
	}
	cfg, errConfig := decodePluginConfig(req.ConfigYAML)
	if errConfig != nil {
		return newPluginCallError("invalid_config", errConfig.Error(), http.StatusBadRequest, false)
	}
	r.mu.Lock()
	r.config = cfg
	r.modelCache = make(map[string]cachedModels)
	r.summaryCache = make(map[string]qoderSummaryCacheEntry)
	r.tokenCache = make(map[string]qoderTokenCacheEntry)
	r.accepting = true
	r.mu.Unlock()
	return nil
}

func (r *pluginRuntime) loadedConfig() pluginConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config
}

func (r *pluginRuntime) startRunner(ctx context.Context, auth qoderAuth, requestedTransport ...string) (*runnerClient, error) {
	cfg := r.loadedConfig()
	transport := r.transportForAuth(auth, requestedTransport...)
	if transport == "sdk_cli" && cfg.QoderCLIPath == "" {
		return nil, newPluginCallError("cli_path_required", "qoder_cli_path must explicitly select an external Qoder CLI", http.StatusServiceUnavailable, false)
	}
	if transport == "direct_openai" && cfg.DirectEndpoint == "" {
		return nil, newPluginCallError("direct_endpoint_required", "direct_endpoint is required for direct_openai transport", http.StatusServiceUnavailable, false)
	}
	r.mu.Lock()
	extra := cloneStringMap(r.runnerExtraEnv)
	r.mu.Unlock()
	client, errStart := newRunnerClient(cfg, auth, extra, transport)
	if errStart != nil {
		return nil, errStart
	}
	if _, errHandshake := client.handshake(ctx, transport); errHandshake != nil {
		client.shutdown()
		return nil, errHandshake
	}
	return client, nil
}

func (r *pluginRuntime) transportForAuth(auth qoderAuth, requestedTransport ...string) string {
	if len(requestedTransport) > 0 && strings.TrimSpace(requestedTransport[0]) != "" {
		return strings.ToLower(strings.TrimSpace(requestedTransport[0]))
	}
	if auth.Transport != "" {
		return auth.Transport
	}
	cfg := r.loadedConfig()
	if cfg.Transport == "" {
		return "sdk_cli"
	}
	return cfg.Transport
}

func (r *pluginRuntime) acquireSession(ctx context.Context, req pluginapi.ExecutorRequest, auth qoderAuth) (*runnerSession, error) {
	transport := r.transportForAuth(auth)
	key := executionSessionKey(req, auth, transport)
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return nil, newPluginCallError("plugin_quiescing", "Qoder plugin is quiescing", http.StatusServiceUnavailable, true)
	}
	if session := r.sessions[key]; session != nil {
		if session.client.ended() {
			delete(r.sessions, key)
			if session.currentRequestID != "" {
				delete(r.requestSession, session.currentRequestID)
			}
		} else {
			if session.currentRequestID != "" {
				r.mu.Unlock()
				return nil, newPluginCallError("turn_conflict", "Qoder execution session already has an active turn", http.StatusConflict, true)
			}
			session.currentRequestID = req.RequestID
			r.requestSession[req.RequestID] = session
			r.mu.Unlock()
			return session, nil
		}
	}
	r.mu.Unlock()

	client, errStart := r.startRunner(ctx, auth, transport)
	if errStart != nil {
		return nil, errStart
	}
	session := &runnerSession{
		key: key, client: client, authID: req.AuthID, authIndex: req.AuthIndex,
		executionSessionID: effectiveExecutionSessionID(req), callerScope: req.CallerScope,
		workspaceIdentity: req.WorkspaceIdentity, currentRequestID: req.RequestID,
		ephemeral: strings.TrimSpace(req.ExecutionSessionID) == "",
	}
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		client.shutdown()
		return nil, newPluginCallError("plugin_quiescing", "Qoder plugin is quiescing", http.StatusServiceUnavailable, true)
	}
	if existing := r.sessions[key]; existing != nil {
		r.mu.Unlock()
		client.shutdown()
		return r.acquireSession(ctx, req, auth)
	}
	r.sessions[key] = session
	r.requestSession[req.RequestID] = session
	r.mu.Unlock()
	return session, nil
}

func (r *pluginRuntime) completeTurn(session *runnerSession, requestID string) {
	if session == nil {
		return
	}
	shouldClose := false
	r.mu.Lock()
	if session.currentRequestID == requestID {
		session.currentRequestID = ""
	}
	delete(r.requestSession, requestID)
	if session.ephemeral {
		delete(r.sessions, session.key)
		shouldClose = true
	}
	r.mu.Unlock()
	if shouldClose {
		session.client.shutdown()
	}
}

func (r *pluginRuntime) cancelExecution(req pluginapi.CancelExecutionRequest) error {
	r.mu.Lock()
	session := r.requestSession[strings.TrimSpace(req.RequestID)]
	r.mu.Unlock()
	if session == nil || !cancelMatches(session, req) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.loadedConfig().RequestTimeout)
	defer cancel()
	return session.client.call(ctx, "cancel", map[string]any{
		"request_id": req.RequestID, "execution_session_id": session.executionSessionID,
	}, nil)
}

func cancelMatches(session *runnerSession, req pluginapi.CancelExecutionRequest) bool {
	if session == nil {
		return false
	}
	if value := strings.TrimSpace(req.ExecutionSessionID); value != "" && value != session.executionSessionID {
		return false
	}
	if value := strings.TrimSpace(req.CallerScope); value != "" && value != session.callerScope {
		return false
	}
	if value := strings.TrimSpace(req.WorkspaceIdentity); value != "" && value != session.workspaceIdentity {
		return false
	}
	if value := strings.TrimSpace(req.Provider); value != "" && value != pluginIdentifier {
		return false
	}
	if value := strings.TrimSpace(req.AuthID); value != "" && value != session.authID {
		return false
	}
	if value := strings.TrimSpace(req.AuthIndex); value != "" && value != session.authIndex {
		return false
	}
	return true
}

func (r *pluginRuntime) closeExecutionSessions(req pluginapi.CloseExecutionSessionRequest) error {
	r.mu.Lock()
	var selected []*runnerSession
	for key, session := range r.sessions {
		if closeMatches(session, req) {
			selected = append(selected, session)
			delete(r.sessions, key)
			if session.currentRequestID != "" {
				delete(r.requestSession, session.currentRequestID)
			}
		}
	}
	r.mu.Unlock()
	for _, session := range selected {
		ctx, cancel := context.WithTimeout(context.Background(), r.loadedConfig().RequestTimeout)
		_ = session.client.call(ctx, "close", map[string]any{"execution_session_id": session.executionSessionID}, nil)
		cancel()
		session.client.shutdown()
	}
	return nil
}

func closeMatches(session *runnerSession, req pluginapi.CloseExecutionSessionRequest) bool {
	switch req.Scope {
	case pluginapi.ExecutionSessionCloseScopeSession:
		return session.executionSessionID == strings.TrimSpace(req.ExecutionSessionID) &&
			(req.CallerScope == "" || session.callerScope == req.CallerScope) &&
			(req.WorkspaceIdentity == "" || session.workspaceIdentity == req.WorkspaceIdentity)
	case pluginapi.ExecutionSessionCloseScopeAuth:
		if req.AuthID == "" && req.AuthIndex == "" {
			return false
		}
		return (req.AuthID == "" || session.authID == req.AuthID) &&
			(req.AuthIndex == "" || session.authIndex == req.AuthIndex)
	case pluginapi.ExecutionSessionCloseScopeProvider:
		return req.Provider == "" || req.Provider == pluginIdentifier
	default:
		return false
	}
}

func (r *pluginRuntime) readiness(req pluginapi.ReadinessRequest) pluginapi.ReadinessResponse {
	cfg := r.loadedConfig()
	checks := []pluginapi.ReadinessCheck{{Level: pluginapi.ReadinessLevelPluginInstalled, State: pluginapi.ReadinessStateReady, Version: pluginVersion}}
	if cfg.QoderCLIPath == "" && readinessTransport(cfg, req) == "sdk_cli" {
		checks = append(checks,
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateUnknown, Message: "runner command is configured but was not started"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateNotReady, Message: "qoder_cli_path is required"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateUnknown, Message: "protocol is not ready"},
		)
		return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: false, Generation: pluginVersion, Checks: checks}
	}
	if len(req.StorageJSON) == 0 {
		if cfg.Transport == "direct_openai" {
			protocolState := pluginapi.ReadinessStateReady
			protocolMessage := "direct endpoint configuration is present; selected auth is required for remote checks"
			if cfg.DirectEndpoint == "" {
				protocolState = pluginapi.ReadinessStateNotReady
				protocolMessage = "direct_endpoint is required for direct_openai"
			}
			checks = append(checks,
				pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Version: "direct-openai"},
				pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: protocolState, Message: protocolMessage},
				pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateUnknown, Message: "selected credential was not supplied"},
			)
			return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: req.Purpose != pluginapi.ReadinessPurposeAdmission && protocolState == pluginapi.ReadinessStateReady, Generation: pluginVersion, Capabilities: []string{"chat_completions", "stream", "direct_openai"}, Checks: checks}
		}
		checks = append(checks,
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Message: "runner command and explicit CLI path are configured"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateUnknown, Message: "runner handshake requires a selected auth-local process"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateUnknown, Message: "selected credential was not supplied"},
		)
		return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: req.Purpose != pluginapi.ReadinessPurposeAdmission, Generation: pluginVersion, Checks: checks}
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		checks = append(checks,
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateUnknown},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateUnknown},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateNotReady, Message: "selected Qoder credential is invalid"},
		)
		return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: false, Generation: pluginVersion, Checks: checks}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()
	transport := r.transportForAuth(auth)
	client, errStart := r.startRunner(ctx, auth, transport)
	if errStart != nil {
		checks = append(checks,
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateNotReady, Message: "Qoder runner could not be started"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateNotReady, Message: "Qoder runner handshake failed"},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateUnknown},
		)
		return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: false, Generation: pluginVersion, Checks: checks}
	}
	defer client.shutdown()
	var state runnerReadiness
	errProbe := client.call(ctx, "readiness", map[string]any{"auth": auth.runnerAuth(transport)}, &state)
	if errProbe != nil {
		checks = append(checks,
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady, Version: strconv.Itoa(runnerProtocol)},
			pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateNotReady, Message: "Qoder auth readiness failed"},
		)
		return pluginapi.ReadinessResponse{Provider: pluginIdentifier, Ready: false, Generation: pluginVersion, Checks: checks}
	}
	checks = append(checks, state.Checks...)
	capabilities := []string{"chat_completions", "stream", "sessions", "cancel", "close", "fixed_permissions"}
	if transport == "direct_openai" {
		capabilities = []string{"chat_completions", "stream", "cancel", "close", "direct_openai", "client_tools"}
	} else {
		checks = append(checks, pluginapi.ReadinessCheck{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnknown, Message: "session is created by executor start"})
	}
	return pluginapi.ReadinessResponse{
		Provider: pluginIdentifier, Ready: state.Ready, Generation: pluginVersion,
		Capabilities: capabilities, Checks: checks,
	}
}

func readinessTransport(cfg pluginConfig, req pluginapi.ReadinessRequest) string {
	if len(req.StorageJSON) > 0 {
		var auth qoderAuth
		if json.Unmarshal(req.StorageJSON, &auth) == nil && strings.EqualFold(strings.TrimSpace(auth.Transport), "direct_openai") {
			return "direct_openai"
		}
	}
	if cfg.Transport == "direct_openai" {
		return "direct_openai"
	}
	return "sdk_cli"
}

func (r *pluginRuntime) quiesce() {
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
}

func (r *pluginRuntime) shutdown() {
	r.quiesce()
	r.mu.Lock()
	sessions := make([]*runnerSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.sessions = make(map[string]*runnerSession)
	r.requestSession = make(map[string]*runnerSession)
	r.modelCache = make(map[string]cachedModels)
	r.summaryCache = make(map[string]qoderSummaryCacheEntry)
	r.tokenCache = make(map[string]qoderTokenCacheEntry)
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, session := range sessions {
			session.client.shutdown()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
	}
}

func effectiveExecutionSessionID(req pluginapi.ExecutorRequest) string {
	if value := strings.TrimSpace(req.ExecutionSessionID); value != "" {
		return value
	}
	return "request-" + strings.TrimSpace(req.RequestID)
}

func executionSessionKey(req pluginapi.ExecutorRequest, auth qoderAuth, requestedTransport ...string) string {
	transport := auth.Transport
	if len(requestedTransport) > 0 && strings.TrimSpace(requestedTransport[0]) != "" {
		transport = strings.ToLower(strings.TrimSpace(requestedTransport[0]))
	}
	if transport == "" {
		transport = "sdk_cli"
	}
	return sessionDigest([]string{
		pluginIdentifier, strings.TrimSpace(req.AuthID), strings.TrimSpace(req.AuthIndex), effectiveExecutionSessionID(req),
		strings.TrimSpace(req.CallerScope), strings.TrimSpace(req.WorkspaceIdentity), auth.AuthMode, transport, auth.AccountID, auth.tokenSource(), auth.ProfileID, auth.ConfigDir,
	})
}

func sessionDigest(parts []string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(strconv.Itoa(len(part))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
