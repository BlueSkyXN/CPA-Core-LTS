package executor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func codexScopedFlexConfig() *config.Config {
	return &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-5.5", Scope: "upstream", Protocol: "codex", AuthKind: "apikey", RequestPaths: []string{"/v1/responses", "/backend-api/codex/responses"}}},
		Params: map[string]any{"service_tier": "flex"},
	}}}}
}

func TestCodexPayloadExecutionScopeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, path, alt, model string
		oauth, stream          bool
		want                   string
	}{
		{"api key", "/v1/responses", "", "gpt-5.5", false, false, "flex"},
		{"api key stream", "/v1/responses", "", "gpt-5.5", false, true, "flex"},
		{"native route", "/backend-api/codex/responses", "", "gpt-5.5", false, false, "flex"},
		{"subscription", "/v1/responses", "", "gpt-5.5", true, false, "priority"},
		{"subscription stream", "/v1/responses", "", "gpt-5.5", true, true, "priority"},
		{"compact", "/v1/responses/compact", "responses/compact", "gpt-5.5", false, false, "priority"},
		{"different model", "/v1/responses", "", "gpt-5.4", false, false, "priority"},
		{"missing path", "", "", "gpt-5.5", false, false, "priority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured capturedCodexRequest
			server := newCodexRoutingHintServer(t, &captured)
			defer server.Close()
			auth := codexAPIKeyTestAuth(server.URL)
			if tc.oauth {
				auth = codexOAuthTestAuth(server.URL)
			}
			payload := []byte(`{"model":"client-alias","service_tier":"priority","input":"synthetic"}`)
			before := bytes.Clone(payload)
			req := core.Request{Model: tc.model, Payload: payload}
			opts := core.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: tc.alt, Metadata: map[string]any{core.RequestPathMetadataKey: tc.path, core.RequestedModelMetadataKey: "client-alias"}}
			exec := NewCodexExecutor(codexScopedFlexConfig())
			if tc.stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if captured.serviceTier.String() != tc.want {
				t.Fatalf("outbound tier=%q want=%q", captured.serviceTier.String(), tc.want)
			}
			if tc.oauth && captured.routingHint != "model="+tc.model+";tier=priority" {
				t.Fatalf("subscription hint=%q", captured.routingHint)
			}
			if !bytes.Equal(before, payload) {
				t.Fatal("executor mutated original payload")
			}
		})
	}
}

func TestCodexPayloadExecutionScopeWebsocket(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("oauth=%v/stream=%v", oauth, stream), func(t *testing.T) {
				var recorder codexWebsocketHintRecorder
				server := newCodexWebsocketHintServer(t, &recorder)
				defer server.Close()
				auth := codexAPIKeyTestAuth(server.URL)
				if oauth {
					auth = codexOAuthTestAuth(server.URL)
				}
				auth.ID = t.Name()
				exec := NewCodexWebsocketsExecutor(codexScopedFlexConfig())
				req := core.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","service_tier":"priority","input":"synthetic"}`)}
				opts := core.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{core.RequestPathMetadataKey: "/v1/responses"}}
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
				want := "flex"
				if oauth {
					want = "priority"
				}
				recorder.mu.Lock()
				defer recorder.mu.Unlock()
				if len(recorder.frames) != 1 || recorder.frames[0].tier != want {
					t.Fatalf("frames=%+v want tier=%s", recorder.frames, want)
				}
			})
		}
	}
}

func TestCodexPayloadExecutionScopeCredentialRetry(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var mu sync.Mutex
			var tiers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				tiers = append(tiers, ojson.GetBytes(body, "service_tier").String())
				attempt := len(tiers)
				mu.Unlock()
				if attempt == 1 {
					w.WriteHeader(500)
					_, _ = io.WriteString(w, `{"error":{"message":"synthetic retry"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"scope-retry","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
			}))
			defer server.Close()
			cfg := codexScopedFlexConfig()
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(1, 0, 0)
			manager.RegisterExecutor(NewCodexExecutor(cfg))
			for i, auth := range []*cliproxyauth.Auth{codexAPIKeyTestAuth(server.URL), codexOAuthTestAuth(server.URL)} {
				auth.ID = fmt.Sprintf("%s-%d", t.Name(), i)
				auth.Status = cliproxyauth.StatusActive
				auth.Attributes["priority"] = fmt.Sprint(2 - i)
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			req := core.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","service_tier":"priority","input":"synthetic"}`)}
			opts := core.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{core.RequestPathMetadataKey: "/v1/responses"}}
			if stream {
				result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(tiers) != 2 || tiers[0] != "flex" || tiers[1] != "priority" {
				t.Fatalf("attempt tiers=%v, want [flex priority]", tiers)
			}
		})
	}
}

func TestCodexPayloadExecutionScopeImages(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "gpt-image-1"} {
		t.Run(model, func(t *testing.T) {
			var tier string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				tier = ojson.GetBytes(body, "service_tier").String()
				if r.URL.Path == "/responses" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"image-scope","status":"completed","output":[{"id":"img","type":"image_generation_call","status":"completed","result":"AA=="}]}}`+"\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"AA=="}]}`)
				}
			}))
			defer server.Close()
			cfg := codexScopedFlexConfig()
			cfg.Payload.Override[0].Models[0].Name = "*"
			cfg.Payload.Override[0].Models[0].Protocol = ""
			exec := NewCodexExecutor(cfg)
			req := core.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"prompt":"synthetic"}`, model))}
			_, err := exec.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), req, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
			if err != nil {
				t.Fatal(err)
			}
			if tier != "" {
				t.Fatalf("image request received service_tier=%q", tier)
			}
		})
	}
}
