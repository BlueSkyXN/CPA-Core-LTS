package main

import (
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
	summaryCache   map[string]qoderSummaryCacheEntry
	tokenCache     map[string]qoderTokenCacheEntry
	nativeActive   map[string]*nativeExecution
	nativeSessions map[string]*nativeExecution
	tokenFlights   map[string]*tokenFlight
	catalogFlights map[string]*catalogFlight
	nativeCatalogs map[string]nativeCatalogEntry
	generation     uint64
}

// executionIdentity 唯一归属一次原生执行的所有权维度，用于 cancel/close 匹配。
type executionIdentity struct {
	key                string
	authID             string
	authIndex          string
	executionSessionID string
	callerScope        string
	workspaceIdentity  string
}

func newPluginRuntime(caller hostCaller) *pluginRuntime {
	return &pluginRuntime{
		caller: caller, config: defaultPluginConfig(), accepting: true,
		summaryCache: make(map[string]qoderSummaryCacheEntry),
		tokenCache:   make(map[string]qoderTokenCacheEntry),
		nativeActive: make(map[string]*nativeExecution), nativeSessions: make(map[string]*nativeExecution),
		tokenFlights: make(map[string]*tokenFlight), catalogFlights: make(map[string]*catalogFlight),
		nativeCatalogs: make(map[string]nativeCatalogEntry),
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
	r.summaryCache = make(map[string]qoderSummaryCacheEntry)
	r.tokenCache = make(map[string]qoderTokenCacheEntry)
	r.nativeCatalogs = make(map[string]nativeCatalogEntry)
	r.generation++
	r.accepting = true
	r.mu.Unlock()
	return nil
}

func (r *pluginRuntime) loadedConfig() pluginConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config
}

func (r *pluginRuntime) cancelExecution(req pluginapi.CancelExecutionRequest) error {
	r.cancelNative(req)
	return nil
}

func cancelMatches(identity *executionIdentity, req pluginapi.CancelExecutionRequest) bool {
	if identity == nil {
		return false
	}
	if value := strings.TrimSpace(req.ExecutionSessionID); value != "" && value != identity.executionSessionID {
		return false
	}
	if value := strings.TrimSpace(req.CallerScope); value != "" && value != identity.callerScope {
		return false
	}
	if value := strings.TrimSpace(req.WorkspaceIdentity); value != "" && value != identity.workspaceIdentity {
		return false
	}
	if value := strings.TrimSpace(req.Provider); value != "" && value != pluginIdentifier {
		return false
	}
	if value := strings.TrimSpace(req.AuthID); value != "" && value != identity.authID {
		return false
	}
	if value := strings.TrimSpace(req.AuthIndex); value != "" && value != identity.authIndex {
		return false
	}
	return true
}

func (r *pluginRuntime) closeExecutionSessions(req pluginapi.CloseExecutionSessionRequest) error {
	r.closeNative(req)
	return nil
}

func closeMatches(identity *executionIdentity, req pluginapi.CloseExecutionSessionRequest) bool {
	switch req.Scope {
	case pluginapi.ExecutionSessionCloseScopeSession:
		return identity.executionSessionID == strings.TrimSpace(req.ExecutionSessionID) &&
			(req.CallerScope == "" || identity.callerScope == req.CallerScope) &&
			(req.WorkspaceIdentity == "" || identity.workspaceIdentity == req.WorkspaceIdentity)
	case pluginapi.ExecutionSessionCloseScopeAuth:
		if req.AuthID == "" && req.AuthIndex == "" {
			return false
		}
		return (req.AuthID == "" || identity.authID == req.AuthID) &&
			(req.AuthIndex == "" || identity.authIndex == req.AuthIndex)
	case pluginapi.ExecutionSessionCloseScopeProvider:
		return req.Provider == "" || req.Provider == pluginIdentifier
	default:
		return false
	}
}

func (r *pluginRuntime) readiness(req pluginapi.ReadinessRequest) pluginapi.ReadinessResponse {
	return r.nativeReadiness(req, r.loadedConfig())
}

func (r *pluginRuntime) quiesce() {
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
	r.closeNative(pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeProvider})
}

func (r *pluginRuntime) shutdown() {
	r.quiesce()
	r.waitNative()
	r.mu.Lock()
	r.summaryCache = make(map[string]qoderSummaryCacheEntry)
	r.tokenCache = make(map[string]qoderTokenCacheEntry)
	r.mu.Unlock()
}

func effectiveExecutionSessionID(req pluginapi.ExecutorRequest) string {
	if value := strings.TrimSpace(req.ExecutionSessionID); value != "" {
		return value
	}
	return "request-" + strings.TrimSpace(req.RequestID)
}

func executionSessionKey(req pluginapi.ExecutorRequest, auth qoderAuth) string {
	return sessionDigest([]string{
		pluginIdentifier, strings.TrimSpace(req.AuthID), strings.TrimSpace(req.AuthIndex), effectiveExecutionSessionID(req),
		strings.TrimSpace(req.CallerScope), strings.TrimSpace(req.WorkspaceIdentity), auth.tokenSource(),
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
