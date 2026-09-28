package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func inlineStorage() []byte {
	return []byte(`{"type":"zcode-coding-plan","label":"inline","api_key":"synthetic-key.synthetic-secret","device_id":"synthetic-device","request_retry":0}`)
}

func TestParseAuthInlineRules(t *testing.T) {
	a, err := parseAuth(inlineStorage())
	if err != nil {
		t.Fatal("valid inline record rejected:", err)
	}
	if a.APIKey != "synthetic-key.synthetic-secret" || a.DeviceID != "synthetic-device" {
		t.Fatal("inline credentials not parsed")
	}
	if _, err = parseAuth([]byte(`{"type":"zcode-coding-plan","api_key":"synthetic-key.synthetic-secret"}`)); err == nil {
		t.Fatal("missing device_id accepted")
	}
	if _, err = parseAuth([]byte(`{"type":"zcode-coding-plan","device_id":"synthetic-device"}`)); err == nil {
		t.Fatal("missing api_key accepted")
	}
	if _, err = parseAuth([]byte(`{"type":"zcode-coding-plan","label":"bare"}`)); err == nil {
		t.Fatal("account without credentials accepted")
	}
	stale := []byte(`{"type":"zcode-coding-plan","label":"stale","config_file":"/tmp/other.json"}`)
	if _, err = parseAuth(stale); err == nil || !strings.Contains(safeError(err).Message, "no longer supported") {
		t.Fatal("legacy config_file reference accepted", err)
	}
	if _, err = parseAuth([]byte(`{"type":"zcode-coding-plan","api_key":"synthetic-key","device_id":"d"}`)); err == nil {
		t.Fatal("malformed api key accepted")
	}
	if _, err = parseAuth([]byte(`{"type":"zcode-coding-plan","api_key":"a.b\nc","device_id":"d"}`)); err == nil {
		t.Fatal("control characters in api key accepted")
	}
}

func TestInlineConfigurationRequiresLoggingAcknowledgement(t *testing.T) {
	r := newRuntime(nil)
	if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.configuration(inlineStorage()); err == nil || safeError(err).Status != 503 || safeError(err).Code != "unsafe_host_logging" {
		t.Fatal("inline credentials loaded without acknowledgement", err)
	}
	ack := true
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": ack})); err != nil {
		t.Fatal(err)
	}
	c, err := r.configuration(inlineStorage())
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "synthetic-key.synthetic-secret" || c.DeviceID != "synthetic-device" {
		t.Fatal("inline credentials not applied to config")
	}
	if len(c.Models) != 2 || c.Models[0] != "glm-5.3" || c.Models[1] != "glm-5.3-flash" {
		t.Fatal("default model allowlist missing:", c.Models)
	}
	if c.Endpoint != "https://open.bigmodel.cn/api/anthropic/v1/messages" {
		t.Fatal("default upstream endpoint missing")
	}
	second := []byte(`{"type":"zcode-coding-plan","api_key":"other-key.other-secret","device_id":"other-device"}`)
	if _, err = r.configuration(second); err == nil {
		t.Fatal("second inline account accepted")
	}
	status, err := r.dispatch("executor.readiness", encode(map[string]any{"StorageJSON": inlineStorage()}))
	if err != nil {
		t.Fatal(err)
	}
	if containsAny(string(encode(status)), []string{"synthetic-key", "synthetic-secret", "synthetic-device"}) {
		t.Fatal("readiness leaked inline credentials")
	}
}

func TestInlineModelMetadata(t *testing.T) {
	c := defaultConfig()
	list := modelList(c)
	if len(list) != 2 {
		t.Fatal("default models missing")
	}
	for _, entry := range list {
		m := entry.(map[string]any)
		id := m["ID"].(string)
		if m["ContextLength"] != int64(1000000) || m["MaxCompletionTokens"] != int64(128000) {
			t.Fatal("builtin limits missing for", id)
		}
		thinking, ok := m["Thinking"].(map[string]any)
		if !ok {
			t.Fatal("thinking levels missing for", id)
		}
		levels, ok := thinking["Levels"].([]string)
		if !ok || len(levels) != 3 || levels[0] != "low" || levels[1] != "high" || levels[2] != "max" {
			t.Fatal("thinking levels wrong for", id, thinking["Levels"])
		}
		input := m["SupportedInputModalities"].([]string)
		if id == "glm-5.3" && len(input) != 1 {
			t.Fatal("glm-5.3 must be text-only")
		}
		if id == "glm-5.3-flash" && len(input) != 2 || (id == "glm-5.3-flash" && input[1] != "image") {
			t.Fatal("glm-5.3-flash must accept images")
		}
	}
	legacy, ok := builtinModelLimits("GLM-5.3-Flash")
	if !ok || legacy.Context != 1000000 || legacy.Output != 128000 {
		t.Fatal("legacy uppercase model id lost builtin limits")
	}
}

func TestInlineExecutionAndReconfigure(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeHost{private: priv, public: pub, reads: map[string]int{}, streamDone: make(chan struct{})}
	r := newRuntime(f)
	defer r.stop()
	ack := true
	if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": ack})); err != nil {
		t.Fatal(err)
	}
	storage := inlineStorage()
	req := executorRequest{RequestID: "r1", CallbackID: "cb", AuthID: "inline-auth", ExecutionSessionID: "conversation", Format: "claude", StorageJSON: storage, Payload: []byte(`{"model":"glm-5.3","max_tokens":2,"messages":[{"role":"user","content":"x"}]}`)}
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	req.Stream = true
	req.StreamID = "host-stream"
	req.RequestID = "r2"
	req.Payload = []byte(`{"model":"glm-5.3-flash","max_tokens":2,"stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	<-f.streamDone
	f.mu.Lock()
	f.mu.Unlock()
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": ack, "prompt_mode": "preserve"})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.configuration(storage); err != nil {
		t.Fatal("inline config lost after reconfigure", err)
	}
	r.mu.Lock()
	key := r.inlineAPIKey
	signer := r.signer
	r.mu.Unlock()
	if key != "synthetic-key.synthetic-secret" || signer == nil {
		t.Fatal("inline credential snapshot lost on reconfigure")
	}
}

func TestManagementFieldsCoverInlineControls(t *testing.T) {
	names := map[string]bool{}
	for _, field := range managementFields() {
		names[field.(map[string]any)["Name"].(string)] = true
	}
	for _, name := range []string{"host_logging_disabled", "upstream", "models", "model_limits"} {
		if !names[name] {
			t.Fatal(name + " missing from management fields")
		}
	}
}
