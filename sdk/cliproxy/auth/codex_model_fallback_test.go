package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	mu           sync.Mutex
	calls        []string
	authCalls    []string
	executeErrs  map[string]error
	streamErrs   map[string]error
	streamChunks map[string][]cliproxyexecutor.StreamChunk
	metadataSeen map[string]map[string]any
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
