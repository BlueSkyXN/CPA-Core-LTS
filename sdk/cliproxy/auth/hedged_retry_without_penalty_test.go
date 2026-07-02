package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type hedgedRetryTestError struct {
	maxRetries         int
	hedgeEnabled       bool
	hedgeMode          string
	hedgeDelay         time.Duration
	requireDistinct    bool
	authID             string
	exhaustedBehavior  string
	fallbackPayload    []byte
	fallbackStreamData []cliproxyexecutor.StreamChunk
}

func (e hedgedRetryTestError) Error() string {
	return "codex_abnormal_reasoning_response: hedged retry test"
}

func (e hedgedRetryTestError) RetryWithoutPenalty() bool {
	return true
}

func (e hedgedRetryTestError) RetryWithoutPenaltyClass() string {
	return "codex.abnormal-reasoning-retry"
}

func (e hedgedRetryTestError) RetryWithoutPenaltyMaxRetries() int {
	return e.maxRetries
}

func (e hedgedRetryTestError) RetryWithoutPenaltyExhaustedBehavior() string {
	return e.exhaustedBehavior
}

func (e hedgedRetryTestError) RetryWithoutPenaltyHedgePolicy() (bool, time.Duration, bool) {
	return e.hedgeEnabled, e.hedgeDelay, e.requireDistinct
}

func (e hedgedRetryTestError) RetryWithoutPenaltyHedgeMode() string {
	return e.hedgeMode
}

func (e hedgedRetryTestError) RetryWithoutPenaltyAuthID() string {
	return e.authID
}

func (e hedgedRetryTestError) RetryWithoutPenaltyUsageDetail() coreusage.Detail {
	return coreusage.Detail{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 516, TotalTokens: 3}
}

func (e hedgedRetryTestError) RetryWithoutPenaltyFallbackResponse() (cliproxyexecutor.Response, bool) {
	if len(e.fallbackPayload) == 0 {
		return cliproxyexecutor.Response{}, false
	}
	return cliproxyexecutor.Response{Payload: e.fallbackPayload}, true
}

func (e hedgedRetryTestError) RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool) {
	if len(e.fallbackStreamData) == 0 {
		return nil, nil, false
	}
	return nil, e.fallbackStreamData, true
}

type hedgedRetryRateLimitError struct {
	retryAfter time.Duration
}

func (e hedgedRetryRateLimitError) Error() string {
	return "rate limited"
}

func (e hedgedRetryRateLimitError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e hedgedRetryRateLimitError) RetryAfter() *time.Duration {
	value := e.retryAfter
	return &value
}

type hedgedRetryBehavior struct {
	kind     string
	delay    time.Duration
	payload  string
	usage    coreusage.Detail
	finalize bool
	barrier  *hedgedRetryBarrier
}

type hedgedRetryBarrier struct {
	need  int
	count int
	ch    chan struct{}
}

func newHedgedRetryBarrier(need int) *hedgedRetryBarrier {
	return &hedgedRetryBarrier{need: need, ch: make(chan struct{})}
}

func (b *hedgedRetryBarrier) wait(ctx context.Context, mu *sync.Mutex) error {
	if b == nil {
		return nil
	}
	mu.Lock()
	b.count++
	if b.count >= b.need {
		select {
		case <-b.ch:
		default:
			close(b.ch)
		}
	}
	ch := b.ch
	mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}

type delayedPrimaryHedgeSelector struct {
	mu              sync.Mutex
	calls           int
	triggerAuthID   string
	secondaryAuthID string
}

func (s *delayedPrimaryHedgeSelector) Pick(ctx context.Context, _ string, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()

	switch call {
	case 1:
		return pickHedgedRetryTestAuth(auths, s.triggerAuthID)
	case 2:
		<-ctx.Done()
		return nil, ctx.Err()
	default:
		return pickHedgedRetryTestAuth(auths, s.secondaryAuthID)
	}
}

func (s *delayedPrimaryHedgeSelector) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func pickHedgedRetryTestAuth(auths []*Auth, authID string) (*Auth, error) {
	for _, auth := range auths {
		if auth != nil && auth.ID == authID {
			return auth, nil
		}
	}
	return nil, &Error{Code: "auth_not_found", Message: "test auth unavailable"}
}

type hedgedRetryTestExecutor struct {
	mu                 sync.Mutex
	calls              []string
	streamCalls        []string
	behaviors          map[string][]hedgedRetryBehavior
	streamBehaviors    map[string][]hedgedRetryBehavior
	maxRetries         int
	hedgeMode          string
	hedgeDelay         time.Duration
	requireDistinct    bool
	disableHedge       bool
	exhaustedBehavior  string
	fallbackPayload    []byte
	fallbackStreamData []cliproxyexecutor.StreamChunk
}

func (e *hedgedRetryTestExecutor) Identifier() string {
	return "codex"
}

func (e *hedgedRetryTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	authID := auth.ID
	behavior := e.nextBehavior(authID, false)
	if err := e.wait(ctx, behavior); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	switch behavior.kind {
	case "retry":
		return cliproxyexecutor.Response{}, e.retryError(authID)
	case "rate_limit":
		return cliproxyexecutor.Response{}, hedgedRetryRateLimitError{retryAfter: time.Millisecond}
	case "wait_cancel":
		<-ctx.Done()
		return cliproxyexecutor.Response{}, ctx.Err()
	case "usage":
		return cliproxyexecutor.Response{Payload: []byte(hedgedRetryUsagePayload(opts))}, nil
	default:
		payload := behavior.payload
		if payload == "" {
			payload = "ok"
		}
		return cliproxyexecutor.Response{Payload: []byte(payload), Metadata: hedgedRetryTestResponseMetadata(behavior)}, nil
	}
}

func (e *hedgedRetryTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	authID := auth.ID
	behavior := e.nextBehavior(authID, true)
	if err := e.wait(ctx, behavior); err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	switch behavior.kind {
	case "retry":
		ch <- cliproxyexecutor.StreamChunk{Err: e.retryError(authID)}
	case "rate_limit":
		return nil, hedgedRetryRateLimitError{retryAfter: time.Millisecond}
	case "wait_cancel":
		<-ctx.Done()
		ch <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
	default:
		payload := behavior.payload
		if payload == "" {
			payload = "stream-ok"
		}
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(payload)}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch, Metadata: hedgedRetryTestStreamMetadata(behavior)}, nil
}

func hedgedRetryTestResponseMetadata(behavior hedgedRetryBehavior) map[string]any {
	if !hasRetryWithoutPenaltyUsageDetail(behavior.usage) && !behavior.finalize {
		return nil
	}
	meta := map[string]any{}
	if hasRetryWithoutPenaltyUsageDetail(behavior.usage) {
		meta[cliproxyexecutor.RetryWithoutPenaltyUsageDetailMetadataKey] = behavior.usage
		meta[cliproxyexecutor.RetryWithoutPenaltyHedgeScoreMetadataKey] = behavior.usage.OutputTokens
	}
	if behavior.finalize {
		meta[cliproxyexecutor.RetryWithoutPenaltyResponseFinalizerMetadataKey] = cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer(func(resp cliproxyexecutor.Response, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot) cliproxyexecutor.Response {
			resp.Payload = []byte(fmt.Sprintf("%s|fold:%d|discarded:%d", resp.Payload, previous.FoldedOutputTokens, previous.Detail.TotalTokens))
			return resp
		})
	}
	return meta
}

func hedgedRetryTestStreamMetadata(behavior hedgedRetryBehavior) map[string]any {
	if !hasRetryWithoutPenaltyUsageDetail(behavior.usage) && !behavior.finalize {
		return nil
	}
	meta := map[string]any{}
	if hasRetryWithoutPenaltyUsageDetail(behavior.usage) {
		meta[cliproxyexecutor.RetryWithoutPenaltyStreamUsageMetadataKey] = &cliproxyexecutor.RetryWithoutPenaltyStreamUsage{
			Detail:     behavior.usage,
			HedgeScore: behavior.usage.OutputTokens,
			OK:         true,
		}
	}
	if behavior.finalize {
		meta[cliproxyexecutor.RetryWithoutPenaltyStreamFinalizerMetadataKey] = cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer(func(headers http.Header, chunks []cliproxyexecutor.StreamChunk, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot) *cliproxyexecutor.StreamResult {
			out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
			for i := range chunks {
				chunk := chunks[i]
				if chunk.Err == nil {
					chunk.Payload = []byte(fmt.Sprintf("%s|fold:%d|discarded:%d", chunk.Payload, previous.FoldedOutputTokens, previous.Detail.TotalTokens))
				}
				out <- chunk
			}
			close(out)
			return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
		})
	}
	return meta
}

func (e *hedgedRetryTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *hedgedRetryTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *hedgedRetryTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *hedgedRetryTestExecutor) nextBehavior(authID string, stream bool) hedgedRetryBehavior {
	e.mu.Lock()
	defer e.mu.Unlock()
	if stream {
		e.streamCalls = append(e.streamCalls, authID)
		queue := e.streamBehaviors[authID]
		if len(queue) == 0 {
			return hedgedRetryBehavior{kind: "success"}
		}
		behavior := queue[0]
		e.streamBehaviors[authID] = queue[1:]
		return behavior
	}
	e.calls = append(e.calls, authID)
	queue := e.behaviors[authID]
	if len(queue) == 0 {
		return hedgedRetryBehavior{kind: "success"}
	}
	behavior := queue[0]
	e.behaviors[authID] = queue[1:]
	return behavior
}

func (e *hedgedRetryTestExecutor) wait(ctx context.Context, behavior hedgedRetryBehavior) error {
	if behavior.barrier != nil {
		return behavior.barrier.wait(ctx, &e.mu)
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

func (e *hedgedRetryTestExecutor) retryError(authID string) error {
	return hedgedRetryTestError{
		maxRetries:         e.maxRetries,
		hedgeEnabled:       !e.disableHedge,
		hedgeMode:          e.hedgeMode,
		hedgeDelay:         e.hedgeDelay,
		requireDistinct:    e.requireDistinct,
		authID:             authID,
		exhaustedBehavior:  e.exhaustedBehavior,
		fallbackPayload:    e.fallbackPayload,
		fallbackStreamData: e.fallbackStreamData,
	}
}

func hedgedRetryUsagePayload(opts cliproxyexecutor.Options) string {
	if opts.Metadata == nil {
		return "usage:0"
	}
	accumulator, _ := opts.Metadata[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey].(*cliproxyexecutor.UsageAccumulator)
	detail := accumulator.Snapshot()
	return fmt.Sprintf("usage:%d", detail.TotalTokens)
}

func (e *hedgedRetryTestExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *hedgedRetryTestExecutor) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *hedgedRetryTestExecutor) streamCallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.streamCalls)
}

func newHedgedRetryTestManager(t *testing.T, executor *hedgedRetryTestExecutor, authIDs ...string) (*Manager, []string) {
	return newHedgedRetryTestManagerWithSelector(t, nil, executor, authIDs...)
}

func newHedgedRetryTestManagerWithSelector(t *testing.T, selector Selector, executor *hedgedRetryTestExecutor, authIDs ...string) (*Manager, []string) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	registered := make([]string, 0, len(authIDs))
	for _, authID := range authIDs {
		if authID == "" {
			authID = uuid.NewString()
		}
		auth := &Auth{ID: authID, Provider: "codex"}
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
		registered = append(registered, auth.ID)
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", auth.ID, err)
		}
	}
	t.Cleanup(func() {
		for _, authID := range registered {
			reg.UnregisterClient(authID)
		}
	})
	return manager, registered
}

func TestManagerExecute_HedgedRetryPrimaryCleanBeforeDelayDoesNotLaunchSecond(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "success", payload: "primary-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      50 * time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "primary-ok" {
		t.Fatalf("payload = %q, want primary-ok", resp.Payload)
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("calls = %d, want initial plus primary only", calls)
	}
}

func TestManagerExecute_HedgedRetryDisabledUsesSerialRetry(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}},
			"auth-b": {{kind: "success", payload: "serial-ok"}},
		},
		maxRetries:   2,
		hedgeDelay:   0,
		disableHedge: true,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "serial-ok" {
		t.Fatalf("payload = %q, want serial-ok", resp.Payload)
	}
	if got := executor.callsSnapshot(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("calls = %#v, want serial auth-a then auth-b", got)
	}
}

func TestManagerExecute_HedgedRetrySecondCleanWinsAfterPrimaryAbnormal(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry", delay: 20 * time.Millisecond}, {kind: "success", payload: "hedge-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "hedge-ok" {
		t.Fatalf("payload = %q, want hedge-ok", resp.Payload)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial plus two hedged lanes", calls)
	}
}

func TestManagerExecute_HedgedRetryRequireDistinctExcludesPrimaryAuth(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}},
			"auth-b": {{kind: "success", delay: 20 * time.Millisecond, payload: "primary-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      10 * time.Millisecond,
		requireDistinct: true,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "primary-ok" {
		t.Fatalf("payload = %q, want primary-ok", resp.Payload)
	}
	if got := executor.callsSnapshot(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("calls = %#v, want no duplicate secondary dispatch for auth-b", got)
	}
}

func TestManagerExecute_HedgedRetryDelayStartsSecondBeforePrimaryAuthSelected(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}},
			"auth-b": {{kind: "success", payload: "hedge-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      10 * time.Millisecond,
		requireDistinct: true,
	}
	selector := &delayedPrimaryHedgeSelector{triggerAuthID: "auth-a", secondaryAuthID: "auth-b"}
	manager, _ := newHedgedRetryTestManagerWithSelector(t, selector, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "hedge-ok" {
		t.Fatalf("payload = %q, want hedge-ok", resp.Payload)
	}
	if got := executor.callsSnapshot(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("calls = %#v, want trigger auth-a then secondary auth-b", got)
	}
	if calls := selector.callCount(); calls < 3 {
		t.Fatalf("selector calls = %d, want secondary pick before primary selection finishes", calls)
	}
}

func TestManagerExecute_HedgedRetrySingleCredentialRefundsZeroDispatchSecondLane(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry", delay: 10 * time.Millisecond}, {kind: "success", payload: "serial-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      time.Millisecond,
		requireDistinct: true,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "serial-ok" {
		t.Fatalf("payload = %q, want serial-ok", resp.Payload)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want zero-dispatch hedge refunded and one serial retry", calls)
	}
}

func TestManagerExecute_HedgedRetryDoubleAbnormalReturnsExhausted(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry"}, {kind: "retry"}},
		},
		maxRetries:      2,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial plus two hedged abnormal lanes", calls)
	}
}

func TestManagerExecute_HedgedRetryPassThroughAfterDoubleAbnormal(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry"}, {kind: "retry"}},
		},
		maxRetries:        2,
		hedgeDelay:        0,
		requireDistinct:   false,
		exhaustedBehavior: retryWithoutPenaltyExhaustedBehaviorPassThrough,
		fallbackPayload:   []byte("abnormal"),
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if string(resp.Payload) != "abnormal" {
		t.Fatalf("payload = %q, want abnormal", resp.Payload)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial plus two hedged abnormal lanes", calls)
	}
}

func TestManagerExecute_HedgedRetryWinnerSeesTriggerAndPrimaryAbnormalUsage(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "usage"}},
			"auth-b": {{kind: "retry"}},
		},
		maxRetries:      2,
		hedgeDelay:      10 * time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "usage:6" {
		t.Fatalf("payload = %q, want accumulated usage:6", resp.Payload)
	}
	if got := executor.callsSnapshot(); len(got) != 3 || got[0] != "auth-a" || got[1] != "auth-b" || got[2] != "auth-a" {
		t.Fatalf("calls = %#v, want auth-a trigger, auth-b primary abnormal, auth-a winner", got)
	}
}

func TestManagerExecute_HedgedRetrySelectedAuthCallbackReportsWinner(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "wait_cancel"}},
			"auth-b": {{kind: "success", delay: 20 * time.Millisecond, payload: "winner-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	var callbackMu sync.Mutex
	var callbackAuthIDs []string
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				callbackAuthIDs = append(callbackAuthIDs, authID)
			},
		},
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, opts)
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "winner-ok" {
		t.Fatalf("payload = %q, want winner-ok", resp.Payload)
	}

	callbackMu.Lock()
	got := append([]string(nil), callbackAuthIDs...)
	callbackMu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "auth-b" {
		t.Fatalf("selected auth callbacks = %#v, want final winner auth-b", got)
	}
}

func TestManagerExecute_HedgedRetryQualityWaitsForAbnormalAndFinalizesWinnerUsage(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "success", payload: "winner-ok", usage: coreusage.Detail{InputTokens: 5, OutputTokens: 20, ReasoningTokens: 8, TotalTokens: 25}, finalize: true},
			},
			"auth-b": {{kind: "retry"}},
		},
		maxRetries:      2,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	var callbackMu sync.Mutex
	var callbackAuthIDs []string
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
				callbackMu.Lock()
				defer callbackMu.Unlock()
				callbackAuthIDs = append(callbackAuthIDs, authID)
			},
		},
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, opts)
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "winner-ok|fold:1032|discarded:6" {
		t.Fatalf("payload = %q, want finalized winner usage", resp.Payload)
	}
	callbackMu.Lock()
	gotCallbacks := append([]string(nil), callbackAuthIDs...)
	callbackMu.Unlock()
	if len(gotCallbacks) == 0 || gotCallbacks[len(gotCallbacks)-1] != "auth-a" {
		t.Fatalf("selected auth callbacks = %#v, want final winner auth-a", gotCallbacks)
	}
	if got := executor.callsSnapshot(); len(got) != 3 || got[0] != "auth-a" || got[1] != "auth-b" || got[2] != "auth-a" {
		t.Fatalf("calls = %#v, want auth-a trigger, auth-b abnormal, auth-a winner", got)
	}
}

func TestManagerExecute_HedgedRetryQualityChoosesLargestOutputAndFoldsLoser(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "success", payload: "big", usage: coreusage.Detail{InputTokens: 5, OutputTokens: 30, ReasoningTokens: 8, TotalTokens: 35}, finalize: true},
			},
			"auth-b": {{kind: "success", payload: "small", usage: coreusage.Detail{InputTokens: 3, OutputTokens: 10, ReasoningTokens: 2, TotalTokens: 13}, finalize: true}},
		},
		maxRetries:      2,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "big|fold:526|discarded:16" {
		t.Fatalf("payload = %q, want big winner with trigger plus loser folded", resp.Payload)
	}
}

func TestManagerExecute_HedgedRetryQualityBudgetCountsDispatchedLanes(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry"}},
			"auth-b": {{kind: "retry"}},
		},
		maxRetries:      2,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want exhausted error")
	}
	if statusCodeFromError(err) != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; err=%v", statusCodeFromError(err), err)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial trigger plus exactly two quality lanes", calls)
	}
}

func TestManagerExecuteStream_HedgedRetryQualityReplaysWinnerOnly(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "success", payload: "big-stream", usage: coreusage.Detail{InputTokens: 5, OutputTokens: 30, ReasoningTokens: 8, TotalTokens: 35}, finalize: true},
			},
			"auth-b": {{kind: "success", payload: "small-stream", usage: coreusage.Detail{InputTokens: 3, OutputTokens: 10, ReasoningTokens: 2, TotalTokens: 13}, finalize: true}},
		},
		maxRetries:      2,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "big-stream|fold:526|discarded:16" {
		t.Fatalf("payload = %q, want finalized big stream winner only", payload)
	}
}

// Mixed-wave semantics: per the V1 hedged retry plan, an ordinary error only
// becomes the wave error when the wave produced no abnormal signal ("两路都普通
// 失败" case). When one lane is abnormal and another fails with an ordinary
// error and no lane succeeded, the anti-degradation guard keeps retrying while
// budget remains instead of surfacing the ordinary error.
// maxRetryCredentials=1 keeps the ordinary error at lane level instead of
// letting the lane rotate onto the other credential.
func TestManagerExecute_HedgedRetryQualityMixedAbnormalAndOrdinaryContinuesWaves(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "retry"},
				{kind: "success", delay: 25 * time.Millisecond, payload: "after-mixed", usage: coreusage.Detail{InputTokens: 5, OutputTokens: 30, TotalTokens: 35}},
			},
			"auth-b": {{kind: "rate_limit", delay: 25 * time.Millisecond}},
		},
		maxRetries:      4,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 1)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want mixed wave to continue retrying", err)
	}
	if string(resp.Payload) != "after-mixed" {
		t.Fatalf("payload = %q, want after-mixed winner from the follow-up wave", resp.Payload)
	}
	if calls := executor.callCount(); calls != 5 {
		t.Fatalf("calls = %d, want trigger plus two waves of two lanes", calls)
	}
}

func TestManagerExecute_HedgedRetryQualityMixedAbnormalAndOrdinaryExhaustsToExhaustedError(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "retry", delay: 25 * time.Millisecond},
				{kind: "rate_limit"},
			},
		},
		maxRetries:      2,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 1)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
	if statusCodeFromError(err) != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 exhausted rather than the ordinary lane error; err=%v", statusCodeFromError(err), err)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial trigger plus one mixed wave", calls)
	}
}

func TestManagerExecuteStream_HedgedRetryQualityMixedAbnormalAndOrdinaryContinuesWaves(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "retry"},
				{kind: "success", delay: 25 * time.Millisecond, payload: "after-mixed-stream", usage: coreusage.Detail{InputTokens: 5, OutputTokens: 30, TotalTokens: 35}},
			},
			"auth-b": {{kind: "rate_limit", delay: 25 * time.Millisecond}},
		},
		maxRetries:      4,
		hedgeMode:       retryWithoutPenaltyHedgeModeQuality,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 1)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want mixed wave to continue retrying", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "after-mixed-stream" {
		t.Fatalf("payload = %q, want after-mixed-stream winner from the follow-up wave", payload)
	}
}

func TestManagerExecute_HedgedRetryCleanLoserCompletedCountsAsSuccess(t *testing.T) {
	barrier := newHedgedRetryBarrier(2)
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "success", barrier: barrier}, {kind: "success", barrier: barrier}},
		},
		maxRetries:      2,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, authIDs := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	waitForAuthSuccess(t, manager, authIDs[0], 2)
	assertAuthNoPenaltyState(t, manager, authIDs[0], 2, 0)
}

func TestManagerExecute_HedgedRetryCanceledLoserRemainsNeutral(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "success", delay: time.Millisecond}, {kind: "wait_cancel"}},
		},
		maxRetries:      2,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, authIDs := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	assertAuthNoPenaltyState(t, manager, authIDs[0], 1, 0)
}

func TestManagerExecute_HedgedRetryOrdinaryErrorDoesNotAmplifyRequestRetryBudget(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "rate_limit"}, {kind: "rate_limit"}},
		},
		maxRetries:      2,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(2, 20*time.Millisecond, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want rate limit")
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; err=%v", statusCodeFromError(err), err)
	}
	if calls := executor.callCount(); calls != 3 {
		t.Fatalf("calls = %d, want initial abnormal plus two hedged ordinary errors only", calls)
	}
}

func TestManagerExecute_HedgedRetryPrimaryOrdinaryBeforeDelayDoesNotLaunchSecond(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "rate_limit"}},
		},
		maxRetries:      2,
		hedgeDelay:      50 * time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want primary 429")
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; err=%v", statusCodeFromError(err), err)
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("calls = %d, want initial abnormal plus primary ordinary error only", calls)
	}
}

func TestManagerExecute_HedgedRetryContinuesAbnormalWavesInsideHelper(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		behaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "retry", delay: 20 * time.Millisecond},
				{kind: "retry"},
				{kind: "wait_cancel"},
				{kind: "success", payload: "wave2-secondary-ok"},
			},
		},
		maxRetries:      5,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "wave2-secondary-ok" {
		t.Fatalf("payload = %q, want wave2-secondary-ok", resp.Payload)
	}
	if calls := executor.callCount(); calls != 5 {
		t.Fatalf("calls = %d, want initial plus two hedged abnormal lanes and second-wave hedge winner", calls)
	}
}

func TestManagerExecuteStream_HedgedRetrySecondWinnerForwardsChunks(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "retry", delay: 20 * time.Millisecond}, {kind: "success", payload: "stream-hedge-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "stream-hedge-ok" {
		t.Fatalf("payload = %q, want stream-hedge-ok", payload)
	}
	if calls := executor.streamCallCount(); calls != 3 {
		t.Fatalf("stream calls = %d, want initial plus two hedged lanes", calls)
	}
}

func TestManagerExecuteStream_HedgedRetryDelayStartsSecondBeforePrimaryAuthSelected(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}},
			"auth-b": {{kind: "success", payload: "stream-hedge-ok"}},
		},
		maxRetries:      2,
		hedgeDelay:      10 * time.Millisecond,
		requireDistinct: true,
	}
	selector := &delayedPrimaryHedgeSelector{triggerAuthID: "auth-a", secondaryAuthID: "auth-b"}
	manager, _ := newHedgedRetryTestManagerWithSelector(t, selector, executor, "auth-a", "auth-b")
	manager.SetRetryConfig(0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "stream-hedge-ok" {
		t.Fatalf("payload = %q, want stream-hedge-ok", payload)
	}
	if calls := executor.streamCallCount(); calls != 2 {
		t.Fatalf("stream calls = %d, want trigger auth-a then secondary auth-b", calls)
	}
	if calls := selector.callCount(); calls < 3 {
		t.Fatalf("selector calls = %d, want secondary pick before primary selection finishes", calls)
	}
}

func TestManagerExecuteStream_HedgedRetryPrimaryOrdinaryBeforeDelayDoesNotLaunchSecond(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {{kind: "retry"}, {kind: "rate_limit"}},
		},
		maxRetries:      2,
		hedgeDelay:      50 * time.Millisecond,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if result != nil {
		t.Fatalf("ExecuteStream result = %#v, want nil on primary 429", result)
	}
	if err == nil {
		t.Fatal("ExecuteStream error = nil, want primary 429")
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; err=%v", statusCodeFromError(err), err)
	}
	if calls := executor.streamCallCount(); calls != 2 {
		t.Fatalf("stream calls = %d, want initial abnormal plus primary ordinary error only", calls)
	}
}

func TestManagerExecuteStream_HedgedRetryContinuesAbnormalWavesInsideHelper(t *testing.T) {
	executor := &hedgedRetryTestExecutor{
		streamBehaviors: map[string][]hedgedRetryBehavior{
			"auth-a": {
				{kind: "retry"},
				{kind: "retry", delay: 20 * time.Millisecond},
				{kind: "retry"},
				{kind: "wait_cancel"},
				{kind: "success", payload: "stream-wave2-secondary-ok"},
			},
		},
		maxRetries:      5,
		hedgeDelay:      0,
		requireDistinct: false,
	}
	manager, _ := newHedgedRetryTestManager(t, executor, "auth-a")
	manager.SetRetryConfig(0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "stream-wave2-secondary-ok" {
		t.Fatalf("payload = %q, want stream-wave2-secondary-ok", payload)
	}
	if calls := executor.streamCallCount(); calls != 5 {
		t.Fatalf("stream calls = %d, want initial plus two hedged abnormal lanes and second-wave hedge winner", calls)
	}
}

func waitForAuthSuccess(t *testing.T, manager *Manager, authID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		auth := manager.auths[authID]
		var got int64
		if auth != nil {
			got = auth.Success
		}
		manager.mu.RUnlock()
		if got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	manager.mu.RLock()
	auth := manager.auths[authID]
	manager.mu.RUnlock()
	if auth == nil {
		t.Fatalf("auth %s not found", authID)
	}
	t.Fatalf("auth.Success = %d, want at least %d", auth.Success, want)
}
