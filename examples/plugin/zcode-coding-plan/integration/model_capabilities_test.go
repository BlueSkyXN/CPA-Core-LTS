package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSelectedAccountCapabilitiesIgnorePeerModels(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("alias=%t", alias), func(t *testing.T) {
			if isolateDynamic(t) {
				return
			}
			const upstream = "glm-5.3-flash"
			f := newV2Fixture(t, upstream)
			auth, ok := f.manager.GetByID("synthetic.json")
			if !ok {
				t.Fatal("selected account missing")
			}
			reg := registry.GetGlobalRegistry()
			model := upstream
			if alias {
				model = "public-flash"
				models := reg.GetModelsForClient(auth.ID)
				selected := *models[0]
				selected.ID, selected.Name, selected.MetadataModelID = model, model, upstream
				reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{&selected})
				f.manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{auth.Provider: {{Name: upstream, Alias: model, Fork: false}}})
				f.manager.RefreshSchedulerEntry(auth.ID)
			}
			const peerID = "unrelated-capability-peer"
			t.Cleanup(func() { reg.UnregisterClient(peerID) })
			calls := 0
			check := func() {
				t.Helper()
				for _, stream := range []bool{false, true} {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello","reasoning":{"effort":"max"},"max_output_tokens":64000,"stream":%t}`, model, stream))
					req := coreexecutor.Request{Model: model, Payload: body, Format: tr.FormatOpenAIResponse}
					opts := coreexecutor.Options{
						Stream: stream, SourceFormat: tr.FormatOpenAIResponse, OriginalRequest: body,
						Metadata: map[string]any{
							coreexecutor.RequestIDMetadataKey:  fmt.Sprintf("selected-capabilities-%d", calls),
							coreexecutor.PinnedAuthMetadataKey: auth.ID,
						},
					}
					var err error
					if stream {
						var result *coreexecutor.StreamResult
						result, err = f.manager.ExecuteStream(ctx, []string{auth.Provider}, req, opts)
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					} else {
						_, err = f.manager.Execute(ctx, []string{auth.Provider}, req, opts)
					}
					cancel()
					if err != nil {
						t.Fatalf("selected account request failed, stream=%t: %v", stream, err)
					}
					f.checkUsage(t, false)
					calls++
					f.transport.lock.Lock()
					captured := f.transport.captured
					if len(captured) != calls {
						f.transport.lock.Unlock()
						t.Fatal("unexpected model dispatch count")
					}
					wire := append([]byte(nil), captured[calls-1]...)
					f.transport.lock.Unlock()
					if gjson.GetBytes(wire, "model").String() != upstream || gjson.GetBytes(wire, "reasoning_effort").String() != "max" || gjson.GetBytes(wire, "max_tokens").Int() != 64000 || gjson.GetBytes(wire, "thinking.type").String() != "enabled" || gjson.GetBytes(wire, "thinking.budget_tokens").Exists() {
						t.Fatal("selected account inherited another provider's model capabilities")
					}
				}
			}
			check()
			for _, thinking := range []*registry.ThinkingSupport{{Min: 1024, Max: 32768}, {Levels: []string{"low", "medium", "high"}}} {
				reg.RegisterClient(peerID, "claude", []*registry.ModelInfo{{ID: upstream, Name: upstream, MaxCompletionTokens: 4096, Thinking: thinking}})
				check()
			}
			reg.UnregisterClient(peerID)
			check()
		})
	}
}
