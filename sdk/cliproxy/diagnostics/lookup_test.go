package diagnostics

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCacheTraceLookupRespectsAssociationAndPredecessor(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	send := func(id, caller, session, receivedID, previousID string) (*Ref, *AttemptRef) {
		now = now.Add(time.Second)
		body := []byte(fmt.Sprintf(`{"prompt_cache_key":"shared-cache-key","previous_response_id":%q}`, previousID))
		ctx := BeginSelection(store.Begin(usage.WithTraceID(t.Context(), "log-"+id), id, caller, session, "model", "responses", body))
		ex := &Execution{Ref: FromContext(ctx), ID: "exec-" + id, AuthIndex: id, Provider: "codex"}
		headers := http.Header{"Session-Id": {"wire-session-" + session}}
		attempt := ex.Begin("websocket", "wss://synthetic.invalid/v1/responses", body, headers)
		attempt.Observe([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","usage":{"input_tokens":15,"input_tokens_details":{"cached_tokens":0}}}}`, receivedID)))
		attempt.End("completed")
		return ex.Ref, attempt
	}
	first, _ := send("root", "caller-A", "session-1", "resp_parent", "")
	foreign, _ := send("foreign", "caller-B", "session-3", "resp_parent", "")
	branch, _ := send("branch", "caller-A", "session-2", "resp_branch", "resp_parent")
	send("edit", "caller-A", "session-2", "resp_edit", "resp_parent")
	send("no-parent", "caller-A", "session-2", "resp_no_parent", "")

	for kind, query := range map[string]string{
		"request_id": "exec-branch", "trace_id": "log-branch", "diagnostics_id": branch.ID(),
		"logical_request_id": "branch", "response_id": "resp_branch",
	} {
		rows, _ := store.Lookup(TraceQuery{ID: query})
		if len(rows) != 1 || !slices.Contains(rows[0].Matched, kind) || rows[0].RequestID != "exec-branch" {
			t.Fatalf("wrong %s: %+v", kind, rows)
		}
		if p := rows[0].Previous; p == nil || p.DiagnosticsID != first.ID() || p.DiagnosticsID == foreign.ID() {
			t.Fatalf("lost exact prior response: %+v", p)
		}
	}
	rows, _ := store.Lookup(TraceQuery{ID: "resp_parent"})
	if len(rows) != 2 {
		t.Fatalf("expected separate duplicate ID candidates, got %d", len(rows))
	}
	rows, _ = store.Lookup(TraceQuery{ID: "resp_no_parent"})
	if len(rows) != 1 || rows[0].Previous != nil {
		t.Fatal("time adjacency produced a predecessor")
	}
	trace, _ := store.Get(branch.ID())
	var pck, session CacheRouteKey
	for _, ref := range trace.Attempts[0].CacheReferences {
		if ref.Kind == "pck" {
			pck = ref.CacheRouteKey
		}
	}
	session = trace.InputSession.CacheRouteKey
	sameKey, _ := store.Lookup(TraceQuery{Route: &pck})
	sameSession, _ := store.Lookup(TraceQuery{Route: &session})
	if len(sameKey) != 4 || len(sameSession) != 3 {
		t.Fatalf("PCK and Session scope differ: %d/%d", len(sameKey), len(sameSession))
	}
	// Reads do not bump timestamps or admit expired traces.
	before, _ := store.Get(branch.ID())
	_, scan := store.Lookup(TraceQuery{})
	_, again := store.Lookup(TraceQuery{})
	after, _ := store.Get(branch.ID())
	if scan != again || before.UpdatedAt != after.UpdatedAt {
		t.Fatal("read changed snapshot")
	}
	now = now.Add(Retention + time.Second)
	expired, _ := store.Lookup(TraceQuery{ID: "resp_branch"})
	if len(expired) != 0 || store.Overview().Active != 0 {
		t.Fatal("read revived an expired trace")
	}
}

func TestCacheTraceLookupSeparatesResponseIDAndResend(t *testing.T) {
	store := NewStore()
	ctx := BeginSelection(store.Begin(t.Context(), "one", "caller", "", "model", "responses", []byte(`{}`)))
	execution := &Execution{Ref: FromContext(ctx), ID: "execution", Provider: "codex"}
	a := execution.Begin("http", "https://synthetic.invalid/v1/responses", []byte(`{}`), nil)
	a.Status(503, false)
	b := execution.Begin("websocket", "wss://synthetic.invalid/v1/responses", []byte(`{}`), nil)
	b.Observe([]byte(`{"type":"response.created","response":{"id":"superseded"}}`))
	b.Observe([]byte(`{"type":"response.completed","response":{"id":"real","status":"completed"}}`))
	b.End("completed")
	execution.Ref.Record(Event{Layer: "transport", Method: "websocket_successor", Result: "no_client_dispatch"})

	for _, id := range []string{"execution", "superseded", "real"} {
		rows, _ := store.Lookup(TraceQuery{ID: id})
		if len(rows) != 1 || rows[0].Attempt != 2 || rows[0].CacheRoute.AttemptCount != 2 {
			t.Fatalf("id %s: %+v", id, rows)
		}
	}
	trace, _ := store.Get(execution.Ref.ID())
	if trace.Attempts[0].Status != 503 || trace.Attempts[0].ResponseID != "" ||
		trace.Attempts[0].RouteScope != trace.Attempts[1].RouteScope {
		t.Fatalf("attempts were merged or differ by transport: %+v", trace.Attempts)
	}
}

func TestCacheTraceClaudeContinuityUsesCompletedSameCredentialMessage(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	send := func(id, auth, request, response string, stop bool) TraceRow {
		t.Helper()
		now = now.Add(time.Second)
		ctx := store.Begin(t.Context(), id, "caller", "conversation", "claude", "claude", []byte(request))
		execution := &Execution{Ref: FromContext(ctx), ID: "exec-" + id, Provider: "claude", AuthIndex: auth}
		attempt := execution.Begin("http", "https://fixture.invalid/v1/messages", []byte(request), nil)
		attempt.Status(200, false)
		attempt.Observe([]byte(fmt.Sprintf(`{"type":"message_start","message":{"id":%q,"usage":{"input_tokens":12}}}`, response)))
		if stop {
			attempt.Observe([]byte(`{"type":"message_stop"}`))
		}
		attempt.End("body_consumed")
		rows, _ := store.Lookup(TraceQuery{ID: "exec-" + id})
		if len(rows) != 1 || rows[0].ResponseID != response {
			t.Fatalf("lookup rows: %+v", rows)
		}
		return rows[0]
	}
	parent := send("parent", "auth-A", `{}`, "msg_thread", true)
	comparison := send("comparison", "auth-A", `{}`, "msg_comparison", true)
	send("other-auth", "auth-B", `{}`, "msg_thread", true)
	send("incomplete", "auth-A", `{}`, "msg_partial", false)
	for _, tt := range []struct {
		id, request, want string
	}{
		{"thread", `{"thread":{"type":"continue","previous_message_id":"msg_thread"},"diagnostics":{"previous_message_id":"msg_comparison"}}`, parent.DiagnosticsID},
		{"comparison-only", `{"diagnostics":{"previous_message_id":"msg_comparison"}}`, comparison.DiagnosticsID},
		{"null", `{"diagnostics":{"previous_message_id":null}}`, ""},
		{"incomplete-parent", `{"thread":{"type":"continue","previous_message_id":"msg_partial"}}`, ""},
	} {
		row := send(tt.id, "auth-A", tt.request, "msg_"+tt.id, true)
		got := ""
		if row.Previous != nil {
			got = row.Previous.DiagnosticsID
		}
		if got != tt.want {
			t.Errorf("%s predecessor %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestCacheTraceOfficialLossUsesReportedAmount(t *testing.T) {
	for _, tt := range []struct {
		name, diag string
		loss       bool
	}{
		{"partial", `"type":"cache_hit","cache_missed_tokens":24`, true},
		{"miss", `"type":"cache_miss"`, true},
		{"hit", `"type":"cache_hit","cache_missed_tokens":0`, false},
		{"baseline absent", `"type":"comparison_response_not_found"`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore()
			ctx := store.Begin(t.Context(), "case", "caller", "", "model", "responses", []byte(`{}`))
			ex := &Execution{Ref: FromContext(ctx), Provider: "codex", ID: "exec"}
			attempt := ex.Begin("http", "https://fixture.invalid/v1/responses", []byte(`{}`), nil)
			attempt.Observe([]byte(`{"object":"response","id":"resp","status":"completed","prompt_cache_diagnostics":{` + tt.diag + `}}`))
			recorded, _ := store.Lookup(TraceQuery{ID: ex.ID})
			if len(recorded) != 1 {
				t.Fatalf("recorded attempts = %d", len(recorded))
			}
			route := recorded[0].CacheRoute
			rows, _ := store.Lookup(TraceQuery{Focus: "official_loss"})
			if (len(rows) == 1) != tt.loss || route.OfficialLoss != tt.loss {
				t.Fatalf("loss evidence in focus=%d summary=%t, want %t", len(rows), route.OfficialLoss, tt.loss)
			}
		})
	}
}

func TestCacheRouteCapturesMetaSessionAndClaudeFieldDigests(t *testing.T) {
	store := NewStore()
	ctx := store.Begin(t.Context(), "case", "caller", "", "model", "meta", []byte(`{}`))
	ex := &Execution{Ref: FromContext(ctx), Provider: "meta", ID: "exec"}
	headers := http.Header{"X-Tbh-Session-Id": {"synthetic-meta-session"}, "Session-Id": {"other-input"}}
	ex.Begin("http", "https://fixture.invalid/v1/chat", []byte(`{}`), headers)
	rows, _ := store.Lookup(TraceQuery{ID: ex.ID})
	if len(rows) != 1 {
		t.Fatalf("recorded attempts = %d", len(rows))
	}
	route := rows[0].CacheRoute
	if len(route.References) != 1 ||
		route.References[0].Source != "X-Tbh-Session-Id" ||
		route.References[0].Digest != Hash("synthetic-meta-session") || !route.References[0].ValidFilter() {
		t.Errorf("Meta route: %+v", route)
	}
	prev := Snapshot([]byte(`{"thread":{"type":"continue","previous_message_id":"msg_private"},"diagnostics":{"previous_message_id":"msg_previous"}}`))
	reset := Snapshot([]byte(`{"thread":{"type":"create"},"diagnostics":{"previous_message_id":null}}`))
	for _, path := range []string{"thread.previous_message_id", "diagnostics.previous_message_id"} {
		if !slices.Contains(Differences(prev, reset), path) || prev.Fields[path].Value != "" {
			t.Errorf("%s not preserved as a private digest: %+v", path, prev)
		}
	}
}

func TestRecordUsageIgnoresAttemptBeyondCap(t *testing.T) {
	store := NewStore()
	ref := FromContext(store.Begin(t.Context(), "cap-case", "caller", "", "model", "responses", []byte(`{}`)))
	ex := &Execution{Ref: ref, ID: "execution", Provider: "codex"}
	var latest *AttemptRef
	for i := 0; i < MaxAttempts; i++ {
		latest = ex.Begin("http", "https://fixture.invalid/v1/responses", []byte(`{}`), nil)
		if latest == nil {
			t.Fatalf("attempt %d unexpectedly dropped", i+1)
		}
		latest.Status(200, false)
	}
	trace, ok := store.Get(ref.ID())
	if !ok || len(trace.Attempts) != MaxAttempts {
		t.Fatalf("retained attempts = %d, want %d", len(trace.Attempts), MaxAttempts)
	}
	latest.RecordUsage(usage.Detail{InputTokens: 10})
	dropped := ex.Begin("http", "https://fixture.invalid/v1/responses", []byte(`{}`), nil)
	if dropped != nil {
		t.Fatalf("expected attempt beyond cap to return nil, got %+v", dropped)
	}
	dropped.WithUsageObserver("codex", nil).RecordUsage(usage.Detail{InputTokens: 47})
	trace, _ = store.Get(ref.ID())
	last := trace.Attempts[MaxAttempts-1]
	if last.InputTokens == nil || *last.InputTokens != 10 {
		t.Fatalf("last retained attempt InputTokens = %v, want 10", last.InputTokens)
	}
}

func TestCacheTraceRevisionTracksPredecessorExpiry(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	send := func(id, receivedID, previousID string) {
		body := []byte(fmt.Sprintf(`{"prompt_cache_key":"shared-cache-key","previous_response_id":%q}`, previousID))
		ctx := BeginSelection(store.Begin(usage.WithTraceID(t.Context(), "log-"+id), id, "caller-A", "session-1", "model", "responses", body))
		ex := &Execution{Ref: FromContext(ctx), ID: "exec-" + id, AuthIndex: id, Provider: "codex"}
		headers := http.Header{"Session-Id": {"wire-session-1"}}
		attempt := ex.Begin("websocket", "wss://synthetic.invalid/v1/responses", body, headers)
		attempt.Observe([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","usage":{"input_tokens":15,"input_tokens_details":{"cached_tokens":0}}}}`, receivedID)))
		attempt.End("completed")
	}
	send("parent", "resp_parent", "")
	now = now.Add(Retention - 5*time.Second)
	send("child", "resp_child", "resp_parent")

	rows1, rev1 := store.Lookup(TraceQuery{ID: "exec-child"})
	if len(rows1) != 1 || rows1[0].Previous == nil {
		t.Fatalf("expected predecessor before expiry: %+v", rows1)
	}

	now = now.Add(10 * time.Second)
	rows2, rev2 := store.Lookup(TraceQuery{ID: "exec-child"})
	if len(rows2) != 1 || rows2[0].Previous != nil {
		t.Fatalf("expected predecessor to expire: %+v", rows2)
	}
	if rev2 == rev1 {
		t.Fatalf("expected revision to change after predecessor expiry, stayed %q", rev2)
	}
}
