package executor

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const nativeCacheRequest = `{"model":"gpt-6-luna","store":true,"generate":true,"previous_response_id":"resp_baseline","max_output_tokens":1,"reasoning":{"effort":"high"},"context_management":[{"type":"compaction","compact_threshold":8000}],"prompt_cache_options":{"mode":"implicit","comparison_response_id":"resp_baseline","ttl":"30m"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":[{"type":"input_text","text":"question","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`
const nativeCacheCompleted = `{"type":"response.completed","response":{"id":"resp_actual","object":"response","status":"completed","model":"gpt-6-luna","output":[{"type":"message","role":"assistant","id":"msg_answer","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024,"cache_write_tokens":1024},"output_tokens":1,"total_tokens":2049},"prompt_cache_diagnostics":{"type":"cache_hit"},"input_transformations":[]}}`

func nativeAPIAuth() *auth.Auth {
	return &auth.Auth{ID: "native-api-test", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": "https://api.openai.com/v1", "auth_kind": auth.AuthKindAPIKey}}
}
func assertNativeCacheFields(t *testing.T, body []byte) {
	t.Helper()
	for _, path := range []string{"store", "generate", "previous_response_id", "max_output_tokens", "reasoning.effort", "context_management", "prompt_cache_options", "input"} {
		if got, want := ojson.GetBytesNoCopy(body, path).Raw, ojson.Get(nativeCacheRequest, path).Raw; got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	if ojson.GetBytesNoCopy(body, "prompt_cache_key").Exists() {
		t.Error("native API request unexpectedly acquired PCK")
	}
	if ojson.GetBytesNoCopy(body, "tools").Exists() {
		t.Error("native API request unexpectedly acquired tools")
	}
}

func TestNativeResponsesAPIKeyWire(t *testing.T) {
	for _, source := range []translator.Format{translator.FormatOpenAIResponse, translator.FormatCodex} {
		t.Run(source.String(), func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					var captured []byte
					rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
						captured, _ = io.ReadAll(r.Body)
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + nativeCacheCompleted + "\n\n")), Request: r}, nil
					})
					ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", rt)
					e := NewCodexExecutor(&config.Config{})
					req := exec.Request{Model: "gpt-6-luna", Payload: []byte(nativeCacheRequest)}
					opts := exec.Options{SourceFormat: source, Stream: stream}
					var output []byte
					if stream {
						res, err := e.ExecuteStream(ctx, nativeAPIAuth(), req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for c := range res.Chunks {
							if c.Err != nil {
								t.Fatal(c.Err)
							}
							output = append(output, c.Payload...)
						}
					} else {
						res, err := e.Execute(ctx, nativeAPIAuth(), req, opts)
						if err != nil {
							t.Fatal(err)
						}
						output = res.Payload
					}
					assertNativeCacheFields(t, captured)
					for _, field := range []string{"prompt_cache_diagnostics", "cache_write_tokens", "input_transformations", "resp_actual"} {
						if !strings.Contains(string(output), field) {
							t.Errorf("response lost %s: %s", field, output)
						}
					}
				})
			}
		})
	}
}

func TestNativeResponsesWebsocketPreparation(t *testing.T) {
	e := NewCodexWebsocketsExecutor(&config.Config{})
	p, err := e.prepareCodexWebsocketStream(t.Context(), nativeAPIAuth(), exec.Request{Model: "gpt-6-luna", Payload: []byte(nativeCacheRequest)}, exec.Options{SourceFormat: translator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	assertNativeCacheFields(t, p.upstreamBody)
	if p.wsURL != "wss://api.openai.com/v1/responses" {
		t.Fatalf("unexpected URL %q", p.wsURL)
	}
}

func TestNativeResponsesWebsocketInstructionsSnapshot(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"instructions": "configured"},
	}}}}
	e := NewCodexWebsocketsExecutor(cfg)
	for _, tc := range []struct{ name, body, want string }{
		{"absent", `{"input":[]}`, ""},
		{"empty", `{"input":[],"instructions":""}`, `""`},
		{"null", `{"input":[],"instructions":null}`, `null`},
		{"explicit", `{"input":[],"instructions":"original"}`, `"original"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := exec.Request{Model: "gpt-6-luna", Payload: []byte(tc.body)}
			prepared, err := e.prepareCodexWebsocketStream(t.Context(), nativeAPIAuth(), req, exec.Options{SourceFormat: translator.FormatOpenAIResponse})
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytes(prepared.upstreamBody, "instructions").String(); got != "configured" {
				t.Fatalf("outbound instructions = %q, want configured", got)
			}
			if got := prepared.unconfiguredInstructions; got != tc.want {
				t.Errorf("duplex instructions snapshot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNativeResponsesEndpointBoundary(t *testing.T) {
	a := nativeAPIAuth()
	for _, endpoint := range []string{"https://api.openai.com.evil.invalid/v1", "http://api.openai.com/v1", "https://chatgpt.com/backend-api/codex", "https://api.openai.com:444/v1", "https://user@api.openai.com/v1", "https://api.openai.com/v1/responses-unknown"} {
		if nativeOpenAIResponses(a, endpoint) {
			t.Errorf("accepted %s", endpoint)
		}
	}
	oauth := &auth.Auth{Provider: "codex", Attributes: map[string]string{"auth_kind": auth.AuthKindOAuth}}
	if nativeOpenAIResponses(oauth, "https://api.openai.com/v1") {
		t.Error("OAuth must retain its backend compatibility")
	}
}

func TestNativeResponsesExplicitPCKWithIdentityConfuse(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{IdentityConfuse: true}}
	e := NewCodexWebsocketsExecutor(cfg)
	body, _ := ojson.SetBytes([]byte(nativeCacheRequest), "prompt_cache_key", "explicit-synthetic")
	p, err := e.prepareCodexWebsocketStream(t.Context(), nativeAPIAuth(), exec.Request{Model: "gpt-6-luna", Payload: body}, exec.Options{SourceFormat: translator.FormatOpenAIResponse, Headers: http.Header{"X-Session-Id": {"synthetic-session"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ojson.GetBytes(p.upstreamBody, "prompt_cache_key").String(); got != "explicit-synthetic" {
		t.Fatalf("PCK changed to %q", got)
	}
	request, out, _, err := e.cacheHelper(t.Context(), translator.FormatOpenAIResponse, "https://api.openai.com/v1/responses", nativeAPIAuth(), exec.Request{Payload: body}, body, body)
	if err != nil {
		t.Fatal(err)
	}
	_ = request.Body.Close()
	if ojson.GetBytes(out, "prompt_cache_key").String() != "explicit-synthetic" {
		t.Fatal("HTTP PCK changed")
	}
}

// Reuse a local connected socket under the exact native connection key. This
// exercises writes and reads without DNS, TLS or requests to the public API.
func TestNativeResponsesWebsocketWire(t *testing.T) {
	cases := append([]nativeProtocolRequest{
		{"responses", translator.FormatOpenAIResponse, nativeCacheRequest},
		{"codex", translator.FormatCodex, nativeCacheRequest},
	}, nativeCrossProtocolRequests()...)
	for _, tc := range cases {
		for _, terminal := range []string{"response.completed", "response.done"} {
			for _, stream := range []bool{false, true} {
				t.Run(tc.name+"/"+terminal+"/"+map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
						if err != nil {
							return
						}
						defer conn.Close()
						_, body, err := conn.ReadMessage()
						if err != nil {
							return
						}
						captured <- body
						_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Replace(nativeCacheCompleted, "response.completed", terminal, 1)))
						for {
							if _, _, err := conn.ReadMessage(); err != nil {
								return
							}
						}
					}))
					defer server.Close()
					conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
					if err != nil {
						t.Fatal(err)
					}
					e := NewCodexWebsocketsExecutor(&config.Config{})
					e.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
					const sessionID = "synthetic-native-session"
					const model = "gpt-6-luna"
					a := nativeAPIAuth()
					sess := e.getOrCreateSession(sessionID)
					sess.conn = conn
					sess.connGen = 1
					sess.connKey = newCodexWebsocketConnectionKey(a.ID, "wss://api.openai.com/v1/responses", model, resolveCodexModelHeaderProfile(model).digest)
					sess.wsURL = sess.connKey.wsURL
					sess.authID = a.ID
					sess.connCloser = newWebsocketConnectionCloser(conn)
					defer e.CloseExecutionSession(sessionID)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					store := diagnostics.NewStore()
					ctx = store.Begin(ctx, "logical", "caller", "session", model, "responses", []byte(tc.body))
					req := exec.Request{Model: model, Payload: []byte(tc.body)}
					opts := exec.Options{SourceFormat: tc.from, Stream: stream, Metadata: map[string]any{exec.ExecutionSessionMetadataKey: sessionID}}
					if stream {
						res, err := e.ExecuteStream(ctx, a, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else {
						if _, err := e.Execute(ctx, a, req, opts); err != nil {
							t.Fatal(err)
						}
					}
					if tc.from == translator.FormatOpenAIResponse || tc.from == translator.FormatCodex {
						assertNativeCacheFields(t, <-captured)
					} else {
						assertNativeCrossProtocolInput(t, <-captured)
					}
					trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
					if len(trace.Attempts) != 1 {
						t.Fatalf("attempts=%d", len(trace.Attempts))
					}
					attempt := trace.Attempts[0]
					if attempt.Outcome != "completed" || attempt.ObservationState != "observed" || attempt.Endpoint != "openai_public" || attempt.ResponseID != "resp_actual" || attempt.Diagnostic == nil || attempt.CacheObservation == nil || *attempt.CacheObservation.Read.Value != 1024 {
						t.Fatalf("WS observation=%+v", attempt)
					}
				})
			}
		}
	}
}
