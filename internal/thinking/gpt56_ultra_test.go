package thinking_test

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestParseLevelSuffixUltra(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		level thinking.ThinkingLevel
	}{
		{name: "lowercase ultra", raw: "ultra", level: thinking.LevelUltra},
		{name: "case insensitive ultra", raw: "ULTRA", level: thinking.LevelUltra},
		{name: "max remains supported", raw: "max", level: thinking.LevelMax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, ok := thinking.ParseLevelSuffix(tt.raw)
			if !ok {
				t.Fatalf("ParseLevelSuffix(%q) returned ok=false", tt.raw)
			}
			if level != tt.level {
				t.Fatalf("ParseLevelSuffix(%q) = %q, want %q", tt.raw, level, tt.level)
			}
		})
	}
}

func TestGPT56UltraCodexThinking(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       string
		wantEffort string
		wantErr    thinking.ErrorCode
	}{
		{
			name:       "sol body",
			model:      "gpt-5.6-sol",
			body:       `{"model":"gpt-5.6-sol","reasoning":{"effort":"ultra"}}`,
			wantEffort: "ultra",
		},
		{
			name:       "sol suffix overrides body",
			model:      "gpt-5.6-sol(ultra)",
			body:       `{"model":"gpt-5.6-sol","reasoning":{"effort":"low"}}`,
			wantEffort: "ultra",
		},
		{
			name:       "terra body",
			model:      "gpt-5.6-terra",
			body:       `{"model":"gpt-5.6-terra","reasoning":{"effort":"ultra"}}`,
			wantEffort: "ultra",
		},
		{
			name:       "terra suffix overrides body",
			model:      "gpt-5.6-terra(ultra)",
			body:       `{"model":"gpt-5.6-terra","reasoning":{"effort":"low"}}`,
			wantEffort: "ultra",
		},
		{
			name:       "luna keeps max support",
			model:      "gpt-5.6-luna(max)",
			body:       `{"model":"gpt-5.6-luna","reasoning":{"effort":"low"}}`,
			wantEffort: "max",
		},
		{
			name:    "luna rejects ultra body",
			model:   "gpt-5.6-luna",
			body:    `{"model":"gpt-5.6-luna","reasoning":{"effort":"ultra"}}`,
			wantErr: thinking.ErrLevelNotSupported,
		},
		{
			name:    "luna rejects ultra suffix",
			model:   "gpt-5.6-luna(ultra)",
			body:    `{"model":"gpt-5.6-luna","reasoning":{"effort":"low"}}`,
			wantErr: thinking.ErrLevelNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "codex", "codex", "codex")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ApplyThinking() error = nil, want code %s; body=%s", tt.wantErr, out)
				}
				var thinkingErr *thinking.ThinkingError
				if !errors.As(err, &thinkingErr) {
					t.Fatalf("ApplyThinking() error type = %T, want *thinking.ThinkingError", err)
				}
				if thinkingErr.Code != tt.wantErr {
					t.Fatalf("ApplyThinking() error code = %s, want %s", thinkingErr.Code, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyThinking() unexpected error: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != tt.wantEffort {
				t.Fatalf("reasoning.effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
		})
	}
}

func TestUltraClampsToMaxWhenCrossProviderTargetStopsAtMax(t *testing.T) {
	model := &registry.ModelInfo{
		ID:   "max-only-model",
		Type: "openai",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	}

	got, err := thinking.ValidateConfig(
		thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelUltra},
		model,
		"claude",
		"codex",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() unexpected error: %v", err)
	}
	if got.Level != thinking.LevelMax {
		t.Fatalf("ValidateConfig() level = %q, want %q", got.Level, thinking.LevelMax)
	}
}
