package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

const flashImageFixture = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="

func TestResponsesThinkingAndImageControls(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, "glm-5.3", "glm-5.3-flash")
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		for _, effort := range []string{"low", "high", "max"} {
			for _, stream := range []bool{false, true} {
				res := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello","reasoning":{"effort":%q},"stream":%t}`, model, effort, stream))
				if res.Code != 200 {
					t.Fatalf("%s/%s stream=%v HTTP=%d: %s", model, effort, stream, res.Code, res.Body.String())
				}
				f.checkUsage(t, false)
				body := f.transport.captured[len(f.transport.captured)-1]
				if gjson.GetBytes(body, "reasoning_effort").String() != effort || gjson.GetBytes(body, "thinking.type").String() != "enabled" || gjson.GetBytes(body, "thinking.budget_tokens").Exists() || gjson.GetBytes(body, "output_config").Exists() {
					t.Fatal("thinking effort not preserved in the dispatched GLM request")
				}
			}
		}
		before := len(f.transport.captured)
		res := f.post(t, fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,%s"}]}]}`, model, flashImageFixture))
		if model == "glm-5.3" {
			if res.Code != 400 || len(f.transport.captured) != before {
				t.Fatal("text-only model accepted an image")
			}
			f.checkUsage(t, true)
			continue
		}
		if res.Code != 200 {
			t.Fatalf("Flash image HTTP=%d: %s", res.Code, res.Body.String())
		}
		f.checkUsage(t, false)
		body := f.transport.captured[len(f.transport.captured)-1]
		if gjson.GetBytes(body, "messages.0.content.1.type").String() != "image" || gjson.GetBytes(body, "messages.0.content.1.source.data").String() != flashImageFixture {
			t.Fatal("image was dropped or changed during translation")
		}
	}
}

func TestMessagesThinkingAndToolImages(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, "glm-5.3", "glm-5.3-flash")
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		for _, effort := range []string{"low", "high", "max"} {
			for _, stream := range []bool{false, true} {
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":100,"thinking":{"type":"adaptive"},"output_config":{"effort":%q},"stream":%t}`, model, effort, stream)
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
				if rec.Code != 200 {
					t.Fatalf("Messages %s/%s stream=%v HTTP=%d: %s", model, effort, stream, rec.Code, rec.Body.String())
				}
				f.checkUsage(t, false)
				last := f.transport.captured[len(f.transport.captured)-1]
				if gjson.GetBytes(last, "reasoning_effort").String() != effort || gjson.GetBytes(last, "thinking.type").String() != "enabled" {
					t.Fatal("native Messages effort did not reach the provider")
				}
			}
		}
		for _, nested := range []bool{false, true} {
			image := fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, flashImageFixture)
			history := `[{"role":"user","content":[` + image + `]}]`
			imagePath := "messages.0.content.0.source.data"
			if nested {
				history = `[{"role":"assistant","content":[{"type":"tool_use","id":"call-image","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-image","content":[` + image + `]}]}]`
				imagePath = "messages.1.content.0.content.0.source.data"
			}
			before := len(f.transport.captured)
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":%s,"max_tokens":100}`, model, history))))
			if model == "glm-5.3" {
				if rec.Code != 400 || len(f.transport.captured) != before {
					t.Fatal("text-only native Messages accepted an image")
				}
				f.checkUsage(t, true)
				continue
			}
			if rec.Code != 200 || gjson.GetBytes(f.transport.captured[len(f.transport.captured)-1], imagePath).String() != flashImageFixture {
				t.Fatalf("native Messages image nested=%v HTTP=%d", nested, rec.Code)
			}
			f.checkUsage(t, false)
		}
	}
}

func TestWebsocketThinkingAndImageControls(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t, "glm-5.3-flash")
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = conn.Close()
		select {
		case <-handlerDone:
		case <-time.After(5 * time.Second):
			t.Error("WebSocket handler did not finish before plugin cleanup")
		}
	}()
	body := fmt.Sprintf(`{"type":"response.create","model":"glm-5.3-flash","reasoning":{"effort":"max"},"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,%s"}]}]}`, flashImageFixture)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, event, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		switch gjson.GetBytes(event, "type").String() {
		case "error", "response.failed":
			t.Fatal("WebSocket thinking/image request failed", string(event))
		case "response.completed":
			f.checkUsage(t, false)
			f.transport.lock.Lock()
			defer f.transport.lock.Unlock()
			last := f.transport.captured[len(f.transport.captured)-1]
			if gjson.GetBytes(last, "reasoning_effort").String() != "max" || gjson.GetBytes(last, "thinking.type").String() != "enabled" || gjson.GetBytes(last, "messages.0.content.0.source.data").String() != flashImageFixture {
				t.Fatal("WebSocket controls/image did not reach the provider")
			}
			return
		}
	}
}

func TestHostLoggingAcknowledgementRevocation(t *testing.T) {
	for _, setting := range []string{"      host_logging_disabled: false\n", ""} {
		t.Run(fmt.Sprintf("explicit=%v", setting != ""), func(t *testing.T) {
			if isolateDynamic(t) {
				return
			}
			f := newV2Fixture(t)
			body := fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model)
			if first := f.post(t, body); first.Code != 200 {
				t.Fatalf("baseline HTTP=%d", first.Code)
			}
			f.checkUsage(t, false)
			next, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n%s      models: [%q]\n", f.cfg.Plugins.Dir, setting, v2Model)))
			if err != nil {
				t.Fatal(err)
			}
			f.host.ApplyConfig(context.Background(), next)
			res := f.post(t, body)
			if res.Code < 400 || len(f.transport.captured) != 1 {
				t.Fatal("revoked logging acknowledgement dispatched a model request")
			}
			status := httptest.NewRecorder()
			f.router.ServeHTTP(status, httptest.NewRequest("GET", "/v0/management/plugins/zcode-coding-plan/readiness?auth_index=v2-fixture-index", nil))
			if status.Code != 200 || gjson.GetBytes(status.Body.Bytes(), "Ready").Bool() {
				t.Fatal("revoked acknowledgement still reports readiness")
			}
			f.host.ApplyConfig(context.Background(), f.cfg)
			if res := f.post(t, body); res.Code != 200 || len(f.transport.captured) != 2 {
				t.Fatal("re-acknowledgement failed to restore execution")
			}
		})
	}
}

func (f *v2Fixture) saveAccount(t *testing.T, name, key, device string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "api_key": key, "device_id": device, "request_retry": 0})
	req := httptest.NewRequest("POST", "/v0/management/auth-files?name="+name, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth-file save status=%d", rec.Code)
	}
}

func TestManagementAccountRotationAndReplacement(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t)
	body := fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model)
	if res := f.post(t, body); res.Code != 200 {
		t.Fatal("baseline request failed")
	}
	f.checkUsage(t, false)
	f.saveAccount(t, "synthetic.json", "rotated-key.rotated-secret", "rotated-device")
	auth, ok := f.manager.GetByID("synthetic.json")
	if !ok {
		t.Fatal("updated account missing")
	}
	models := f.host.ModelsForAuth(context.Background(), auth)
	if models.Err != nil {
		t.Fatal("same-file credential rotation did not refresh models", models.Err)
	}
	f.transport.apiKey = "rotated-key.rotated-secret"
	if res := f.post(t, body); res.Code != 200 {
		t.Fatalf("rotated account HTTP=%d: %s", res.Code, res.Body.String())
	}
	if f.transport.handshakes != 2 {
		t.Fatal("rotation reused the old signing key")
	}
	last := f.transport.captured[len(f.transport.captured)-1]
	if !strings.Contains(gjson.GetBytes(last, "metadata.user_id").String(), "rotated-device") {
		t.Fatal("updated device identity did not reach upstream")
	}
	removed := httptest.NewRecorder()
	f.router.ServeHTTP(removed, httptest.NewRequest("DELETE", "/v0/management/auth-files?name=synthetic.json", nil))
	if removed.Code != 200 || len(f.manager.List()) != 0 {
		t.Fatalf("old account removal failed: HTTP=%d", removed.Code)
	}
	registry.GetGlobalRegistry().UnregisterClient("synthetic.json")
	f.saveAccount(t, "replacement.json", "replacement-key.replacement-secret", "replacement-device")
	replacement, ok := f.manager.GetByID("replacement.json")
	if !ok {
		t.Fatal("replacement account missing")
	}
	models = f.host.ModelsForAuth(context.Background(), replacement)
	if models.Err != nil {
		t.Fatal("replacement account rejected after removal", models.Err)
	}
	registry.GetGlobalRegistry().RegisterClient(replacement.ID, replacement.Provider, models.Models)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(replacement.ID) })
	f.manager.RefreshSchedulerEntry(replacement.ID)
	f.transport.apiKey = "replacement-key.replacement-secret"
	if res := f.post(t, body); res.Code != 200 {
		t.Fatalf("replacement account HTTP=%d: %s", res.Code, res.Body.String())
	}
	if f.transport.handshakes != 3 {
		t.Fatal("replacement reused the removed account signer")
	}
}
