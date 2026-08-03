package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type providerTransitionTestExecutor struct {
	provider       string
	executeStarted chan struct{}
	executeRelease chan struct{}
	streamStarted  chan struct{}
	streamRelease  chan struct{}
	countStarted   chan struct{}
	countRelease   chan struct{}
	prepareStarted chan struct{}
	prepareRelease chan struct{}
	refreshStarted chan struct{}
	refreshRelease chan struct{}
	refreshErr     error
}

func (e *providerTransitionTestExecutor) Identifier() string { return e.provider }

func (e *providerTransitionTestExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	return e.prepareStarted != nil
}

func (e *providerTransitionTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if e.prepareStarted != nil {
		close(e.prepareStarted)
	}
	if e.prepareRelease != nil {
		<-e.prepareRelease
	}
	updated := auth.Clone()
	updated.Metadata = map[string]any{"epoch": "old-provider-prepared"}
	updated.Runtime = "old-provider-prepared-runtime"
	return updated, nil
}

func (e *providerTransitionTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.executeStarted != nil {
		close(e.executeStarted)
	}
	if e.executeRelease != nil {
		<-e.executeRelease
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old provider quota"}
}

func (e *providerTransitionTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamStarted != nil {
		close(e.streamStarted)
	}
	if e.streamRelease != nil {
		<-e.streamRelease
	}
	return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old provider stream quota"}
}

func (e *providerTransitionTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if e.refreshStarted != nil {
		close(e.refreshStarted)
	}
	if e.refreshRelease != nil {
		<-e.refreshRelease
	}
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	updated := auth.Clone()
	updated.Metadata = map[string]any{
		"access_token":  "old-provider-refreshed-token",
		"refresh_token": "old-provider-refresh-token",
	}
	updated.Runtime = "old-provider-refreshed-runtime"
	return updated, nil
}

func (e *providerTransitionTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.countStarted != nil {
		close(e.countStarted)
	}
	if e.countRelease != nil {
		<-e.countRelease
	}
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old provider count quota"}
}

func (*providerTransitionTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type providerTransitionTestHook struct {
	updated atomic.Int32
	results atomic.Int32
}

func (*providerTransitionTestHook) OnAuthRegistered(context.Context, *Auth) {}

func (h *providerTransitionTestHook) OnAuthUpdated(context.Context, *Auth) {
	h.updated.Add(1)
}

func (h *providerTransitionTestHook) OnResult(context.Context, Result) {
	h.results.Add(1)
}

func waitProviderTransitionSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func transitionTestAuthProvider(t *testing.T, manager *Manager, authID, provider string) *Auth {
	t.Helper()
	current, ok := manager.GetByID(authID)
	if !ok || current == nil {
		t.Fatalf("auth %q missing before provider transition", authID)
	}
	updated := current.Clone()
	updated.Provider = provider
	updated.Metadata = map[string]any{
		"access_token":  "new-provider-token",
		"refresh_token": "new-provider-refresh-token",
		"epoch":         "new-provider",
	}
	updated.Attributes = map[string]string{"epoch": "new-provider"}
	updated.Runtime = "new-provider-runtime"
	saved, err := manager.Update(WithSkipPersist(context.Background()), updated)
	if err != nil {
		t.Fatalf("transition auth provider: %v", err)
	}
	if saved == nil || saved.Provider != provider {
		t.Fatalf("provider transition saved auth = %#v, want provider %q", saved, provider)
	}
	snapshot, ok := manager.GetByID(authID)
	if !ok || snapshot == nil {
		t.Fatalf("auth %q missing after provider transition", authID)
	}
	return snapshot
}

func assertProviderTransitionSnapshotUnchanged(t *testing.T, manager *Manager, authID string, want *Auth) {
	t.Helper()
	got, ok := manager.GetByID(authID)
	if !ok || got == nil {
		t.Fatalf("auth %q missing after stale writeback", authID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("auth changed after stale writeback\n got: %#v\nwant: %#v", got, want)
	}
}

func TestManager_ProviderTransitionRejectsStaleExecutionGeneration(t *testing.T) {
	const (
		authID      = "provider-transition-execute"
		oldProvider = "generation-old-execute"
		newProvider = "generation-new-execute"
		model       = "generation-execute-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	executor := &providerTransitionTestExecutor{
		provider:       oldProvider,
		executeStarted: make(chan struct{}),
		executeRelease: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	manager.SetRetryConfig(0, 0, 1)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	auth := &Auth{
		ID:       authID,
		Provider: oldProvider,
		Metadata: map[string]any{
			"access_token":  "old-provider-token",
			"refresh_token": "old-provider-refresh-token",
		},
		Runtime: "old-provider-runtime",
	}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	executeDone := make(chan error, 1)
	go func() {
		_, err := manager.Execute(context.Background(), []string{oldProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		executeDone <- err
	}()
	waitProviderTransitionSignal(t, executor.executeStarted, "old provider execution")

	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)
	if models := reg.GetAvailableModels(newProvider); len(models) != 1 {
		t.Fatalf("available models before stale result = %d, want 1", len(models))
	}

	close(executor.executeRelease)
	select {
	case err := <-executeDone:
		if err == nil {
			t.Fatal("Execute() error = nil, want old provider failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old provider execution to finish")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale execution persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale execution emitted %d result hooks, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale execution emitted %d auth update hooks, want 0", got)
	}
	if models := reg.GetAvailableModels(newProvider); len(models) != 1 {
		t.Fatalf("available models after stale result = %d, want 1", len(models))
	}
}

func TestManager_ProviderTransitionRejectsStaleCountTokensGeneration(t *testing.T) {
	const (
		authID      = "provider-transition-count"
		oldProvider = "generation-old-count"
		newProvider = "generation-new-count"
		model       = "generation-count-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	executor := &providerTransitionTestExecutor{
		provider:     oldProvider,
		countStarted: make(chan struct{}),
		countRelease: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	manager.SetRetryConfig(0, 0, 1)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: authID, Provider: oldProvider}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	countDone := make(chan error, 1)
	go func() {
		_, err := manager.ExecuteCount(context.Background(), []string{oldProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		countDone <- err
	}()
	waitProviderTransitionSignal(t, executor.countStarted, "old provider count-tokens execution")

	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)
	close(executor.countRelease)

	select {
	case err := <-countDone:
		if err == nil {
			t.Fatal("ExecuteCount() error = nil, want old provider failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old provider count-tokens execution")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale count-tokens execution persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale count-tokens execution emitted %d result hooks, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale count-tokens execution emitted %d auth update hooks, want 0", got)
	}
}

func TestManager_ProviderTransitionRejectsStaleStreamGeneration(t *testing.T) {
	const (
		authID      = "provider-transition-stream"
		oldProvider = "generation-old-stream"
		newProvider = "generation-new-stream"
		model       = "generation-stream-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	executor := &providerTransitionTestExecutor{
		provider:      oldProvider,
		streamStarted: make(chan struct{}),
		streamRelease: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	manager.SetRetryConfig(0, 0, 1)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: authID, Provider: oldProvider}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	streamDone := make(chan struct{}, 1)
	go func() {
		result, _ := manager.ExecuteStream(context.Background(), []string{oldProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		if result != nil && result.Chunks != nil {
			for range result.Chunks {
			}
		}
		streamDone <- struct{}{}
	}()
	waitProviderTransitionSignal(t, executor.streamStarted, "old provider stream execution")

	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)
	close(executor.streamRelease)

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old provider stream execution")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale stream execution persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale stream execution emitted %d result hooks, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale stream execution emitted %d auth update hooks, want 0", got)
	}
}

func TestManager_ProviderTransitionRejectsStaleRequestAuthPreparation(t *testing.T) {
	const (
		authID      = "provider-transition-request-prepare"
		oldProvider = "generation-old-request-prepare"
		newProvider = "generation-new-request-prepare"
		model       = "generation-request-prepare-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	executor := &providerTransitionTestExecutor{
		provider:       oldProvider,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	manager.SetRetryConfig(0, 0, 1)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: oldProvider,
		Metadata: map[string]any{"epoch": "old-provider"},
		Runtime:  "old-provider-runtime",
	}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	executeDone := make(chan error, 1)
	go func() {
		_, err := manager.Execute(context.Background(), []string{oldProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		executeDone <- err
	}()
	waitProviderTransitionSignal(t, executor.prepareStarted, "old provider request preparation")

	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)
	close(executor.prepareRelease)

	select {
	case err := <-executeDone:
		if err == nil {
			t.Fatal("Execute() error = nil, want stale request preparation rejection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stale request preparation")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale request preparation persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale request preparation emitted %d result hooks, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale request preparation emitted %d auth update hooks, want 0", got)
	}
}

func TestManager_SelectAuthForRequestGenerationRejectsStaleExternalResult(t *testing.T) {
	const (
		authID      = "provider-transition-external-result"
		oldProvider = "generation-old-external-result"
		newProvider = "generation-new-external-result"
		model       = "generation-external-result-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	manager.RegisterExecutor(&providerTransitionTestExecutor{provider: oldProvider})

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: oldProvider,
	}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	selected, resultCtx, err := manager.SelectAuthForRequest(context.Background(), oldProvider, model, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	if selected == nil || selected.ID != authID {
		t.Fatalf("SelectAuthForRequest() auth = %#v, want %q", selected, authID)
	}

	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)

	manager.MarkResult(resultCtx, Result{
		AuthID:   authID,
		Provider: oldProvider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old provider quota"},
	})

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale external result persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale external result emitted %d result hooks, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale external result emitted %d auth update hooks, want 0", got)
	}
	if models := reg.GetAvailableModels(newProvider); len(models) != 1 {
		t.Fatalf("available models after stale external result = %d, want 1", len(models))
	}
}

func TestManager_StaleRequestCannotRefreshReplacementProvider(t *testing.T) {
	const (
		authID      = "provider-transition-refresh-context"
		oldProvider = "generation-old-refresh-context"
		newProvider = "generation-new-refresh-context"
		model       = "generation-refresh-context-model"
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&providerTransitionTestExecutor{provider: oldProvider})
	newRefreshStarted := make(chan struct{})
	manager.RegisterExecutor(&providerTransitionTestExecutor{provider: newProvider, refreshStarted: newRefreshStarted})

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, oldProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: oldProvider,
		Metadata: map[string]any{"access_token": "old-provider-token", "refresh_token": "old-provider-refresh-token"},
	}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	_, requestCtx, err := manager.SelectAuthForRequest(context.Background(), oldProvider, model, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	want := transitionTestAuthProvider(t, manager, authID, newProvider)

	refreshed, err := manager.refreshAuthForRequest(requestCtx, authID, "old-provider-token")
	if refreshed != nil {
		t.Fatalf("stale refresh returned auth %#v, want nil", refreshed)
	}
	var lifecycleErr *Error
	if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != "auth_lifecycle_changed" {
		t.Fatalf("stale refresh error = %v, want auth_lifecycle_changed", err)
	}
	select {
	case <-newRefreshStarted:
		t.Fatal("stale request invoked replacement provider refresh")
	default:
	}
	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
}

func TestManager_MarkResultRejectsStaleProviderWithoutGenerationContext(t *testing.T) {
	const (
		authID      = "provider-transition-provider-fence"
		oldProvider = "generation-old-provider-fence"
		newProvider = "generation-new-provider-fence"
		model       = "generation-provider-fence-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: authID, Provider: oldProvider}); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}
	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: oldProvider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old provider quota"},
	})

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale provider result persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale provider result emitted %d result hooks, want 0", got)
	}
}

func TestManager_SelectAuthForRequestGenerationRejectsSameProviderReplacement(t *testing.T) {
	const (
		authID   = "provider-transition-same-provider-replacement"
		provider = "generation-same-provider-replacement"
		model    = "generation-same-provider-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	manager.RegisterExecutor(&providerTransitionTestExecutor{provider: provider})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: provider,
		Metadata: map[string]any{"epoch": "old"},
	}); err != nil {
		t.Fatalf("register original auth: %v", err)
	}

	_, resultCtx, err := manager.SelectAuthForRequest(context.Background(), provider, model, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	manager.Remove(context.Background(), authID)
	if _, err = manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: provider,
		Metadata: map[string]any{"epoch": "replacement"},
	}); err != nil {
		t.Fatalf("register replacement auth: %v", err)
	}
	want, ok := manager.GetByID(authID)
	if !ok || want == nil {
		t.Fatal("replacement auth missing")
	}
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)

	manager.MarkResult(resultCtx, Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "old lifecycle quota"},
	})

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale generation persisted auth %d times, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale generation emitted %d result hooks, want 0", got)
	}
}

func TestManager_ProviderTransitionRejectsStaleRefreshSuccessGeneration(t *testing.T) {
	const (
		authID      = "provider-transition-refresh-success"
		oldProvider = "generation-old-refresh-success"
		newProvider = "generation-new-refresh-success"
		model       = "generation-refresh-success-model"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	executor := &providerTransitionTestExecutor{
		provider:       oldProvider,
		refreshStarted: make(chan struct{}),
		refreshRelease: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	oldAuth := &Auth{
		ID:       authID,
		Provider: oldProvider,
		Metadata: map[string]any{
			"access_token":  "old-provider-token",
			"refresh_token": "old-provider-refresh-token",
		},
		Runtime: "old-provider-runtime",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
				LastError:   &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized, Message: "old provider unauthorized"},
			},
		},
	}
	if _, err := manager.Register(WithSkipPersist(context.Background()), oldAuth); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	type refreshOutcome struct {
		auth *Auth
		err  error
	}
	refreshDone := make(chan refreshOutcome, 1)
	go func() {
		updated, err := manager.refreshAuthForRequest(context.Background(), authID, "")
		refreshDone <- refreshOutcome{auth: updated, err: err}
	}()
	waitProviderTransitionSignal(t, executor.refreshStarted, "old provider refresh")

	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, newProvider, []*registry.ModelInfo{{ID: model}})
	reg.SuspendClientModel(authID, model, "new-provider-maintenance")
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)
	if models := reg.GetAvailableModels(newProvider); len(models) != 0 {
		t.Fatalf("available models before stale refresh = %d, want 0", len(models))
	}

	close(executor.refreshRelease)
	select {
	case outcome := <-refreshDone:
		if outcome.err != nil {
			t.Fatalf("stale successful refresh returned error: %v", outcome.err)
		}
		if outcome.auth != nil {
			t.Fatalf("stale successful refresh returned auth %#v, want nil", outcome.auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old provider refresh to finish")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale refresh persisted auth %d times, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale refresh emitted %d auth update hooks, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale refresh emitted %d result hooks, want 0", got)
	}
	if models := reg.GetAvailableModels(newProvider); len(models) != 0 {
		t.Fatalf("stale refresh resumed new provider model; available models = %d, want 0", len(models))
	}
}

func TestManager_ProviderTransitionRejectsStaleRefreshFailureGeneration(t *testing.T) {
	const (
		authID      = "provider-transition-refresh-failure"
		oldProvider = "generation-old-refresh-failure"
		newProvider = "generation-new-refresh-failure"
	)

	store := &countingStore{}
	hook := &providerTransitionTestHook{}
	manager := NewManager(store, nil, hook)
	expectedErr := errors.New("old provider refresh failed")
	executor := &providerTransitionTestExecutor{
		provider:       oldProvider,
		refreshStarted: make(chan struct{}),
		refreshRelease: make(chan struct{}),
		refreshErr:     expectedErr,
	}
	manager.RegisterExecutor(executor)

	oldAuth := &Auth{
		ID:       authID,
		Provider: oldProvider,
		Metadata: map[string]any{
			"access_token":  "old-provider-token",
			"refresh_token": "old-provider-refresh-token",
		},
		Runtime: "old-provider-runtime",
	}
	if _, err := manager.Register(WithSkipPersist(context.Background()), oldAuth); err != nil {
		t.Fatalf("register old provider auth: %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.refreshAuthForRequest(context.Background(), authID, "")
		refreshDone <- err
	}()
	waitProviderTransitionSignal(t, executor.refreshStarted, "old provider refresh")

	want := transitionTestAuthProvider(t, manager, authID, newProvider)
	store.saveCount.Store(0)
	hook.updated.Store(0)
	hook.results.Store(0)

	close(executor.refreshRelease)
	select {
	case err := <-refreshDone:
		if !errors.Is(err, expectedErr) {
			t.Fatalf("refresh error = %v, want %v", err, expectedErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for old provider refresh to finish")
	}

	assertProviderTransitionSnapshotUnchanged(t, manager, authID, want)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("stale failed refresh persisted auth %d times, want 0", got)
	}
	if got := hook.updated.Load(); got != 0 {
		t.Fatalf("stale failed refresh emitted %d auth update hooks, want 0", got)
	}
	if got := hook.results.Load(); got != 0 {
		t.Fatalf("stale failed refresh emitted %d result hooks, want 0", got)
	}
}
