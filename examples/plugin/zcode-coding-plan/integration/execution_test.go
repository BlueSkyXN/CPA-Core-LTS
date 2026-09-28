package integration

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type fixtureUsageSink struct{ records chan coreusage.Record }

func (s *fixtureUsageSink) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Provider == "zcode-coding-plan" {
		s.records <- record
	}
}

type fixtureTransport struct {
	mu                 sync.Mutex
	pub                ed25519.PublicKey
	priv               ed25519.PrivateKey
	handshakes, models int
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.URL.Host != "open.bigmodel.cn" && req.URL.Host != "api.z.ai" {
		return nil, fmt.Errorf("unexpected host blocked")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	text := ""
	ct := "application/json"
	if strings.HasSuffix(req.URL.Path, "/client") {
		f.handshakes++
		key, _ := hkdf.Key(sha256.New, []byte("synthetic-secret"), []byte("WD_CLIENT_SIGN_KDF_SALT"), "ed25519_priv", 32)
		block, _ := aes.NewCipher(key)
		gcm, _ := cipher.NewGCM(block)
		iv := bytes.Repeat([]byte{7}, 12)
		der, _ := x509.MarshalPKCS8PrivateKey(f.priv)
		ciphertext := append(iv, gcm.Seal(nil, iv, []byte(base64.StdEncoding.EncodeToString(der)), []byte("synthetic-key"))...)
		data, _ := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"privateCipher": base64.StdEncoding.EncodeToString(ciphertext)}})
		text = string(data)
	} else {
		f.models++
		var b map[string]any
		if json.Unmarshal(body, &b) != nil {
			return nil, fmt.Errorf("invalid body")
		}
		h := req.Header
		sig, _ := base64.StdEncoding.DecodeString(h.Get("X-Client-Sig"))
		if !ed25519.Verify(f.pub, []byte("synthetic-key\n"+h.Get("X-Client-Ts")+"\n3.14.3\n"+h.Get("X-Session-Id")+"\n"+h.Get("X-Client-Nonce")), sig) {
			return nil, fmt.Errorf("invalid signature")
		}
		if b["system"] != "caller" {
			return nil, fmt.Errorf("caller system lost")
		}
		text = `{"type":"message","id":"synthetic-message","model":"test-model","role":"assistant","content":[{"type":"text","text":"fixture-ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":2}}`
		if b["stream"] == true {
			ct = "text/event-stream"
			text = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"id\":\"synthetic-message\",\"model\":\"test-model\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture-ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(text)), Request: req}, nil
}
func TestCPAExecutorWithNoNetworkTransport(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	library := os.Getenv("CP_PLUGIN_LIBRARY")
	if library == "" {
		t.Skip("CP_PLUGIN_LIBRARY required")
	}
	dir := t.TempDir()
	lib, err := os.ReadFile(library)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "zcode-coding-plan"+filepath.Ext(library)), lib, 0700)
	cfg, err := config.ParseConfigBytes([]byte(fmt.Sprintf("plugins:\n  enabled: true\n  dir: %q\n  configs:\n    zcode-coding-plan:\n      enabled: true\n      host_logging_disabled: true\n      models: [\"test-model\"]\nrequest-log: false\n", dir)))
	if err != nil {
		t.Fatal(err)
	}
	h := pluginhost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer h.ShutdownAll()
	h.ApplyConfig(ctx, cfg)
	storage, _ := json.Marshal(map[string]any{"type": "zcode-coding-plan", "api_key": "synthetic-key.synthetic-secret", "device_id": "synthetic-device", "request_retry": 0})
	auth, handled, err := h.ParseAuth(ctx, pluginapi.AuthParseRequest{RawJSON: storage, FileName: "synthetic.json"})
	if err != nil || !handled {
		t.Fatalf("auth %v", err)
	}
	auth.ID = "fixture-auth"
	auth.Index = "fixture-index"
	models := h.ModelsForAuth(ctx, auth)
	if models.Err != nil {
		t.Fatal(models.Err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h.RegisterExecutors(manager, nil)
	executor, ok := manager.Executor("zcode-coding-plan")
	if !ok {
		t.Fatal("executor missing")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	transport := &fixtureTransport{pub: pub, priv: priv}
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
	sink := &fixtureUsageSink{records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin("coding-plan-fixture", sink)
	for _, stream := range []bool{false, true} {
		req := coreexecutor.Request{Model: "test-model", Payload: []byte(fmt.Sprintf(`{"model":"test-model","max_tokens":10,"stream":%t,"system":"caller","messages":[{"role":"user","content":"hello"}]}`, stream))}
		opts := coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatClaude, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: fmt.Sprintf("fixture-%v", stream)}}
		var body []byte
		if stream {
			result, err := executor.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				body = append(body, chunk.Payload...)
			}
		} else {
			result, err := executor.Execute(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			body = result.Payload
		}
		if !bytes.Contains(body, []byte("fixture-ok")) {
			t.Fatal("response lost")
		}
		select {
		case record := <-sink.records:
			if record.Failed || record.AuthIndex != "fixture-index" || record.Detail.TotalTokens != 12 || record.UsageProvenance != "provider_reported_unverified" {
				t.Fatal("usage attribution mismatch")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("host did not publish usage")
		}
	}
	select {
	case <-sink.records:
		t.Fatal("usage counted twice")
	default:
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.handshakes != 1 || transport.models != 2 {
		t.Fatalf("unexpected calls %d %d", transport.handshakes, transport.models)
	}
}
