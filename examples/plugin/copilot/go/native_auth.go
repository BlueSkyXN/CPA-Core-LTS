package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxCopilotAccountBody = 256 * 1024

// copilotIdentityHeaders 是 GitHub/Copilot 面共用的身份串头；数值全部来自配置，
// 缺省值对齐 vscode Copilot Chat 扩展的线上形态。
func copilotIdentityHeaders(cfg pluginConfig) http.Header {
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("Editor-Version", "vscode/"+cfg.EditorVersion)
	headers.Set("Editor-Plugin-Version", cfg.EditorPluginVersion)
	headers.Set("User-Agent", cfg.UserAgent)
	headers.Set("X-Github-Api-Version", cfg.APIVersion)
	headers.Set("Copilot-Integration-Id", "vscode-chat")
	headers.Set("X-Vscode-User-Agent-Library-Version", "electron-fetch")
	return headers
}

func copilotChatHeaders(cfg pluginConfig, token, requestID string) http.Header {
	headers := copilotIdentityHeaders(cfg)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Openai-Intent", "conversation-agent")
	if requestID != "" {
		headers.Set("X-Request-Id", requestID)
	}
	return headers
}

type copilotTokenState struct {
	Source    string
	Token     string
	ExpiresAt time.Time
}

type copilotTokenCacheEntry struct {
	FetchedAt time.Time
	State     copilotTokenState
}

type tokenFlight struct {
	done  chan struct{}
	state copilotTokenState
	err   error
}

func copilotCredentialFingerprint(auth copilotAuth) string {
	sum := sha256.Sum256([]byte(auth.tokenSource()))
	return hex.EncodeToString(sum[:8])
}

func copilotTokenCacheKey(auth copilotAuth, endpoint string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{copilotCredentialFingerprint(auth), endpoint}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// cachedCopilotToken 汇聚同一凭证的目录、summary 和推理换票；不在 runtime 锁内做网络调用。
// rejectedToken 非空表示已在线上被判失效，命中缓存也会强制重换。
func (r *pluginRuntime) cachedCopilotToken(auth copilotAuth, callbackID string, cfg pluginConfig, rejectedToken string) (copilotTokenState, error) {
	if callbackID == "" || r.caller == nil {
		return copilotTokenState{}, newPluginCallError("auth_unavailable", "Copilot token exchange requires a host callback context", http.StatusServiceUnavailable, true)
	}
	r.mu.Lock()
	epoch := r.generation
	key := sessionDigest([]string{copilotTokenCacheKey(auth, cfg.GitHubAPIEndpoint), cfg.CopilotAPIEndpoint, strconv.FormatUint(epoch, 10)})
	cached, exists := r.tokenCache[key]
	if exists && cached.State.ExpiresAt.After(time.Now().Add(30*time.Second)) && (rejectedToken == "" || cached.State.Token != rejectedToken) {
		r.mu.Unlock()
		return cached.State, nil
	}
	if flight := r.tokenFlights[key]; flight != nil {
		r.mu.Unlock()
		<-flight.done
		return flight.state, flight.err
	}
	flight := &tokenFlight{done: make(chan struct{})}
	r.tokenFlights[key] = flight
	r.mu.Unlock()
	state, err := r.copilotTokenRequest(cfg, auth, callbackID)
	if _, typed := err.(*pluginCallError); err != nil && !typed {
		err = newPluginCallError("auth_invalid_response", "Copilot token endpoint returned an invalid response", http.StatusBadGateway, false)
	}
	r.mu.Lock()
	if err == nil && r.generation == epoch {
		r.tokenCache[key] = copilotTokenCacheEntry{FetchedAt: time.Now(), State: state}
	}
	flight.state, flight.err = state, err
	delete(r.tokenFlights, key)
	close(flight.done)
	r.mu.Unlock()
	return state, err
}

// copilotTokenRequest 用长期 GitHub token 换短时 Copilot token（约 30 分钟）。
func (r *pluginRuntime) copilotTokenRequest(cfg pluginConfig, auth copilotAuth, callbackID string) (copilotTokenState, error) {
	headers := copilotIdentityHeaders(cfg)
	headers.Set("Authorization", "token "+auth.tokenSource())
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID, Method: http.MethodGet,
		URL: cfg.GitHubAPIEndpoint + "/copilot_internal/v2/token", Headers: headers,
	})
	if errRequest != nil {
		return copilotTokenState{}, newPluginCallError("auth_unavailable", "Copilot token endpoint connection failed", http.StatusBadGateway, true)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return copilotTokenState{}, copilotUpstreamError(response.StatusCode, response.Body)
	}
	if len(response.Body) == 0 || len(response.Body) > maxCopilotAccountBody {
		return copilotTokenState{}, newPluginCallError("auth_invalid_response", "Copilot token response is invalid", http.StatusBadGateway, false)
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var value map[string]any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return copilotTokenState{}, newPluginCallError("auth_invalid_response", "Copilot token response is not JSON", http.StatusBadGateway, false)
	}
	token := stringValueFromMap(value, "token")
	if token == "" {
		return copilotTokenState{}, newPluginCallError("auth_invalid_response", "Copilot token response has no token", http.StatusBadGateway, false)
	}
	expiresAt := copilotTokenExpiry(value)
	return copilotTokenState{Source: auth.tokenSource(), Token: token, ExpiresAt: expiresAt}, nil
}

// 优先相对 refresh_in，其次绝对 expires_at；上游缺失时按保守的 5 分钟处理。
func copilotTokenExpiry(value map[string]any) time.Time {
	if refreshIn := numberValueFromMap(value, "refresh_in", "refresh_in_seconds"); refreshIn > 0 && refreshIn < 24*60*60 {
		return time.Now().Add(time.Duration(refreshIn * float64(time.Second)))
	}
	if expiresAt := copilotUnixTime(numberValueFromMap(value, "expires_at", "expires_at_timestamp")); !expiresAt.IsZero() {
		return expiresAt
	}
	return time.Now().Add(5 * time.Minute)
}

func copilotUnixTime(value float64) time.Time {
	if value > 100_000_000_000 {
		value /= 1000
	}
	if value <= 0 || value > 10_000_000_000 {
		return time.Time{}
	}
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * 1e9)
	return time.Unix(seconds, nanos).UTC()
}

// copilotUpstreamError 只输出固定文案，绝不回显上游 body，避免携带任何令牌材料。
func copilotUpstreamError(status int, raw []byte) error {
	switch {
	case status == http.StatusUnauthorized:
		return newPluginCallError("auth_expired", "Copilot authentication was rejected", http.StatusUnauthorized, false)
	case status == http.StatusForbidden:
		return newPluginCallError("upstream_forbidden", "Copilot denied this request", http.StatusForbidden, false)
	case status == http.StatusNotFound:
		return newPluginCallError("direct_invalid_request", "Copilot upstream endpoint was not found", http.StatusNotFound, false)
	case status == http.StatusTooManyRequests:
		return newPluginCallError("quota_or_rate_limit", "Copilot rate limited the request", http.StatusTooManyRequests, true)
	case status >= 400 && status < 500:
		return newPluginCallError("direct_invalid_request", "Copilot rejected the request", status, false)
	default:
		return newPluginCallError("direct_upstream_error", "Copilot reported an upstream error", http.StatusBadGateway, true)
	}
}
