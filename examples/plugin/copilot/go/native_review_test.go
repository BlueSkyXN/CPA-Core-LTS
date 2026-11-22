package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestNativeReplayAbsentAndUnretainable(t *testing.T) {
	const state = "state-from-A"
	large := strings.Repeat("x", nativeReasoningMaxBytes+1)
	for _, tc := range []struct {
		name, first, second string
		ambiguous           bool
	}{
		{"both absent", "", "", false}, {"absent after opaque", state, "", true}, {"absent before opaque", "", state, true},
		{"oversized after opaque", state, large, true}, {"oversized before opaque", large, state, true}, {"only oversized", large, large, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPluginRuntime(nil)
			exec := &nativeExecution{identity: executionIdentity{key: "fixture-scope"}}
			d := newNativeDecoder("fixture", "model", nil)
			if _, err := d.data([]byte(copilotToolResponse)); err != nil {
				t.Fatal(err)
			}
			for _, opaque := range []string{tc.first, tc.second} {
				d.projection.reasoningOpaque = opaque
				r.rememberNativeReasoning(exec, "model", d.projection)
			}
			if d := r.nativeReasoning[nativeReasoningKey(exec, "model", d.projection.toolCallPayload())]; d.opaque != "" {
				t.Fatal("retained or reused another turn's state")
			}
			body := copilotToolFollow("B", "call-fixture", "").Payload
			out, err := r.restoreNativeReasoning(exec, "model", body, nil)
			if tc.ambiguous {
				expectCopilotConflict(t, err, "tool_continuation_ambiguous", false)
			} else if err != nil || !bytes.Equal(out, body) {
				t.Fatalf("two stateless calls should pass unchanged: %v", err)
			}
		})
	}
}

func TestNativeGuardUsesPublishedToolIdentity(t *testing.T) {
	r := newPluginRuntime(&copilotFixtureHost{})
	req := copilotReq("producer", true)
	auth, _ := parseStoredAuth(req.StorageJSON)
	exec, err := r.registerNative(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer r.releaseNative(exec)
	for _, completed := range []string{copilotToolResponse, strings.NewReplacer("lookup", "weather", "fixture-opaque-state", "weather-state").Replace(copilotToolResponse)} {
		other := newNativeDecoder("completed", req.Model, nil)
		if _, err := other.data([]byte(completed)); err != nil {
			t.Fatal(err)
		}
		r.rememberNativeReasoning(exec, req.Model, other.projection)
	}
	base := copilotToolFollow("follow", "call-fixture", "").Payload
	follower := &nativeExecution{identity: exec.identity, model: exec.model, generation: exec.generation}
	check := func(name, args, wantState string, pending bool) {
		t.Helper()
		request := strings.Replace(string(base), `lookup`, name, 1)
		request = strings.Replace(request, `{\"city\":\"Paris\"}`, args, 1)
		out, err := r.restoreNativeReasoning(follower, req.Model, []byte(request), nil)
		if pending {
			expectCopilotConflict(t, err, "tool_continuation_pending", true)
		} else if err != nil || ojson.GetBytes(out, "messages.0.reasoning_opaque").String() != wantState {
			t.Fatalf("completed branch %s blocked or lost its own state: %v", name, err)
		}
	}
	d := newNativeDecoder("producer", req.Model, func([]byte) error { return nil })
	d.beforeTool = func(index int, tool nativeToolIdentity) { r.trackNativeTool(exec, index, tool) }
	feed := func(s string) {
		t.Helper()
		if _, err := d.data([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-fixture","function":{"name":"lookup","arguments":"{\"city\":"}}]}}]}`)
	check("weather", `{\"city\":\"Paris\"}`, "weather-state", false)
	check("unseen", "{}", "", false)
	check("lookup", `{\"city\":\"Paris\"}`, "", true)
	feed(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Rome\"}"}}]}}]}`)
	check("lookup", `{\"city\":\"Paris\"}`, "", true)
	feed(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	check("lookup", `{\"city\":\"Paris\"}`, "fixture-opaque-state", false)
	check("lookup", `{ \"city\" : \"Rome\" }`, "", true)
}

func TestNativeAdmissionConfigGenerationSnapshot(t *testing.T) {
	r := newPluginRuntime(&copilotFixtureHost{})
	req := copilotReq("old", false)
	auth, _ := parseStoredAuth(req.StorageJSON)
	old, err := r.registerNative(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer r.releaseNative(old)
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("chat_cache_breakpoints: true\n")})
	if err := r.configure(raw); err != nil {
		t.Fatal(err)
	}
	req.RequestID = "new"
	fresh, err := r.registerNative(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer r.releaseNative(fresh)
	if old.config.ChatCacheBreakpoints || !fresh.config.ChatCacheBreakpoints || old.generation == fresh.generation {
		t.Fatal("configuration and replay generation did not switch together")
	}
	d := newNativeDecoder("old", req.Model, nil)
	if _, err := d.data([]byte(copilotToolResponse)); err != nil {
		t.Fatal(err)
	}
	r.rememberNativeReasoning(old, req.Model, d.projection)
	if len(r.nativeReasoning) != 0 {
		t.Fatal("old config wrote current generation replay")
	}
}

func TestNativeStaleAdmissionCannotRepopulateCatalog(t *testing.T) {
	r, _ := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			serveCopilotModels(w)
		default:
			t.Errorf("unexpected request to %s", req.URL.Path)
		}
	})
	req := copilotReq("old-config", false)
	auth, _ := parseStoredAuth(req.StorageJSON)
	old, err := r.registerNative(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer r.releaseNative(old)
	cfg := old.config
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: fmt.Appendf(nil, "github_api_endpoint: %s\ncopilot_api_endpoint: %s\nexcluded_model_prefixes: [gpt-5]\n", cfg.GitHubAPIEndpoint, cfg.CopilotAPIEndpoint)})
	if err := r.configure(raw); err != nil {
		t.Fatal(err)
	}
	// 两个请求用同一账号、同一端点。旧执行在重配后才开始取目录。
	models, err := r.nativeModels(auth, req.HostCallbackID, cfg)
	if err != nil || !catalogAllowsMethod(models, req.Model, "chat") {
		t.Fatalf("old admission lost its model: %v", err)
	}
	if len(r.tokenCache) != 0 || len(r.nativeCatalogs) != 0 {
		t.Error("stale admission populated current token or model cache")
	}
	models, err = r.nativeModels(auth, req.HostCallbackID, r.loadedConfig())
	if err != nil || catalogAllowsMethod(models, req.Model, "chat") || !catalogAllowsMethod(models, "gpt-4.1-mini", "chat") {
		t.Fatalf("reconfigured catalog did not honor its model exclusions: %v", err)
	}
}

func TestNativeOriginalCarriersRequireScopedWitness(t *testing.T) {
	r := newPluginRuntime(nil)
	exec := &nativeExecution{identity: executionIdentity{key: "same"}}
	d := newNativeDecoder("branch", "model", nil)
	if _, err := d.data([]byte(copilotToolResponse)); err != nil {
		t.Fatal(err)
	}
	r.rememberNativeReasoning(exec, "model", d.projection)
	d.projection.reasoningOpaque = "other-state"
	r.rememberNativeReasoning(exec, "model", d.projection)
	chat := copilotToolFollow("follow", "call-fixture", "").Payload
	for _, tc := range []struct{ name, raw string }{
		{"responses", `{"input":[{"type":"reasoning","encrypted_content":"other-state"},{"type":"function_call","call_id":"call-fixture","name":"lookup","arguments":"{\"city\":\"Paris\"}"},{"type":"function_call_output","call_id":"call-fixture","output":"result"}]}`},
		{"messages", `{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"other-state"},{"type":"tool_use","id":"call-fixture","name":"lookup","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-fixture","content":"result"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := r.restoreNativeReasoning(exec, "model", chat, []byte(tc.raw))
			if err != nil || ojson.GetBytes(out, "messages.0.reasoning_opaque").String() != "other-state" {
				t.Fatalf("verified state did not round trip: %v", err)
			}
			for _, bad := range []string{strings.ReplaceAll(tc.raw, "other-state", "foreign"), strings.ReplaceAll(tc.raw, "Paris", "Rome"), strings.ReplaceAll(tc.raw, "call-fixture", "wrong")} {
				_, err = r.restoreNativeReasoning(exec, "model", chat, []byte(bad))
				expectCopilotConflict(t, err, "tool_continuation_ambiguous", false)
				if strings.Contains(err.Error(), "include the original assistant reasoning") {
					t.Fatal("unusable remediation for translated client")
				}
			}
			out, err = r.restoreNativeReasoning(&nativeExecution{identity: executionIdentity{key: "other-scope"}}, "model", chat, []byte(tc.raw))
			if err != nil || ojson.GetBytes(out, "messages.0.reasoning_opaque").Exists() {
				t.Fatal("unwitnessed carrier crossed scope")
			}
		})
	}
}

func TestNativeUnreadableOriginalNeverAddsCacheMarkers(t *testing.T) {
	for _, src := range []string{`{"input":`, `null`, `{"input":"hi","prompt_cache_options":{"mode":"implicit"}` + strings.Repeat(" ", maxExecutorBodyBytes) + `}`} {
		req := copilotReq("large-original", false).ExecutorRequest
		req.Payload = []byte(strings.Replace(cacheFixtureAB, `"role":"system"`, `"role":"system","copilot_cache_control":{"type":"ephemeral"}`, 1))
		req.OriginalRequest = []byte(src)
		out, err := nativeRequestPayload(req, true)
		if err != nil {
			t.Fatal(err)
		}
		if inds := cachedIndexes(t, out); fmt.Sprint(inds) != "[0]" {
			t.Fatalf("unreadable original added markers: %v", inds)
		}
	}
}
