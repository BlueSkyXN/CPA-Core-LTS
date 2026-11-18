package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"testing"
	"time"
)

func TestCacheDiagnosticsExplicitIdentitySkipsLCPAndSnapshotIsReadOnly(t *testing.T) {
	m := NewManager(nil, nil, nil)
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()
	m.SetSelector(selector)
	a := &Auth{ID: "diagnostic-auth", Provider: "openai"}
	a.EnsureIndex()
	_, _ = m.Register(t.Context(), a)
	ctx := m.cacheDiagnostics.Begin(t.Context(), "request", "scope", "", "model", "openai", []byte(`{}`))
	ctx = diagnostics.BeginSelection(ctx)
	opts := exec.Options{Headers: map[string][]string{"X-Session-Id": {"synthetic-session"}}, Metadata: map[string]any{}}
	if _, err := selector.Pick(ctx, "openai", "model", opts, []*Auth{a}); err != nil {
		t.Fatal(err)
	}
	trace, _ := m.CacheDiagnostics(diagnostics.FromContext(ctx).ID())
	skipped := false
	for _, event := range trace.Events {
		if event.Method == "lcp" {
			if event.Result != "not_run" || event.Reason != "explicit_identity" {
				t.Fatalf("invented match: %+v", event)
			}
			skipped = true
		}
	}
	if !skipped {
		t.Fatal("missing skip event")
	}
	current := m.SnapshotCacheSession("openai", "model", trace.SessionHash)
	if current.Status != "bound" || len(current.Bindings) == 0 {
		t.Fatalf("snapshot=%+v", current)
	}
	// Expired objects are reported, not removed; queries cannot prolong TTL.
	selector.cache.mu.Lock()
	for key, entry := range selector.cache.groups {
		entry.expiresAt = time.Now().Add(-time.Hour)
		selector.cache.groups[key] = entry
	}
	before := len(selector.cache.groups)
	selector.cache.mu.Unlock()
	current = m.SnapshotCacheSession("openai", "model", trace.SessionHash)
	if current.Status != "unbound" || len(current.Bindings) == 0 || !current.Bindings[0].Expired {
		t.Fatalf("expired snapshot=%+v", current)
	}
	selector.cache.mu.RLock()
	after := len(selector.cache.groups)
	selector.cache.mu.RUnlock()
	if before != after {
		t.Fatal("query cleaned cache")
	}
}

func TestCacheDiagnosticsSnapshotUsesActivePluginScheduler(t *testing.T) {
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()
	m := NewManager(nil, selector, nil)
	for _, tc := range []struct {
		name      string
		scheduler PluginScheduler
		want      string
	}{
		{"no_host", nil, "unbound"},
		{"idle_host", &inactivePluginScheduler{}, "unbound"},
		{"active_scheduler", &fakePluginScheduler{}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.SetPluginScheduler(tc.scheduler)
			snapshot := m.SnapshotCacheSession("codex", "model", diagnostics.Hash("session"))
			if snapshot.Status != tc.want {
				t.Errorf("snapshot status = %q, want %q", snapshot.Status, tc.want)
			}
		})
	}
}

func TestCacheDiagnosticsLCPNoCandidate(t *testing.T) {
	s := NewSessionAffinitySelector(nil)
	defer s.Stop()
	store := diagnostics.NewStore()
	ctx := store.Begin(t.Context(), "request", "caller", "", "model", "openai", []byte(`{}`))
	opts := exec.Options{OriginalRequest: []byte(`{"messages":[{"role":"system","content":"synthetic system"},{"role":"user","content":"synthetic user"}]}`), Metadata: map[string]any{exec.CallerScopeMetadataKey: "caller"}}
	_, err := s.Pick(ctx, "openai", "model", opts, []*Auth{{ID: "auth", Provider: "openai"}})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
	found := false
	for _, event := range trace.Events {
		if event.Method == "lcp" {
			if event.Result != "no_candidate" || event.ObjectHash != "" {
				t.Fatalf("invented candidate: %+v", event)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing search result")
	}
	if trace.SessionHash == "" {
		t.Fatal("new LCP session was not linked")
	}
}

type cacheRetryExecutor struct {
	ProviderExecutor
	calls   int
	traceID string
}

func (*cacheRetryExecutor) Identifier() string { return "cache-diagnostic-test" }
func (e *cacheRetryExecutor) Execute(ctx context.Context, a *Auth, req exec.Request, _ exec.Options) (exec.Response, error) {
	e.calls++
	ref := diagnostics.FromContext(ctx)
	e.traceID = ref.ID()
	attempt := (&diagnostics.Execution{Ref: ref, ID: a.ID + "-execution", AuthIndex: a.Index, Provider: e.Identifier(), Model: req.Model}).Begin("http", "custom", req.Payload, nil)
	if e.calls == 1 {
		attempt.Status(503, false)
		attempt.End("body_consumed")
		return exec.Response{}, &Error{Code: "server_error", Message: "synthetic failure", HTTPStatus: 503}
	}
	attempt.Status(200, false)
	attempt.Observe([]byte(`{"object":"response","usage":{"input_tokens_details":{"cached_tokens":12}}}`))
	attempt.End("body_consumed")
	return exec.Response{}, nil
}
func TestCacheDiagnosticsManagerRetryKeepsDecisions(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&config.Config{UsageStatisticsEnabled: true})
	e := &cacheRetryExecutor{}
	m.RegisterExecutor(e)
	for _, id := range []string{"diagnostic-A", "diagnostic-B"} {
		a := &Auth{ID: id, Provider: e.Identifier(), Status: StatusActive}
		_, err := m.Register(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, a.Provider, []*registry.ModelInfo{{ID: "diagnostic-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	_, err := m.Execute(t.Context(), []string{e.Identifier()}, exec.Request{Model: "diagnostic-model", Payload: []byte(`{"input":"hello"}`)}, exec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	trace, ok := m.CacheDiagnostics(e.traceID)
	if !ok || len(trace.Attempts) != 2 {
		t.Fatalf("attempts=%+v", trace)
	}
	a, b := trace.Attempts[0], trace.Attempts[1]
	if a.DecisionID == "" || a.DecisionID == b.DecisionID || a.AuthIndex == b.AuthIndex || a.Status != 503 || b.Status != 200 || a.CacheObservation != nil {
		t.Fatalf("attempts combined: %+v", trace.Attempts)
	}
	for _, attempt := range trace.Attempts {
		found := false
		for _, event := range trace.Events {
			if event.Method == "selection" && event.DecisionID == attempt.DecisionID {
				found = true
				if event.AuthIndex != attempt.AuthIndex {
					t.Fatal("wrong credential reason")
				}
			}
		}
		if !found {
			t.Fatal("orphan attempt")
		}
	}
}
