package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// 登录测试使用固定响应的假 host 回调，不发起真实网络请求。
type loginFakeHost struct {
	mu         sync.Mutex
	responses  map[string]hostHTTPResponse
	tokenPosts atomic.Int32
}

func (h *loginFakeHost) Call(method string, payload any) (json.RawMessage, error) {
	if method != pluginabi.MethodHostHTTPDo {
		return nil, http.ErrAbortHandler
	}
	req := payload.(hostHTTPRequest)
	if strings.HasSuffix(req.URL, "/login/oauth/access_token") {
		h.tokenPosts.Add(1)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	response, ok := h.responses[keyForURL(req.URL)]
	if !ok {
		return json.Marshal(hostHTTPResponse{StatusCode: http.StatusNotFound, Headers: http.Header{}, Body: []byte(`{}`)})
	}
	return json.Marshal(response)
}

func keyForURL(rawURL string) string {
	if index := strings.Index(rawURL, "://"); index >= 0 {
		rest := rawURL[index+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			return rest[slash:]
		}
	}
	return rawURL
}

func loginRuntime(t *testing.T, host *loginFakeHost) *pluginRuntime {
	t.Helper()
	runtime := newPluginRuntime(host)
	runtime.config.GitHubEndpoint = "https://github.example.test"
	runtime.config.GitHubAPIEndpoint = "https://api.github.example.test"
	t.Cleanup(runtime.shutdown)
	return runtime
}

func startFixtureLogin(t *testing.T, runtime *pluginRuntime) pluginapi.AuthLoginStartResponse {
	t.Helper()
	raw, _ := json.Marshal(rpcAuthLoginStartRequest{HostCallbackID: "fixture-callback", AuthLoginStartRequest: pluginapi.AuthLoginStartRequest{Provider: pluginIdentifier}})
	response, err := runtime.startLoginRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestDeviceLoginStateMachine(t *testing.T) {
	host := &loginFakeHost{responses: map[string]hostHTTPResponse{
		"/login/device/code":        {StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":"https://github.example.test/login/device","expires_in":900,"interval":0.01}`)},
		"/login/oauth/access_token": {StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"access_token":"gho-fixture","token_type":"bearer","scope":"read:user"}`)},
		"/user":                     {StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"login":"octocat","id":1}`)},
	}}
	runtime := loginRuntime(t, host)
	start := startFixtureLogin(t, runtime)
	if start.Provider != pluginIdentifier || start.URL != "https://github.example.test/login/device" || start.State == "" {
		t.Fatalf("start response = %#v", start)
	}
	// state 必须满足 Core ValidateOAuthState 字符集：字母数字与 -_.。
	for _, r := range start.State {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			t.Fatalf("state charset invalid: %q", start.State)
		}
	}
	if start.Metadata["user_code"] != "ABCD-1234" {
		t.Fatalf("user code = %#v", start.Metadata)
	}
	pollRaw, _ := json.Marshal(rpcAuthLoginPollRequest{HostCallbackID: "fixture-callback", AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{Provider: pluginIdentifier, State: start.State}})
	// 间隔未到：pending 且不打上游。
	pending, err := runtime.pollLoginRequest(pollRaw)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("early poll = %#v", pending)
	}
	if host.tokenPosts.Load() != 0 {
		t.Fatal("early poll hit the token endpoint")
	}
	// 间隔到点后换 access_token 成功，并用 /user 登录名做 label。
	time.Sleep(20 * time.Millisecond)
	success, err := runtime.pollLoginRequest(pollRaw)
	if err != nil {
		t.Fatal(err)
	}
	if success.Status != pluginapi.AuthLoginStatusSuccess || success.Auth.Provider != pluginIdentifier || success.Auth.Label != "octocat" {
		t.Fatalf("success poll = %#v", success.Auth)
	}
	// Core 落盘按 FileName 派生 auth 文件；缺失会重现 UAT 抓到的 "missing id" 失败。
	if success.Auth.FileName != "copilot-octocat.json" {
		t.Fatalf("login auth FileName = %q", success.Auth.FileName)
	}
	var stored copilotAuth
	if err := json.Unmarshal(success.Auth.StorageJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Type != "copilot" || stored.AuthMode != "oauth" || stored.GitHubToken != "gho-fixture" || stored.Label != "octocat" {
		t.Fatalf("storage = %#v", stored)
	}
	if _, err := parseStoredAuth(success.Auth.StorageJSON); err != nil {
		t.Fatalf("login storage failed provider validation: %v", err)
	}
	// 成功后流程销毁，再次 poll 报 unknown。
	if done, err := runtime.pollLoginRequest(pollRaw); err != nil || done.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("post-success poll = %#v, %v", done, err)
	}
	// device_code 只出现在 StorageJSON/上游请求，绝不外露在响应里。
	if strings.Contains(string(success.Auth.StorageJSON), "dc-fixture-secret") {
		t.Fatal("device_code leaked through login response")
	}
}

func TestDeviceLoginPendingSlowDownAndDenial(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantStatus  pluginapi.AuthLoginStatus
		wantMessage string
		slowDown    bool
	}{
		{"pending", `{"error":"authorization_pending","error_description":"pending"}`, pluginapi.AuthLoginStatusPending, "", false},
		{"slow_down", `{"error":"slow_down","error_description":"slow"}`, pluginapi.AuthLoginStatusPending, "", true},
		{"denied", `{"error":"access_denied","error_description":"denied"}`, pluginapi.AuthLoginStatusError, "denied", false},
		{"expired", `{"error":"expired_token","error_description":"expired"}`, pluginapi.AuthLoginStatusError, "expired", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &loginFakeHost{responses: map[string]hostHTTPResponse{
				"/login/device/code":        {StatusCode: http.StatusOK, Body: []byte(`{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":"https://github.example.test/login/device","expires_in":900,"interval":0.001}`)},
				"/login/oauth/access_token": {StatusCode: http.StatusOK, Body: []byte(tc.body)},
			}}
			runtime := loginRuntime(t, host)
			start := startFixtureLogin(t, runtime)
			time.Sleep(5 * time.Millisecond)
			raw, _ := json.Marshal(rpcAuthLoginPollRequest{HostCallbackID: "fixture-callback", AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{State: start.State}})
			result, err := runtime.pollLoginRequest(raw)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %s", result.Status)
			}
			runtime.mu.Lock()
			flow := runtime.loginFlows[start.State]
			interval := time.Duration(0)
			if flow != nil {
				interval = flow.Interval
			}
			runtime.mu.Unlock()
			if tc.slowDown && interval <= copilotDeviceIntervalBase {
				t.Fatalf("slow_down did not raise the interval: %v", interval)
			}
			if tc.wantStatus == pluginapi.AuthLoginStatusError && flow != nil {
				t.Fatal("terminal poll result kept the login flow")
			}
		})
	}
}

// TestDeviceLoginConcurrentPollSingleWindow 复现 Core GetAuthStatus 对同一 state 的并发
// PollLogin：轮询窗口必须在锁内原子占用，否则并发 poll 会同时打上游并构成对
// NextPoll/Interval 的未同步读写。旧实现该断言会得到 16 次上游请求并触发 -race。
func TestDeviceLoginConcurrentPollSingleWindow(t *testing.T) {
	host := &loginFakeHost{responses: map[string]hostHTTPResponse{
		"/login/device/code":        {StatusCode: http.StatusOK, Body: []byte(`{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":"https://github.example.test/login/device","expires_in":900}`)},
		"/login/oauth/access_token": {StatusCode: http.StatusOK, Body: []byte(`{"error":"authorization_pending","error_description":"pending"}`)},
	}}
	runtime := loginRuntime(t, host)
	start := startFixtureLogin(t, runtime)
	raw, _ := json.Marshal(rpcAuthLoginPollRequest{HostCallbackID: "fixture-callback", AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{State: start.State}})
	// 把轮询窗口拨到过去，让所有并发 poll 立即竞争同一个窗口；Interval 保持默认 5s，
	// 赢得窗口的一方会把 NextPoll 推到 5s 之后，其余 poll 必须全部返回 pending。
	runtime.mu.Lock()
	runtime.loginFlows[start.State].NextPoll = time.Now().Add(-time.Millisecond)
	runtime.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := runtime.pollLoginRequest(raw)
			if err != nil {
				t.Errorf("concurrent poll error: %v", err)
				return
			}
			if result.Status != pluginapi.AuthLoginStatusPending {
				t.Errorf("concurrent poll status = %s", result.Status)
			}
		}()
	}
	wg.Wait()
	if posts := host.tokenPosts.Load(); posts != 1 {
		t.Fatalf("one polling window produced %d upstream token posts, want 1", posts)
	}
}

func TestDeviceLoginUnknownStateAndExpiry(t *testing.T) {
	host := &loginFakeHost{responses: map[string]hostHTTPResponse{
		"/login/device/code": {StatusCode: http.StatusOK, Body: []byte(`{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":"https://github.example.test/login/device","expires_in":900,"interval":0.001}`)},
	}}
	runtime := loginRuntime(t, host)
	raw, _ := json.Marshal(rpcAuthLoginPollRequest{HostCallbackID: "fixture-callback", AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{State: "missing"}})
	if result, err := runtime.pollLoginRequest(raw); err != nil || result.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("unknown state poll = %#v, %v", result, err)
	}
	if host.tokenPosts.Load() != 0 {
		t.Fatal("unknown state hit the token endpoint")
	}
	// 过期流程直接报 error 且不再打上游。
	expired := startFixtureLogin(t, runtime)
	runtime.mu.Lock()
	runtime.loginFlows[expired.State].ExpiresAt = time.Now().Add(-time.Second)
	runtime.mu.Unlock()
	raw, _ = json.Marshal(rpcAuthLoginPollRequest{HostCallbackID: "fixture-callback", AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{State: expired.State}})
	if result, err := runtime.pollLoginRequest(raw); err != nil || result.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("expired poll = %#v, %v", result, err)
	}
	if host.tokenPosts.Load() != 0 {
		t.Fatal("expired flow hit the token endpoint")
	}
}

func TestManagementLoginInfoRoute(t *testing.T) {
	host := &loginFakeHost{responses: map[string]hostHTTPResponse{
		"/login/device/code": {StatusCode: http.StatusOK, Body: []byte(`{"device_code":"dc-fixture-secret","user_code":"ABCD-1234","verification_uri":"https://github.example.test/login/device","expires_in":900,"interval":7}`)},
	}}
	runtime := loginRuntime(t, host)
	start := startFixtureLogin(t, runtime)
	raw, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet, Path: "/v0/management/plugins/copilot/login-info",
			Query: url.Values{"state": {start.State}},
		},
		HostCallbackID: "management-callback",
	})
	response, err := runtime.handleManagement(raw)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("login-info response = %#v, %v", response, err)
	}
	body := string(response.Body)
	for _, expected := range []string{`"user_code":"ABCD-1234"`, `"verification_uri":"https://github.example.test/login/device"`, `"interval_seconds":7`, "expires_at"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("login-info body missing %s: %s", expected, body)
		}
	}
	if strings.Contains(body, "dc-fixture-secret") {
		t.Fatal("login-info leaked the device code")
	}
	unknown, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet, Path: "/v0/management/plugins/copilot/login-info",
			Query: url.Values{"state": {"unknown"}},
		},
	})
	if response, err := runtime.handleManagement(unknown); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown state = %#v, %v", response, err)
	}
	missing, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/plugins/copilot/login-info"},
	})
	if response, err := runtime.handleManagement(missing); err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing state = %#v, %v", response, err)
	}
	post, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/v0/management/plugins/copilot/login-info"},
	})
	if response, err := runtime.handleManagement(post); err != nil || response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post = %#v, %v", response, err)
	}
}
