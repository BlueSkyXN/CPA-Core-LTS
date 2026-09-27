package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryMetadataPointsToPluginSource(t *testing.T) {
	data := registration().(map[string]any)
	metadata := data["metadata"].(map[string]any)
	if metadata["GitHubRepository"] != "https://github.com/BlueSkyXN/CPA-Core-LTS" {
		t.Fatal("plugin repository link does not point to its containing source repository")
	}
}

func TestSharedPromptCases(t *testing.T) {
	raw, err := os.ReadFile("../testdata/prompt-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Mode, System string
		Position           string `json:"move_position"`
		Expected           any    `json:"expectedSystem"`
		Index              *int   `json:"movedIndex"`
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			cfg := &config{Models: []string{"test-model"}, DeviceID: "synthetic-device", Prompt: promptConfig{Mode: c.Mode, Template: "test", MovePosition: c.Position, Templates: map[string]string{"test": "template"}}}
			input := encode(map[string]any{"model": "test-model", "max_tokens": 10, "system": c.System, "messages": []any{map[string]any{"role": "user", "content": "one"}, map[string]any{"role": "assistant", "content": "reply"}, map[string]any{"role": "user", "content": "two"}}})
			result, err := transform(input, cfg, "session")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result["system"], c.Expected) {
				t.Fatalf("system mismatch: %s", encode(result["system"]))
			}
			if c.Index != nil {
				m := result["messages"].([]any)[*c.Index].(map[string]any)
				block := m["content"].([]any)[0].(map[string]any)
				if block["text"] != "Caller context (moved from system; user-level):\ncaller" {
					t.Fatal("move")
				}
			}
		})
	}
}
func TestPromptOverride(t *testing.T) {
	cfg := &config{Models: []string{"m"}, DeviceID: "synthetic", Prompt: promptConfig{Mode: "preserve", Templates: map[string]string{"t": "new"}}}
	raw := []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"x_coding_plan":{"prompt":{"mode":"replace","template":"t"}}}`)
	if _, err := transform(raw, cfg, "s"); err == nil {
		t.Fatal("override accepted")
	}
	cfg.Prompt.AllowRequestOverride = true
	b, err := transform(raw, cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b["x_coding_plan"]; ok {
		t.Fatal("extension leaked")
	}
}
func TestSSE(t *testing.T) {
	wire := []byte("event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"text\":\"你好\"}\r\n\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n")
	var out bytes.Buffer
	p := sseParser{emit: func(b []byte) error { out.Write(b); return nil }}
	for _, b := range wire {
		if err := p.feed([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.finish(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), wire) {
		t.Fatal("SSE mismatch")
	}
	q := sseParser{emit: func([]byte) error { return nil }}
	if err := q.finish(); err == nil {
		t.Fatal("truncation accepted")
	}
}
func TestCryptoRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	iv := bytes.Repeat([]byte{7}, 12)
	key := derive("synthetic-secret", "ed25519_priv")
	b, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(b)
	ciphertext := append(iv, g.Seal(nil, iv, []byte(base64.StdEncoding.EncodeToString(der)), []byte("synthetic-key"))...)
	decoded, err := decryptKey("synthetic-key.synthetic-secret", base64.StdEncoding.EncodeToString(ciphertext))
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(signMessage(decoded, "synthetic-key", "s", "1", "n"))
	if !ed25519.Verify(pub, []byte("synthetic-key\n1\n3.14.3\ns\nn"), sig) {
		t.Fatal("signature")
	}
	if _, err := pow(context.Background(), "id", "session", "1"); err != nil {
		t.Fatal(err)
	}
}

type fakeHost struct {
	mu                 sync.Mutex
	private            ed25519.PrivateKey
	public             ed25519.PublicKey
	handshakes, models int
	reads              map[string]int
	closed             int
	emitted            [][]byte
	streamDone         chan struct{}
	captured           map[string]any
}

func (f *fakeHost) Call(method string, value any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "host.http.do_stream":
		req := value.(httpRequest)
		if !req.DisableRedirects {
			return nil, problem(500, "redirect_policy", "Redirects must be disabled")
		}
		if strings.HasSuffix(req.URL, "/client") {
			f.handshakes++
			if req.Headers.Get("Authorization") != "synthetic-key.synthetic-secret" {
				return nil, problem(401, "bad_auth", "bad auth")
			}
			return encode(httpStream{StatusCode: 200, StreamID: "handshake"}), nil
		}
		f.models++
		var body map[string]any
		json.Unmarshal(req.Body, &body)
		f.captured = body
		h := req.Headers
		sig, _ := base64.StdEncoding.DecodeString(h.Get("X-Client-Sig"))
		if !ed25519.Verify(f.public, []byte("synthetic-key\n"+h.Get("X-Client-Ts")+"\n3.14.3\n"+h.Get("X-Session-Id")+"\n"+h.Get("X-Client-Nonce")), sig) {
			return nil, problem(401, "bad_signature", "bad signature")
		}
		ct := "application/json"
		id := "json"
		if body["stream"] == true {
			ct = "text/event-stream"
			id = "sse"
		}
		f.reads[id] = 0
		return encode(httpStream{StatusCode: 200, Headers: http.Header{"Content-Type": []string{ct}}, StreamID: id}), nil
	case "host.http.stream_read":
		id := value.(map[string]any)["stream_id"].(string)
		f.reads[id]++
		if id == "handshake" {
			der, _ := x509.MarshalPKCS8PrivateKey(f.private)
			iv := bytes.Repeat([]byte{7}, 12)
			b, _ := aes.NewCipher(derive("synthetic-secret", "ed25519_priv"))
			g, _ := cipher.NewGCM(b)
			ct := append(iv, g.Seal(nil, iv, []byte(base64.StdEncoding.EncodeToString(der)), []byte("synthetic-key"))...)
			return encode(httpChunk{Payload: encode(map[string]any{"code": 200, "data": map[string]any{"privateCipher": base64.StdEncoding.EncodeToString(ct)}}), Done: true}), nil
		}
		if id == "json" {
			return encode(httpChunk{Payload: []byte(`{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`), Done: true}), nil
		}
		return encode(httpChunk{Payload: []byte("data: {\"type\":\"message_stop\"}\n\n"), Done: true}), nil
	case "host.http.stream_close":
		f.closed++
		return []byte(`{}`), nil
	case "host.stream.emit":
		f.emitted = append(f.emitted, value.(map[string]any)["payload"].([]byte))
		return []byte(`{}`), nil
	case "host.stream.close":
		close(f.streamDone)
		return []byte(`{}`), nil
	}
	return nil, problem(500, "unknown_callback", "Unknown callback")
}
func fixtureConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CP_TEST_KEY", "synthetic-key.synthetic-secret")
	t.Setenv("CP_TEST_DEVICE", "synthetic-device")
	path := filepath.Join(dir, "private.json")
	raw := []byte(`{"credential":{"api_key_env":"CP_TEST_KEY"},"identity":{"device_id_env":"CP_TEST_DEVICE","platform":"linux-x64","os_category":"linux","os_version":"test","language":"en","timezone":"UTC"},"models":["test-model"],"host_logging_disabled":true,"prompt":{"mode":"preserve"}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestExecutorHostCallbacks(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeHost{private: priv, public: pub, reads: map[string]int{}, streamDone: make(chan struct{})}
	r := newRuntime(f)
	defer r.stop()
	path := fixtureConfig(t)
	storage := encode(authRecord{Type: provider, ConfigFile: path})
	req := executorRequest{RequestID: "r1", CallbackID: "cb", AuthID: "synthetic-auth", ExecutionSessionID: "conversation", Format: "claude", StorageJSON: storage, Payload: []byte(`{"model":"test-model","max_tokens":2,"messages":[{"role":"user","content":"x"}]}`)}
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	req.Stream = true
	req.StreamID = "host-stream"
	req.RequestID = "r2"
	req.Payload = []byte(`{"model":"test-model","max_tokens":2,"stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	<-f.streamDone
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handshakes != 1 || f.models != 2 || len(f.emitted) != 1 {
		t.Fatalf("counts %d %d %d", f.handshakes, f.models, len(f.emitted))
	}
}
func TestMetadataWireOrder(t *testing.T) {
	cfg := &config{Models: []string{"m"}, DeviceID: "synthetic", Prompt: promptConfig{Mode: "preserve"}}
	b, err := transform([]byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`), cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	if b["metadata"].(map[string]any)["user_id"] != `{"device_id":"synthetic","account_uuid":"","session_id":"s"}` {
		t.Fatal("metadata mismatch")
	}
}
