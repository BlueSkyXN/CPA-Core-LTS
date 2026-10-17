package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// The whole HTTP path, signing and native plugin run locally. No transport in
// this fixture can send a request to the network.
type contextCountTransport struct {
	base                            *v2Transport
	mu                              sync.Mutex
	counts, generations             int
	countBody, generationBody       []byte
	countSession, generationSession string
}

func (f *contextCountTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return f }
func (f *contextCountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/client") {
		return f.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(req.URL.Path, "/count_tokens") {
		response, err := f.base.fixtureTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		response.Body = io.NopCloser(strings.NewReader(`{"input_tokens":101100}`))
		f.counts++
		f.countBody = bytes.Clone(body)
		f.countSession = req.Header.Get("X-Session-Id")
		return response, nil
	}
	response, err := f.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	f.generations++
	f.generationBody = bytes.Clone(body)
	f.generationSession = req.Header.Get("X-Session-Id")
	message := `{"type":"message","id":"fixture-search","role":"assistant","model":"GLM-5.3-Flash","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"before search","signature":"opaque-fixture"},{"type":"server_tool_use","id":"srvtoolu_fixture","name":"web_search_prime","input":{"search_query":"fixture"}},{"type":"tool_result","tool_use_id":"srvtoolu_fixture","content":"[{'text': [{'refer': 'ref_1', 'title': 'Fixture', 'link': 'https://example.com/', 'content': 'retained search body'}]}]"},{"type":"text","text":"delivered answer"}],"usage":{"input_tokens":400000,"cache_read_input_tokens":105000,"output_tokens":500}}`
	if gjson.GetBytes(body, "stream").Bool() {
		root := gjson.Parse(message)
		var stream strings.Builder
		emit := func(value any) { raw, _ := json.Marshal(value); fmt.Fprintf(&stream, "data: %s\n\n", raw) }
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "fixture-search", "type": "message", "role": "assistant", "content": []any{}, "usage": json.RawMessage(root.Get("usage").Raw)}})
		for i, block := range root.Get("content").Array() {
			emit(map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(block.Raw)})
			emit(map[string]any{"type": "content_block_stop", "index": i})
		}
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 500}})
		emit(map[string]any{"type": "message_stop"})
		message = stream.String()
		response.Header.Set("Content-Type", "text/event-stream")
	}
	response.Body = io.NopCloser(strings.NewReader(message))
	return response, nil
}

type blockedContextNetwork struct{}

func (blockedContextNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network disabled in context count fixture")
}

func TestZcodeContextUsageHTTPAndNativeCount(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	prior := http.DefaultTransport
	http.DefaultTransport = blockedContextNetwork{}
	t.Cleanup(func() { http.DefaultTransport = prior })
	f := newV2Fixture(t)
	// A single plugin slot proves that counting waits for generation cleanup.
	pluginConfig := f.cfg.Plugins.Configs["zcode-coding-plan"]
	var settings map[string]any
	if err := pluginConfig.Raw.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	settings["max_inflight"] = 1
	if err := pluginConfig.Raw.Encode(settings); err != nil {
		t.Fatal(err)
	}
	f.cfg.Plugins.Configs["zcode-coding-plan"] = pluginConfig
	transport := &contextCountTransport{base: f.transport}
	f.manager.SetRoundTripperProvider(transport)
	for _, stream := range []bool{false, true} {
		for _, mode := range []config.CodexZcodeContextUsageMode{config.ZcodeContextUsageDisabled, config.ZcodeContextUsageObserve, config.ZcodeContextUsageCorrect} {
			f.cfg.Codex.ZcodeContextUsage = mode
			f.host.ApplyConfig(context.Background(), f.cfg)
			before := transport.counts
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"instructions":"caller","input":"hello","stream":%t}`, v2Model, stream))).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "codex_cli_rs/0.1")
			res := httptest.NewRecorder()
			f.router.ServeHTTP(res, req)
			cancel()
			if res.Code != http.StatusOK {
				t.Fatalf("mode=%s stream=%t HTTP=%d %s", mode, stream, res.Code, res.Body.String())
			}
			body := res.Body.Bytes()
			if stream {
				for _, line := range strings.Split(res.Body.String(), "\n") {
					data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if gjson.Get(data, "type").String() == "response.completed" {
						body = []byte(gjson.Get(data, "response").Raw)
					}
				}
			}
			want := int64(505500)
			if mode == config.ZcodeContextUsageCorrect {
				want = 101100
			}
			if got := gjson.GetBytes(body, "usage.total_tokens").Int(); got != want {
				t.Fatalf("mode=%s stream=%t total=%d want=%d", mode, stream, got, want)
			}
			if mode == config.ZcodeContextUsageDisabled {
				if transport.counts != before {
					t.Fatal("disabled mode counted")
				}
			} else {
				if transport.counts != before+1 {
					t.Fatal("native count missing")
				}
				if transport.countSession == "" || transport.generationSession == "" {
					t.Fatal("signed session missing")
				}
				if gjson.GetBytes(transport.countBody, "stream").Exists() || gjson.GetBytes(transport.countBody, "max_tokens").Exists() {
					t.Fatal("count retained generation controls")
				}
				if !bytes.Contains(transport.countBody, []byte("retained search body")) || !bytes.Contains(transport.countBody, []byte("before search")) || !bytes.Contains(transport.countBody, []byte("delivered answer")) {
					t.Fatal("delivered context was not replayed")
				}
			}
			select {
			case record := <-f.usage.records:
				if record.Failed || record.Detail.TotalTokens != 505500 || record.Detail.CachedTokens != 105000 {
					t.Fatalf("provider usage changed: %+v", record.Detail)
				}
				if record.Size.UpstreamRequestBytes != int64(len(transport.generationBody)) {
					t.Fatalf("count polluted request size: %+v", record.Size)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing usage")
			}
		}
	}
	if transport.generations != 6 {
		t.Fatal("generation retried", transport.generations)
	}
}
