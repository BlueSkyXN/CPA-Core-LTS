package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

type flowFixtureExecutor struct {
	calls  atomic.Int32
	finish chan struct{}
}

func (*flowFixtureExecutor) Identifier() string { return "flow-fixture" }
func (e *flowFixtureExecutor) Execute(ctx context.Context, _ *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	e.calls.Add(1)
	select {
	case <-ctx.Done():
		return executor.Response{}, ctx.Err()
	case <-e.finish:
		return executor.Response{Payload: []byte("ok")}, nil
	}
}
func (e *flowFixtureExecutor) CountTokens(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (*flowFixtureExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (*flowFixtureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *flowFixtureExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ executor.Request, _ executor.Options) (*executor.StreamResult, error) {
	e.calls.Add(1)
	ch := make(chan executor.StreamChunk)
	go func() {
		defer close(ch)
		select {
		case ch <- executor.StreamChunk{Payload: []byte("first")}:
		case <-ctx.Done():
			return
		}
		select {
		case <-e.finish:
		case <-ctx.Done():
		}
	}()
	return &executor.StreamResult{Chunks: ch}, nil
}
func flowFixture(t *testing.T, c flowcontrol.Config) (*Manager, *flowFixtureExecutor, *Auth) {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	e := &flowFixtureExecutor{finish: make(chan struct{})}
	m.RegisterExecutor(e)
	m.SetConfig(&internalconfig.Config{FlowControl: c})
	a, err := m.Register(context.Background(), &Auth{ID: "flow-" + t.Name(), Provider: e.Identifier(), Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, e.Identifier(), []*registry.ModelInfo{{ID: "flow-model"}})
	t.Cleanup(func() { m.CloseFlowControl(); registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	return m, e, a
}
func waitFlow(t *testing.T, m *Manager, predicate func(flowcontrol.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if predicate(m.FlowControlSnapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state did not converge: %+v", m.FlowControlSnapshot())
}
func TestFlowManagerStreamingHoldsAccountAndCanceledQueueDoesNotCooldown(t *testing.T) {
	c := flowcontrol.Config{Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 4, MaxWaitMS: 1000}, Rules: []flowcontrol.Rule{
		{ID: "logical", Stage: "request", Scope: "global", MaxConcurrent: 8}, {ID: "account", Stage: "attempt", Scope: "account", MaxConcurrent: 1},
	}}
	m, e, a := flowFixture(t, c)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first, err := m.ExecuteStream(firstCtx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-first.Chunks; !ok {
		t.Fatal("first chunk missing")
	}
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Attempts == 1 && s.Requests == 1 })
	secondCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(secondCtx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{})
		done <- err
	}()
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Waiting == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Quota.Exceeded || current.Failed != 0 || e.calls.Load() != 1 {
		t.Fatalf("local cancellation altered quota or dispatched: %+v", current)
	}
	close(e.finish)
	for range first.Chunks {
	}
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Requests == 0 && s.Attempts == 0 && s.Waiting == 0 })
}
func TestFlowManagerLocalBusyNeverRunsExecutor(t *testing.T) {
	c := flowcontrol.Config{Enabled: true, Rules: []flowcontrol.Rule{{ID: "key", Stage: "request", Scope: "key", MaxConcurrent: 1}}}
	m, e, a := flowFixture(t, c)
	p, err := m.flowControl.Acquire(context.Background(), flowcontrol.Identity{Stage: "request", Key: "anonymous"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	_, err = m.Execute(context.Background(), []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{})
	if !flowcontrol.IsError(err) || SafeResponseHeaders(err).Get("Retry-After") == "" {
		t.Fatalf("busy: %v", err)
	}
	current, _ := m.GetByID(a.ID)
	if e.calls.Load() != 0 || current.Quota.Exceeded || current.Failed != 0 {
		t.Fatal("busy counted as upstream failure")
	}
}
func TestLegacyFlowAccountReferenceCompatibility(t *testing.T) {
	a := &Auth{ID: "one", Provider: "codex", Metadata: map[string]any{"account_id": "same", "access_token": "secret1"}}
	b := &Auth{ID: "two", Provider: "codex", Metadata: map[string]any{"account_id": "same", "access_token": "secret2"}}
	if legacyFlowAccountReference(a) != legacyFlowAccountReference(b) {
		t.Fatal("duplicate account files get separate caps")
	}
	b.Provider = "other"
	if legacyFlowAccountReference(a) == legacyFlowAccountReference(b) {
		t.Fatal("cross-provider group collision")
	}
}
func TestFlowWaitingDoesNotRejectOrdinaryAuthGenerationUpdate(t *testing.T) {
	c := flowcontrol.Config{Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 2, MaxWaitMS: 1000}, Rules: []flowcontrol.Rule{{ID: "a", Stage: "attempt", Scope: "account", MaxConcurrent: 1}}}
	m, e, a := flowFixture(t, c)
	req := executor.Request{Model: "flow-model"}
	p, _ := m.flowControl.Acquire(context.Background(), m.flowIdentity(a, req, executor.Options{}), 0)
	done := make(chan error, 1)
	go func() {
		ctx, err := m.admitFlowExecution(context.Background(), e, a, req, executor.Options{})
		if err == nil {
			flowAttemptPermit(ctx).Release()
		}
		done <- err
	}()
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Waiting == 1 })
	m.mu.Lock()
	m.auths[a.ID].Generation++
	m.mu.Unlock()
	p.Release()
	if err := <-done; err != nil {
		t.Fatalf("ordinary observation incorrectly invalidated account: %v", err)
	}
}
func TestFlowWaitingRejectsDisabledAccountWithoutRateSpend(t *testing.T) {
	c := flowcontrol.Config{Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 2, MaxWaitMS: 1000}, Rules: []flowcontrol.Rule{{ID: "a", Stage: "attempt", Scope: "account", MaxConcurrent: 1, Windows: []flowcontrol.Window{{Requests: 10, PeriodMS: 10000}}}}}
	m, e, a := flowFixture(t, c)
	req := executor.Request{Model: "flow-model"}
	p, _ := m.flowControl.Acquire(context.Background(), m.flowIdentity(a, req, executor.Options{}), 0)
	done := make(chan error, 1)
	go func() {
		_, err := m.admitFlowExecution(context.Background(), e, a, req, executor.Options{})
		done <- err
	}()
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Waiting == 1 })
	m.mu.Lock()
	m.auths[a.ID].Disabled = true
	m.mu.Unlock()
	p.Release()
	if err := <-done; !flowcontrol.IsError(err) {
		t.Fatalf("disabled target was used: %v", err)
	}
	s := m.FlowControlSnapshot()
	if s.Attempts != 0 || s.Buckets[0].WindowCounts[0] != 1 {
		t.Fatalf("zero dispatch leaked rate record: %+v", s)
	}
}

func TestFlowContinuityReservationDoesNotEnterAttemptQueue(t *testing.T) {
	c := flowcontrol.Config{Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 2, MaxWaitMS: 1000}, Rules: []flowcontrol.Rule{{ID: "a", Stage: "attempt", Scope: "account", MaxConcurrent: 1}}}
	m, e, a := flowFixture(t, c)
	req := executor.Request{Model: "flow-model"}
	p, err := m.flowControl.Acquire(context.Background(), m.flowIdentity(a, req, executor.Options{}), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	ctx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, codexRateLimitContinuityAttempt{key: codexRateLimitContinuityKey{authID: a.ID, model: "flow-model"}, sessionID: "session"})
	_, err = m.admitFlowExecution(ctx, e, a, req, executor.Options{})
	if !isLocalFlowControlError(err) || !isRequestStopError(err) {
		t.Fatalf("want local request stop: %v", err)
	}
	if s := m.FlowControlSnapshot(); s.Waiting != 0 || s.Waited != 0 || e.calls.Load() != 0 {
		t.Fatalf("continuity evidence was queued: %+v", s)
	}
}

func TestLegacyFlowCredentialAndAccountReferencesHaveDifferentLifetimes(t *testing.T) {
	a := &Auth{ID: "oauth-file-a", Provider: "codex", Metadata: map[string]any{"account_id": "shared-account", "access_token": "old"}}
	b := a.Clone()
	b.ID = "oauth-file-b"
	if legacyFlowAccountReference(a) != legacyFlowAccountReference(b) || FlowCredentialReference(a) == FlowCredentialReference(b) {
		t.Fatal("account and individual file scopes were conflated")
	}
	first := FlowCredentialReference(a)
	a.Metadata["access_token"] = "refreshed"
	if first != FlowCredentialReference(a) {
		t.Fatal("refresh reset the credential reference")
	}
	a.Attributes = map[string]string{"api_key": "upstream-secret"}
	a.Metadata = nil
	b = a.Clone()
	b.ID = "api-entry-b"
	if legacyFlowAccountReference(a) != legacyFlowAccountReference(b) || FlowCredentialReference(a) == FlowCredentialReference(b) {
		t.Fatal("API-key entries must share account but retain independent credential scopes")
	}
}

func TestFlowDeferredContinuityWaiterBecomesIncumbentOnlyAfterAdmission(t *testing.T) {
	ex := &codexContinuityTestExecutor{failures: make(map[string]error)}
	m := newCodexContinuityManager(t, ex, []string{"v2-wait-auth"}, []string{"gpt-5"}, 2)
	t.Cleanup(m.CloseFlowControl)
	m.SetConfig(&internalconfig.Config{
		Routing:     internalconfig.RoutingConfig{SessionAffinity: true},
		Codex:       internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{Enabled: true, ObservationWindowSeconds: 10, EstablishedSuccessThreshold: 2, EstablishedSessionTTLSeconds: 3600}},
		FlowControl: flowcontrol.Config{Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 2, MaxWaitMS: 1000}, Rules: []flowcontrol.Rule{{ID: "account", Stage: "attempt", Scope: "account", MaxConcurrent: 1}}},
	})
	a, _ := m.GetByID("v2-wait-auth")
	opts := codexContinuityOptions("queued-session")
	req := executor.Request{Model: "gpt-5"}
	held, err := m.flowControl.Acquire(context.Background(), m.flowIdentity(a, req, opts), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	ctx, allowed := m.beginCodexRateLimitContinuityAttempt(context.Background(), a, "codex", req.Model, opts)
	if !allowed {
		t.Fatal("healthy deferred request rejected")
	}
	if _, ok := codexRateLimitContinuityAttemptFromContext(ctx); ok {
		t.Fatal("waiter reserved continuity before capacity")
	}
	type result struct {
		ctx context.Context
		err error
	}
	done := make(chan result, 1)
	go func() { next, err := m.admitFlowExecution(ctx, ex, a, req, opts); done <- result{next, err} }()
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Waiting == 1 })
	if _, ok := codexRateLimitContinuityAttemptFromContext(ctx); ok {
		t.Fatal("queued request became incumbent")
	}
	held.Release()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer flowAttemptPermit(got.ctx).Release()
		if _, ok := codexRateLimitContinuityAttemptFromContext(ctx); !ok {
			t.Fatal("parent context cannot abandon the newly activated attempt")
		}
		m.abandonCodexRateLimitContinuityAttempt(ctx)
		m.codexRateLimitContinuity.mu.Lock()
		defer m.codexRateLimitContinuity.mu.Unlock()
		for _, s := range m.codexRateLimitContinuity.states {
			if len(s.inFlight) != 0 {
				t.Fatal("continuity leaked after abandon")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queue did not resume after release")
	}
}

func TestFlowV3AuthIDIsTheOnlyAccountIdentity(t *testing.T) {
	a := &Auth{ID: "file-a", Provider: "codex", Metadata: map[string]any{"account_id": "same", "flow_control_group": "same", "access_token": "old"}}
	b := a.Clone()
	b.ID = "file-b"
	if FlowAccountReference(a) == FlowAccountReference(b) {
		t.Fatal("inferred grouping in v3")
	}
	old := FlowAccountReference(a)
	a.Metadata["account_id"] = "changed"
	a.Metadata["flow_control_group"] = "changed"
	a.Metadata["access_token"] = "refreshed"
	if FlowAccountReference(a) != old {
		t.Fatal("metadata or refresh changed account identity")
	}
	a.Attributes = map[string]string{"api_key": "new-secret"}
	if FlowAccountReference(a) != old {
		t.Fatal("API key contents changed stable identity")
	}
}
func TestFlowV3ManagerJointFirstWaitAndNoCooldown(t *testing.T) {
	cfg := flowcontrol.Config{Version: 3, Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 4, MaxWaitMS: 2000}, Rules: []flowcontrol.Rule{{ID: "call", Stage: "request", Scope: "key", MaxConcurrent: 3}, {ID: "acct", Stage: "attempt", Scope: "account", MaxConcurrent: 1, Models: []string{"flow-model", "another"}}}}
	m, e, a := flowFixture(t, cfg)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first, err := m.ExecuteStream(firstCtx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-first.Chunks; !ok {
		t.Fatal("no first data")
	}
	secondCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(secondCtx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{})
		done <- err
	}()
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Waiting == 1 })
	s := m.FlowControlSummary()
	if s.Requests != 1 || s.Attempts != 1 || s.WaitingRequests != 1 {
		t.Fatal("waiter consumed logical slot", s)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Quota.Exceeded || current.Failed != 0 || e.calls.Load() != 1 {
		t.Fatal("local wait changed upstream state")
	}
	close(e.finish)
	for range first.Chunks {
	}
	waitFlow(t, m, func(s flowcontrol.Snapshot) bool { return s.Requests+s.Attempts+s.Waiting == 0 })
}

func TestFlowV3PreviewAccountMetadataIsNotInvented(t *testing.T) {
	m, _, a := flowFixture(t, flowcontrol.Config{Version: 3, Enabled: true, Rules: []flowcontrol.Rule{{ID: "a", Stage: "attempt", Scope: "account", MaxConcurrent: 2}}})
	targets := []flowcontrol.Identity{{Stage: "attempt", Account: FlowAccountReference(a), Model: "flow-model"}, {Stage: "attempt", Account: FlowAccountReference(a), Provider: "different", Model: "flow-model"}, {Stage: "attempt", Account: "unavailable", Model: "flow-model"}}
	rows, err := m.PreviewFlowControl(nil, targets)
	if err != nil {
		t.Fatal(err)
	}
	if !rows[0].Complete || rows[0].Identity.Provider != FlowAccountProvider(a) {
		t.Fatal("metadata not resolved", rows[0])
	}
	for _, row := range rows[1:] {
		if row.Complete || row.CanStart || row.Matches[0].Known {
			t.Fatal("invented target is shown usable", row)
		}
	}
}
