package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	authpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const affinityCompleted = `{"type":"response.completed","response":{"id":"synthetic","status":"completed","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`

type affinityWire struct {
	headers http.Header
	body    []byte
}

func affinityTestServer(t *testing.T, ws bool) (*httptest.Server, chan affinityWire) {
	t.Helper()
	captured := make(chan affinityWire, 128)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws {
			c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				_, b, err := c.ReadMessage()
				if err != nil {
					return
				}
				captured <- affinityWire{r.Header.Clone(), b}
				if c.WriteMessage(websocket.TextMessage, []byte(affinityCompleted)) != nil {
					return
				}
			}
		}
		b, _ := io.ReadAll(r.Body)
		captured <- affinityWire{r.Header.Clone(), b}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", affinityCompleted)
	})), captured
}
func affinityTestAuth(url, kind string) *authpkg.Auth {
	return &authpkg.Auth{ID: "synthetic-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": kind, "base_url": url}, Metadata: map[string]any{"access_token": "synthetic"}}
}
func affinityTestConfig(strategy string) *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, Codex: config.CodexConfig{CacheAffinity: config.CodexCacheAffinityConfig{Strategy: strategy}}}
}

func TestCodexCacheAffinityFinalWireProtocolMatrix(t *testing.T) {
	protocols := []struct {
		name   string
		format translator.Format
		body   string
	}{
		{"responses", translator.FormatOpenAIResponse, `{"model":"gpt-5.6-sol","input":[{"role":"user","content":"synthetic question"}]}`},
		{"codex", translator.FormatCodex, `{"model":"gpt-5.6-sol","input":[{"role":"user","content":"synthetic question"}]}`},
		{"chat", translator.FormatOpenAI, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"synthetic question"}]}`},
		{"claude", translator.FormatClaude, `{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"synthetic question"}]}`},
		{"gemini", translator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"synthetic question"}]}]}`},
		{"interactions", translator.FormatInteractions, `{"model":"gpt-5.6-sol","input":"synthetic question"}`},
	}
	for _, p := range protocols {
		for _, strategy := range []string{"stable-id", "client-aware"} {
			for _, transport := range []string{"http", "sse", "ws", "ws-stream"} {
				for _, kind := range []string{"oauth", "apikey", "unknown"} {
					t.Run(p.name+"/"+strategy+"/"+transport+"/"+kind, func(t *testing.T) {
						server, ch := affinityTestServer(t, strings.HasPrefix(transport, "ws"))
						defer server.Close()
						e := NewCodexExecutor(affinityTestConfig(strategy))
						we := NewCodexWebsocketsExecutor(e.cfg)
						auth := affinityTestAuth(server.URL, kind)
						if kind == "unknown" {
							auth.Metadata = nil
						}
						if kind == "apikey" {
							auth.Attributes["api_key"] = "synthetic"
						}
						var previous string
						for n := 0; n < 2; n++ {
							req := execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(p.body), Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller-a", execpkg.RequestIDMetadataKey: fmt.Sprint(n)}}
							opts := execpkg.Options{SourceFormat: p.format, Headers: http.Header{"X-Session-Id": {"zcode-session"}, "User-Agent": {"ZCode/3.14.0"}}}
							var err error
							switch transport {
							case "http":
								_, err = e.Execute(context.Background(), auth, req, opts)
							case "ws":
								_, err = we.Execute(context.Background(), auth, req, opts)
							default:
								var stream *execpkg.StreamResult
								if transport == "sse" {
									stream, err = e.ExecuteStream(context.Background(), auth, req, opts)
								} else {
									stream, err = we.ExecuteStream(context.Background(), auth, req, opts)
								}
								if err == nil {
									for c := range stream.Chunks {
										if c.Err != nil {
											err = c.Err
										}
									}
								}
							}
							if err != nil {
								t.Fatal(err)
							}
							select {
							case wire := <-ch:
								key := wire.headers.Get("Session-Id")
								if kind == "oauth" && key == "" {
									t.Fatal("missing final Session-Id")
								}
								if kind == "oauth" && n > 0 && key != previous {
									t.Fatal("unstable final Session-Id")
								}
								if kind != "oauth" && key == affinityUUID("caller-a", "x-session-id", "zcode-session") {
									t.Fatal("excluded credential entered adapter")
								}
								previous = key
								if gjson.GetBytes(wire.body, "prompt_cache_key").Exists() {
									t.Fatalf("unexpected synthesized pck: %s", p.name)
								}
							case <-time.After(3 * time.Second):
								t.Fatal("no final request")
							}
						}
					})
				}
			}
		}
	}
}

func TestCodexCacheAffinityCredentialsAndLegacyFinalWire(t *testing.T) {
	for _, kind := range []string{"oauth", "apikey", "unknown"} {
		for _, strategy := range []string{"legacy", "stable-id", "client-aware"} {
			t.Run(kind+"/"+strategy, func(t *testing.T) {
				server, ch := affinityTestServer(t, false)
				defer server.Close()
				e := NewCodexExecutor(affinityTestConfig(strategy))
				a := affinityTestAuth(server.URL, kind)
				if kind == "unknown" {
					a.Metadata = nil
				}
				if kind == "apikey" {
					a.Attributes["api_key"] = "synthetic-api-key"
				}
				var keys []string
				for n := 0; n < 2; n++ {
					_, err := e.Execute(context.Background(), a, execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"input":"synthetic"}`), Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller"}}, execpkg.Options{SourceFormat: translator.FormatOpenAIResponse, Headers: http.Header{"X-Session-Id": {"x"}}})
					if err != nil {
						t.Fatal(err)
					}
					wire := <-ch
					keys = append(keys, wire.headers.Get("Session-Id"))
					if gjson.GetBytes(wire.body, "prompt_cache_key").Exists() {
						t.Fatal("unexpected pck")
					}
				}
				expected := affinityUUID("caller", "x-session-id", "x")
				if kind == "oauth" && strategy != "legacy" {
					if keys[0] != expected || keys[1] != expected {
						t.Fatal("OAuth mapping missing")
					}
				} else if keys[0] == expected || keys[1] == expected {
					t.Fatal("excluded credentials entered adapter")
				}
			})
		}
	}
}

func TestCodexCacheAffinityNativeSemantics(t *testing.T) {
	for _, mode := range []string{"strict", "repair", "off"} {
		for _, test := range []struct{ name, pck, header, real string }{{"root", "P", "P", "P"}, {"fork", "P", "P", "F"}, {"guardian", "guardian:P", "P", "P"}, {"subagent", "P", "P", "P"}} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				cfg := affinityTestConfig("client-aware")
				cfg.Codex.ClientMetadata.Mode = mode
				e := NewCodexExecutor(cfg)
				metadata := fmt.Sprintf(`{"request_kind":"turn","session_id":%q,"thread_id":"child","window_id":"window"}`, test.real)
				raw := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"synthetic","prompt_cache_key":%q,"client_metadata":{"x-codex-turn-metadata":%q}}`, test.pck, metadata))
				headers := http.Header{"Session-Id": {test.header}, "User-Agent": {"codex-cli/1"}, "Originator": {"codex_cli"}}
				request, body, state, err := e.cacheHelper(context.Background(), translator.FormatCodex, "https://synthetic.invalid/responses", affinityTestAuth("", "oauth"), execpkg.Request{Payload: raw}, raw, raw, headers)
				if err != nil {
					t.Fatal(err)
				}
				applyCodexHeaders(request, affinityTestAuth("", "oauth"), "synthetic", true, cfg, headers)
				applyFinalCodexClientHeaders(request.Header, resolveCodexModelHeaderProfile("gpt-5.6-sol"), affinityTestAuth("", "oauth"))
				applyCodexOutboundMetadataHeaders(request.Header, &state)
				if got := codexSessionHeaderValue(request.Header); got != test.header {
					t.Fatalf("header=%q expected=%q", got, test.header)
				}
				if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != test.pck {
					t.Fatal("pck changed")
				}
				if state.clientMetadata.SessionID != test.real {
					t.Fatal("real identity changed")
				}
			})
		}
	}
}
func affinitySnapshot(input string) codexAffinitySnapshot {
	return codexAffinitySnapshotOf([]byte(`{"model":"m","instructions":"i","input":` + input + `}`))
}
func TestCodexCacheAffinityPrefixAdoption(t *testing.T) {
	a := affinitySnapshot(`[{"role":"user","content":"unique task"},{"role":"assistant","content":"answer"}]`)
	b := affinitySnapshot(`[{"role":"user","content":"unique task"},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]`)
	s := newCodexAffinityStore()
	ctx := context.Background()
	first := s.decide(ctx, "scope", "id-a", "", "r1", "stable-a", "", true, a)
	first.complete([]byte(affinityCompleted))
	first.close()
	next := s.decide(ctx, "scope", "", "", "r2", "fallback", "", false, b)
	defer next.close()
	if next.group != "stable-a" || !next.adopted {
		t.Fatal("continuation not adopted")
	}
	explicit := s.decide(ctx, "scope", "id-b", "", "r3", "stable-b", "", true, b)
	defer explicit.close()
	if explicit.group != "stable-b" {
		t.Fatal("different reliable identity merged")
	}
	fork := s.decide(ctx, "scope", "id-child", "id-a", "r4", "child", "", true, b)
	defer fork.close()
	if fork.group != "stable-a" {
		t.Fatal("explicit fork not inherited")
	}
	fork.complete([]byte(affinityCompleted))
	edited := s.decide(ctx, "scope", "id-child", "", "r5", "child", "", true, affinitySnapshot(`[{"role":"user","content":"edited"}]`))
	defer edited.close()
	if edited.group != "stable-a" {
		t.Fatal("established binding rotated after edit")
	}
	other := s.decide(ctx, "other-scope", "", "", "r6", "other", "", false, b)
	defer other.close()
	if other.group == "stable-a" {
		t.Fatal("cross-domain match")
	}
}
func TestCodexCacheAffinityAmbiguityAndAnchors(t *testing.T) {
	ctx := context.Background()
	s := newCodexAffinityStore()
	a := affinitySnapshot(`[{"role":"user","content":"shared"},{"role":"assistant","content":"answer"}]`)
	for _, group := range []string{"g1", "g2"} {
		d := s.decide(ctx, "s", group, "", group, group, "", true, a)
		d.complete([]byte(affinityCompleted))
		d.close()
	}
	d := s.decide(ctx, "s", "", "", "ambiguous", "fallback", "g1", false, a)
	defer d.close()
	if d.group == "g1" || d.group == "g2" || d.adopted {
		t.Fatal("ambiguous match adopted")
	}
	s = newCodexAffinityStore()
	short := affinitySnapshot(`[{"role":"user","content":"hello"}]`)
	d = s.decide(ctx, "s", "id", "", "r1", "g1", "", true, short)
	d.complete([]byte(affinityCompleted))
	d.close()
	d = s.decide(ctx, "s", "", "", "r2", "fallback", "", false, affinitySnapshot(`[{"role":"user","content":"hello"},{"role":"assistant","content":"different answer"}]`))
	defer d.close()
	if d.group == "g1" {
		t.Fatal("generic greeting merged without anchor")
	}
	d = s.decide(ctx, "s", "", "", "r3", "fallback", "", false, short)
	defer d.close()
	if d.group != "g1" {
		t.Fatal("exact resend not recognized")
	}
}
func TestCodexCacheAffinityStrictContent(t *testing.T) {
	base := `{"model":"m","input":[{"role":"user","content":"x"},{"role":"assistant","content":"2026-09-20 uuid-a"}],"tools":[{"name":"a"},{"name":"b"}],"reasoning":{"effort":"high"}}`
	first := codexAffinitySnapshotOf([]byte(base))
	if !first.known {
		t.Fatal("known input rejected")
	}
	for _, changed := range []string{strings.Replace(base, "uuid-a", "uuid-b", 1), strings.Replace(base, "2026-09-20", "2026-09-21", 1), strings.Replace(base, "high", "low", 1), strings.Replace(base, `{"name":"a"},{"name":"b"}`, `{"name":"b"},{"name":"a"}`, 1)} {
		got := codexAffinitySnapshotOf([]byte(changed))
		if first.items[len(first.items)-1] == got.items[len(got.items)-1] {
			t.Fatal("strict difference masked")
		}
	}
	for _, raw := range []string{`{"input":[{"type":"input_image","image_url":"https://mutable.invalid/a"}]}`, `{"input":[{"type":"input_file","file_id":"file-x"}]}`, `{"input":[],"input":["x"]}`, `{"input":["x"],"previous_response_id":"r"}`} {
		if codexAffinitySnapshotOf([]byte(raw)).known {
			t.Fatal("unverifiable input accepted")
		}
	}
	if !codexAffinitySnapshotOf([]byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,eA==","detail":"high"}]}`)).known {
		t.Fatal("inline image rejected")
	}
}
func TestCodexCacheAffinityConcurrencyRetryAndGeneration(t *testing.T) {
	s := newCodexAffinityStore()
	ctx := context.Background()
	snap := affinitySnapshot(`[{"role":"user","content":"x"}]`)
	var wg sync.WaitGroup
	groups := make(chan string, 32)
	decisions := make(chan *codexAffinityDecision, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := s.decide(ctx, "s", "", "", fmt.Sprint(i), "temporary", "", false, snap)
			groups <- d.group
			decisions <- d
		}(i)
	}
	wg.Wait()
	close(groups)
	first := ""
	for g := range groups {
		if first != "" && g != first {
			t.Fatal("cold reservation split")
		}
		first = g
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	retry := s.decide(ctx, "s", "", "", "0", "new", "", false, codexAffinitySnapshot{})
	if retry.group != first {
		t.Fatal("active retry evicted")
	}
	retry.close()
	s.resetGeneration()
	close(decisions)
	for d := range decisions {
		d.complete([]byte(affinityCompleted))
		d.close()
	}
	if len(s.trajectories) != 0 {
		t.Fatal("late completion crossed generation")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	d := s.decide(cancelCtx, "s", "i", "", "cancel", "g", "", true, snap)
	cancel()
	d.complete([]byte(affinityCompleted))
	d.close()
	if len(s.trajectories) != 0 {
		t.Fatal("cancelled trajectory published")
	}
}
func TestCodexCacheAffinityReloadSharedState(t *testing.T) {
	e := NewCodexAutoExecutor(affinityTestConfig("client-aware"))
	if e.httpExec.affinity != e.wsExec.affinity {
		t.Fatal("transport store split")
	}
	same := NewCodexAutoExecutor(affinityTestConfig("client-aware"), e)
	if same.httpExec.affinity != e.httpExec.affinity {
		t.Fatal("reload lost store")
	}
	changed := NewCodexAutoExecutor(affinityTestConfig("legacy"), same)
	if changed.httpExec.affinityGeneration == same.httpExec.affinityGeneration {
		t.Fatal("strategy did not advance generation")
	}
}
func TestCodexCacheAffinityBudget(t *testing.T) {
	for _, size := range []int{512 << 10, 18 << 20, codexAffinityMaxBytes} {
		raw := []byte(`{"input":[{"role":"user","content":"` + strings.Repeat("a", size) + `"}]}`)
		snap := codexAffinitySnapshotOf(raw)
		if snap.known != (len(raw) <= codexAffinityMaxBytes) {
			t.Fatal("scan budget incorrect")
		}
	}
	items := strings.Repeat(`{"role":"user","content":"a"},`, codexAffinityMaxItems) + `{"role":"user","content":"z"}`
	if affinitySnapshot("[" + items + "]").known {
		t.Fatal("truncated input accepted")
	}
}

func TestCodexCacheAffinityIdentitySourcesAndCallerIsolation(t *testing.T) {
	for _, test := range []struct{ header, payload, kind string }{{"X-Session-Id", `{}`, "x-session-id"}, {"X-Thread-Id", `{}`, "x-thread-id"}, {"", `{"session_id":"same"}`, "session_id"}, {"", `{"conversation":{"id":"same"}}`, "conversation.id"}, {"", `{"metadata":{"user_id":"same"}}`, ""}, {"X-Request-Id", `{}`, ""}, {"X-Codex-Window-Id", `{}`, ""}} {
		h := make(http.Header)
		if test.header != "" {
			h.Set(test.header, "same")
		}
		kind, _, _ := codexAffinityIdentity(h, []byte(test.payload))
		if kind != test.kind {
			t.Fatalf("source %s classified %q", test.header, kind)
		}
	}
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"x"}]}`)
	var keys []string
	e := NewCodexExecutor(affinityTestConfig("client-aware"))
	for _, caller := range []string{"one", "two"} {
		r, _, state, err := e.cacheHelper(context.Background(), translator.FormatOpenAIResponse, "https://example.invalid/responses", affinityTestAuth("", "oauth"), execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: caller}}, raw, raw, http.Header{"X-Session-Id": {"same"}})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, r.Header.Get("Session-Id"))
		state.affinity.close()
	}
	if keys[0] == keys[1] {
		t.Fatal("cross caller identity collision")
	}
	for _, strategy := range []string{"stable-id", "client-aware"} {
		e = NewCodexExecutor(affinityTestConfig(strategy))
		req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CanonicalSessionIDMetadataKey: "request-id-is-not-session", execpkg.RequestIDMetadataKey: "logical"}}
		r, _, state, err := e.cacheHelper(context.Background(), translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if state.cacheSource != cacheTemporary || (state.affinity != nil && state.affinity.snapshot.known) {
			t.Fatal("canonical or missing caller trusted")
		}
		if r.Header.Get("Session-Id") == "request-id-is-not-session" {
			t.Fatal("canonical forwarded")
		}
	}
}

func TestCodexCacheAffinityXSIDCheckedButNotMerged(t *testing.T) {
	e := NewCodexExecutor(affinityTestConfig("client-aware"))
	ctx := context.Background()
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"task"},{"role":"assistant","content":"answer"}]}`)
	for n, id := range []string{"xsid-a", "xsid-b", "xsid-b"} {
		req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: fmt.Sprint(n)}}
		request, _, state, err := e.cacheHelper(ctx, translator.FormatOpenAIResponse, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, http.Header{"X-Session-Id": {id}, "User-Agent": {"ZCode/3"}})
		if err != nil {
			t.Fatal(err)
		}
		if state.affinity == nil || !state.affinity.snapshot.known {
			t.Fatal("XSID bypassed prefix check")
		}
		if n > 0 && state.affinity.check != "eligible" {
			t.Fatal("cross XSID continuity was not checked")
		}
		if request.Header.Get("Session-Id") != affinityUUID("caller", "x-session-id", id) {
			t.Fatal("reliable XSID merged by prefix")
		}
		state.affinity.complete([]byte(affinityCompleted))
		state.affinity.close()
	}
}

func TestCodexCacheAffinityPreservesMappingsAndReplay(t *testing.T) {
	ctx := context.Background()
	e := NewCodexExecutor(affinityTestConfig("client-aware"))
	a := affinityTestAuth("", "oauth")
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"task"}]}`)
	for _, test := range []struct {
		name    string
		format  translator.Format
		payload string
		meta    map[string]any
		h       http.Header
	}{
		{"execution", translator.FormatOpenAIResponse, string(raw), map[string]any{execpkg.ExecutionSessionMetadataKey: "execution"}, nil},
		{"claude", translator.FormatClaude, `{"metadata":{"user_id":"{\"session_id\":\"claude-session\",\"agent_id\":\"agent\"}"},"messages":[{"role":"user","content":"task"}]}`, nil, nil},
		{"native", translator.FormatCodex, string(raw), nil, http.Header{"Session-Id": {"native"}}},
		{"pck", translator.FormatOpenAIResponse, `{"prompt_cache_key":"explicit","input":"task"}`, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := execpkg.Request{Model: "m", Payload: []byte(test.payload), Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller"}}
			for k, v := range test.meta {
				req.Metadata[k] = v
			}
			base, b, err := oauthCacheBase(ctx, test.format, req, raw, test.h)
			if err != nil {
				t.Fatal(err)
			}
			opts := execpkg.Options{SourceFormat: test.format, Headers: test.h}
			replay := codexReasoningReplayScopeFromRequest(ctx, test.format, req, opts, b)
			b, state, err := prepareCodexOutboundMetadata(ctx, e.cfg, a, req.Payload, b, test.h)
			if err != nil {
				t.Fatal(err)
			}
			b, h := e.adaptOAuthCache(ctx, a, req, test.format, "endpoint", req.Payload, b, test.h, base, &state)
			defer state.affinity.close()
			if test.name == "native" {
				if codexSessionHeaderValue(h) != "native" {
					t.Fatal("native mapping changed")
				}
			} else if base.key == "" || gjson.GetBytes(b, "prompt_cache_key").String() != base.key {
				t.Fatal("healthy pck changed")
			}
			if test.name == "claude" {
				after := codexReasoningReplayScopeFromRequest(ctx, test.format, req, opts, b)
				if replay.sessionKey == "" || after != replay {
					t.Fatal("Claude replay scope changed")
				}
			}
			if _, ok := req.Metadata[execpkg.CanonicalSessionIDMetadataKey]; ok {
				t.Fatal("adapter wrote canonical identity")
			}
		})
	}
}

func TestCodexCacheAffinityConfigOverrideAndNativeConflict(t *testing.T) {
	ctx := context.Background()
	cfg := affinityTestConfig("client-aware")
	e := NewCodexExecutor(cfg)
	a := affinityTestAuth("", "oauth")
	a.Attributes["header:Session-Id"] = "config-session"
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"x"}]}`)
	req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller"}}
	r, _, state, err := e.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", a, req, raw, raw, http.Header{"X-Session-Id": {"id"}})
	if err != nil {
		t.Fatal(err)
	}
	applyCodexHeaders(r, a, "synthetic", true, cfg)
	applyCodexOutboundMetadataHeaders(r.Header, &state)
	state.verifyAffinityHeader(r.Header)
	defer state.affinity.close()
	if r.Header.Get("Session-Id") != "config-session" || state.affinity == nil || !state.affinity.publicationBlocked {
		t.Fatal("config header lost or indexed under wrong group")
	}
	raw = []byte(`{"model":"m","input":"x","prompt_cache_key":"P","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"F\"}"}}`)
	r, _, state, err = e.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", a, req, raw, raw, http.Header{"Session-Id": {"unrelated"}})
	if err != nil {
		t.Fatal(err)
	}
	applyCodexOutboundMetadataHeaders(r.Header, &state)
	if codexSessionHeaderValue(r.Header) != "F" {
		t.Fatal("arbitrary conflict bypassed canonical metadata")
	}
}

func TestCodexCacheAffinityLifecycleLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newCodexAffinityStore()
	snap := affinitySnapshot(`[{"role":"user","content":"x"}]`)
	d := s.decide(ctx, "account-a", "", "", "logical", "fallback", "", false, snap)
	g := d.group
	d.close()
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	retry := s.decide(ctx, "account-b", "", "", "logical", "other", "", false, snap)
	if retry.group != g {
		t.Fatal("logical retry changed account affinity")
	}
	retry.close()
	for i := len(s.requests); i < codexAffinityMaxTrajectories; i++ {
		s.requests[fmt.Sprint(i)] = &codexAffinityFrozen{refs: 1}
	}
	d = s.decide(ctx, "s", "", "", "overflow", "deterministic-fallback", "", false, snap)
	d.close()
	if d.group != "deterministic-fallback" || len(s.requests) > codexAffinityMaxTrajectories {
		t.Fatal("request admission limit failed")
	}
	s = newCodexAffinityStore()
	first := s.decide(ctx, "s", "", "", "a", "f", "shared-seed", false, snap)
	second := s.decide(ctx, "s", "", "", "b", "f", "shared-seed", false, affinitySnapshot(`[{"role":"user","content":"different"}]`))
	defer first.close()
	defer second.close()
	if first.group == second.group {
		t.Fatal("incompatible cold requests reused seed")
	}
}

func TestCodexCacheAffinityExcludesCompact(t *testing.T) {
	raw := []byte(`{"model":"m","input":"x"}`)
	e := NewCodexExecutor(affinityTestConfig("client-aware"))
	r, _, state, err := e.cacheHelper(context.Background(), translator.FormatCodex, "https://example.invalid/responses/compact", affinityTestAuth("", "oauth"), execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller"}}, raw, raw, http.Header{"X-Session-Id": {"id"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.affinity != nil || r.Header.Get("Session-Id") == affinityUUID("caller", "x-session-id", "id") {
		t.Fatal("compact entered adapter")
	}
}

func TestCodexCacheAffinityCompletedOutputAnchor(t *testing.T) {
	s := newCodexAffinityStore()
	ctx := context.Background()
	first := s.decide(ctx, "s", "", "", "one", "fallback", "", false, affinitySnapshot(`[{"role":"user","content":"task"}]`))
	first.complete([]byte(`{"type":"response.completed","response":{"output":[{"role":"assistant","content":"answer"}]}}`))
	first.close()
	next := s.decide(ctx, "s", "", "", "two", "fallback", "", false, affinitySnapshot(`[{"role":"user","content":"task"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]`))
	defer next.close()
	if next.group != first.group || !next.adopted {
		t.Fatal("completed output did not establish continuation anchor")
	}
}

func BenchmarkCodexCacheAffinitySnapshot(b *testing.B) {
	for _, size := range []int{512 << 10, 18 << 20, codexAffinityMaxBytes} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			raw := []byte(`{"input":[{"role":"user","content":"` + strings.Repeat("a", size-64) + `"}]}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = codexAffinitySnapshotOf(raw)
			}
		})
	}
}

func TestCodexCacheAffinityIndexLimitsAndTTL(t *testing.T) {
	s := newCodexAffinityStore()
	now := time.Now()
	snap := affinitySnapshot(`[{"role":"user","content":"task"},{"role":"assistant","content":"answer"}]`)
	for i := 0; i < codexAffinityMaxCandidates+1; i++ {
		s.publish("s", fmt.Sprint(i), fmt.Sprint(i), snap, now)
	}
	d := s.decide(context.Background(), "s", "", "", "request", "fallback", "", false, snap)
	d.close()
	if d.check != "unknown" || d.group != "fallback" {
		t.Fatal("candidate budget did not fail closed")
	}
	s = newCodexAffinityStore()
	for i := 0; i < codexAffinityMaxTrajectories+1; i++ {
		s.publish("s", "", fmt.Sprint(i), snap, now.Add(time.Duration(i)))
	}
	if len(s.trajectories) != codexAffinityMaxTrajectories || s.prefixCount != 2*codexAffinityMaxTrajectories {
		t.Fatal("trajectory LRU cap")
	}
	large := snap
	large.items = make([]string, codexAffinityMaxItems)
	large.anchors = make([]bool, codexAffinityMaxItems)
	for i := range large.items {
		large.items[i] = fmt.Sprint(i)
	}
	for i := 0; i < 300; i++ {
		s.publish("large", "", fmt.Sprint(i), large, now.Add(time.Second))
	}
	if s.prefixCount > codexAffinityMaxPrefixes {
		t.Fatal("prefix cap exceeded")
	}
	s.expire(now.Add(2 * time.Hour))
	if len(s.trajectories) != 0 || s.prefixCount != 0 || len(s.prefixes) != 0 {
		t.Fatal("TTL left orphan prefixes")
	}
}
func TestCodexCacheAffinityStrategyChangeKeepsActiveDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := NewCodexAutoExecutor(affinityTestConfig("stable-id"))
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"task"}]}`)
	req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "logical"}}
	r, _, first, err := old.httpExec.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := NewCodexAutoExecutor(affinityTestConfig("client-aware"), old)
	r2, _, second, err := next.httpExec.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.affinity.close()
	defer second.affinity.close()
	if r.Header.Get("Session-Id") != r2.Header.Get("Session-Id") {
		t.Fatal("strategy change rotated active request")
	}
	first.affinity.complete([]byte(affinityCompleted))
	if len(next.httpExec.affinity.trajectories) != 0 {
		t.Fatal("old callback polluted new index")
	}
}
func TestCodexCacheAffinityStrictJSONAndRouteAmbiguity(t *testing.T) {
	sameA := affinitySnapshot(`[{"role":"user","content":"a"}]`)
	sameB := affinitySnapshot(`[{"content":"\u0061","role":"user"}]`)
	if sameA.items[0] != sameB.items[0] {
		t.Fatal("JSON representation changed identity")
	}
	for _, raw := range []string{`{"input":[{"role":"user","role":"assistant","content":"a"}]}`, `{"input":[` + strings.Repeat("[", 70) + `1` + strings.Repeat("]", 70) + `]}`} {
		if codexAffinitySnapshotOf([]byte(raw)).known {
			t.Fatal("ambiguous/unbounded JSON trusted")
		}
	}
	if codexUnambiguousCacheRoute(http.Header{"Session-Id": {"P", "Q"}}, []byte(`{"prompt_cache_key":"P"}`), "P") {
		t.Fatal("conflicting native carriers accepted")
	}
	if codexUnambiguousCacheRoute(http.Header{"Session-Id": {"P"}}, []byte(`{"prompt_cache_key":"P","prompt_cache_key":"P"}`), "P") {
		t.Fatal("duplicate pck accepted")
	}
}
func TestCodexCacheAffinityExplicitKeySkipsPrefixScan(t *testing.T) {
	e := NewCodexExecutor(affinityTestConfig("client-aware"))
	raw := []byte(`{"model":"m","prompt_cache_key":"explicit","input":"` + strings.Repeat("a", 18<<20) + `"}`)
	req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "logical"}}
	_, _, state, err := e.cacheHelper(context.Background(), translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.affinity != nil {
		t.Fatal("explicit key entered prefix scan")
	}
}

func TestCodexCacheAffinityFailedLateCompletionAndPCKFreeze(t *testing.T) {
	s := newCodexAffinityStore()
	snap := affinitySnapshot(`[{"role":"user","content":"x"}]`)
	d := s.decide(context.Background(), "s", "i", "", "r", "g", "", true, snap)
	d.close()
	d.complete([]byte(affinityCompleted))
	if len(s.trajectories) != 0 || len(s.bindings) != 0 {
		t.Fatal("closed attempt published late success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := NewCodexAutoExecutor(affinityTestConfig("stable-id"))
	raw := []byte(`{"model":"m","input":[{"role":"user","content":"x"}]}`)
	req := execpkg.Request{Payload: raw, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "request", execpkg.DerivedSessionIDMetadataKey: "derived"}}
	r, b, first, err := old.httpExec.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := NewCodexAutoExecutor(affinityTestConfig("client-aware"), old)
	r2, b2, second, err := next.httpExec.cacheHelper(ctx, translator.FormatCodex, "https://example.invalid/responses", affinityTestAuth("", "oauth"), req, raw, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.affinity.close()
	defer second.affinity.close()
	if r.Header.Get("Session-Id") != r2.Header.Get("Session-Id") || gjson.GetBytes(b, "prompt_cache_key").String() == "" || gjson.GetBytes(b, "prompt_cache_key").String() != gjson.GetBytes(b2, "prompt_cache_key").String() {
		t.Fatal("H/K were not frozen together")
	}
}

func TestCodexCacheAffinityExcludedOptionsDoNotChangeLegacyBody(t *testing.T) {
	for _, kind := range []string{"apikey", "unknown", "oauth"} {
		server, ch := affinityTestServer(t, false)
		strategy := "client-aware"
		if kind == "oauth" {
			strategy = "legacy"
		}
		e := NewCodexExecutor(affinityTestConfig(strategy))
		a := affinityTestAuth(server.URL, kind)
		if kind == "unknown" {
			a.Metadata = nil
		}
		if kind == "apikey" {
			a.Attributes["api_key"] = "synthetic"
		}
		_, err := e.Execute(context.Background(), a, execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"input":"synthetic"}`)}, execpkg.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{execpkg.ExecutionSessionMetadataKey: "option-execution", execpkg.CallerScopeMetadataKey: "caller"}})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		wire := <-ch
		server.Close()
		if gjson.GetBytes(wire.body, "prompt_cache_key").Exists() {
			t.Fatal("excluded path gained an option-derived pck")
		}
	}
}

func TestCodexCacheAffinityLegacyRollbackKeepsInFlightWire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, ch := affinityTestServer(t, false)
	defer server.Close()
	old := NewCodexAutoExecutor(affinityTestConfig("client-aware"))
	req := execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"input":[{"role":"user","content":"synthetic"}]}`)}
	opts := execpkg.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "logical"}}
	_, err := old.Execute(ctx, affinityTestAuth(server.URL, "oauth"), req, opts)
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	next := NewCodexAutoExecutor(affinityTestConfig("legacy"), old)
	_, err = next.Execute(ctx, affinityTestAuth(server.URL, "oauth"), req, opts)
	if err != nil {
		t.Fatal(err)
	}
	second := <-ch
	if first.headers.Get("Session-Id") != second.headers.Get("Session-Id") {
		t.Fatal("legacy rollback changed frozen request")
	}
	opts.Metadata[execpkg.RequestIDMetadataKey] = "new-request"
	_, err = next.Execute(ctx, affinityTestAuth(server.URL, "oauth"), req, opts)
	if err != nil {
		t.Fatal(err)
	}
	third := <-ch
	if third.headers.Get("Session-Id") == first.headers.Get("Session-Id") {
		t.Fatal("new legacy request reused inferred group")
	}
}

func TestCodexCacheAffinityLongConversationUsesLongestBucket(t *testing.T) {
	s := newCodexAffinityStore()
	input := `[{"role":"user","content":"task"},{"role":"assistant","content":"answer"}`
	for i := 0; i < 90; i++ {
		input += fmt.Sprintf(`,{"role":"user","content":"turn-%d"}`, i)
		snap := affinitySnapshot(input + "]")
		s.publish("s", "", "one-group", snap, time.Now())
	}
	next := affinitySnapshot(input + `,{"role":"user","content":"next"}]`)
	d := s.decide(context.Background(), "s", "", "", "new", "fallback", "", false, next)
	defer d.close()
	if d.group != "one-group" || !d.adopted {
		t.Fatal("short common prefix exhausted budget before unique long prefix")
	}
}

func TestCodexCacheAffinityEstablishedIdentitySurvivesModelAccountChange(t *testing.T) {
	s := newCodexAffinityStore()
	ctx := context.Background()
	snap := affinitySnapshot(`[{"role":"user","content":"task"},{"role":"assistant","content":"answer"}]`)
	p := s.decide(ctx, "account-model-a", "caller-parent", "", "p", "parent-group", "", true, snap)
	p.complete([]byte(affinityCompleted))
	p.close()
	child := s.decide(ctx, "account-model-a", "caller-child", "caller-parent", "child", "child-fallback", "", true, snap)
	child.complete([]byte(affinityCompleted))
	child.close()
	next := s.decide(ctx, "account-model-b", "caller-child", "", "next", "child-fallback", "", true, snap)
	defer next.close()
	if child.group != "parent-group" || next.group != child.group {
		t.Fatal("reliable binding changed across model/account scope")
	}
	isolated := s.decide(ctx, "account-model-b", "other-caller-child", "", "isolated", "isolated-group", "", true, snap)
	defer isolated.close()
	if isolated.group == next.group {
		t.Fatal("caller isolation lost")
	}
}
