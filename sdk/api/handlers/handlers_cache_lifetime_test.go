package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type cacheLifetimeBootstrapExecutor struct{ bootstrapStreamExecutor }

func (*cacheLifetimeBootstrapExecutor) Identifier() string { return "codex" }

func TestCodexCacheLifetimeSpansHandlerBootstrapRetries(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			seen := make(chan (<-chan struct{}), 4)
			finish := make(chan struct{})
			e := &cacheLifetimeBootstrapExecutor{bootstrapStreamExecutor: bootstrapStreamExecutor{stream: func(ctx context.Context, call int) (*coreexecutor.StreamResult, error) {
				seen <- util.LogicalRequestDone(ctx)
				chunks := make(chan coreexecutor.StreamChunk, 1)
				if call == 1 {
					chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{Code: "unauthorized", Message: "synthetic", HTTPStatus: http.StatusUnauthorized}}
					close(chunks)
				} else {
					chunks <- coreexecutor.StreamChunk{Payload: []byte("ok")}
					go func() {
						defer close(chunks)
						select {
						case <-ctx.Done():
						case <-finish:
						}
					}()
				}
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}}}
			m := coreauth.NewManager(nil, nil, nil)
			m.RegisterExecutor(e)
			for _, id := range []string{"cache-lifetime-a", "cache-lifetime-b"} {
				if _, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "cache-lifetime-model"}})
				defer registry.GetGlobalRegistry().UnregisterClient(id)
			}
			h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}}, m)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai", "cache-lifetime-model", []byte(`{"model":"cache-lifetime-model"}`), "")
			if e.Calls() != 2 {
				t.Fatalf("expected bootstrap retry, got %d calls", e.Calls())
			}
			first, second := <-seen, <-seen
			if first == nil || first != second {
				t.Fatal("bootstrap retry did not inherit outer logical lifetime")
			}
			select {
			case <-first:
				t.Fatal("request ended at inner Manager boundary")
			default:
			}
			if cancelRequest {
				cancel()
			} else {
				close(finish)
			}
			for range data {
			}
			for msg := range errs {
				if msg != nil && !cancelRequest {
					t.Fatalf("unexpected stream error: %v", msg)
				}
			}
			select {
			case <-first:
			case <-time.After(time.Second):
				t.Fatal("handler completion did not release logical lifetime")
			}
		})
	}
}
