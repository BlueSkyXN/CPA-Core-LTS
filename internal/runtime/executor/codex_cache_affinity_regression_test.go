package executor

import (
	"context"
	"errors"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	chatconv "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	execpkg "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// 回归断言实际出站行为和已结束逻辑请求的回收，旧提交应失败。
func TestCodexCacheAffinityWSReusedHandshake(t *testing.T) {
	for _, required := range []bool{false, true} {
		for _, mismatch := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v/mismatch=%v/required=%v", stream, mismatch, required), func(t *testing.T) {
					server, ch := affinityTestServer(t, true)
					defer server.Close()
					e := NewCodexWebsocketsExecutor(affinityTestConfig("client-aware"))
					sessionID := "affinity-regression-" + t.Name()
					defer e.CloseExecutionSession(sessionID)
					a := affinityTestAuth(server.URL, "oauth")
					var firstConn any
					for n := 0; n < 2; n++ {
						req := execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"synthetic question"}]}`)}
						opts := execpkg.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: fmt.Sprint(n), execpkg.ExecutionSessionMetadataKey: sessionID}, Headers: make(http.Header)}
						if n == 0 && mismatch {
							opts.Headers.Set("Session-Id", "synthetic-H1")
						}
						ctx := context.Background()
						if required && n > 0 {
							ctx = execpkg.WithRequiredUpstreamWebsocket(ctx)
						}
						if stream {
							r, err := e.ExecuteStream(ctx, a, req, opts)
							if err != nil {
								t.Fatal(err)
							}
							for c := range r.Chunks {
								if c.Err != nil {
									t.Fatal(c.Err)
								}
							}
						} else {
							if _, err := e.Execute(ctx, a, req, opts); err != nil {
								t.Fatal(err)
							}
						}
						w := <-ch
						sess := e.getOrCreateSession(sessionID)
						sess.connMu.Lock()
						conn := sess.conn
						gen := sess.connGen
						sess.connMu.Unlock()
						if n == 0 {
							firstConn = conn
						} else {
							if firstConn != conn {
								t.Fatal("did not reuse connection")
							}
							store := e.affinityStore()
							store.mu.Lock()
							defer store.mu.Unlock()
							if !mismatch {
								if len(store.trajectories) != 1 || len(store.bindings) != 1 {
									t.Fatal("matching persistent connection did not publish")
								}
								for _, tr := range store.trajectories {
									if tr.group != w.headers.Get("Session-Id") {
										t.Fatal("matching group differs from wire")
									}
								}
								return
							}
							if w.headers.Get("Session-Id") != "synthetic-H1" || gen != 1 {
								t.Fatal("unexpected handshake or reconnect")
							}
							if len(store.trajectories) != 0 || len(store.bindings) != 0 {
								t.Fatal("unverified connection affinity published")
							}
							if len(store.requests) != 1 {
								t.Fatal("mismatch discarded logical request freeze")
							}
							for _, r := range store.requests {
								if r.group != ojson.GetBytes(w.body, "prompt_cache_key").String() || !r.fieldsFrozen || r.refs != 0 {
									t.Fatal("freeze or cleanup changed")
								}
							}

						}
					}
				})
			}
		}
	}
}

func TestCodexCacheAffinityFinalInputSnapshot(t *testing.T) {
	for caseIndex, input := range []string{
		`[{"type":"message","id":"a","role":"user","content":"synthetic"}]`,
		`[{"type":"message","id":"a","role":"user","content":"synthetic"},{"type":"message","id":"msg_a","role":"assistant","content":"answer"}]`,
		`[{"type":"message","id":"` + strings.Repeat("a", 80) + `","role":"user","content":"synthetic"}]`,
		`[{"type":"message","id":"a","role":"user","content":"synthetic"},{"type":"reasoning","id":"rs_` + strings.Repeat("a", 80) + `","encrypted_content":"` + validCodexReasoningEncryptedContentForTestSeed(7) + `"}]`,
	} {
		for _, transport := range []string{"http", "ws", "ws-stream"} {
			t.Run(fmt.Sprintf("case=%d/%s", caseIndex, transport), func(t *testing.T) {
				server, ch := affinityTestServer(t, transport != "http")
				defer server.Close()
				e := NewCodexExecutor(affinityTestConfig("client-aware"))
				we := NewCodexWebsocketsExecutor(e.cfg)
				we.CodexExecutor = e
				req := execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":` + input + `}`), Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "snapshot"}}
				opts := execpkg.Options{SourceFormat: translator.FormatOpenAIResponse, Headers: http.Header{"X-Session-Id": {"synthetic-session"}}}
				a := affinityTestAuth(server.URL, "oauth")
				switch transport {
				case "http":
					if _, err := e.Execute(context.Background(), a, req, opts); err != nil {
						t.Fatal(err)
					}
				case "ws":
					if _, err := we.Execute(context.Background(), a, req, opts); err != nil {
						t.Fatal(err)
					}
				default:
					r, err := we.ExecuteStream(context.Background(), a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for c := range r.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
					}
				}
				w := <-ch
				body := w.body
				if transport != "http" {
					body, _ = ojson.DeleteBytes(body, "type")
				}
				expected := helps.SanitizeCodexInputItemIDs(req.Payload)
				if ojson.GetBytes(expected, "input").Raw != ojson.GetBytes(body, "input").Raw {
					t.Fatal("unexpected final normalized input")
				}
				final := codexAffinitySnapshotOf(body)
				store := e.affinityStore()
				store.mu.Lock()
				defer store.mu.Unlock()
				if len(store.trajectories) == 0 {
					t.Fatal("no trajectory")
				}
				for _, tr := range store.trajectories {
					equal := tr.snapshot.settings == final.settings && reflect.DeepEqual(tr.snapshot.items, final.items)
					t.Logf("final_id=%s final_known=%v index_equals_wire=%v", ojson.GetBytes(body, "input.0.id"), final.known, equal)
					if !equal {
						t.Fatal("unexpected diagnostic result")
					}
				}
			})
		}
	}
}

func TestCodexCacheAffinityCompletedRequestCapacity(t *testing.T) {
	s := newCodexAffinityStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	snap := affinitySnapshot(`[{"role":"user","content":"synthetic unique task"},{"role":"assistant","content":"synthetic answer"}]`)
	for n := 0; n < 4096; n++ {
		ctx, cancel := util.WithLogicalRequestLifetime(context.Background())
		d := s.decide(ctx, "scope", "identity", "", fmt.Sprint(n), "stable-group", "", true, snap)
		d.complete([]byte(affinityCompleted))
		d.close()
		cancel()
	}
	next := affinitySnapshot(`[{"role":"user","content":"synthetic unique task"},{"role":"assistant","content":"synthetic answer"},{"role":"user","content":"continue"}]`)
	d := s.decide(context.Background(), "scope", "", "", "4097", "temporary-fallback", "", false, next)
	defer d.close()
	t.Logf("completed_request_records=%d trajectories=%d adopted=%v check=%s group=%s", len(s.requests), len(s.trajectories), d.adopted, d.check, d.group)
	if len(s.requests) > 1 || len(s.trajectories) != 1 || !d.adopted || d.group != "stable-group" {
		t.Fatal("completed requests blocked existing candidate")
	}

}

// Checks actual translator representation, without relaxing equality rules.
func TestCodexCacheAffinityTranslatedOutputAnchor(t *testing.T) {
	original := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"synthetic question"}]}`)
	first, _ := chatconv.ConvertOpenAIRequestToCodex("gpt-5.6-sol", original, false)
	completed := []byte(`{"type":"response.completed","response":{"id":"synthetic","model":"gpt-5.6-sol","status":"completed","output":[{"id":"msg_synthetic","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"synthetic answer","annotations":[]}]}]}}`)
	answer := chatconv.ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-5.6-sol", original, first, completed, nil)
	message := ojson.GetBytes(answer, "choices.0.message")
	if message.Get("content").String() != "synthetic answer" {
		t.Fatal("response translation failed")
	}
	next, _ := ojson.SetRawBytes(original, "messages.-1", []byte(message.Raw))
	next, _ = ojson.SetRawBytes(next, "messages.-1", []byte(`{"role":"user","content":"continue"}`))
	nextBody, _ := chatconv.ConvertOpenAIRequestToCodex("gpt-5.6-sol", next, false)
	s := newCodexAffinityStore()
	d := s.decide(context.Background(), "scope", "", "", "first", "fallback", "", false, codexAffinitySnapshotOf(first))
	d.complete(completed)
	d.close()
	continuation := s.decide(context.Background(), "scope", "", "", "second", "fallback", "", false, codexAffinitySnapshotOf(nextBody))
	defer continuation.close()
	t.Logf("raw_output_has_id=%v translated_assistant_has_id=%v adopted=%v check=%s", ojson.GetBytes(completed, "response.output.0.id").Exists(), ojson.GetBytes(nextBody, "input.1.id").Exists(), continuation.adopted, continuation.check)
	if continuation.adopted {
		t.Fatal("expected conservative representation mismatch")
	}
}

func TestCodexCacheAffinityLogicalLifetimeProtectsRetries(t *testing.T) {
	s := newCodexAffinityStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	root, finish := util.WithLogicalRequestLifetime(context.Background())
	defer finish()
	lane, cancel := context.WithCancel(root)
	snap := affinitySnapshot(`[{"role":"user","content":"unique request"}]`)
	first := s.decide(lane, "scope", "", "", "logical", "fallback", "", false, snap)
	original := first.group
	first.freezeFields([]byte(`{}`), "first-pck")
	first.close()
	cancel()
	// 一个 lane 已结束，逻辑请求还活着；跨 TTL/配置 generation 仍不能丢失 H/K。
	now = now.Add(2 * time.Hour)
	s.resetGeneration()
	retry := s.decide(root, "different-account-scope", "", "", "logical", "different-fallback", "", false, snap)
	_, pck := retry.freezeFields([]byte(`{}`), "different-pck")
	if retry.group != original || pck != "first-pck" {
		t.Fatal("retry lost frozen H/K")
	}
	retry.close()
	if len(s.requests) != 1 {
		t.Fatal("live logical request was evicted between attempts")
	}
	// 整次结束后仍有在途引用：不能淘汰；迟到成功也不能发布。
	pending := s.decide(root, "scope", "identity", "", "logical", "other", "", true, snap)
	finish()
	s.expire(now)
	if len(s.requests) != 1 {
		t.Fatal("active attempt was evicted")
	}
	pending.complete([]byte(affinityCompleted))
	if len(s.trajectories) != 0 || len(s.bindings) != 0 {
		t.Fatal("late completion published")
	}
	pending.close()
	s.expire(now)
	if len(s.requests) != 0 {
		t.Fatal("finished logical request was not reclaimed")
	}
}

func TestCodexCacheAffinityMismatchOnlyBlocksCurrentPublication(t *testing.T) {
	s := newCodexAffinityStore()
	ctx := context.Background()
	snap := affinitySnapshot(`[{"role":"user","content":"unique request"}]`)
	first := s.decide(ctx, "scope", "id", "", "r1", "stable", "", true, snap)
	first.complete([]byte(affinityCompleted))
	first.close()
	next := s.decide(ctx, "scope", "id", "", "r2", "stable", "", true, snap)
	next.verifySessionHeader("other", true)
	next.complete([]byte(affinityCompleted))
	next.close()
	if len(s.bindings) != 1 || s.bindings["id"].group != "stable" || len(s.trajectories) != 1 {
		t.Fatal("mismatch modified established state")
	}
	third := s.decide(ctx, "scope", "new-id", "", "r3", "new-stable", "", true, snap)
	third.verifySessionHeader("new-stable", true)
	third.complete([]byte(affinityCompleted))
	third.close()
	if len(s.bindings) != 2 || len(s.trajectories) != 2 {
		t.Fatal("matching request no longer publishes")
	}
}

func TestCodexCacheAffinityWSConnectionGeneration(t *testing.T) {
	server, _ := affinityTestServer(t, true)
	defer server.Close()
	e := NewCodexWebsocketsExecutor(affinityTestConfig("client-aware"))
	sess := e.getOrCreateSession(t.Name())
	defer e.CloseExecutionSession(t.Name())
	a := affinityTestAuth(server.URL, "oauth")
	url, _ := buildCodexResponsesWebsocketURL(server.URL + "/responses")
	key := newCodexWebsocketConnectionKey(a.ID, url, "gpt-5.6-sol", [32]byte{})
	one, _, _, err := e.ensureUpstreamConn(context.Background(), a, sess, key, http.Header{"Session-Id": {"H1"}})
	if err != nil {
		t.Fatal(err)
	}
	two, _, _, err := e.ensureUpstreamConn(context.Background(), a, sess, key, http.Header{"Session-Id": {"H2"}})
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("automatic header changed connection ownership")
	}
	existing, _ := existingCodexWebsocketSessionConnection(sess, key)
	if h, ok := sess.affinityHeader(existing); !ok || h != "H1" {
		t.Fatal("existing connection lost actual handshake")
	}
	e.invalidateUpstreamConnWithoutDisconnectNotify(sess, one, "test_reconnect", nil)
	next, _, _, err := e.ensureUpstreamConn(context.Background(), a, sess, key, http.Header{"Session-Id": {"H2"}})
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := sess.affinityHeader(next); !ok || h != "H2" {
		t.Fatal("reconnect did not record new header")
	}
	if _, ok := sess.affinityHeader(one); ok {
		t.Fatal("old generation accepted")
	}
}

func TestCodexCacheAffinityConcurrentDialUsesWinningHandshake(t *testing.T) {
	type handshake struct{ addr, header string }
	arrived := make(chan handshake, 2)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- handshake{r.RemoteAddr, r.Header.Get("Session-Id")}
		<-gate
		c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err = c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	defer release()
	e := NewCodexWebsocketsExecutor(affinityTestConfig("client-aware"))
	sess := e.getOrCreateSession(t.Name())
	defer e.CloseExecutionSession(t.Name())
	a := affinityTestAuth(server.URL, "oauth")
	url, _ := buildCodexResponsesWebsocketURL(server.URL + "/responses")
	key := newCodexWebsocketConnectionKey(a.ID, url, "gpt-5.6-sol", [32]byte{})
	type result struct {
		ref codexWebsocketConnectionRef
		err error
	}
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, header := range []string{"H1", "H2"} {
		go func(h string) {
			ref, _, _, err := e.ensureUpstreamConn(ctx, a, sess, key, http.Header{"Session-Id": {h}})
			results <- result{ref, err}
		}(header)
	}
	actual := map[string]string{}
	for n := 0; n < 2; n++ {
		select {
		case h := <-arrived:
			actual[h.addr] = h.header
		case <-ctx.Done():
			t.Fatal("concurrent dials did not reach handshake")
		}
	}
	release()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("dial failed: %v / %v", first.err, second.err)
	}
	if first.ref != second.ref {
		t.Fatal("racing dials did not converge on one connection")
	}
	want := actual[first.ref.conn.LocalAddr().String()]
	for _, ref := range []codexWebsocketConnectionRef{first.ref, second.ref} {
		if got, ok := sess.affinityHeader(ref); !ok || got != want {
			t.Fatal("losing dial header replaced actual winning handshake")
		}
	}
}

func assertAffinityStoreReleased(t *testing.T, store *codexAffinityStore, finish func()) {
	t.Helper()
	store.mu.Lock()
	if len(store.requests) == 0 {
		store.mu.Unlock()
		t.Fatal("expected at least one affinity request record before release check")
	}
	for k, r := range store.requests {
		if r.refs != 0 {
			refs := r.refs
			store.mu.Unlock()
			t.Fatalf("affinity request %q leaked refs=%d, want 0", k, refs)
		}
	}
	if len(store.cold) != 0 {
		coldCount := len(store.cold)
		store.mu.Unlock()
		t.Fatalf("affinity store.cold len=%d, want 0", coldCount)
	}
	store.mu.Unlock()

	if finish != nil {
		finish()
		store.expire(store.now())
		store.mu.Lock()
		remaining := len(store.requests)
		store.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("store.requests len=%d after logical lifetime finish + expire, want 0", remaining)
		}
	}
}

func TestCodexCacheAffinityHostedImageValidationReleasesAffinity(t *testing.T) {
	payload := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"draw"}],"tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`)

	for _, mode := range []string{"http_execute", "http_stream", "ws_stream"} {
		t.Run(mode, func(t *testing.T) {
			server, ch := affinityTestServer(t, mode == "ws_stream")
			defer server.Close()

			cfg := affinityTestConfig("client-aware")
			cfg.DisableImageGeneration = config.DisableImageGenerationOff
			httpExec := NewCodexExecutor(cfg)
			wsExec := NewCodexWebsocketsExecutor(cfg)
			wsExec.CodexExecutor = httpExec
			auth := affinityTestAuth(server.URL, "oauth")
			if mode == "ws_stream" {
				auth.Attributes["websockets"] = "true"
			}

			rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
			defer finish()
			req := execpkg.Request{
				Model:    "gpt-6-astra",
				Payload:  payload,
				Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "hosted-img-" + mode},
			}
			opts := execpkg.Options{
				SourceFormat: translator.FormatOpenAIResponse,
				Headers:      http.Header{"X-Session-Id": {"synthetic-session"}},
				Metadata:     map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "hosted-img-" + mode},
			}

			var err error
			switch mode {
			case "http_execute":
				_, err = httpExec.Execute(rootCtx, auth, req, opts)
			case "http_stream":
				_, err = httpExec.ExecuteStream(rootCtx, auth, req, opts)
			case "ws_stream":
				_, err = wsExec.ExecuteStream(rootCtx, auth, req, opts)
			}
			if err == nil {
				t.Fatal("expected ValidateCodexHostedImageGenerationDoc error, got nil")
			}
			var statusHolder interface{ StatusCode() int }
			if !errors.As(err, &statusHolder) || statusHolder.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error = %v, want status 400", err)
			}
			select {
			case w := <-ch:
				t.Fatalf("unexpected upstream request sent: %+v", w)
			default:
			}

			assertAffinityStoreReleased(t, httpExec.affinityStore(), finish)
		})
	}

	for _, transport := range []string{"positive_handoff_http_stream", "positive_handoff_ws_stream"} {
		t.Run(transport, func(t *testing.T) {
			isWS := transport == "positive_handoff_ws_stream"
			release := make(chan struct{})
			var releaseOnce sync.Once
			doRelease := func() { releaseOnce.Do(func() { close(release) }) }
			defer doRelease()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isWS {
					c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer c.Close()
					if _, _, err := c.ReadMessage(); err != nil {
						return
					}
					if c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`)) != nil {
						return
					}
					<-release
					_ = c.WriteMessage(websocket.TextMessage, []byte(affinityCompleted))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				<-release
				fmt.Fprintf(w, "data: %s\n\n", affinityCompleted)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}))
			defer server.Close()

			cfg := affinityTestConfig("client-aware")
			httpExec := NewCodexExecutor(cfg)
			wsExec := NewCodexWebsocketsExecutor(cfg)
			wsExec.CodexExecutor = httpExec
			auth := affinityTestAuth(server.URL, "oauth")
			if isWS {
				auth.Attributes["websockets"] = "true"
			}

			rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
			defer finish()
			req := execpkg.Request{
				Model:    "gpt-5.6-sol",
				Payload:  []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"hello"}]}`),
				Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "handoff-" + transport},
			}
			opts := execpkg.Options{
				SourceFormat: translator.FormatOpenAIResponse,
				Headers:      http.Header{"X-Session-Id": {"synthetic-session"}},
				Metadata:     map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "handoff-" + transport},
			}

			var res *execpkg.StreamResult
			var err error
			if isWS {
				res, err = wsExec.ExecuteStream(rootCtx, auth, req, opts)
			} else {
				res, err = httpExec.ExecuteStream(rootCtx, auth, req, opts)
			}
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			firstChunk, ok := <-res.Chunks
			if !ok || firstChunk.Err != nil {
				t.Fatalf("first chunk = (%+v, ok=%t), want initial stream chunk", firstChunk, ok)
			}

			store := httpExec.affinityStore()
			store.mu.Lock()
			activeRefs := 0
			for _, r := range store.requests {
				activeRefs += r.refs
			}
			store.mu.Unlock()
			if activeRefs < 1 {
				t.Fatalf("active affinity refs while streaming = %d, want >= 1 (handoff must preserve affinity)", activeRefs)
			}

			doRelease()
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}

			assertAffinityStoreReleased(t, store, finish)
		})
	}
}

func TestCodexCacheAffinityDuplexEarlyReturnsReleaseAffinity(t *testing.T) {
	t.Run("len_pending_gte_16", func(t *testing.T) {
		releaseServer := make(chan struct{})
		defer close(releaseServer)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
			if c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`)) != nil {
				return
			}
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}))
		defer upstream.Close()

		cfg := affinityTestConfig("client-aware")
		cfg.Codex.ResponseSteering = true
		exec := NewCodexWebsocketsExecutor(cfg)
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-affinity-pending-limit"
		defer exec.CloseExecutionSession(sessionID)

		rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
		defer finish()
		ctx, cancel := context.WithTimeout(rootCtx, 8*time.Second)
		defer cancel()
		input := make(chan execpkg.WebsocketInput, 20)
		ctx = execpkg.WithWebsocketInput(execpkg.WithDownstreamWebsocket(ctx), input)

		auth := affinityTestAuth(upstream.URL, "oauth")
		auth.Attributes["websockets"] = "true"
		req := execpkg.Request{
			Model:    "gpt-5.6-sol",
			Payload:  []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"init"}]}`),
			Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "pending-limit"},
		}
		opts := execpkg.Options{
			SourceFormat: translator.FormatOpenAIResponse,
			Headers:      http.Header{"X-Session-Id": {"duplex-pending"}},
			Metadata: map[string]any{
				execpkg.CallerScopeMetadataKey:      "caller",
				execpkg.RequestIDMetadataKey:        "pending-limit",
				execpkg.ExecutionSessionMetadataKey: sessionID,
			},
		}

		res, err := exec.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}

		sent := false
		sawLimitErr := false
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				if strings.Contains(chunk.Err.Error(), "too many outstanding response.create requests") {
					sawLimitErr = true
				}
				continue
			}
			if !sent && ojson.GetBytes(chunk.Payload, "type").String() == "response.created" {
				sent = true
				for i := 0; i < 17; i++ {
					input <- execpkg.WebsocketInput{Payload: []byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":"r1","input":[{"role":"user","content":"follow-%d"}]}`, i))}
				}
			}
		}
		if !sawLimitErr {
			t.Fatal("expected too many outstanding response.create requests error")
		}
		assertAffinityStoreReleased(t, exec.affinityStore(), finish)
	})

	t.Run("ws_url_mismatch", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
			_ = c.WriteMessage(websocket.TextMessage, []byte(affinityCompleted))
			_, _, _ = c.ReadMessage()
		}))
		defer upstream.Close()

		cfg := affinityTestConfig("client-aware")
		cfg.Codex.ResponseSteering = true
		exec := NewCodexWebsocketsExecutor(cfg)
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-affinity-wsurl-mismatch"
		defer exec.CloseExecutionSession(sessionID)

		rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
		defer finish()
		ctx, cancel := context.WithTimeout(rootCtx, 8*time.Second)
		defer cancel()
		input := make(chan execpkg.WebsocketInput, 2)
		ctx = execpkg.WithWebsocketInput(execpkg.WithDownstreamWebsocket(ctx), input)

		auth := affinityTestAuth(upstream.URL, "oauth")
		auth.Attributes["websockets"] = "true"
		req := execpkg.Request{
			Model:    "gpt-5.6-sol",
			Payload:  []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"init"}]}`),
			Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "wsurl-mismatch"},
		}
		opts := execpkg.Options{
			SourceFormat: translator.FormatOpenAIResponse,
			Headers:      http.Header{"X-Session-Id": {"duplex-wsurl"}},
			Metadata: map[string]any{
				execpkg.CallerScopeMetadataKey:      "caller",
				execpkg.RequestIDMetadataKey:        "wsurl-mismatch",
				execpkg.ExecutionSessionMetadataKey: sessionID,
			},
		}

		res, err := exec.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}

		sent := false
		sawReplayErr := false
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				if execpkg.IsUpstreamWebsocketReplayRequired(chunk.Err) {
					sawReplayErr = true
				}
				continue
			}
			if !sent && ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
				sent = true
				auth.Attributes["base_url"] = "http://127.0.0.1:1"
				input <- execpkg.WebsocketInput{Payload: []byte(`{"type":"response.create","previous_response_id":"r1","input":[{"role":"user","content":"follow"}]}`)}
			}
		}
		if !sawReplayErr {
			t.Fatal("expected upstream websocket replay-required error on wsURL mismatch")
		}
		assertAffinityStoreReleased(t, exec.affinityStore(), finish)
	})

	t.Run("on_connection_mismatch", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`))
			_, _, _ = c.ReadMessage()
		}))
		defer upstream.Close()

		cfg := affinityTestConfig("client-aware")
		cfg.Codex.ResponseSteering = true
		exec := NewCodexWebsocketsExecutor(cfg)
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-affinity-onconn-mismatch"
		defer exec.CloseExecutionSession(sessionID)

		rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
		defer finish()
		ctx, cancel := context.WithTimeout(rootCtx, 8*time.Second)
		defer cancel()
		input := make(chan execpkg.WebsocketInput, 2)
		ctx = execpkg.WithWebsocketInput(execpkg.WithDownstreamWebsocket(ctx), input)

		auth := affinityTestAuth(upstream.URL, "oauth")
		auth.Attributes["websockets"] = "true"
		initialPayload := []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"init"}],"instructions":"Base instructions","client_metadata":{"cpa_session_instructions":"start"}}`)
		req := execpkg.Request{
			Model:    "gpt-5.6-sol",
			Payload:  initialPayload,
			Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "onconn-mismatch"},
		}
		opts := execpkg.Options{
			SourceFormat: translator.FormatOpenAIResponse,
			Headers:      http.Header{"X-Session-Id": {"duplex-onconn-session"}},
			Metadata: map[string]any{
				execpkg.CallerScopeMetadataKey:      "caller",
				execpkg.RequestIDMetadataKey:        "onconn-mismatch",
				execpkg.ExecutionSessionMetadataKey: sessionID,
			},
		}

		res, err := exec.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}

		sent := false
		sawPreviousNotFound := false
		sess := exec.getOrCreateSession(sessionID)
		var origConn *websocket.Conn
		beforeRefs, beforeCold := 0, 0
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error = %v", chunk.Err)
			}
			kind := ojson.GetBytes(chunk.Payload, "type").String()
			if !sent && kind == "response.completed" {
				sent = true
				store := exec.affinityStore()
				store.mu.Lock()
				for _, r := range store.requests {
					beforeRefs += r.refs
				}
				beforeCold = len(store.cold)
				store.mu.Unlock()
				if beforeRefs != 1 {
					t.Fatalf("initial duplex refs = %d, want 1 while duplex socket is open", beforeRefs)
				}

				otherConn := &websocket.Conn{}
				turn, errTurn := helps.PrepareCodexSessionInstructions(ctx, ojson.NewDoc(initialPayload), req, opts, nil, nil, false)
				if errTurn != nil {
					t.Fatalf("PrepareCodexSessionInstructions() error = %v", errTurn)
				}
				receipt := sess.sessionInstructions.Sent(otherConn, turn)
				receipt.Observe([]byte(`{"type":"response.created","response":{"id":"r1"}}`))
				receipt.Observe([]byte(`{"type":"response.completed","response":{"id":"r1","status":"completed"}}`))

				sess.connMu.Lock()
				origConn = sess.conn
				sess.conn = otherConn
				sess.connMu.Unlock()

				input <- execpkg.WebsocketInput{Payload: []byte(`{"type":"response.create","previous_response_id":"r1","input":[{"role":"user","content":"follow"}],"client_metadata":{"cpa_session_instructions":"reuse"}}`)}
			}
			if kind == "error" && ojson.GetBytes(chunk.Payload, "error.code").String() == "previous_response_not_found" {
				sawPreviousNotFound = true
				// Verify follow-up affinity ref was released immediately while duplex stays open.
				store := exec.affinityStore()
				store.mu.Lock()
				afterRefs, afterCold := 0, len(store.cold)
				for _, r := range store.requests {
					afterRefs += r.refs
				}
				store.mu.Unlock()
				if afterRefs != beforeRefs || afterCold != beforeCold {
					t.Fatalf("OnConnection early return leaked affinity while duplex open: refs %d->%d, cold %d->%d", beforeRefs, afterRefs, beforeCold, afterCold)
				}
				sess.connMu.Lock()
				sess.conn = origConn
				sess.connMu.Unlock()
				cancel()
				break
			}
		}
		for range res.Chunks {
		}
		if !sawPreviousNotFound {
			t.Fatal("expected previous_response_not_found error from OnConnection mismatch")
		}
		assertAffinityStoreReleased(t, exec.affinityStore(), finish)
	})
}

func TestCodexCacheAffinityOpenAIImagesReleasesAffinity(t *testing.T) {
	payload := []byte(`{"model":"gpt-image-1","prompt":"draw a cat"}`)

	for _, stream := range []bool{false, true} {
		for _, wantErr := range []bool{true, false} {
			name := fmt.Sprintf("stream=%v/err=%v", stream, wantErr)
			t.Run(name, func(t *testing.T) {
				release := make(chan struct{})
				var releaseOnce sync.Once
				doRelease := func() { releaseOnce.Do(func() { close(release) }) }
				defer doRelease()

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if wantErr {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"bad image request"}}`))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					if stream {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.image_generation_call.partial_image","partial_image_index":0,"partial_image_b64":"AQ==","output_format":"png"}`)
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
						<-release
					}
					_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.completed","response":{"id":"r_img","status":"completed","created_at":1713833628,"output":[{"type":"image_generation_call","result":"AA==","output_format":"png"}]}}`)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}))
				defer server.Close()

				cfg := affinityTestConfig("client-aware")
				cfg.DisableImageGeneration = config.DisableImageGenerationOff
				httpExec := NewCodexExecutor(cfg)
				auth := affinityTestAuth(server.URL, "oauth")

				rootCtx, finish := util.WithLogicalRequestLifetime(context.Background())
				defer finish()
				req := execpkg.Request{
					Model:    "gpt-image-1",
					Payload:  payload,
					Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "openai-img-" + name},
				}
				opts := execpkg.Options{
					SourceFormat: translator.FromString(codexOpenAIImageSourceFormat),
					Stream:       stream,
					Headers:      http.Header{"X-Session-Id": {"synthetic-session"}},
					Metadata: map[string]any{
						execpkg.CallerScopeMetadataKey: "caller",
						execpkg.RequestIDMetadataKey:   "openai-img-" + name,
						execpkg.RequestPathMetadataKey: codexImagesGenerationsPath,
					},
				}

				if !stream {
					resp, err := httpExec.Execute(rootCtx, auth, req, opts)
					if wantErr {
						if err == nil {
							t.Fatal("expected HTTP 400 error, got nil")
						}
					} else if err != nil || len(resp.Payload) == 0 {
						t.Fatalf("Execute() = (len=%d, err=%v), want image payload and nil err", len(resp.Payload), err)
					}
					assertAffinityStoreReleased(t, httpExec.affinityStore(), finish)
					return
				}

				res, err := httpExec.ExecuteStream(rootCtx, auth, req, opts)
				if wantErr {
					if err == nil {
						t.Fatal("expected HTTP 400 error, got nil")
					}
					assertAffinityStoreReleased(t, httpExec.affinityStore(), finish)
					return
				}
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}

				firstChunk, ok := <-res.Chunks
				if !ok || firstChunk.Err != nil {
					t.Fatalf("first chunk = (%+v, ok=%t), want initial partial_image frame", firstChunk, ok)
				}

				store := httpExec.affinityStore()
				store.mu.Lock()
				activeRefs := 0
				for _, r := range store.requests {
					activeRefs += r.refs
				}
				store.mu.Unlock()
				if activeRefs < 1 {
					t.Fatalf("active affinity refs while streaming = %d, want >= 1 (handoff must preserve affinity)", activeRefs)
				}

				doRelease()
				sawCompleted := false
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					if strings.Contains(string(chunk.Payload), "image_generation.completed") {
						sawCompleted = true
					}
				}
				if !sawCompleted {
					t.Fatal("missing image_generation.completed SSE frame")
				}
				assertAffinityStoreReleased(t, store, finish)
			})
		}
	}
}
