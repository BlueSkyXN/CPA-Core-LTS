package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const v2Model = "GLM-5.3-Flash"

type v2Transport struct {
	*fixtureTransport
	lock           sync.Mutex
	captured       [][]byte
	headers        []http.Header
	mode           string
	redirectStatus int
	targets        int
	handshakeBody  string
	blocked        *blockedFixtureBody
}

func (f *v2Transport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return f }
func (f *v2Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	if req.URL.Host != "open.bigmodel.cn" {
		f.targets++
		return nil, fmt.Errorf("non-fixture destination blocked")
	}
	handshake := strings.HasSuffix(req.URL.Path, "/client")
	if (handshake && f.mode == "handshake-block") || (!handshake && f.mode == "model-block") {
		f.blocked.ctx = req.Context()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: f.blocked, Request: req}, nil
	}
	if handshake && f.mode == "network" {
		return nil, fmt.Errorf("synthetic network failure")
	}
	if handshake && (f.mode == "handshake-redirect" || f.mode == "handshake-error") {
		status := f.redirectStatus
		if f.mode == "handshake-error" {
			status = 401
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://elsewhere.invalid/capture"}}, Body: io.NopCloser(strings.NewReader(`{"privateCipher":"synthetic-sensitive-response"}`)), Request: req}, nil
	}
	if handshake {
		res, err := f.fixtureTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		f.handshakeBody = string(body)
		res.Body = io.NopCloser(bytes.NewReader(body))
		return res, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	h := req.Header
	sig, _ := base64.StdEncoding.DecodeString(h.Get("X-Client-Sig"))
	if !ed25519.Verify(f.pub, []byte("synthetic-key\n"+h.Get("X-Client-Ts")+"\n3.14.3\n"+h.Get("X-Session-Id")+"\n"+h.Get("X-Client-Nonce")), sig) {
		return nil, fmt.Errorf("invalid synthetic signature")
	}
	if h.Get("X-Coding-Plan-Prompt") != "" || gjson.GetBytes(body, "x_coding_plan").Exists() {
		return nil, fmt.Errorf("prompt control leaked")
	}
	f.captured = append(f.captured, bytes.Clone(body))
	f.headers = append(f.headers, h.Clone())
	if f.mode == "model-redirect" {
		return &http.Response{StatusCode: f.redirectStatus, Header: http.Header{"Location": {"https://elsewhere.invalid/capture"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: req}, nil
	}
	text, ct := v2Reply(body, len(f.captured)), "application/json"
	if gjson.GetBytes(body, "stream").Bool() {
		ct = "text/event-stream"
		text = v2Events(text)
		if f.mode == "truncate" {
			text = strings.TrimSuffix(text, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
	}
	if f.mode == "invalid" {
		text = "invalid"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(text)), Request: req}, nil
}
func v2Reply(body []byte, n int) string {
	content := []any{map[string]any{"type": "text", "text": "fixture-ok"}}
	stop := "end_turn"
	if gjson.GetBytes(body, "tools.#").Int() > 0 && !bytes.Contains(body, []byte(`"tool_result"`)) {
		content = []any{map[string]any{"type": "thinking", "thinking": "synthetic reason", "signature": "synthetic-opaque"}, map[string]any{"type": "tool_use", "id": "call-fixture", "name": "lookup", "input": map[string]any{"q": "hello"}}}
		stop = "tool_use"
	}
	b, _ := json.Marshal(map[string]any{"type": "message", "id": fmt.Sprintf("message-%d", n), "model": v2Model, "role": "assistant", "content": content, "stop_reason": stop, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2}})
	return string(b)
}
func v2Events(reply string) string {
	var message map[string]any
	_ = json.Unmarshal([]byte(reply), &message)
	blocks := message["content"].([]any)
	stop := message["stop_reason"]
	message["content"] = []any{}
	delete(message, "stop_reason")
	message["usage"] = map[string]int{"input_tokens": 10, "output_tokens": 0}
	var out strings.Builder
	emit := func(typ string, v map[string]any) {
		v["type"] = typ
		b, _ := json.Marshal(v)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", typ, b)
	}
	emit("message_start", map[string]any{"message": message})
	for i, block := range blocks {
		emit("content_block_start", map[string]any{"index": i, "content_block": block})
		emit("content_block_stop", map[string]any{"index": i})
	}
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 2}})
	emit("message_stop", map[string]any{})
	return out.String()
}

type v2Fixture struct {
	host      *pluginhost.Host
	router    *gin.Engine
	transport *v2Transport
	usage     *fixtureUsageSink
	logs      []string
	cfg       *config.Config
}

func newV2Fixture(t *testing.T, override bool) *v2Fixture {
	t.Helper()
	library := os.Getenv("CP_PLUGIN_LIBRARY")
	if library == "" {
		t.Fatal("CP_PLUGIN_LIBRARY required")
	}
	dir := t.TempDir()
	lib, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "zcode-coding-plan"+filepath.Ext(library)), lib, 0700); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "private.json")
	if err = os.WriteFile(filepath.Join(dir, "template"), []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"credential":{"api_key_env":"CP_INTEGRATION_KEY"},"identity":{"device_id_env":"CP_INTEGRATION_DEVICE","platform":"linux-x64","os_category":"linux","os_version":"synthetic","language":"en","timezone":"UTC"},"models":["%s"],"host_logging_disabled":true,"prompt":{"mode":"preserve","allow_request_override":%t,"templates":{"t":"template"}}}`, v2Model, override)
	if err = os.WriteFile(private, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n      config_file: %q\nrequest-log: true\nrequest-retry: 0\n", dir, private)))
	if err != nil {
		t.Fatal(err)
	}
	h := pluginhost.New()
	t.Cleanup(h.ShutdownAll)
	h.ApplyConfig(context.Background(), cfg)
	storage, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "request_retry": 0})
	auth, handled, err := h.ParseAuth(context.Background(), pluginapi.AuthParseRequest{RawJSON: storage, FileName: "synthetic.json"})
	if err != nil || !handled {
		t.Fatalf("auth parse: %v", err)
	}
	auth.ID = "v2-fixture-auth"
	auth.Index = "v2-fixture-index"
	auth.Status = coreauth.StatusActive
	models := h.ModelsForAuth(context.Background(), auth)
	if models.Err != nil {
		t.Fatal(models.Err)
	}
	if len(models.Models) != 1 || !models.Models[0].IsCompat || models.Models[0].ContextLength != 500000 || models.Models[0].MaxCompletionTokens != 128000 {
		t.Fatal("model metadata not applied")
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, models.Models)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h.SetAuthManager(manager)
	h.RegisterExecutors(manager, nil)
	if _, err = manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &v2Fixture{host: h, cfg: cfg, transport: &v2Transport{fixtureTransport: &fixtureTransport{pub: pub, priv: priv}}, usage: &fixtureUsageSink{records: make(chan coreusage.Record, 128)}}
	manager.SetRoundTripperProvider(f.transport)
	coreusage.RegisterNamedPlugin("coding-plan-v2", f.usage)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	f.router = router
	router.Use(func(c *gin.Context) {
		c.Next()
		var captured strings.Builder
		for _, key := range []string{"API_REQUEST", "API_RESPONSE"} {
			if v, ok := c.Get(key); ok {
				if b, ok := v.([]byte); ok {
					captured.Write(b)
				}
			}
		}
		if v, ok := c.Get(logging.DeferredAPIRequestContextKey); ok {
			if source, ok := v.(interface {
				SnapshotDeferredAPIRequests() []logging.DeferredAPIRequest
			}); ok {
				for _, snapshot := range source.SnapshotDeferredAPIRequests() {
					captured.Write(snapshot())
				}
			}
		}
		f.logs = append(f.logs, captured.String())
	})
	handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router.POST("/v1/responses", handler.Responses)
	router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
	mgmt := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	mgmt.SetPluginHost(h)
	router.GET("/v0/management/plugins/:id/readiness", mgmt.GetPluginReadiness)
	router.GET("/v0/management/plugins", mgmt.ListPlugins)
	if len(h.DeclaredSensitiveEndpoints()) != 1 {
		t.Fatal("credential endpoint declaration missing")
	}
	return f
}
func (f *v2Fixture) post(t *testing.T, body, header string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set("X-Coding-Plan-Prompt", header)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func (f *v2Fixture) checkUsage(t *testing.T, failed bool) {
	t.Helper()
	select {
	case r := <-f.usage.records:
		if r.Failed != failed || r.AuthIndex != "v2-fixture-index" {
			t.Fatalf("usage failed=%v auth=%s", r.Failed, r.AuthIndex)
		}
		if !failed && r.Detail.TotalTokens != 12 {
			t.Fatal("usage tokens lost")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("usage missing")
	}
}
func TestV2ResponsesHTTPPromptAndHistory(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, true)
	for _, mode := range []string{"preserve", "replace", "prepend", "append", "move_to_user"} {
		for _, stream := range []bool{false, true} {
			request := fmt.Sprintf(`{"model":%q,"instructions":"caller","input":"hello","stream":%t}`, v2Model, stream)
			res := f.post(t, request, fmt.Sprintf(`{"mode":%q,"template":"t"}`, mode))
			if res.Code != 200 || !strings.Contains(res.Body.String(), "fixture-ok") {
				t.Fatalf("%s stream=%v status=%d: %s", mode, stream, res.Code, res.Body.String())
			}
			f.checkUsage(t, false)
			body := f.transport.captured[len(f.transport.captured)-1]
			system := gjson.GetBytes(body, "system").Raw
			if mode == "preserve" && !strings.Contains(system, "caller") {
				t.Fatal("caller lost")
			}
			if mode != "preserve" && !strings.Contains(system, "template") {
				t.Fatal("template lost")
			}
			if mode == "replace" && strings.Contains(system, "caller") {
				t.Fatal("caller not replaced")
			}
			if mode == "move_to_user" && !strings.Contains(gjson.GetBytes(body, "messages").Raw, "Caller context") {
				t.Fatal("caller not moved")
			}
		}
	}
	tools := `[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]`
	first := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello","tools":%s}`, v2Model, tools), "")
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	f.checkUsage(t, false)
	if gjson.Get(first.Body.String(), "output.0.encrypted_content").String() != "synthetic-opaque" {
		t.Fatal("thinking carrier lost")
	}
	history := []any{map[string]any{"role": "user", "content": "hello"}}
	for _, item := range gjson.Get(first.Body.String(), "output").Array() {
		history = append(history, item.Value())
	}
	history = append(history, map[string]any{"type": "function_call_output", "call_id": "call-fixture", "output": "found"})
	input, _ := json.Marshal(history)
	second := f.post(t, fmt.Sprintf(`{"model":%q,"input":%s,"tools":%s}`, v2Model, input, tools), "")
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	f.checkUsage(t, false)
	last := string(f.transport.captured[len(f.transport.captured)-1])
	if !strings.Contains(last, `"signature":"synthetic-opaque"`) || !strings.Contains(last, `"tool_result"`) {
		t.Fatal("tool/thinking history not preserved")
	}
	clamped := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello","max_output_tokens":128001}`, v2Model), "")
	if clamped.Code != 200 || gjson.GetBytes(f.transport.captured[len(f.transport.captured)-1], "max_tokens").Int() != 128000 {
		t.Fatal("host model limit not respected")
	}
	f.checkUsage(t, false)
	before := len(f.transport.captured)
	for _, control := range []string{`"reasoning":{"effort":"high"}`, `"text":{"format":{"type":"json_schema","strict":true}}`, `"previous_response_id":"old"`, `"x_coding_plan":{"prompt":{"mode":"replace","template":"t"}}`} {
		res := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello",%s}`, v2Model, control), "")
		if res.Code < 400 {
			t.Fatalf("unsupported control accepted: %s", control)
		}
	}
	if len(f.transport.captured) != before {
		t.Fatal("invalid request reached model")
	}
}
func TestV2PromptOverrideDisabled(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, false)
	res := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model), `{"mode":"replace","template":"t"}`)
	if res.Code != 400 || len(f.transport.captured) != 0 || f.transport.handshakes != 0 {
		t.Fatal("disabled override reached upstream")
	}
}
func TestV2WebsocketToolContinuation(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, true)
	server := httptest.NewServer(f.router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", http.Header{"X-Coding-Plan-Prompt": {`{"mode":"replace","template":"t"}`}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func() []byte {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			typ := gjson.GetBytes(b, "type").String()
			if typ == "response.completed" || typ == "error" || typ == "response.failed" {
				return b
			}
		}
	}
	send := func(s string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":"hello"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, v2Model))
	first := read()
	if gjson.GetBytes(first, "type").String() != "response.completed" {
		t.Fatalf("first turn: %s", first)
	}
	f.checkUsage(t, false)
	parent := gjson.GetBytes(first, "response.id").String()
	send(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":"call-fixture","output":"found"}]}`, parent))
	second := read()
	if gjson.GetBytes(second, "type").String() != "response.completed" {
		t.Fatalf("second turn: %s", second)
	}
	f.checkUsage(t, false)
	f.transport.lock.Lock()
	body := string(f.transport.captured[1])
	session1, session2 := f.transport.headers[0].Get("X-Session-Id"), f.transport.headers[1].Get("X-Session-Id")
	f.transport.lock.Unlock()
	if !strings.Contains(body, `"tool_result"`) || !strings.Contains(body, `"signature":"synthetic-opaque"`) || !strings.Contains(body, "template") || session1 != session2 {
		t.Fatal("WS history/session/prompt ownership lost")
	}
	send(`{"type":"response.create","previous_response_id":"not-on-this-connection","input":[]}`)
	bad := read()
	if gjson.GetBytes(bad, "type").String() != "error" {
		t.Fatalf("bad parent accepted: %s", bad)
	}
	f.transport.lock.Lock()
	defer f.transport.lock.Unlock()
	if len(f.transport.captured) != 2 {
		t.Fatal("bad WS parent reached model")
	}
}
func TestV2SensitiveLoggingRedirectAndFailure(t *testing.T) {
	for _, mode := range []string{"normal", "handshake-error", "network", "handshake-redirect", "model-redirect", "truncate"} {
		t.Run(mode, func(t *testing.T) {
			if isolateDynamic(t) {
				return
			}
			f := newV2Fixture(t, true)
			f.transport.mode = mode
			f.transport.redirectStatus = 307
			res := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello","stream":%t}`, v2Model, mode == "truncate"), "")
			if mode == "normal" {
				if res.Code != 200 {
					t.Fatal(res.Body.String())
				}
			} else if mode != "truncate" && res.Code < 400 {
				t.Fatal("failure accepted")
			}
			f.checkUsage(t, mode != "normal")
			logs := strings.Join(f.logs, "\n")
			for _, secret := range []string{"synthetic-key.synthetic-secret", "synthetic-sensitive-response"} {
				if strings.Contains(logs, secret) {
					t.Fatal("credential leaked into capture")
				}
			}
			if mode != "network" && mode != "handshake-redirect" && mode != "handshake-error" && strings.Contains(logs, f.transport.handshakeBody) {
				t.Fatal("handshake response leaked")
			}
			if !strings.Contains(logs, "REDACTED CREDENTIAL EXCHANGE") {
				t.Fatal("sensitive endpoint not applied to capture")
			}
			if f.transport.targets != 0 || len(f.transport.captured) > 1 {
				t.Fatal("redirect or retry dispatched a second model request")
			}
			if !f.host.UnloadPlugin("zcode-coding-plan") {
				t.Fatal("unload failed")
			}
			if len(f.host.DeclaredSensitiveEndpoints()) != 0 {
				t.Fatal("sensitive declaration survived unload")
			}
		})
	}
}
