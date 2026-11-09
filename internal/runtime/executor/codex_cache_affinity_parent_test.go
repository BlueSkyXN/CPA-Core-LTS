package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	execpkg "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexCacheAffinityParentHeaders(t *testing.T) {
	for _, tt := range []struct {
		name, identityHeader, parentHeader, parentValue, payload, want string
	}{
		{"session", "X-Session-Id", "X-Parent-Session-Id", "parent", `{}`, "parent"},
		{"thread", "X-Thread-Id", "X-Parent-Thread-Id", "parent", `{}`, "parent"},
		{"conversation", "X-Conversation-Id", "X-Parent-Conversation-Id", "parent", `{}`, "parent"},
		{"thread alias", "Thread-Id", "X-Parent-Thread-Id", "parent", `{}`, "parent"},
		{"conversation alias", "Conversation-Id", "X-Parent-Conversation-Id", "parent", `{}`, "parent"},
		{"generic parent", "X-Session-Id", "X-Parent-Id", "parent", `{}`, "parent"},
		{"lowercase", "x-session-id", "x-parent-session-id", "parent", `{}`, "parent"},
		{"header precedence", "X-Session-Id", "X-Parent-Session-Id", "header-parent", `{"parent_session_id":"body-parent"}`, "header-parent"},
		{"body fallback", "X-Session-Id", "X-Parent-Session-Id", "", `{"parent_session_id":"body-parent"}`, "body-parent"},
		{"self parent", "X-Session-Id", "X-Parent-Session-Id", "child", `{}`, ""},
		{"self parent body fallback", "X-Session-Id", "X-Parent-Session-Id", "child", `{"parent_session_id":"body-parent"}`, "body-parent"},
		{"invalid parent", "X-Session-Id", "X-Parent-Session-Id", "parent\n", `{}`, ""},
		{"oversized parent", "X-Session-Id", "X-Parent-Session-Id", strings.Repeat("p", 257), `{}`, ""},
		{"unrelated family", "X-Session-Id", "X-Parent-Thread-Id", "parent", `{}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{tt.identityHeader: {"child"}, tt.parentHeader: {tt.parentValue}}
			kind, id, parent := codexAffinityIdentity(headers, []byte(tt.payload))
			if kind != strings.ToLower(tt.identityHeader) || id != "child" || parent != tt.want {
				t.Fatalf("identity = %q, %q, %q; want unchanged child identity and parent %q", kind, id, parent, tt.want)
			}
		})
	}
}

func TestCodexCacheAffinityParentHeaderAdoption(t *testing.T) {
	for _, tt := range []struct {
		name, identityHeader, parentHeader string
		mismatch, otherCaller, explicit    bool
	}{
		{name: "session", identityHeader: "X-Session-Id", parentHeader: "X-Parent-Session-Id"},
		{name: "thread", identityHeader: "X-Thread-Id", parentHeader: "X-Parent-Thread-Id"},
		{name: "conversation", identityHeader: "X-Conversation-Id", parentHeader: "X-Parent-Conversation-Id"},
		{name: "different content", identityHeader: "X-Session-Id", parentHeader: "X-Parent-Session-Id", mismatch: true},
		{name: "different caller", identityHeader: "X-Session-Id", parentHeader: "X-Parent-Session-Id", otherCaller: true},
		{name: "explicit cache key", identityHeader: "X-Session-Id", parentHeader: "X-Parent-Session-Id", explicit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := NewCodexExecutor(affinityTestConfig("client-aware"))
			auth := affinityTestAuth("", "oauth")
			parentBody := []byte(`{"model":"m","input":[{"role":"user","content":"unique task"},{"role":"assistant","content":"answer"}]}`)
			parentReq := execpkg.Request{Model: "m", Payload: parentBody, Metadata: map[string]any{
				execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: "parent-request",
			}}
			parent, _, parentState, err := e.cacheHelper(context.Background(), translator.FormatOpenAIResponse, "https://example.invalid/responses", auth, parentReq, parentBody, parentBody, http.Header{tt.identityHeader: {"parent"}})
			if err != nil || parentState.affinity == nil {
				t.Fatalf("prepare parent: %v", err)
			}
			parentState.affinity.complete([]byte(affinityCompleted))
			parentState.affinity.close()

			childBody := []byte(`{"model":"m","input":[{"role":"user","content":"unique task"},{"role":"assistant","content":"answer"},{"role":"user","content":"branch task"}]}`)
			if tt.mismatch {
				childBody = []byte(strings.Replace(string(childBody), "unique task", "different task", 1))
			}
			if tt.explicit {
				var errSet error
				if childBody, errSet = ojson.SetBytes(childBody, "prompt_cache_key", "client-cache-key"); errSet != nil {
					t.Fatalf("set explicit cache key: %v", errSet)
				}
			}
			caller := "caller"
			if tt.otherCaller {
				caller = "other-caller"
			}
			childReq := execpkg.Request{Model: "m", Payload: childBody, Metadata: map[string]any{
				execpkg.CallerScopeMetadataKey: caller, execpkg.RequestIDMetadataKey: "child-request",
			}}
			child, body, childState, err := e.cacheHelper(context.Background(), translator.FormatOpenAIResponse, "https://example.invalid/responses", auth, childReq, childBody, childBody, http.Header{
				tt.identityHeader: {"child"}, tt.parentHeader: {"parent"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer childState.affinity.close()
			wantInherited := !tt.mismatch && !tt.otherCaller && !tt.explicit
			inherited := child.Header.Get("Session-Id") == parent.Header.Get("Session-Id")
			if inherited != wantInherited {
				t.Fatalf("inherited parent cache group = %v, want %v", inherited, wantInherited)
			}
			if tt.explicit {
				if childState.affinity != nil || ojson.GetBytes(body, "prompt_cache_key").String() != "client-cache-key" {
					t.Fatal("explicit cache key entered inferred parent routing")
				}
			} else if ojson.GetBytes(body, "prompt_cache_key").Exists() {
				t.Fatal("parent inheritance synthesized a prompt cache key")
			}
			if wantInherited && (childState.affinity == nil || !childState.affinity.adopted) {
				t.Fatal("matching parent was not adopted")
			}
		})
	}
}
