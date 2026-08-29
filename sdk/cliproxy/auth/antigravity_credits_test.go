package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type antigravityCreditsFallbackExecutor struct {
	streamCreditsRequested []bool
}

type antigravityCreditsAdmissionExecutor struct {
	admissionCalls int
	executeCalls   int
	streamCalls    int
}

func (*antigravityCreditsAdmissionExecutor) Identifier() string { return "antigravity" }

func (e *antigravityCreditsAdmissionExecutor) AdmitExecution(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (context.Context, error) {
	e.admissionCalls++
	return ctx, NewRequestScopedError("credits runner is not ready", http.StatusServiceUnavailable)
}

func (e *antigravityCreditsAdmissionExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	return cliproxyexecutor.Response{}, nil
}

func (e *antigravityCreditsAdmissionExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls++
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*antigravityCreditsAdmissionExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*antigravityCreditsAdmissionExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*antigravityCreditsAdmissionExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *antigravityCreditsFallbackExecutor) Identifier() string { return "antigravity" }

func (e *antigravityCreditsFallbackExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *antigravityCreditsFallbackExecutor) ExecuteStream(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	creditsRequested := AntigravityCreditsRequested(ctx)
	e.streamCreditsRequested = append(e.streamCreditsRequested, creditsRequested)
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if !creditsRequested {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Initial": {req.Model}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("credits fallback")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Credits": {req.Model}}, Chunks: ch}, nil
}

func (e *antigravityCreditsFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *antigravityCreditsFallbackExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *antigravityCreditsFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

type codexOnlyFailureExecutor struct{}

func (codexOnlyFailureExecutor) Identifier() string { return "codex" }

func (codexOnlyFailureExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "codex quota exhausted"}
}

func (codexOnlyFailureExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "codex quota exhausted"}
}

func (codexOnlyFailureExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (codexOnlyFailureExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "codex quota exhausted"}
}

func (codexOnlyFailureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "codex quota exhausted"}
}

type captureLogHook struct {
	messages []string
}

func (h *captureLogHook) Levels() []log.Level {
	return log.AllLevels
}

func (h *captureLogHook) Fire(entry *log.Entry) error {
	h.messages = append(h.messages, entry.Message)
	return nil
}

func TestManagerExecuteStream_AntigravityCreditsFallbackAfterBootstrap429(t *testing.T) {
	const model = "claude-opus-4-6-thinking"
	executor := &antigravityCreditsFallbackExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient("ag-credits", "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("ag-credits") })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "ag-credits", Provider: "antigravity"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream: %v", errExecute)
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "credits fallback" {
		t.Fatalf("payload = %q, want %q", string(payload), "credits fallback")
	}
	if got := streamResult.Headers.Get("X-Credits"); got != model {
		t.Fatalf("X-Credits header = %q, want routed model", got)
	}
	if len(executor.streamCreditsRequested) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(executor.streamCreditsRequested))
	}
	if executor.streamCreditsRequested[0] || !executor.streamCreditsRequested[1] {
		t.Fatalf("credits flags = %v, want [false true]", executor.streamCreditsRequested)
	}
}

func TestAntigravityCreditsFallbackAdmitsBeforeSelectionAndDispatch(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, ok, errExecute := manager.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: "claude-admission-test"}, opts)
				if ok {
					return fmt.Errorf("credits execute unexpectedly reported success")
				}
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, ok, errStream := manager.tryAntigravityCreditsExecuteStream(context.Background(), cliproxyexecutor.Request{Model: "claude-admission-test"}, opts)
				if ok {
					return fmt.Errorf("credits stream unexpectedly reported success")
				}
				return errStream
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &antigravityCreditsAdmissionExecutor{}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := "ag-admission-" + tt.name
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			SetAntigravityCreditsHint(authID, AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
			selected := 0
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) { selected++ },
			}}

			if errRun := tt.run(manager, opts); errRun == nil {
				t.Fatal("credits fallback error = nil, want admission rejection")
			}
			if executor.admissionCalls != 1 {
				t.Fatalf("admission calls = %d, want 1", executor.admissionCalls)
			}
			if executor.executeCalls != 0 || executor.streamCalls != 0 {
				t.Fatalf("executor calls = execute:%d stream:%d, want none", executor.executeCalls, executor.streamCalls)
			}
			if selected != 0 {
				t.Fatalf("selected-auth callbacks = %d, want none before rejected admission", selected)
			}
		})
	}
}

func TestManagerExecuteStream_AntigravityCreditsHomeModeFailsClosedWithoutDispatch(t *testing.T) {
	const model = "claude-opus-4-6-thinking"
	executor := &antigravityCreditsFallbackExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:          internalconfig.HomeConfig{Enabled: true},
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient("ag-credits-home-kv", "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("ag-credits-home-kv") })
	homekv.SetCurrent(homekv.New(internalconfig.HomeConfig{Enabled: false}))
	t.Cleanup(homekv.ClearCurrent)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "ag-credits-home-kv", Provider: "antigravity"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, errExecute := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("ExecuteStream() error = nil, want home kv unavailable error")
	}
	if status := statusCodeFromError(errExecute); status != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteStream() status = %d, want %d; err=%v", status, http.StatusServiceUnavailable, errExecute)
	}
	if !strings.Contains(errExecute.Error(), "home dispatch bundle unavailable") {
		t.Fatalf("ExecuteStream() error = %v, want home dispatch bundle unavailable", errExecute)
	}
}

func TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback(t *testing.T) {
	const model = "gpt-5.5"
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	hook := &captureLogHook{}
	logger.SetLevel(log.DebugLevel)
	logger.AddHook(hook)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.ReplaceHooks(oldHooks)
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.RegisterExecutor(codexOnlyFailureExecutor{})
	manager.RegisterExecutor(&antigravityCreditsFallbackExecutor{})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("codex-only", "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient("ag-unrelated", "antigravity", []*registry.ModelInfo{{ID: "gemini-3-flash"}})
	t.Cleanup(func() {
		reg.UnregisterClient("codex-only")
		reg.UnregisterClient("ag-unrelated")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-only", Provider: "codex"}); errRegister != nil {
		t.Fatalf("register codex auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "ag-unrelated", Provider: "antigravity"}); errRegister != nil {
		t.Fatalf("register antigravity auth: %v", errRegister)
	}

	_, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected codex execution failure")
	}

	for _, message := range hook.messages {
		if strings.Contains(message, "shouldAttemptAntigravityCreditsFallback") {
			t.Fatalf("codex-only request entered antigravity credits fallback gate; messages=%v", hook.messages)
		}
	}
}

func TestStatusCodeFromError_UnwrapsStreamBootstrap429(t *testing.T) {
	bootstrapErr := newStreamBootstrapError(&Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}, nil)
	wrappedErr := fmt.Errorf("conductor stream failed: %w", bootstrapErr)

	if status := statusCodeFromError(wrappedErr); status != http.StatusTooManyRequests {
		t.Fatalf("statusCodeFromError() = %d, want %d", status, http.StatusTooManyRequests)
	}
}

func TestIsAuthBlockedForModel_ClaudeWithCreditsStillBlockedDuringCooldown(t *testing.T) {
	auth := &Auth{
		ID:       "ag-1",
		Provider: "antigravity",
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4-6": {
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(10 * time.Minute),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: time.Now().Add(10 * time.Minute),
				},
			},
		},
	}

	SetAntigravityCreditsHint(auth.ID, AntigravityCreditsHint{
		Known:     true,
		Available: true,
		UpdatedAt: time.Now(),
	})

	blocked, reason, _ := isAuthBlockedForModel(auth, "claude-sonnet-4-6", time.Now())
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("expected auth to be blocked during cooldown even with credits, got blocked=%v reason=%v", blocked, reason)
	}
}

func TestIsAuthBlockedForModel_KeepsGeminiBlockedWithoutCreditsBypass(t *testing.T) {
	auth := &Auth{
		ID:       "ag-2",
		Provider: "antigravity",
		ModelStates: map[string]*ModelState{
			"gemini-3-flash": {
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(10 * time.Minute),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: time.Now().Add(10 * time.Minute),
				},
			},
		},
	}

	SetAntigravityCreditsHint(auth.ID, AntigravityCreditsHint{
		Known:     true,
		Available: true,
		UpdatedAt: time.Now(),
	})

	blocked, reason, _ := isAuthBlockedForModel(auth, "gemini-3-flash", time.Now())
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("expected gemini model to remain blocked, got blocked=%v reason=%v", blocked, reason)
	}
}
