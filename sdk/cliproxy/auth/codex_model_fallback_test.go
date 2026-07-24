package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type codexFallbackTestError struct {
	message string
	reason  string
	blocked bool
}

func (e *codexFallbackTestError) Error() string { return e.message }
func (e *codexFallbackTestError) StatusCode() int {
	return http.StatusTooManyRequests
}
func (e *codexFallbackTestError) ModelFallbackReason() string { return e.reason }
func (e *codexFallbackTestError) ModelFallbackBlocked() bool  { return e.blocked }

type codexModelFallbackTestExecutor struct {
	mu             sync.Mutex
	calls          []string
	authCalls      []string
	executeErrs    map[string]error
	streamErrs     map[string]error
	streamChunks   map[string][]cliproxyexecutor.StreamChunk
	metadataSeen   map[string]map[string]any
	closedSessions []string
}

type codexModelFallbackRetryBehavior struct {
	kind              string
	payload           string
	delay             time.Duration
	maxRetries        int
	hedgeEnabled      bool
	hedgeMode         string
	hedgeDelay        time.Duration
	requireDistinct   bool
	exhaustedBehavior string
	deliveryPolicy    string
	fallbackPolicy    string
	usage             coreusage.Detail
	finalize          bool
}

type codexModelFallbackRetryExecutor struct {
	mu              sync.Mutex
	calls           []string
	behaviors       map[string][]codexModelFallbackRetryBehavior
	streamBehaviors map[string][]codexModelFallbackRetryBehavior
}

func (e *codexModelFallbackRetryExecutor) Identifier() string { return "codex" }

func codexModelFallbackRetryBehaviorKey(authID, model string) string {
	return authID + "|" + model
}

func (e *codexModelFallbackRetryExecutor) next(authID, model string, stream bool) codexModelFallbackRetryBehavior {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := codexModelFallbackRetryBehaviorKey(authID, model)
	e.calls = append(e.calls, key)
	queues := e.behaviors
	if stream {
		queues = e.streamBehaviors
	}
	queue := queues[key]
	if len(queue) == 0 {
		return codexModelFallbackRetryBehavior{kind: "success", payload: "ok"}
	}
	behavior := queue[0]
	queues[key] = queue[1:]
	return behavior
}

func (e *codexModelFallbackRetryExecutor) wait(ctx context.Context, behavior codexModelFallbackRetryBehavior) error {
	if behavior.kind == "wait_cancel" {
		<-ctx.Done()
		return ctx.Err()
	}
	if behavior.delay <= 0 {
		return nil
	}
	timer := time.NewTimer(behavior.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *codexModelFallbackRetryExecutor) retryError(authID string, behavior codexModelFallbackRetryBehavior, stream bool) error {
	usage := behavior.usage
	if !hasRetryWithoutPenaltyUsageDetail(usage) {
		usage = coreusage.Detail{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 516, TotalTokens: 3}
	}
	fallbackPayload := []byte(behavior.payload)
	fallbackStream := []cliproxyexecutor.StreamChunk(nil)
	if stream && behavior.payload != "" {
		fallbackStream = []cliproxyexecutor.StreamChunk{{Payload: []byte(behavior.payload)}}
	}
	return hedgedRetryTestError{
		maxRetries:         behavior.maxRetries,
		hedgeEnabled:       behavior.hedgeEnabled,
		hedgeMode:          behavior.hedgeMode,
		hedgeDelay:         behavior.hedgeDelay,
		requireDistinct:    behavior.requireDistinct,
		authID:             authID,
		usage:              usage,
		exhaustedBehavior:  behavior.exhaustedBehavior,
		deliveryPolicy:     behavior.deliveryPolicy,
		fallbackPolicy:     behavior.fallbackPolicy,
		fallbackPayload:    fallbackPayload,
		fallbackStreamData: fallbackStream,
	}
}

func (e *codexModelFallbackRetryExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	behavior := e.next(auth.ID, req.Model, false)
	if err := e.wait(ctx, behavior); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	switch behavior.kind {
	case "abnormal":
		return cliproxyexecutor.Response{}, e.retryError(auth.ID, behavior, false)
	case "usage_limit":
		return cliproxyexecutor.Response{}, &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	case "usage":
		return cliproxyexecutor.Response{Payload: []byte(hedgedRetryUsagePayload(opts))}, nil
	default:
		payload := behavior.payload
		if payload == "" {
			payload = "ok"
		}
		meta := hedgedRetryTestResponseMetadata(hedgedRetryBehavior{
			usage:    behavior.usage,
			finalize: behavior.finalize,
			policy:   hedgedRetryTestCandidatePolicy(behavior.deliveryPolicy),
		})
		return cliproxyexecutor.Response{Payload: []byte(payload), Metadata: meta}, nil
	}
}

func (e *codexModelFallbackRetryExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	behavior := e.next(auth.ID, req.Model, true)
	if err := e.wait(ctx, behavior); err != nil {
		return nil, err
	}
	if behavior.kind == "usage_limit" {
		return nil, &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if behavior.kind == "abnormal" {
		ch <- cliproxyexecutor.StreamChunk{Err: e.retryError(auth.ID, behavior, true)}
	} else {
		payload := behavior.payload
		if behavior.kind == "usage" {
			payload = hedgedRetryUsagePayload(opts)
		}
		if payload == "" {
			payload = "ok"
		}
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(payload)}
	}
	close(ch)
	meta := hedgedRetryTestStreamMetadata(hedgedRetryBehavior{
		usage:    behavior.usage,
		finalize: behavior.finalize,
		policy:   hedgedRetryTestCandidatePolicy(behavior.deliveryPolicy),
	})
	return &cliproxyexecutor.StreamResult{Chunks: ch, Metadata: meta}, nil
}

func (e *codexModelFallbackRetryExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexModelFallbackRetryExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexModelFallbackRetryExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *codexModelFallbackRetryExecutor) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

type codexModelFallbackAuthSpec struct {
	id     string
	models []string
}

type codexModelFallbackSequenceSelector struct {
	mu      sync.Mutex
	byModel map[string][]string
}

func (s *codexModelFallbackSequenceSelector) Pick(_ context.Context, _ string, model string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	sequence := s.byModel[model]
	authID := ""
	if len(sequence) > 0 {
		authID = sequence[0]
		s.byModel[model] = sequence[1:]
	}
	s.mu.Unlock()
	if authID != "" {
		return pickHedgedRetryTestAuth(auths, authID)
	}
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "test auth unavailable"}
	}
	return auths[0], nil
}

func newCodexModelFallbackRetryManager(t *testing.T, executor *codexModelFallbackRetryExecutor, targets []string, auths ...codexModelFallbackAuthSpec) *Manager {
	return newCodexModelFallbackRetryManagerWithSelector(t, executor, &FillFirstSelector{}, targets, auths...)
}

func newCodexModelFallbackRetryManagerWithSelector(t *testing.T, executor *codexModelFallbackRetryExecutor, selector Selector, targets []string, auths ...codexModelFallbackAuthSpec) *Manager {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: targets}},
	}}})
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	for _, spec := range auths {
		models := make([]*registry.ModelInfo, 0, len(spec.models))
		for _, model := range spec.models {
			models = append(models, &registry.ModelInfo{ID: model})
		}
		reg.RegisterClient(spec.id, "codex", models)
		if _, err := manager.Register(context.Background(), &Auth{ID: spec.id, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("register auth %s: %v", spec.id, err)
		}
	}
	t.Cleanup(func() {
		for _, spec := range auths {
			reg.UnregisterClient(spec.id)
		}
	})
	return manager
}

func (e *codexModelFallbackTestExecutor) Identifier() string { return "codex" }

func (e *codexModelFallbackTestExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, req.Model, opts.Metadata)
	if err := e.executeErrs[req.Model]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *codexModelFallbackTestExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth, req.Model, opts.Metadata)
	if chunks, ok := e.streamChunks[req.Model]; ok {
		ch := make(chan cliproxyexecutor.StreamChunk, len(chunks))
		for _, chunk := range chunks {
			ch <- chunk
		}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err := e.streamErrs[req.Model]; err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *codexModelFallbackTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexModelFallbackTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexModelFallbackTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *codexModelFallbackTestExecutor) record(auth *Auth, model string, metadata map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, model)
	if auth != nil {
		e.authCalls = append(e.authCalls, auth.ID)
	} else {
		e.authCalls = append(e.authCalls, "")
	}
	if e.metadataSeen == nil {
		e.metadataSeen = make(map[string]map[string]any)
	}
	e.metadataSeen[model] = cloneSchedulerAnyMap(metadata)
}

func (e *codexModelFallbackTestExecutor) authSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authCalls...)
}

func (e *codexModelFallbackTestExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closedSessions = append(e.closedSessions, sessionID)
}

func (e *codexModelFallbackTestExecutor) closedSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.closedSessions...)
}

func (e *codexModelFallbackTestExecutor) snapshot() ([]string, map[string]map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := append([]string(nil), e.calls...)
	metadata := make(map[string]map[string]any, len(e.metadataSeen))
	for model, values := range e.metadataSeen {
		metadata[model] = cloneSchedulerAnyMap(values)
	}
	return calls, metadata
}

func newCodexModelFallbackTestManager(t *testing.T, executor *codexModelFallbackTestExecutor, mode string) (*Manager, string) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Codex: internalconfig.CodexConfig{
			ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled:             true,
				ReasoningContinuity: mode,
				Mappings: []internalconfig.CodexModelFallbackMapping{
					{From: "gpt-source", To: []string{"gpt-target"}},
				},
			},
		},
	})
	manager.RegisterExecutor(executor)
	authID := "codex-fallback-auth"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return manager, authID
}

func newCodexGlobalFallbackTestManager(t *testing.T, executor *codexModelFallbackTestExecutor, mappings []internalconfig.CodexModelFallbackMapping) (*Manager, string) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:       true,
		GlobalTargets: []string{"gpt-global"},
		Mappings:      mappings,
	}}})
	manager.RegisterExecutor(executor)
	authID := "codex-global-fallback-auth"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}, {ID: "gpt-global"}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return manager, authID
}

func codexUsageLimitTestError() error {
	return &codexFallbackTestError{
		message: "decorated provider usage limit",
		reason:  internalconfig.CodexModelFallbackTriggerUsageLimit,
	}
}

func TestManagerExecuteCodexGlobalFallbackOnConfirmedUsageLimitCooldown(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": codexUsageLimitTestError()}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, nil)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-global" {
		t.Fatalf("response payload = %q, want gpt-global", got)
	}
	calls, metadata := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-global" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-global]", calls)
	}
	if got := metadata["gpt-global"][cliproxyexecutor.CodexModelFallbackSourceModelMetadataKey]; got != "gpt-source" {
		t.Fatalf("global fallback source metadata = %#v, want gpt-source", got)
	}
	if got := metadata["gpt-global"][cliproxyexecutor.RequestedModelMetadataKey]; got != "gpt-source" {
		t.Fatalf("requested model metadata = %#v, want gpt-source", got)
	}
}

func TestManagerExecuteCodexGlobalFallbackIgnoresOrdinary429(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{
		"gpt-source": &codexFallbackTestError{message: `{"error":{"type":"rate_limit_error"}}`},
	}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, nil)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute() error = nil, want ordinary 429")
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only", calls)
	}
}

func TestManagerExecuteCodexGlobalFallbackRunsAfterMappedTargetsCannotDispatch(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": codexUsageLimitTestError()}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-missing"}}})

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-global" {
		t.Fatalf("response payload = %q, want gpt-global", got)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-global" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-global]", calls)
	}
}

func TestManagerExecuteCodexGlobalFallbackDoesNotBypassSuccessfulMapping(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": codexUsageLimitTestError()}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target"}}})

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-target" {
		t.Fatalf("response payload = %q, want gpt-target", got)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-target" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-target]", calls)
	}
}

func TestManagerExecuteCodexGlobalFallbackUsesExistingConfirmedCooldown(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{}}
	manager, authID := newCodexGlobalFallbackTestManager(t, executor, nil)
	now := time.Now()
	manager.mu.Lock()
	manager.auths[authID].ModelStates = map[string]*ModelState{
		"gpt-source": {
			Status:              StatusError,
			Unavailable:         true,
			NextRetryAfter:      now.Add(time.Minute),
			LastError:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "decorated provider usage limit"},
			Quota:               QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)},
			modelFallbackReason: internalconfig.CodexModelFallbackTriggerUsageLimit,
		},
	}
	manager.mu.Unlock()
	manager.RefreshSchedulerEntry(authID)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-global" {
		t.Fatalf("response payload = %q, want gpt-global", got)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-global" {
		t.Fatalf("calls = %#v, want global target only", calls)
	}
}

func TestManagerCodexGlobalFallbackRequiresEveryEligibleSourceCredentialCooldown(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{}}
	manager, firstAuthID := newCodexGlobalFallbackTestManager(t, executor, nil)
	secondAuthID := "codex-global-fallback-active-auth"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(secondAuthID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}})
	t.Cleanup(func() {
		reg.UnregisterClient(secondAuthID)
	})
	if _, err := manager.Register(context.Background(), &Auth{ID: secondAuthID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("register second source auth: %v", err)
	}

	now := time.Now()
	confirmedState := func() *ModelState {
		return &ModelState{
			Status:              StatusError,
			Unavailable:         true,
			NextRetryAfter:      now.Add(time.Minute),
			Quota:               QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)},
			modelFallbackReason: internalconfig.CodexModelFallbackTriggerUsageLimit,
		}
	}
	manager.mu.Lock()
	manager.auths[firstAuthID].ModelStates = map[string]*ModelState{"gpt-source": confirmedState()}
	manager.mu.Unlock()

	if manager.codexModelHasConfirmedUsageLimitCooldown("gpt-source", cliproxyexecutor.Options{}) {
		t.Fatal("confirmed cooldown = true while an eligible source credential remains active")
	}

	manager.mu.Lock()
	manager.auths[secondAuthID].ModelStates = map[string]*ModelState{"gpt-source": confirmedState()}
	manager.mu.Unlock()
	if !manager.codexModelHasConfirmedUsageLimitCooldown("gpt-source", cliproxyexecutor.Options{}) {
		t.Fatal("confirmed cooldown = false after every eligible source credential entered typed cooldown")
	}
}

func TestManagerCodexGlobalFallbackFailsClosedInHomeMode(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{}}
	manager, authID := newCodexGlobalFallbackTestManager(t, executor, nil)
	manager.SetConfig(&internalconfig.Config{
		Home: internalconfig.HomeConfig{Enabled: true},
		Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
			Enabled:       true,
			GlobalTargets: []string{"gpt-global"},
		}},
	})
	now := time.Now()
	manager.mu.Lock()
	manager.auths[authID].ModelStates = map[string]*ModelState{
		"gpt-source": {
			Status:              StatusError,
			Unavailable:         true,
			NextRetryAfter:      now.Add(time.Minute),
			Quota:               QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)},
			modelFallbackReason: internalconfig.CodexModelFallbackTriggerUsageLimit,
		},
	}
	manager.mu.Unlock()

	if manager.codexModelHasConfirmedUsageLimitCooldown("gpt-source", cliproxyexecutor.Options{}) {
		t.Fatal("confirmed cooldown = true in Home mode without authoritative control-plane candidate state")
	}
}

func TestManagerExecuteCodexGlobalFallbackIgnoresDisabledSourceCandidates(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": codexUsageLimitTestError()}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, nil)
	reg := registry.GetGlobalRegistry()

	disabledAuthID := "codex-global-fallback-disabled-auth"
	reg.RegisterClient(disabledAuthID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}})
	if _, err := manager.Register(context.Background(), &Auth{ID: disabledAuthID, Provider: "codex", Status: StatusDisabled}); err != nil {
		t.Fatalf("register disabled auth: %v", err)
	}
	disabledModelID := "codex-global-fallback-disabled-model"
	reg.RegisterClient(disabledModelID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}})
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       disabledModelID,
		Provider: "codex",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"gpt-source": {Status: StatusDisabled},
		},
	}); err != nil {
		t.Fatalf("register auth with disabled model: %v", err)
	}
	t.Cleanup(func() {
		reg.UnregisterClient(disabledAuthID)
		reg.UnregisterClient(disabledModelID)
	})

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-global" {
		t.Fatalf("response payload = %q, want gpt-global", got)
	}
}

func TestManagerExecuteStreamCodexGlobalFallbackBeforeFirstPayload(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{streamErrs: map[string]error{"gpt-source": codexUsageLimitTestError()}}
	manager, _ := newCodexGlobalFallbackTestManager(t, executor, nil)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); got != "gpt-global" {
		t.Fatalf("stream payload = %q, want gpt-global", got)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-global" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-global]", calls)
	}
}

func TestManagerExecuteCodexModelFallbackOnUsageLimit(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{
		executeErrs: map[string]error{
			"gpt-source": &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit},
		},
	}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "gpt-source"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-target" {
		t.Fatalf("response payload = %q, want gpt-target", got)
	}
	calls, metadata := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-target" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-target]", calls)
	}
	if got := metadata["gpt-target"][cliproxyexecutor.CodexModelFallbackSourceModelMetadataKey]; got != "gpt-source" {
		t.Fatalf("fallback source metadata = %#v, want gpt-source", got)
	}
	if got := metadata["gpt-target"][cliproxyexecutor.AuthSelectionModelMetadataKey]; got != "gpt-target" {
		t.Fatalf("fallback auth-selection model = %#v, want gpt-target", got)
	}
}

func TestManagerExecuteCodexModelFallbackSelectsCredentialForTargetModel(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{
		"gpt-source": &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit},
	}}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{
		ModelFallback: internalconfig.CodexModelFallbackConfig{
			Enabled: true,
			Mappings: []internalconfig.CodexModelFallbackMapping{
				{From: "gpt-source", To: []string{"gpt-target"}},
			},
		},
	}})
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("source-auth", "codex", []*registry.ModelInfo{{ID: "gpt-source"}})
	reg.RegisterClient("target-auth", "codex", []*registry.ModelInfo{{ID: "gpt-target"}})
	t.Cleanup(func() {
		reg.UnregisterClient("source-auth")
		reg.UnregisterClient("target-auth")
	})
	for _, authID := range []string{"source-auth", "target-auth"} {
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "gpt-source"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "gpt-target" {
		t.Fatalf("response payload = %q, want gpt-target", got)
	}
	if got := executor.authSnapshot(); len(got) != 2 || got[0] != "source-auth" || got[1] != "target-auth" {
		t.Fatalf("auth calls = %#v, want [source-auth target-auth]", got)
	}
}

func TestManagerExecuteCodexModelFallbackIgnoresUnclassified429(t *testing.T) {
	transient := &codexFallbackTestError{message: "rate limited"}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": transient}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != transient {
		t.Fatalf("Execute() error = %v, want original transient error", err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only", calls)
	}
}

func TestManagerExecuteCodexModelFallbackBlockedReturnsOriginalAndDoesNotCooldownTarget(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{
		"gpt-source": initial,
		"gpt-target": &codexFallbackTestError{message: "continuity blocked", blocked: true},
	}}
	manager, authID := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != initial {
		t.Fatalf("Execute() error = %v, want original usage-limit error", err)
	}
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatal("GetByID() = nil")
	}
	if _, ok := auth.ModelStates["gpt-target"]; ok {
		t.Fatalf("target model was penalized despite zero dispatch: %#v", auth.ModelStates["gpt-target"])
	}
}

func TestManagerExecuteCodexModelFallbackSameModelOnlyBlocksPreviousResponseBeforeTargetSelection(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-source",
		Payload: []byte(`{"previous_response_id":"resp-source","input":[{"type":"message","role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{})
	if err != initial {
		t.Fatalf("Execute() error = %v, want original source error", err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only", calls)
	}
}

func TestManagerExecuteCodexModelFallbackBlocksCachedClaudeReplayBeforeTargetSelection(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	if !internalcache.CacheCodexReasoningReplayItem("gpt-source", "claude:"+sessionID+":agent:main", []byte(`{"type":"function_call","call_id":"call-preflight","name":"tool","arguments":"{}"}`)) {
		t.Fatal("failed to cache replay test item")
	}
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-source",
		Payload: []byte(`{"metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account_123e4567-e89b-12d3-a456-426614174001_session_` + sessionID + `"},"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != initial {
		t.Fatalf("Execute() error = %v, want source error", err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want target zero-dispatch", calls)
	}
}

func TestManagerExecuteCodexModelFallbackReplayPreflightUsesAgentScopedExecutorKeyPriority(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	const sessionID = "123e4567-e89b-12d3-a456-426614174010"
	if !internalcache.CacheCodexReasoningReplayItem("gpt-source", "claude:"+sessionID+":agent:main", []byte(`{"type":"function_call","call_id":"call-agent-scope","name":"tool","arguments":"{}"}`)) {
		t.Fatal("failed to cache replay test item")
	}
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-source",
		Payload: []byte(`{"prompt_cache_key":"higher-priority-key-without-replay","metadata":{"user_id":"{\"session_id\":\"` + sessionID + `\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != initial {
		t.Fatalf("Execute() error = %v, want source error", err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want target zero-dispatch", calls)
	}
}

func TestManagerExecuteCodexModelFallbackReplayPreflightUsesSharedHeaderResolver(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cacheKey   string
		optsHeader http.Header
		ginHeader  http.Header
	}{
		{
			name:       "lowercase options header",
			cacheKey:   "window:window-lowercase",
			optsHeader: http.Header{"x-codex-window-id": {"window-lowercase"}},
		},
		{
			name:      "gin-only header",
			cacheKey:  "conversation_id:conversation-gin",
			ginHeader: http.Header{"conversation_id": {"conversation-gin"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			internalcache.ClearCodexReasoningReplayCache()
			t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
			if !internalcache.CacheCodexReasoningReplayItem("gpt-source", tc.cacheKey, []byte(`{"type":"function_call","call_id":"call-shared-resolver","name":"tool","arguments":"{}"}`)) {
				t.Fatal("failed to cache replay test item")
			}
			initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
			executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
			manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
			ctx := context.Background()
			if tc.ginHeader != nil {
				ctx = context.WithValue(ctx, "gin", &gin.Context{Request: &http.Request{Header: tc.ginHeader}})
			}
			_, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{
				Model:   "gpt-source",
				Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: tc.optsHeader})
			if err != initial {
				t.Fatalf("Execute() error = %v, want source error", err)
			}
			calls, _ := executor.snapshot()
			if len(calls) != 1 || calls[0] != "gpt-source" {
				t.Fatalf("calls = %#v, want target zero-dispatch", calls)
			}
		})
	}
}

func TestCodexModelFallbackStatefulContinuityDoesNotTreatPromptWordsAsState(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"input":[{"type":"message","role":"user","content":"Explain the word thinking and the JSON snippet {\\\"type\\\":\\\"reasoning\\\"}."}],"reasoning":{"effort":"high"}}`)}
	if codexModelFallbackHasStatefulContinuity(req, cliproxyexecutor.Options{}) {
		t.Fatalf("ordinary user text/config must not be classified as reasoning continuity")
	}
}

func TestManagerExecuteCodexModelFallbackContextResetDropsSourcePinAndPublishesTarget(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:             true,
		ReasoningContinuity: internalconfig.CodexModelFallbackReasoningContinuityContextReset,
		Mappings:            []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target"}}},
	}}})
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("fallback-source-pin", "codex", []*registry.ModelInfo{{ID: "gpt-source"}})
	reg.RegisterClient("fallback-target-pin", "codex", []*registry.ModelInfo{{ID: "gpt-target"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fallback-source-pin")
		reg.UnregisterClient("fallback-target-pin")
	})
	for _, authID := range []string{"fallback-source-pin", "fallback-target-pin"} {
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}
	var selected []string
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey:                           "fallback-source-pin",
		cliproxyexecutor.ExecutionSessionMetadataKey:                     "fallback-reset-session",
		cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey: true,
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selected = append(selected, authID)
		},
	}})
	if err != nil || string(resp.Payload) != "gpt-target" {
		t.Fatalf("Execute() response/error = %q/%v, want target success", resp.Payload, err)
	}
	if got := executor.authSnapshot(); len(got) != 2 || got[0] != "fallback-source-pin" || got[1] != "fallback-target-pin" {
		t.Fatalf("auth calls = %#v, want source then distinct target", got)
	}
	if len(selected) != 2 || selected[1] != "fallback-target-pin" {
		t.Fatalf("selected callbacks = %#v, want final target callback", selected)
	}
	if closed := executor.closedSnapshot(); len(closed) != 1 || closed[0] != "fallback-reset-session" {
		t.Fatalf("closed source sessions = %#v, want fallback-reset-session", closed)
	}
}

func TestManagerExecuteCodexModelFallbackContextResetContinuesOrderedTargetsWithContinuity(t *testing.T) {
	initial := &codexFallbackTestError{message: "source usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	targetLimit := &codexFallbackTestError{message: "target usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{
		"gpt-source":   initial,
		"gpt-target-a": targetLimit,
	}}
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	manager := NewManager(nil, selector, nil)
	t.Cleanup(selector.Stop)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{
			RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
				Enabled:                      true,
				ObservationWindowSeconds:     10,
				EstablishedSuccessThreshold:  2,
				EstablishedSessionTTLSeconds: 3600,
			},
			ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled:             true,
				ReasoningContinuity: internalconfig.CodexModelFallbackReasoningContinuityContextReset,
				Mappings: []internalconfig.CodexModelFallbackMapping{{
					From: "gpt-source",
					To:   []string{"gpt-target-a", "gpt-target-b"},
				}},
			},
		},
	})
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authModels := map[string]string{
		"fallback-source":   "gpt-source",
		"fallback-target-a": "gpt-target-a",
		"fallback-target-b": "gpt-target-b",
	}
	for authID, model := range authModels {
		reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}
	t.Cleanup(func() {
		for authID := range authModels {
			reg.UnregisterClient(authID)
		}
	})

	var callbacks []string
	sessionID := "ordered-context-reset"
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey:                           "fallback-source",
		cliproxyexecutor.ExecutionSessionMetadataKey:                     sessionID,
		cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey: true,
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			callbacks = append(callbacks, authID)
		},
	}})
	if err != nil || string(resp.Payload) != "gpt-target-b" {
		t.Fatalf("Execute() response/error = %q/%v, want target B success", resp.Payload, err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 3 || calls[0] != "gpt-source" || calls[1] != "gpt-target-a" || calls[2] != "gpt-target-b" {
		t.Fatalf("calls = %#v, want source, target A, target B", calls)
	}
	if authCalls := executor.authSnapshot(); len(authCalls) != 3 || authCalls[0] != "fallback-source" || authCalls[1] != "fallback-target-a" || authCalls[2] != "fallback-target-b" {
		t.Fatalf("auth calls = %#v", authCalls)
	}
	if len(callbacks) != 2 || callbacks[0] != "fallback-source" || callbacks[1] != "fallback-target-b" {
		t.Fatalf("selected callbacks = %#v, want source and final target B", callbacks)
	}
	if closed := executor.closedSnapshot(); len(closed) != 1 || closed[0] != sessionID {
		t.Fatalf("closed source sessions = %#v, want one close", closed)
	}

	manager.codexRateLimitContinuity.mu.Lock()
	stateA := manager.codexRateLimitContinuity.states[codexRateLimitContinuityKey{authID: "fallback-target-a", model: "gpt-target-a"}]
	stateB := manager.codexRateLimitContinuity.states[codexRateLimitContinuityKey{authID: "fallback-target-b", model: "gpt-target-b"}]
	leaseB := time.Time{}
	if stateB != nil {
		leaseB = stateB.establishedSessions["execution:"+sessionID]
	}
	manager.codexRateLimitContinuity.mu.Unlock()
	if stateA == nil || stateA.phase != codexRateLimitContinuityFreshBlocked {
		t.Fatalf("target A continuity state = %+v, want FreshBlocked", stateA)
	}
	if stateB == nil || stateB.phase != codexRateLimitContinuityHealthy || leaseB.IsZero() {
		t.Fatalf("target B continuity state = %+v lease=%v, want Healthy established", stateB, leaseB)
	}
}

func TestManagerExecuteCodexModelFallbackContinuesAfterTargetAuthNotFound(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-missing", "gpt-target"}}},
	}}})
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("fallback-ordered", "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}})
	t.Cleanup(func() { reg.UnregisterClient("fallback-ordered") })
	if _, err := manager.Register(context.Background(), &Auth{ID: "fallback-ordered", Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != nil || string(resp.Payload) != "gpt-target" {
		t.Fatalf("Execute() response/error = %q/%v, want second target success", resp.Payload, err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-target" {
		t.Fatalf("calls = %#v, want source and second target", calls)
	}
}

func TestManagerExecuteCodexModelFallbackContinuesAfterTypedTargetAndHidesIntermediateCallback(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{
		"gpt-source":   initial,
		"gpt-target-a": &codexFallbackTestError{message: "capacity", reason: internalconfig.CodexModelFallbackTriggerCapacity},
	}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target-a", "gpt-target"}}},
	}}})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("codex-fallback-auth", "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target-a"}, {ID: "gpt-target"}})
	var callbacks []string
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) { callbacks = append(callbacks, authID) },
	}})
	if err != nil || string(resp.Payload) != "gpt-target" {
		t.Fatalf("Execute() response/error = %q/%v, want target B success", resp.Payload, err)
	}
	if len(callbacks) != 2 { // source + final B; target A is deliberately internal.
		t.Fatalf("callbacks = %#v, want source and final target only", callbacks)
	}
}

func TestManagerExecuteCodexModelFallbackAllZeroDispatchReturnsSourceError(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:  true,
		Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-missing-a", "gpt-missing-b"}}},
	}}})
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
	if err != initial {
		t.Fatalf("Execute() error = %v, want source error after all zero-dispatch targets", err)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only", calls)
	}
}

func TestManagerExecuteCodexModelFallbackContextResetDoesNotCloseSourceForZeroDispatchTarget(t *testing.T) {
	initial := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuityContextReset)
	manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
		Enabled:             true,
		ReasoningContinuity: internalconfig.CodexModelFallbackReasoningContinuityContextReset,
		Mappings:            []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-missing"}}},
	}}})
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey:                     "must-not-close",
		cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey: true,
	}})
	if err != initial {
		t.Fatalf("Execute() error = %v, want source error", err)
	}
	if closed := executor.closedSnapshot(); len(closed) != 0 {
		t.Fatalf("closed sessions = %#v, zero-dispatch target must not close source", closed)
	}
}

func TestCodexModelFallbackTargetSharesRequestRetryHedgeAndUsageBudget(t *testing.T) {
	source := withCodexModelFallbackRequestBudget(cliproxyexecutor.Options{}, false)
	target := withCodexModelFallbackMetadata(source, "gpt-source", "gpt-target", internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)
	sourceBudget := codexModelFallbackBudget(source, false)
	targetBudget := codexModelFallbackBudget(target, false)
	if sourceBudget != targetBudget {
		t.Fatal("target received a fresh retry/hedge/usage budget")
	}
	sourceBudget.retryCounts["abnormal"] = 1
	sourceBudget.usage.Add(coreusage.Detail{InputTokens: 2, OutputTokens: 3, TotalTokens: 5})
	if targetBudget.retryCounts["abnormal"] != 1 {
		t.Fatalf("target retry budget = %#v, want source-consumed counter", targetBudget.retryCounts)
	}
	if got := targetBudget.usage.RetryWithoutPenaltySnapshot().Detail.TotalTokens; got != 5 {
		t.Fatalf("target usage snapshot total = %d, want source usage included", got)
	}
	if !codexModelFallbackTargetWave(target) {
		t.Fatal("target must be limited to a single auth-selection wave")
	}
}

func TestManagerExecuteCodexModelFallbackSharesRemainingAbnormalBudgetAndHedgeWinner(t *testing.T) {
	executor := &codexModelFallbackRetryExecutor{behaviors: map[string][]codexModelFallbackRetryBehavior{
		codexModelFallbackRetryBehaviorKey("auth-source", "gpt-source"): {
			{kind: "abnormal", maxRetries: 3},
			{kind: "usage_limit"},
		},
		codexModelFallbackRetryBehaviorKey("auth-target-a", "gpt-target"): {
			{kind: "abnormal", maxRetries: 3, hedgeEnabled: true, hedgeMode: retryWithoutPenaltyHedgeModeQuality, requireDistinct: true},
			{kind: "abnormal", maxRetries: 3, hedgeEnabled: true, hedgeMode: retryWithoutPenaltyHedgeModeQuality, requireDistinct: true},
		},
		codexModelFallbackRetryBehaviorKey("auth-target-b", "gpt-target"): {
			{kind: "usage"},
		},
	}}
	selector := &codexModelFallbackSequenceSelector{byModel: map[string][]string{
		"gpt-source": {"auth-source", "auth-source"},
		"gpt-target": {"auth-target-a", "auth-target-a", "auth-target-a", "auth-target-b"},
	}}
	manager := newCodexModelFallbackRetryManagerWithSelector(t, executor, selector, []string{"gpt-target"},
		codexModelFallbackAuthSpec{id: "auth-source", models: []string{"gpt-source"}},
		codexModelFallbackAuthSpec{id: "auth-target-a", models: []string{"gpt-target"}},
		codexModelFallbackAuthSpec{id: "auth-target-b", models: []string{"gpt-target"}},
	)

	var callbackMu sync.Mutex
	var callbacks []string
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			callbackMu.Lock()
			defer callbackMu.Unlock()
			callbacks = append(callbacks, authID)
		},
	}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "usage:9" {
		t.Fatalf("payload = %q, want target winner with source+target abnormal usage; calls=%#v", got, executor.callsSnapshot())
	}
	counts := make(map[string]int)
	for _, call := range executor.callsSnapshot() {
		counts[call]++
	}
	for call, want := range map[string]int{
		codexModelFallbackRetryBehaviorKey("auth-source", "gpt-source"):   2,
		codexModelFallbackRetryBehaviorKey("auth-target-a", "gpt-target"): 2,
		codexModelFallbackRetryBehaviorKey("auth-target-b", "gpt-target"): 1,
	} {
		if got := counts[call]; got != want {
			t.Fatalf("call count %s = %d, want %d; all=%#v", call, got, want, executor.callsSnapshot())
		}
	}
	callbackMu.Lock()
	gotCallbacks := append([]string(nil), callbacks...)
	callbackMu.Unlock()
	if len(gotCallbacks) != 3 || gotCallbacks[0] != "auth-source" || gotCallbacks[1] != "auth-source" || gotCallbacks[2] != "auth-target-b" {
		t.Fatalf("selected callbacks = %#v, want source attempts plus final target hedge winner", gotCallbacks)
	}
}

func TestManagerExecuteStreamCodexModelFallbackSharesRemainingAbnormalBudgetAndHedgeWinner(t *testing.T) {
	executor := &codexModelFallbackRetryExecutor{streamBehaviors: map[string][]codexModelFallbackRetryBehavior{
		codexModelFallbackRetryBehaviorKey("auth-source-stream", "gpt-source"): {
			{kind: "abnormal", maxRetries: 3},
			{kind: "usage_limit"},
		},
		codexModelFallbackRetryBehaviorKey("auth-target-stream-a", "gpt-target"): {
			{kind: "abnormal", maxRetries: 3, hedgeEnabled: true, hedgeMode: retryWithoutPenaltyHedgeModeQuality, requireDistinct: true},
			{kind: "abnormal", maxRetries: 3, hedgeEnabled: true, hedgeMode: retryWithoutPenaltyHedgeModeQuality, requireDistinct: true},
		},
		codexModelFallbackRetryBehaviorKey("auth-target-stream-b", "gpt-target"): {
			{kind: "usage"},
		},
	}}
	selector := &codexModelFallbackSequenceSelector{byModel: map[string][]string{
		"gpt-source": {"auth-source-stream", "auth-source-stream"},
		"gpt-target": {"auth-target-stream-a", "auth-target-stream-a", "auth-target-stream-a", "auth-target-stream-b"},
	}}
	manager := newCodexModelFallbackRetryManagerWithSelector(t, executor, selector, []string{"gpt-target"},
		codexModelFallbackAuthSpec{id: "auth-source-stream", models: []string{"gpt-source"}},
		codexModelFallbackAuthSpec{id: "auth-target-stream-a", models: []string{"gpt-target"}},
		codexModelFallbackAuthSpec{id: "auth-target-stream-b", models: []string{"gpt-target"}},
	)

	var callbackMu sync.Mutex
	var callbacks []string
	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{
		Stream: true,
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				callbacks = append(callbacks, authID)
			},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if got := string(collectHedgedRetryStreamPayload(t, result)); got != "usage:9" {
		t.Fatalf("stream payload = %q, want target winner with source+target abnormal usage; calls=%#v", got, executor.callsSnapshot())
	}
	counts := make(map[string]int)
	for _, call := range executor.callsSnapshot() {
		counts[call]++
	}
	for call, want := range map[string]int{
		codexModelFallbackRetryBehaviorKey("auth-source-stream", "gpt-source"):   2,
		codexModelFallbackRetryBehaviorKey("auth-target-stream-a", "gpt-target"): 2,
		codexModelFallbackRetryBehaviorKey("auth-target-stream-b", "gpt-target"): 1,
	} {
		if got := counts[call]; got != want {
			t.Fatalf("stream call count %s = %d, want %d; all=%#v", call, got, want, executor.callsSnapshot())
		}
	}
	callbackMu.Lock()
	gotCallbacks := append([]string(nil), callbacks...)
	callbackMu.Unlock()
	if len(gotCallbacks) != 3 || gotCallbacks[0] != "auth-source-stream" || gotCallbacks[1] != "auth-source-stream" || gotCallbacks[2] != "auth-target-stream-b" {
		t.Fatalf("stream selected callbacks = %#v, want source attempts plus final target hedge winner", gotCallbacks)
	}
}

func TestManagerExecuteCodexModelFallbackDoesNotReuseSourceMaxOutputCandidate(t *testing.T) {
	executor := &codexModelFallbackRetryExecutor{behaviors: map[string][]codexModelFallbackRetryBehavior{
		codexModelFallbackRetryBehaviorKey("auth-source-max", "gpt-source"): {
			{kind: "abnormal", payload: "source-abnormal-much-longer", maxRetries: 1, deliveryPolicy: retryWithoutPenaltyDeliveryPolicyMaxOutput},
			{kind: "usage_limit"},
		},
		codexModelFallbackRetryBehaviorKey("auth-target-max", "gpt-target"): {
			{kind: "usage"},
		},
	}}
	manager := newCodexModelFallbackRetryManager(t, executor, []string{"gpt-target"},
		codexModelFallbackAuthSpec{id: "auth-source-max", models: []string{"gpt-source"}},
		codexModelFallbackAuthSpec{id: "auth-target-max", models: []string{"gpt-target"}},
	)

	var callbacks []string
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) { callbacks = append(callbacks, authID) },
	}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "usage:3" {
		t.Fatalf("payload = %q, source max-output candidate leaked across models", got)
	}
	if len(callbacks) == 0 || callbacks[len(callbacks)-1] != "auth-target-max" {
		t.Fatalf("callbacks = %#v, want actual target result auth", callbacks)
	}
}

func TestManagerCodexModelFallbackTargetExhaustionPreservesBehavior(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, exhaustedBehavior := range []string{retryWithoutPenaltyExhaustedBehaviorError, retryWithoutPenaltyExhaustedBehaviorPassThrough} {
			name := exhaustedBehavior
			if stream {
				name = "stream-" + name
			} else {
				name = "non-stream-" + name
			}
			t.Run(name, func(t *testing.T) {
				queues := map[string][]codexModelFallbackRetryBehavior{
					codexModelFallbackRetryBehaviorKey("auth-source-exhausted", "gpt-source"): {{kind: "usage_limit"}},
					codexModelFallbackRetryBehaviorKey("auth-target-exhausted", "gpt-target"): {{
						kind: "abnormal", payload: "target-abnormal", maxRetries: 0, exhaustedBehavior: exhaustedBehavior,
					}},
				}
				executor := &codexModelFallbackRetryExecutor{}
				if stream {
					executor.streamBehaviors = queues
				} else {
					executor.behaviors = queues
				}
				manager := newCodexModelFallbackRetryManager(t, executor, []string{"gpt-target"},
					codexModelFallbackAuthSpec{id: "auth-source-exhausted", models: []string{"gpt-source"}},
					codexModelFallbackAuthSpec{id: "auth-target-exhausted", models: []string{"gpt-target"}},
				)

				if stream {
					result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Stream: true})
					if exhaustedBehavior == retryWithoutPenaltyExhaustedBehaviorError {
						assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
						return
					}
					if err != nil {
						t.Fatalf("ExecuteStream() error = %v", err)
					}
					if got := string(collectHedgedRetryStreamPayload(t, result)); got != "target-abnormal" {
						t.Fatalf("stream pass-through payload = %q, want target-abnormal", got)
					}
					return
				}

				resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
				if exhaustedBehavior == retryWithoutPenaltyExhaustedBehaviorError {
					assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
					return
				}
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				if got := string(resp.Payload); got != "target-abnormal" {
					t.Fatalf("pass-through payload = %q, want target-abnormal", got)
				}
			})
		}
	}
}

func TestManagerExecuteCodexModelFallbackContinuesPastRealTargetCooldown(t *testing.T) {
	for _, allCooldown := range []bool{false, true} {
		name := "target-b-success"
		if allCooldown {
			name = "all-targets-cooldown"
		}
		t.Run(name, func(t *testing.T) {
			initial := &codexFallbackTestError{message: "source usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
			executor := &codexModelFallbackTestExecutor{executeErrs: map[string]error{"gpt-source": initial}}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled:  true,
				Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target-a", "gpt-target-b"}}},
			}}})
			manager.RegisterExecutor(executor)
			next := time.Now().Add(time.Hour)
			modelStates := map[string]*ModelState{
				"gpt-target-a": {Status: StatusActive, Unavailable: true, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, NextRecoverAt: next}},
			}
			if allCooldown {
				modelStates["gpt-target-b"] = &ModelState{Status: StatusActive, Unavailable: true, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, NextRecoverAt: next}}
			}
			authID := "auth-real-cooldown-" + name
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target-a"}, {ID: "gpt-target-b"}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive, ModelStates: modelStates}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{})
			if allCooldown {
				if err != initial {
					t.Fatalf("Execute() error = %v, want original source error", err)
				}
			} else {
				if err != nil || string(resp.Payload) != "gpt-target-b" {
					t.Fatalf("Execute() response/error = %q/%v, want target B success", resp.Payload, err)
				}
			}
			calls, _ := executor.snapshot()
			wantCalls := []string{"gpt-source"}
			if !allCooldown {
				wantCalls = append(wantCalls, "gpt-target-b")
			}
			if len(calls) != len(wantCalls) {
				t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
			}
			for i := range wantCalls {
				if calls[i] != wantCalls[i] {
					t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
				}
			}
		})
	}
}

func TestManagerExecuteStreamCodexModelFallbackBeforeFirstPayload(t *testing.T) {
	executor := &codexModelFallbackTestExecutor{streamErrs: map[string]error{
		"gpt-source": &codexFallbackTestError{message: "capacity", reason: internalconfig.CodexModelFallbackTriggerCapacity},
	}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); got != "gpt-target" {
		t.Fatalf("stream payload = %q, want gpt-target", got)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 2 || calls[0] != "gpt-source" || calls[1] != "gpt-target" {
		t.Fatalf("calls = %#v, want [gpt-source gpt-target]", calls)
	}
}

func TestManagerExecuteStreamCodexModelFallbackPreservesUnclassifiedBootstrapError(t *testing.T) {
	initial := &codexFallbackTestError{message: "transient rate limit"}
	executor := &codexModelFallbackTestExecutor{streamErrs: map[string]error{"gpt-source": initial}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunks := make([]cliproxyexecutor.StreamChunk, 0, 1)
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 1 || chunks[0].Err != initial {
		t.Fatalf("stream chunks = %#v, want original bootstrap error", chunks)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only", calls)
	}
}

func TestManagerExecuteStreamCodexModelFallbackDoesNotReplayAfterPayload(t *testing.T) {
	initial := &codexFallbackTestError{message: "capacity", reason: internalconfig.CodexModelFallbackTriggerCapacity}
	executor := &codexModelFallbackTestExecutor{streamChunks: map[string][]cliproxyexecutor.StreamChunk{
		"gpt-source": {
			{Payload: []byte("partial")},
			{Err: initial},
		},
	}}
	manager, _ := newCodexModelFallbackTestManager(t, executor, internalconfig.CodexModelFallbackReasoningContinuitySameModelOnly)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var payload []byte
	var streamErr error
	for chunk := range result.Chunks {
		payload = append(payload, chunk.Payload...)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if got := string(payload); got != "partial" {
		t.Fatalf("stream payload = %q, want partial", got)
	}
	if streamErr != initial {
		t.Fatalf("stream error = %v, want original capacity error", streamErr)
	}
	calls, _ := executor.snapshot()
	if len(calls) != 1 || calls[0] != "gpt-source" {
		t.Fatalf("calls = %#v, want source only after downstream delivery", calls)
	}
}
