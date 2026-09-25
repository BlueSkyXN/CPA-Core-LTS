package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	copilotDeviceScope        = "read:user"
	copilotDeviceFallbackURL  = "https://github.com/login/device"
	copilotDeviceGrantType    = "urn:ietf:params:oauth:grant-type:device_code"
	copilotDeviceIntervalBase = 5 * time.Second
	// GitHub 在 slow_down 时要求把轮询间隔至少加 5 秒。
	copilotDeviceSlowDownStep = 5 * time.Second
	// 过期后保留一段时间供 login-info 查询失败原因，之后由 TTL 清理。
	copilotLoginFlowRetention = 10 * time.Minute
)

type copilotLoginFlow struct {
	State           string
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        time.Duration
	ExpiresAt       time.Time
	NextPoll        time.Time
}

func (r *pluginRuntime) startLoginRequest(raw []byte) (pluginapi.AuthLoginStartResponse, error) {
	var req rpcAuthLoginStartRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("invalid_login", "Copilot login start request is invalid", http.StatusBadRequest, false)
	}
	if r.caller == nil || strings.TrimSpace(req.HostCallbackID) == "" {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("auth_unavailable", "Copilot login requires a host callback context", http.StatusServiceUnavailable, true)
	}
	cfg := r.loadedConfig()
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID, Method: http.MethodPost,
		URL:     cfg.GitHubEndpoint + "/login/device/code",
		Headers: copilotIdentityHeaders(cfg), Body: mustMarshalJSON(map[string]string{"client_id": cfg.OAuthClientID, "scope": copilotDeviceScope}),
	})
	if errRequest != nil {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("auth_unavailable", "Copilot device login request failed", http.StatusBadGateway, true)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.AuthLoginStartResponse{}, copilotUpstreamError(response.StatusCode, response.Body)
	}
	if len(response.Body) == 0 || len(response.Body) > maxCopilotAccountBody {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("auth_invalid_response", "Copilot device login response is invalid", http.StatusBadGateway, false)
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var value map[string]any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("auth_invalid_response", "Copilot device login response is not JSON", http.StatusBadGateway, false)
	}
	deviceCode := stringValueFromMap(value, "device_code")
	userCode := stringValueFromMap(value, "user_code")
	if deviceCode == "" || userCode == "" {
		return pluginapi.AuthLoginStartResponse{}, newPluginCallError("auth_invalid_response", "Copilot device login response is missing codes", http.StatusBadGateway, false)
	}
	state, errState := newCopilotLoginState()
	if errState != nil {
		return pluginapi.AuthLoginStartResponse{}, errState
	}
	interval := copilotDeviceIntervalBase
	if seconds := numberValueFromMap(value, "interval"); seconds > 0 {
		interval = time.Duration(seconds * float64(time.Second))
	}
	expiresIn := 15 * time.Minute
	if seconds := numberValueFromMap(value, "expires_in"); seconds > 0 {
		expiresIn = time.Duration(seconds * float64(time.Second))
	}
	verificationURI := stringValueFromMap(value, "verification_uri")
	if verificationURI == "" {
		verificationURI = copilotDeviceFallbackURL
	}
	now := time.Now()
	flow := &copilotLoginFlow{
		State: state, DeviceCode: deviceCode, UserCode: userCode, VerificationURI: verificationURI,
		Interval: interval, ExpiresAt: now.Add(expiresIn), NextPoll: now.Add(interval),
	}
	r.mu.Lock()
	r.purgeLoginFlowsLocked(now)
	r.loginFlows[state] = flow
	r.mu.Unlock()
	return pluginapi.AuthLoginStartResponse{
		Provider: pluginIdentifier, URL: verificationURI, State: state, ExpiresAt: flow.ExpiresAt.UTC(),
		Metadata: map[string]any{"user_code": userCode, "interval_seconds": int64(interval / time.Second)},
	}, nil
}

func (r *pluginRuntime) pollLoginRequest(raw []byte) (pluginapi.AuthLoginPollResponse, error) {
	var req rpcAuthLoginPollRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.AuthLoginPollResponse{}, newPluginCallError("invalid_login", "Copilot login poll request is invalid", http.StatusBadRequest, false)
	}
	if r.caller == nil || strings.TrimSpace(req.HostCallbackID) == "" {
		return pluginapi.AuthLoginPollResponse{}, newPluginCallError("auth_unavailable", "Copilot login poll requires a host callback context", http.StatusServiceUnavailable, true)
	}
	state := strings.TrimSpace(req.State)
	now := time.Now()
	r.mu.Lock()
	r.purgeLoginFlowsLocked(now)
	flow := r.loginFlows[state]
	if flow == nil {
		r.mu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Copilot login flow not found or expired"}, nil
	}
	if now.After(flow.ExpiresAt) {
		// 已持锁，直接删除；removeLoginFlow 会再次加锁导致死锁。
		delete(r.loginFlows, state)
		r.mu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Copilot login flow expired"}, nil
	}
	deviceCode := flow.DeviceCode
	// Interval/NextPoll 会被 slow_down 分支在锁内改写，读取与推进都必须持锁。
	// 间隔未到直接返回 pending，不打上游。
	if now.Before(flow.NextPoll) {
		r.mu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for GitHub device authorization"}, nil
	}
	// 在锁内原子占用本轮轮询窗口：同一 state 的并发 poll 只有一个能通过闸门，
	// 且每个窗口之后都重新按 Interval 计时，而不是只在首个窗口生效。
	flow.NextPoll = now.Add(flow.Interval)
	r.mu.Unlock()
	cfg := r.loadedConfig()
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID, Method: http.MethodPost,
		URL:     cfg.GitHubEndpoint + "/login/oauth/access_token",
		Headers: copilotIdentityHeaders(cfg), Body: mustMarshalJSON(map[string]string{
			"client_id": cfg.OAuthClientID, "device_code": deviceCode, "grant_type": copilotDeviceGrantType,
		}),
	})
	if errRequest != nil {
		return pluginapi.AuthLoginPollResponse{}, newPluginCallError("auth_unavailable", "Copilot login poll request failed", http.StatusBadGateway, true)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.AuthLoginPollResponse{}, copilotUpstreamError(response.StatusCode, response.Body)
	}
	if len(response.Body) == 0 || len(response.Body) > maxCopilotAccountBody {
		return pluginapi.AuthLoginPollResponse{}, newPluginCallError("auth_invalid_response", "Copilot login poll response is invalid", http.StatusBadGateway, false)
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var value map[string]any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return pluginapi.AuthLoginPollResponse{}, newPluginCallError("auth_invalid_response", "Copilot login poll response is not JSON", http.StatusBadGateway, false)
	}
	if accessToken := stringValueFromMap(value, "access_token"); accessToken != "" {
		return r.completeCopilotLogin(req, flow, accessToken)
	}
	switch stringValueFromMap(value, "error") {
	case "authorization_pending":
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for GitHub device authorization"}, nil
	case "slow_down":
		r.mu.Lock()
		if stored := r.loginFlows[state]; stored != nil {
			stored.Interval += copilotDeviceSlowDownStep
			stored.NextPoll = time.Now().Add(stored.Interval)
		}
		r.mu.Unlock()
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "GitHub asked to slow down polling"}, nil
	case "access_denied":
		r.removeLoginFlow(state)
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Copilot login was denied"}, nil
	case "expired_token":
		r.removeLoginFlow(state)
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Copilot login flow expired"}, nil
	default:
		// 未知响应按仍等待处理，最终由 expires_at 兜底退出。
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "waiting for GitHub device authorization"}, nil
	}
}

// completeCopilotLogin 拉 GitHub 登录名做 label，并把长期层凭据封装成 auth 文件形状。
func (r *pluginRuntime) completeCopilotLogin(req rpcAuthLoginPollRequest, flow *copilotLoginFlow, accessToken string) (pluginapi.AuthLoginPollResponse, error) {
	cfg := r.loadedConfig()
	label := ""
	headers := copilotIdentityHeaders(cfg)
	headers.Set("Authorization", "token "+accessToken)
	if response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID, Method: http.MethodGet,
		URL: cfg.GitHubAPIEndpoint + "/user", Headers: headers,
	}); errRequest == nil && response.StatusCode == http.StatusOK && len(response.Body) > 0 && len(response.Body) <= maxCopilotAccountBody {
		decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
		decoder.UseNumber()
		var user map[string]any
		if decoder.Decode(&user) == nil {
			label = stringValueFromMap(user, "login")
		}
	}
	if label == "" {
		label = "GitHub Copilot"
	}
	storage := mustMarshalJSON(map[string]string{
		"type": pluginIdentifier, "auth_mode": "oauth", "github_token": accessToken, "label": label,
	})
	// Core 落盘按 FileName 派生 auth 文件与运行时 ID；同账号重复登录覆盖同一文件。
	fileName := copilotAuthFileName(label)
	r.removeLoginFlow(flow.State)
	return pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess, Message: "Copilot login completed",
		Auth: pluginapi.AuthData{
			Provider: pluginIdentifier, Label: label, FileName: fileName, StorageJSON: storage,
			Metadata:   map[string]any{"type": pluginIdentifier, "auth_mode": "oauth"},
			Attributes: map[string]string{"auth_mode": "oauth", "multi_account": "supported"},
		},
	}, nil
}

// copilotAuthFileName 用 GitHub 登录名生成安全的 auth 文件名；兜底为固定名。
func copilotAuthFileName(login string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return -1
	}, strings.TrimSpace(login))
	if safe == "" || safe == "." || safe == ".." {
		return "copilot-login.json"
	}
	if len(safe) > 64 {
		safe = safe[:64]
	}
	return "copilot-" + strings.ToLower(safe) + ".json"
}

func (r *pluginRuntime) removeLoginFlow(state string) {
	r.mu.Lock()
	delete(r.loginFlows, state)
	r.mu.Unlock()
}

func (r *pluginRuntime) purgeLoginFlowsLocked(now time.Time) {
	for state, flow := range r.loginFlows {
		if now.After(flow.ExpiresAt.Add(copilotLoginFlowRetention)) {
			delete(r.loginFlows, state)
		}
	}
}

// loginFlowSnapshot 提取 management login-info 所需字段；device_code 绝不外发。
func (r *pluginRuntime) loginFlowSnapshot(state string) (map[string]any, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeLoginFlowsLocked(now)
	flow := r.loginFlows[strings.TrimSpace(state)]
	if flow == nil || now.After(flow.ExpiresAt) {
		return nil, false
	}
	// Interval 会被 poll 路径在锁内改写，快照字段必须持锁读取。
	return map[string]any{
		"provider":         pluginIdentifier,
		"verification_uri": flow.VerificationURI,
		"user_code":        flow.UserCode,
		"expires_at":       flow.ExpiresAt.UTC().Format(time.RFC3339),
		"interval_seconds": int64(flow.Interval / time.Second),
	}, true
}

func newCopilotLoginState() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", newPluginCallError("login_state_unavailable", "Copilot login state generation failed", http.StatusInternalServerError, false)
	}
	return hex.EncodeToString(buffer), nil
}
