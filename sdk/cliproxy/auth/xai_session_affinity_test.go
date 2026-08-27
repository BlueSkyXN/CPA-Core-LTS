package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type xaiSessionAffinityExecutor struct {
	mu                sync.Mutex
	authIDs           []string
	executionSessions []string
}

func (*xaiSessionAffinityExecutor) Identifier() string { return "xai" }

func (e *xaiSessionAffinityExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, req, opts)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *xaiSessionAffinityExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth, req, opts)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*xaiSessionAffinityExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *xaiSessionAffinityExecutor) CountTokens(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth, req, opts)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (*xaiSessionAffinityExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func (e *xaiSessionAffinityExecutor) record(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authIDs = append(e.authIDs, auth.ID)
	sessionID := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey])
	if requestSessionID := contextStringValue(req.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); requestSessionID != sessionID {
		sessionID = "mismatch:" + sessionID + ":" + requestSessionID
	}
	e.executionSessions = append(e.executionSessions, sessionID)
}

func (e *xaiSessionAffinityExecutor) snapshot() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...), append([]string(nil), e.executionSessions...)
}

func newXAISessionAffinityManager(t *testing.T, model string) (*Manager, *xaiSessionAffinityExecutor) {
	t.Helper()
	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager := NewManager(nil, selector, nil)
	t.Cleanup(selector.Stop)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&config.Config{Routing: config.RoutingConfig{SessionAffinity: true}})
	executor := &xaiSessionAffinityExecutor{}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"xai-affinity-a", "xai-affinity-b"} {
		reg.RegisterClient(authID, "xai", []*registry.ModelInfo{{ID: model}})
		if _, err := manager.Register(context.Background(), &Auth{
			ID:       authID,
			Provider: "xai",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "xai", "access_token": "test-token"},
		}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}
	t.Cleanup(func() {
		for _, authID := range []string{"xai-affinity-a", "xai-affinity-b"} {
			reg.UnregisterClient(authID)
		}
	})
	return manager, executor
}

func TestManagerXAIUsesCanonicalCodexSessionAcrossChangingRequests(t *testing.T) {
	const model = "grok-4.6"
	tests := []struct {
		name   string
		invoke func(*Manager, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			invoke: func(manager *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, err := manager.Execute(context.Background(), []string{"xai"}, req, opts)
				return err
			},
		},
		{
			name: "count_tokens",
			invoke: func(manager *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, err := manager.ExecuteCount(context.Background(), []string{"xai"}, req, opts)
				return err
			},
		},
		{
			name: "stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				result, err := manager.ExecuteStream(context.Background(), []string{"xai"}, req, opts)
				if err != nil {
					return err
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			},
		},
	}

	canonicalJSON, err := json.Marshal(map[string]any{
		"request_kind": "turn",
		"session_id":   "canonical-xai-session",
		"thread_id":    "canonical-xai-session",
	})
	if err != nil {
		t.Fatalf("marshal canonical metadata: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, executor := newXAISessionAffinityManager(t, model)
			bodies := [][]byte{
				xaiCanonicalSessionBody(t, model, string(canonicalJSON), "first turn"),
				xaiCanonicalSessionBody(t, model, string(canonicalJSON), "different compacted turn"),
			}
			for index, body := range bodies {
				errExecute := tt.invoke(manager, cliproxyexecutor.Request{Model: model, Payload: body}, cliproxyexecutor.Options{
					Headers:         http.Header{"X-Client-Request-Id": []string{fmt.Sprintf("request-%d", index+1)}},
					OriginalRequest: body,
					SourceFormat:    sdktranslator.FormatOpenAIResponse,
				})
				if errExecute != nil {
					t.Fatalf("request %d error = %v", index, errExecute)
				}
			}

			authIDs, executionSessions := executor.snapshot()
			if len(authIDs) != 2 || authIDs[0] == "" || authIDs[1] != authIDs[0] {
				t.Fatalf("selected auths = %#v, want one stable xAI credential", authIDs)
			}
			if len(executionSessions) != 2 || executionSessions[0] != "canonical-xai-session" || executionSessions[1] != "canonical-xai-session" {
				t.Fatalf("execution sessions = %#v, want canonical xAI session on request and options", executionSessions)
			}
		})
	}
}

func TestPreflightXAIOnlyParsesCodexMetadataCarrier(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{})

	t.Run("ordinary xAI client metadata is left alone", func(t *testing.T) {
		body := []byte(`{"model":"grok-4.6","client_metadata":{"vendor":"first"},"client_metadata":{"vendor":"second"}}`)
		opts, err := manager.preflightCodexClientMetadata([]string{"xai"}, cliproxyexecutor.Request{Payload: body}, cliproxyexecutor.Options{
			Metadata: map[string]any{"marker": "keep"},
		})
		if err != nil {
			t.Fatalf("ordinary xAI metadata was rejected by Codex preflight: %v", err)
		}
		if got := contextStringValue(opts.Metadata["marker"]); got != "keep" {
			t.Fatalf("metadata marker = %q, want keep", got)
		}
		if got := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); got != "" {
			t.Fatalf("execution session = %q, want empty", got)
		}
	})

	t.Run("direct canonical header projects execution session", func(t *testing.T) {
		canonical := `{"request_kind":"turn","session_id":"header-xai-session"}`
		opts, err := manager.preflightCodexClientMetadata([]string{"xai"}, cliproxyexecutor.Request{Payload: []byte(`{"model":"grok-4.6"}`)}, cliproxyexecutor.Options{
			Headers: http.Header{"x-codex-turn-metadata": {canonical}},
		})
		if err != nil {
			t.Fatalf("preflightCodexClientMetadata() error = %v", err)
		}
		if got := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); got != "header-xai-session" {
			t.Fatalf("execution session = %q, want header-xai-session", got)
		}
	})

	t.Run("explicit execution session remains authoritative", func(t *testing.T) {
		canonical := `{"request_kind":"turn","session_id":"canonical-xai-session"}`
		body := xaiCanonicalSessionBody(t, "grok-4.6", canonical, "hello")
		opts, err := manager.preflightCodexClientMetadata([]string{"xai"}, cliproxyexecutor.Request{Payload: body}, cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "caller-session"},
		})
		if err != nil {
			t.Fatalf("preflightCodexClientMetadata() error = %v", err)
		}
		if got := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); got != "caller-session" {
			t.Fatalf("execution session = %q, want caller-session", got)
		}
	})
}

func xaiCanonicalSessionBody(t *testing.T, model, canonical, message string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": model,
		"client_metadata": map[string]any{
			"x-codex-turn-metadata": canonical,
		},
		"input": []map[string]any{{
			"role":    "user",
			"content": message,
		}},
	})
	if err != nil {
		t.Fatalf("marshal xAI request: %v", err)
	}
	return body
}

func TestSessionLogIdentityHashesOpaqueValue(t *testing.T) {
	first := sessionLogIdentity("derived:ctx:v1:first-private-session")
	repeated := sessionLogIdentity("derived:ctx:v1:first-private-session")
	second := sessionLogIdentity("derived:ctx:v1:second-private-session")
	if first == "" || first != repeated || first == second {
		t.Fatalf("session log identities are not stable and distinct: first=%q repeated=%q second=%q", first, repeated, second)
	}
	if !strings.HasPrefix(first, "derived:sha256:") || strings.Contains(first, "private-session") {
		t.Fatalf("session log identity exposes the opaque value: %q", first)
	}
	if got := sessionLogIdentity(""); got != "" {
		t.Fatalf("empty session log identity = %q, want empty", got)
	}
}

func TestSessionAffinityAuthLogIDUsesStableIndex(t *testing.T) {
	auth := &Auth{ID: "xai-private-user@example.invalid.json", Provider: "xai"}
	want := auth.Clone().EnsureIndex()
	got := sessionAffinityAuthLogID(auth)
	if got == "" || got != want {
		t.Fatalf("auth log identity = %q, want stable index %q", got, want)
	}
	if strings.Contains(got, "private-user") || strings.Contains(got, "example.invalid") {
		t.Fatalf("auth log identity exposes auth ID: %q", got)
	}
	if auth.Index != "" {
		t.Fatalf("auth log identity mutated selector candidate index to %q", auth.Index)
	}
}
