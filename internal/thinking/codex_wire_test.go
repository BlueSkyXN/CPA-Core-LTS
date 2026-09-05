package thinking_test

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestCanonicalCodexWireEffortIsModelAware(t *testing.T) {
	ultraModel := &registry.ModelInfo{
		ID: "gpt-5.6-sol",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max", "ultra"},
		},
	}
	maxOnlyModel := &registry.ModelInfo{
		ID: "gpt-5.6-luna",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	}
	userDefinedModel := &registry.ModelInfo{
		ID:          "custom-codex-model",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"ultra"},
		},
	}

	tests := []struct {
		name      string
		effort    string
		modelInfo *registry.ModelInfo
		want      string
	}{
		{name: "known ultra model", effort: "ultra", modelInfo: ultraModel, want: "max"},
		{name: "known ultra model case insensitive", effort: " ULTRA ", modelInfo: ultraModel, want: "max"},
		{name: "known max-only model preserves unsupported value for validator", effort: "ultra", modelInfo: maxOnlyModel, want: "ultra"},
		{name: "unknown model passthrough", effort: "ultra", modelInfo: nil, want: "ultra"},
		{name: "user-defined model passthrough", effort: "ultra", modelInfo: userDefinedModel, want: "ultra"},
		{name: "max remains unchanged", effort: "max", modelInfo: ultraModel, want: "max"},
		{name: "non-ultra casing remains unchanged", effort: " HIGH ", modelInfo: ultraModel, want: " HIGH "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := thinking.CanonicalCodexWireEffort(tt.effort, tt.modelInfo); got != tt.want {
				t.Fatalf("CanonicalCodexWireEffort(%q) = %q, want %q", tt.effort, got, tt.want)
			}
		})
	}
}

func TestNormalizeCodexReasoningEffortForWire(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       string
		wantEffort string
		wantErr    thinking.ErrorCode
	}{
		{name: "astra ultra becomes max", model: "gpt-6-astra", body: `{"reasoning":{"effort":"ultra"}}`, wantEffort: "max"},
		{name: "astra max remains max", model: "gpt-6-astra", body: `{"reasoning":{"effort":"max"}}`, wantEffort: "max"},
		{name: "sol ultra becomes max", model: "gpt-5.6-sol", body: `{"reasoning":{"effort":"ultra"}}`, wantEffort: "max"},
		{name: "terra ultra becomes max", model: "gpt-5.6-terra", body: `{"reasoning":{"effort":"ULTRA"}}`, wantEffort: "max"},
		{name: "luna ultra remains unsupported", model: "gpt-5.6-luna", body: `{"reasoning":{"effort":"ultra"}}`, wantEffort: "ultra", wantErr: thinking.ErrLevelNotSupported},
		{name: "sol max remains max", model: "gpt-5.6-sol", body: `{"reasoning":{"effort":"max"}}`, wantEffort: "max"},
		{name: "terra high remains byte-compatible", model: "gpt-5.6-terra", body: `{"reasoning":{"effort":" HIGH "}}`, wantEffort: " HIGH "},
		{name: "unknown custom model passthrough", model: "custom-codex-ultra", body: `{"reasoning":{"effort":"ultra"}}`, wantEffort: "ultra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.NormalizeCodexReasoningEffortForWire([]byte(tt.body), tt.model)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeCodexReasoningEffortForWire() error = nil, want %s", tt.wantErr)
				}
				var thinkingErr *thinking.ThinkingError
				if !errors.As(err, &thinkingErr) || thinkingErr.Code != tt.wantErr {
					t.Fatalf("error = %T %v, want code %s", err, err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("NormalizeCodexReasoningEffortForWire() error = %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != tt.wantEffort {
				t.Fatalf("reasoning.effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
		})
	}
}

func TestNormalizeCodexReasoningEffortForWirePreservesUserDefinedGPT56Ultra(t *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	clientID := "test-user-defined-gpt56-ultra-passthrough"
	registryRef.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:          "gpt-5.6-sol",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	}})
	t.Cleanup(func() { registryRef.UnregisterClient(clientID) })

	body := []byte(`{"reasoning":{"effort":"ultra"}}`)
	out, err := thinking.NormalizeCodexReasoningEffortForWire(body, "gpt-5.6-sol")
	if err != nil {
		t.Fatalf("NormalizeCodexReasoningEffortForWire() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "ultra" {
		t.Fatalf("reasoning.effort = %q, want literal ultra for user-defined model; body=%s", got, out)
	}
}
