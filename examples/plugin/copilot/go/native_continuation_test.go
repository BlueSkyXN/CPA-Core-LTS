package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type toolEmitGate struct {
	hostCaller
	seen   chan string
	resume map[string]chan struct{}
}

func (gate toolEmitGate) Call(method string, value any) (json.RawMessage, error) {
	if method == pluginabi.MethodHostStreamEmit {
		frame := value.(pluginStreamEmitRequest)
		data := bytes.TrimSpace(bytes.TrimPrefix(frame.Payload, []byte("data:")))
		if id := ojson.GetBytes(data, "choices.0.delta.tool_calls.0.id").String(); gate.resume[id] != nil {
			gate.seen <- id
			<-gate.resume[id]
		}
	}
	return gate.hostCaller.Call(method, value)
}

func copilotToolFollow(id, call, opaque string) rpcExecutorRequest {
	req := copilotReq(id, false)
	message := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
		"id": call, "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"city":"Paris"}`},
	}}}
	if opaque != "" {
		message["reasoning_opaque"] = opaque
	}
	req.Payload, _ = json.Marshal(map[string]any{"messages": []any{message, map[string]any{"role": "tool", "tool_call_id": call, "content": "result"}}})
	return req
}

func expectCopilotConflict(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	if e, ok := err.(*pluginCallError); !ok || e.code != code || e.statusCode != http.StatusConflict || e.retryable != retryable {
		t.Fatalf("expected %s (409, retryable=%t), got %v", code, retryable, err)
	}
}

func TestNativeConcurrentToolBranchesAndEarlyFollow(t *testing.T) {
	var chats atomic.Int32
	r, h := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			serveCopilotModels(w)
		case "/chat/completions":
			chats.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			id := req.Header.Get("X-Request-Id")
			if id == "A" || id == "B" {
				frame := strings.NewReplacer("fixture-opaque-state", "state-"+id, "call-fixture", "call-"+id).Replace(copilotToolResponse)
				// B 的 ID 后到：先发无 ID 的起始片段，第二片才使 ID 对下游可见。
				if id == "B" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"lookup\"}}]}}]}\n\n")
				}
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
				return
			}
			var body json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			state := ojson.GetBytes(body, "messages.0.reasoning_opaque").String()
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "got:"+state)
		}
	})
	seen := make(chan string, 2)
	resumeA, resumeB := make(chan struct{}), make(chan struct{})
	// 每个流单独门控，既验证发帧前的登记，也能让 B 先完成。
	r.caller = toolEmitGate{h, seen, map[string]chan struct{}{"call-A": resumeA, "call-B": resumeB}}
	unblock := func(ch chan struct{}) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	defer unblock(resumeA)
	defer unblock(resumeB)
	follow := func(req rpcExecutorRequest, want string) {
		t.Helper()
		result, err := r.executeNative(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := ojson.GetBytes(result.Payload, "choices.0.message.content").String(); got != "got:"+want {
			t.Fatalf("%s: state %q, expected %q", req.RequestID, got, want)
		}
	}
	for _, id := range []string{"A", "B"} {
		if _, err := r.executeNativeStream(copilotReq(id, true)); err != nil {
			t.Fatal(err)
		}
		select {
		case call := <-seen:
			if call != "call-"+id {
				t.Fatal(call)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("tool ID was not emitted")
		}
		_, err := r.executeNative(copilotToolFollow("early-"+id, "call-"+id, ""))
		expectCopilotConflict(t, err, "tool_continuation_pending", true)
	}
	withAttachment := copilotToolFollow("with-attachment", "call-A", "")
	withAttachment.Payload = append(bytes.TrimSuffix(withAttachment.Payload, []byte(`]}`)), []byte(`,{"role":"user","content":[{"type":"text","text":"attached result"}]}]}`)...)
	_, err := r.executeNative(withAttachment)
	expectCopilotConflict(t, err, "tool_continuation_pending", true)
	_, err = r.executeNativeStream(copilotReq("A", true))
	expectCopilotConflict(t, err, "duplicate_request", false)
	if chats.Load() != 2 {
		t.Fatal("pending or duplicate request reached inference")
	}
	follow(copilotToolFollow("explicit", "call-A", "client-supplied"), "client-supplied")
	other := copilotToolFollow("other-session", "call-A", "")
	other.ExecutionSessionID = "other"
	follow(other, "")
	unblock(resumeB)
	if c := waitCopilotClose(t, h); c.StreamID != "downstream-B" || c.Error != "" {
		t.Fatal(c)
	}
	follow(copilotToolFollow("follow-b", "call-B", ""), "state-B")
	unblock(resumeA)
	if c := waitCopilotClose(t, h); c.StreamID != "downstream-A" || c.Error != "" {
		t.Fatal(c)
	}
	follow(copilotToolFollow("follow-a", "call-A", ""), "state-A")
	follow(copilotToolFollow("repeat-b", "call-B", ""), "state-B")
	assertNativeReleased(t, r, h)
}

func TestNativeToolOwnersCancelCloseAndReconfigure(t *testing.T) {
	r := newPluginRuntime(&copilotFixtureHost{})
	register := func(id, session string) *nativeExecution {
		t.Helper()
		req := copilotReq(id, true)
		req.ExecutionSessionID = session
		auth, err := parseStoredAuth(req.StorageJSON)
		if err != nil {
			t.Fatal(err)
		}
		exec, err := r.registerNative(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.releaseNative(exec) })
		return exec
	}
	a, b, c := register("A", "same"), register("B", "same"), register("C", "other")
	follower := register("follow", "same")
	for _, exec := range []*nativeExecution{a, b, c} {
		r.trackNativeTool(exec, 0, nativeToolIdentity{id: "shared"})
	}
	body := copilotToolFollow("req", "shared", "").Payload
	pending := func(exec *nativeExecution, model string) error {
		_, err := r.restoreNativeReasoning(exec, model, body, nil)
		return err
	}
	expectCopilotConflict(t, pending(follower, b.model), "tool_continuation_pending", true)
	if err := pending(follower, "other-model"); err != nil {
		t.Fatal("pending call crossed model")
	}
	r.cancelNative(pluginapi.CancelExecutionRequest{RequestID: a.requestID, ExecutionSessionID: "same"})
	if !a.isCanceled() || b.isCanceled() {
		t.Fatal("cancel changed peer ownership")
	}
	expectCopilotConflict(t, pending(follower, b.model), "tool_continuation_pending", true)
	if err := r.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeSession, ExecutionSessionID: "same"}); err != nil {
		t.Fatal(err)
	}
	if !b.isCanceled() || !follower.isCanceled() || c.isCanceled() {
		t.Fatal("session close selected wrong executions")
	}
	if err := pending(register("new-follow", "same"), b.model); err != nil {
		t.Fatal("closed session retained pending tools")
	}
	if err := r.configure(nil); err != nil {
		t.Fatal(err)
	}
	r.trackNativeTool(c, 0, nativeToolIdentity{id: "late-from-old-generation"})
	if err := pending(register("new-generation", "other"), c.model); err != nil {
		t.Fatal("old generation blocked new continuation")
	}
}

func TestNativeReplayAmbiguityAndCleanup(t *testing.T) {
	r := newPluginRuntime(nil)
	exec := &nativeExecution{model: "model", identity: executionIdentity{key: "scope", executionSessionID: "session"}}
	decoder := newNativeDecoder("producer", "model", nil)
	if _, err := decoder.data([]byte(copilotToolResponse)); err != nil {
		t.Fatal(err)
	}
	p := decoder.projection
	missing := copilotToolFollow("follow", "call-fixture", "").Payload
	restore := func(body []byte) (string, error) {
		out, err := r.restoreNativeReasoning(exec, "model", body, nil)
		return ojson.GetBytes(out, "messages.0.reasoning_opaque").String(), err
	}
	for _, want := range []string{"first", "first"} {
		p.reasoningOpaque = want
		r.rememberNativeReasoning(exec, "model", p)
		if got, err := restore(missing); err != nil || got != want {
			t.Fatalf("same state: got %q, err %v", got, err)
		}
	}
	for _, v := range []string{"second", "first"} {
		p.reasoningOpaque = v
		r.rememberNativeReasoning(exec, "model", p)
		_, err := restore(missing)
		expectCopilotConflict(t, err, "tool_continuation_ambiguous", false)
	}
	if got, err := restore(copilotToolFollow("explicit", "call-fixture", "explicit").Payload); err != nil || got != "explicit" {
		t.Fatalf("explicit state: got %q, err %v", got, err)
	}
	r.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{Scope: pluginapi.ExecutionSessionCloseScopeSession, ExecutionSessionID: "session"})
	if got, err := restore(missing); err != nil || got != "" {
		t.Fatal("session close retained ambiguous state")
	}
}
