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

type codexClientMetadataPreflightExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *codexClientMetadataPreflightExecutor) Identifier() string { return "codex" }

func (e *codexClientMetadataPreflightExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, "execute")
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *codexClientMetadataPreflightExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth, "stream")
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *codexClientMetadataPreflightExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, "count")
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (e *codexClientMetadataPreflightExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexClientMetadataPreflightExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *codexClientMetadataPreflightExecutor) record(auth *Auth, kind string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.calls = append(e.calls, kind+":"+authID)
}

func (e *codexClientMetadataPreflightExecutor) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func newCodexClientMetadataPreflightManager(t *testing.T, authIDs ...string) (*Manager, *codexClientMetadataPreflightExecutor) {
	t.Helper()
	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager := NewManager(nil, selector, nil)
	t.Cleanup(selector.Stop)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}})
	executor := &codexClientMetadataPreflightExecutor{}
	manager.RegisterExecutor(executor)

	model := "gpt-codex-client-metadata-preflight"
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
	return manager, executor
}

func TestManagerCanonicalCodexSessionPreflightPinsChangingRequestIDs(t *testing.T) {
	manager, executor := newCodexClientMetadataPreflightManager(t, "codex-client-metadata-a", "codex-client-metadata-b")
	body := []byte(`{"model":"gpt-codex-client-metadata-preflight","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"canonical-session-1\",\"thread_id\":\"canonical-session-1\"}"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-codex-client-metadata-preflight", Payload: body}

	var selected []string
	for _, requestID := range []string{"request-1", "request-2"} {
		resp, err := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{
			Headers: http.Header{
				"Session_id":          {"legacy-" + requestID},
				"X-Client-Request-Id": {requestID},
			},
			OriginalRequest: body,
		})
		if err != nil {
			t.Fatalf("Execute(%s) error = %v", requestID, err)
		}
		selected = append(selected, string(resp.Payload))
	}
	if len(selected) != 2 || selected[0] == "" || selected[1] != selected[0] {
		t.Fatalf("selected auths = %#v, want stable canonical-session binding", selected)
	}
	if calls := executor.snapshot(); len(calls) != 2 {
		t.Fatalf("executor calls = %#v, want 2", calls)
	}
}

func TestPreflightCodexClientMetadataUsesCurrentPayloadOverOriginalRequest(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	current := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"current-session\"}"}}`)
	original := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"old-session\"}"}}`)
	opts, err := manager.preflightCodexClientMetadata([]string{"codex"}, cliproxyexecutor.Request{Payload: current}, cliproxyexecutor.Options{OriginalRequest: original})
	if err != nil {
		t.Fatalf("preflightCodexClientMetadata() error = %v", err)
	}
	if got := contextStringValue(opts.Metadata[codexCanonicalSessionMetadataKey]); got != "current-session" {
		t.Fatalf("canonical session = %q, want current-session", got)
	}
}

func TestPreflightCodexClientMetadataReadsCaseInsensitiveDirectHeader(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	canonical := `{"request_kind":"turn","session_id":"lowercase-header-session"}`
	opts, err := manager.preflightCodexClientMetadata([]string{"codex"}, cliproxyexecutor.Request{Payload: []byte(`{"model":"gpt-5.6"}`)}, cliproxyexecutor.Options{
		Headers: http.Header{"x-codex-turn-metadata": {canonical}},
	})
	if err != nil {
		t.Fatalf("preflightCodexClientMetadata() error = %v", err)
	}
	if got := contextStringValue(opts.Metadata[codexCanonicalSessionMetadataKey]); got != "lowercase-header-session" {
		t.Fatalf("canonical session = %q, want lowercase-header-session", got)
	}
}

func TestCanonicalSessionPrivateMetadataPriorityAndContinuity(t *testing.T) {
	metadata := map[string]any{
		codexCanonicalSessionMetadataKey:             "canonical-session",
		cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session",
	}
	headers := http.Header{"Session_id": {"legacy-session"}}
	primary, _ := extractSessionIDs(headers, nil, metadata)
	if primary != "execution:execution-session" {
		t.Fatalf("extractSessionIDs() = %q, want execution session priority", primary)
	}

	delete(metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	primary, _ = extractSessionIDs(headers, nil, metadata)
	if primary != "codex:canonical-session" {
		t.Fatalf("extractSessionIDs() = %q, want canonical session above legacy header", primary)
	}
	if got := codexRateLimitStableSessionID(cliproxyexecutor.Options{Headers: headers, Metadata: metadata}); got != "codex:canonical-session" {
		t.Fatalf("codexRateLimitStableSessionID() = %q, want private canonical session", got)
	}
}

func TestManagerCodexClientMetadataPreflightOffModeAllowsMalformedCanonical(t *testing.T) {
	manager, executor := newCodexClientMetadataPreflightManager(t, "codex-client-metadata-off-malformed")
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{ClientMetadata: internalconfig.CodexClientMetadataConfig{
			Mode: internalconfig.CodexClientMetadataModeOff,
		}},
	})
	body := []byte(`{"model":"gpt-codex-client-metadata-preflight","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\""}}`)
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-codex-client-metadata-preflight",
		Payload: body,
	}, cliproxyexecutor.Options{OriginalRequest: body})
	if err != nil {
		t.Fatalf("Execute() off-mode malformed canonical error = %v", err)
	}
	if string(resp.Payload) != "codex-client-metadata-off-malformed" {
		t.Fatalf("Execute() payload = %q, want selected auth ID", resp.Payload)
	}
	if calls := executor.snapshot(); len(calls) != 1 {
		t.Fatalf("executor calls = %#v, want one", calls)
	}
}

func TestManagerCodexClientMetadataPreflightRejectsBeforeExecutor(t *testing.T) {
	manager, executor := newCodexClientMetadataPreflightManager(t, "codex-client-metadata-invalid")
	body := []byte(`{"model":"gpt-codex-client-metadata-preflight","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"one\"}"},"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"two\"}"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-codex-client-metadata-preflight", Payload: body}
	opts := cliproxyexecutor.Options{OriginalRequest: body}

	tests := []struct {
		name   string
		invoke func() error
	}{
		{name: "execute", invoke: func() error {
			_, err := manager.Execute(context.Background(), []string{"codex"}, req, opts)
			return err
		}},
		{name: "count", invoke: func() error {
			_, err := manager.ExecuteCount(context.Background(), []string{"codex"}, req, opts)
			return err
		}},
		{name: "stream", invoke: func() error {
			_, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.invoke()
			if err == nil {
				t.Fatal("execution accepted ambiguous client metadata")
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error = %T %v, want request-scoped 400", err, err)
			}
			requestErr, ok := err.(interface{ IsRequestScoped() bool })
			if !ok || !requestErr.IsRequestScoped() {
				t.Fatalf("error = %T %v, want request-scoped", err, err)
			}
		})
	}
	if calls := executor.snapshot(); len(calls) != 0 {
		t.Fatalf("executor calls = %#v, want none", calls)
	}
}
