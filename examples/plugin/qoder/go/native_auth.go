package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type tokenFlight struct {
	done  chan struct{}
	state qoderTokenState
	err   error
}

// 同一凭证的目录、Summary 和推理共享换票；不在 runtime 锁内做网络调用。
func (r *pluginRuntime) cachedPATToken(auth qoderAuth, callbackID string, cfg pluginConfig, rejectedToken string) (qoderTokenState, error) {
	if callbackID == "" || r.caller == nil {
		return qoderTokenState{}, newPluginCallError("auth_unavailable", "Qoder token exchange requires a host callback context", 503, true)
	}
	r.mu.Lock()
	epoch := r.generation
	key := sessionDigest([]string{qoderTokenCacheKey(auth, cfg.OpenAPIEndpoint), cfg.OpenAPIUserAgent, strconv.FormatUint(epoch, 10)})
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
	source := auth.tokenSource()
	var state qoderTokenState
	var err error
	if exists && cached.State.RefreshToken != "" {
		state, err = r.qoderTokenRequestWithConfig(cfg, source, callbackID, "/api/v1/jobToken/refresh", map[string]string{"refresh_token": cached.State.RefreshToken})
		if err == nil && state.RefreshToken == "" {
			state.RefreshToken = cached.State.RefreshToken
		}
	}
	// 只有没有刷新票据或明确认证拒绝才重新换票；429/5xx/网络故障不放大请求。
	rejected := false
	if callErr, ok := err.(*pluginCallError); ok {
		rejected = callErr.code == "auth_expired"
	}
	if !exists || cached.State.RefreshToken == "" || rejected {
		state, err = r.qoderTokenRequestWithConfig(cfg, source, callbackID, "/api/v1/jobToken/exchange", map[string]string{"personal_token": source})
	}
	if _, typed := err.(*pluginCallError); err != nil && !typed {
		err = newPluginCallError("auth_invalid_response", "Qoder token exchange or refresh returned an invalid response", 502, false)
	}
	r.mu.Lock()
	if err == nil && r.generation == epoch {
		r.tokenCache[key] = qoderTokenCacheEntry{FetchedAt: time.Now(), State: state}
	}
	flight.state, flight.err = state, err
	delete(r.tokenFlights, key)
	close(flight.done)
	r.mu.Unlock()
	return state, err
}

func (r *pluginRuntime) nativeToken(auth qoderAuth, callbackID string, cfg pluginConfig, rejected string) (qoderTokenState, error) {
	if auth.AuthMode == "local_cli" {
		return qoderTokenState{}, newPluginCallError("invalid_auth", "Qoder direct does not use local CLI credentials", 400, false)
	}
	if auth.isPAT() && cfg.DirectTokenMode != "bearer" {
		return r.cachedPATToken(auth, callbackID, cfg, rejected)
	}
	return qoderTokenState{Source: auth.tokenSource(), Token: auth.tokenSource()}, nil
}

func qoderErrorCode(raw []byte) string {
	var obj map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil {
		return ""
	}
	if nested, ok := obj["error"].(map[string]any); ok {
		obj = nested
	}
	for _, key := range []string{"code", "type"} {
		switch value := obj[key].(type) {
		case string:
			if value != "" {
				return strings.ToLower(value)
			}
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func qoderAuthRejected(status int, raw []byte) bool {
	code := qoderErrorCode(raw)
	if code == "10605" || code == "model_queued" || code == "112" || code == "quota_exceeded" {
		return false
	}
	return status == http.StatusUnauthorized || status == http.StatusForbidden && (code == "token_expire" || code == "token_expired" || code == "invalid_token" || code == "auth_expired")
}

func qoderUpstreamError(status int, raw []byte) error {
	code := qoderErrorCode(raw)
	switch code {
	case "10605", "model_queued":
		return newPluginCallError("model_queued", "Qoder model is queued", 429, true)
	case "112", "quota_exceeded", "insufficient_quota":
		return newPluginCallError("quota_or_rate_limit", "Qoder quota is exhausted", 429, true)
	case "unsupported_model", "model_not_found":
		return newPluginCallError("unsupported_model", "Qoder rejected the selected model", 400, false)
	}
	if qoderAuthRejected(status, raw) {
		return newPluginCallError("auth_expired", "Qoder authentication was rejected", 401, false)
	}
	if status == 403 {
		return newPluginCallError("upstream_forbidden", "Qoder denied this request", 403, false)
	}
	if status == 429 {
		return newPluginCallError("quota_or_rate_limit", "Qoder rate limited the request", 429, true)
	}
	if status >= 400 && status < 500 {
		return newPluginCallError("direct_invalid_request", "Qoder rejected the request", status, false)
	}
	return newPluginCallError("direct_upstream_error", "Qoder reported an upstream error", 502, true)
}
