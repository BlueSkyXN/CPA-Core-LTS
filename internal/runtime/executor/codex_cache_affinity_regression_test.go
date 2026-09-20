package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	chatconv "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	execpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
								if r.group != gjson.GetBytes(w.body, "prompt_cache_key").String() || !r.fieldsFrozen || r.refs != 0 {
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
					body, _ = sjson.DeleteBytes(body, "type")
				}
				expected := helps.SanitizeCodexInputItemIDs(req.Payload)
				if gjson.GetBytes(expected, "input").Raw != gjson.GetBytes(body, "input").Raw {
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
					t.Logf("final_id=%s final_known=%v index_equals_wire=%v", gjson.GetBytes(body, "input.0.id"), final.known, equal)
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
	first := chatconv.ConvertOpenAIRequestToCodex("gpt-5.6-sol", original, false)
	completed := []byte(`{"type":"response.completed","response":{"id":"synthetic","model":"gpt-5.6-sol","status":"completed","output":[{"id":"msg_synthetic","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"synthetic answer","annotations":[]}]}]}}`)
	answer := chatconv.ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-5.6-sol", original, first, completed, nil)
	message := gjson.GetBytes(answer, "choices.0.message")
	if message.Get("content").String() != "synthetic answer" {
		t.Fatal("response translation failed")
	}
	next, _ := sjson.SetRawBytes(original, "messages.-1", []byte(message.Raw))
	next, _ = sjson.SetRawBytes(next, "messages.-1", []byte(`{"role":"user","content":"continue"}`))
	nextBody := chatconv.ConvertOpenAIRequestToCodex("gpt-5.6-sol", next, false)
	s := newCodexAffinityStore()
	d := s.decide(context.Background(), "scope", "", "", "first", "fallback", "", false, codexAffinitySnapshotOf(first))
	d.complete(completed)
	d.close()
	continuation := s.decide(context.Background(), "scope", "", "", "second", "fallback", "", false, codexAffinitySnapshotOf(nextBody))
	defer continuation.close()
	t.Logf("raw_output_has_id=%v translated_assistant_has_id=%v adopted=%v check=%s", gjson.GetBytes(completed, "response.output.0.id").Exists(), gjson.GetBytes(nextBody, "input.1.id").Exists(), continuation.adopted, continuation.check)
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
