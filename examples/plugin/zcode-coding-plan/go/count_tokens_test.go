package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// countFakeHost 扩展 fakeHost：区分 count_tokens 路由，记录 URL 与请求体，
// 可注入签名拒绝和非计数响应。所有状态复用嵌入 fakeHost 的锁。
type countFakeHost struct {
	fakeHost
	countURL     string
	countBody    map[string]any
	rejectSig    bool
	badCountBody bool
}

func (f *countFakeHost) Call(method string, value any) (json.RawMessage, error) {
	if method == "host.http.do_stream" {
		req := value.(httpRequest)
		if strings.Contains(req.URL, "/count_tokens") {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.countURL = req.URL
			var body map[string]any
			json.Unmarshal(req.Body, &body)
			f.countBody = body
			if f.rejectSig {
				return encode(httpStream{StatusCode: 401, Headers: http.Header{"Content-Type": []string{"application/json"}}, StreamID: "count-reject"}), nil
			}
			h := req.Headers
			sig, _ := base64.StdEncoding.DecodeString(h.Get("X-Client-Sig"))
			id, _, _ := strings.Cut(req.Headers.Get("X-Api-Key"), ".")
			if !ed25519.Verify(f.public, []byte(id+"\n"+h.Get("X-Client-Ts")+"\n3.14.3\n"+h.Get("X-Session-Id")+"\n"+h.Get("X-Client-Nonce")), sig) {
				return nil, problem(401, "bad_signature", "bad signature")
			}
			return encode(httpStream{StatusCode: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, StreamID: "count"}), nil
		}
	}
	if method == "host.http.stream_read" {
		if id, _ := value.(map[string]any)["stream_id"].(string); id == "count" {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.badCountBody {
				return encode(httpChunk{Payload: []byte(`{"unexpected":true}`), Done: true}), nil
			}
			return encode(httpChunk{Payload: []byte(`{"input_tokens":13}`), Done: true}), nil
		}
	}
	return f.fakeHost.Call(method, value)
}

func newCountTestRuntime(t *testing.T) (*pluginRuntime, *countFakeHost) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &countFakeHost{}
	f.private, f.public = priv, pub
	f.reads = map[string]int{}
	f.streamDone = make(chan struct{})
	r := newRuntime(f)
	t.Cleanup(r.stop)
	ack := true
	if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": ack})); err != nil {
		t.Fatal(err)
	}
	return r, f
}

func TestCountTokensEndpointRewrite(t *testing.T) {
	if got := countTokensEndpoint("https://open.bigmodel.cn/api/anthropic/v1/messages"); got != "https://open.bigmodel.cn/api/anthropic/v1/messages/count_tokens?beta=true" {
		t.Fatalf("bigmodel rewrite = %q", got)
	}
	if got := countTokensEndpoint("https://api.z.ai/api/anthropic/v1/messages"); got != "https://api.z.ai/api/anthropic/v1/messages/count_tokens?beta=true" {
		t.Fatalf("zai rewrite = %q", got)
	}
	for _, bad := range []string{"", "not-a-url", "http://insecure.example/v1/messages"} {
		if got := countTokensEndpoint(bad); got != "" {
			t.Fatalf("endpoint %q unexpectedly accepted: %q", bad, got)
		}
	}
}

func TestCountTokensHappyPath(t *testing.T) {
	r, f := newCountTestRuntime(t)
	req := executorRequest{RequestID: "c1", CallbackID: "cb", AuthID: "synthetic-auth", ExecutionSessionID: "conversation", Format: "claude", StorageJSON: inlineStorage(), Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"tools":[],"max_tokens":1,"stream":true}`)}
	result, err := r.countTokens(req)
	if err != nil {
		t.Fatal(err)
	}
	payload := result.(map[string]any)["Payload"].([]byte)
	if string(payload) != `{"input_tokens":13}` {
		t.Fatalf("payload = %s", payload)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasSuffix(f.countURL, "/v1/messages/count_tokens?beta=true") {
		t.Fatalf("upstream URL = %q", f.countURL)
	}
	if _, exists := f.countBody["max_tokens"]; exists {
		t.Fatal("max_tokens must be stripped from the count request")
	}
	if _, exists := f.countBody["stream"]; exists {
		t.Fatal("stream must be stripped from the count request")
	}
	userID, _ := f.countBody["metadata"].(map[string]any)["user_id"].(string)
	if !strings.Contains(userID, `"session_id"`) || !strings.Contains(userID, `"device_id":"synthetic-device"`) {
		t.Fatalf("metadata.user_id = %q", userID)
	}
}

func TestCountTokensModelAllowlistEnforced(t *testing.T) {
	r, _ := newCountTestRuntime(t)
	req := executorRequest{RequestID: "c2", CallbackID: "cb", AuthID: "synthetic-auth", Format: "claude", StorageJSON: inlineStorage(), Payload: []byte(`{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`)}
	_, err := r.countTokens(req)
	if err == nil || safeError(err).Code != "model_not_found" {
		t.Fatal("unlisted model accepted", err)
	}
}

func TestCountTokensUpstreamAuthFailurePropagates(t *testing.T) {
	r, f := newCountTestRuntime(t)
	f.mu.Lock()
	f.rejectSig = true
	f.mu.Unlock()
	req := executorRequest{RequestID: "c3", CallbackID: "cb", AuthID: "synthetic-auth", Format: "claude", StorageJSON: inlineStorage(), Payload: []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)}
	_, err := r.countTokens(req)
	if err == nil {
		t.Fatal("upstream 401 accepted")
	}
	e := safeError(err)
	if e.Status != 401 || e.Code != "upstream_error" {
		t.Fatalf("error = %d %s", e.Status, e.Code)
	}
}

func TestCountTokensInvalidCountResponseRejected(t *testing.T) {
	r, f := newCountTestRuntime(t)
	f.mu.Lock()
	f.badCountBody = true
	f.mu.Unlock()
	req := executorRequest{RequestID: "c4", CallbackID: "cb", AuthID: "synthetic-auth", Format: "claude", StorageJSON: inlineStorage(), Payload: []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)}
	_, err := r.countTokens(req)
	if err == nil || safeError(err).Code != "invalid_response" {
		t.Fatal("malformed count body accepted", err)
	}
}
