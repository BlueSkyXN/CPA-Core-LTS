package helps

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/kimi"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/xai"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func policyFixture(mode string) *config.Config {
	effort := "max"
	if mode == "follow" {
		effort = ""
	}
	return &config.Config{ReasoningEffort: []config.ReasoningEffortRule{{Models: []string{"policy-*"}, Mode: mode, Effort: effort}}}
}

func TestSemanticEffortPolicyModesAndFinalPayloadAuthority(t *testing.T) {
	info := &registry.ModelInfo{ID: "policy-model", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}
	for _, format := range []string{"claude", "openai", "codex"} {
		for _, mode := range []string{"follow", "default", "force"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				body := []byte(`{"model":"policy-model","messages":[],"max_tokens":32000,"reasoning":{"summary":"auto"},"output_config":{"format":{"type":"json_schema"}}}`)
				var field string
				switch format {
				case "claude":
					body = []byte(`{"model":"policy-model","thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high","format":{"type":"json_schema"}},"max_tokens":32000,"messages":[]}`)
					field = "output_config.effort"
				case "openai":
					body = []byte(`{"model":"policy-model","reasoning_effort":"high","messages":[]}`)
					field = "reasoning_effort"
				default:
					body = []byte(`{"model":"policy-model","reasoning":{"effort":"high","summary":"auto"},"input":"hello"}`)
					field = "reasoning.effort"
				}
				original := bytes.Clone(body)
				req := coreexecutor.Request{Model: "policy-model", Payload: body}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FromString(format)}
				cfg := policyFixture(mode)
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "policy-model"}}, Params: map[string]any{"reasoning.effort": "low", "reasoning_effort": "low", "output_config.effort": "low"}}}
				prepared, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, body, req, opts, format, format, format, info)
				if err != nil {
					t.Fatal(err)
				}
				got := NewPayloadFinalizer(cfg, format, req.Model, format, "", original, req, opts)(prepared)
				want := "high"
				if mode == "force" {
					want = "max"
				}
				if gjson.GetBytes(got, field).String() != want {
					t.Fatalf("effort=%s, want %s; body=%s", gjson.GetBytes(got, field).String(), want, got)
				}
				if !bytes.Equal(body, original) {
					t.Fatal("policy modified caller-owned bytes")
				}
				if format == "claude" && (gjson.GetBytes(got, "thinking.display").String() != "summarized" || gjson.GetBytes(got, "output_config.format.type").String() != "json_schema" || gjson.GetBytes(got, "max_tokens").Int() != 32000) {
					t.Fatal("policy changed independent output controls")
				}
				if format == "codex" && gjson.GetBytes(got, "reasoning.summary").String() != "auto" {
					t.Fatal("policy changed summary intent")
				}
			})
		}
	}
}

func TestSemanticEffortDefaultPresenceAndRuleOrder(t *testing.T) {
	cfg := policyFixture("default")
	for _, body := range []string{`{"reasoning_effort":"low"}`, `{"output_config":{"effort":"high"}}`, `{"thinking":{"type":"disabled"}}`, `{"thinking":{"type":"enabled","budget_tokens":1024}}`, `{"reasoning":{"effort":"auto"}}`, `{"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}}]}`} {
		if _, apply := RequestEffortOverride(cfg, coreexecutor.Request{Model: "policy-model", Payload: []byte(body)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); apply {
			t.Fatalf("default replaced explicit controls: %s", body)
		}
	}
	if _, apply := RequestEffortOverride(cfg, coreexecutor.Request{Model: "policy-model", Payload: []byte(`{"messages":[]}`)}, coreexecutor.Options{}); !apply {
		t.Fatal("default did not fill a missing effort")
	}
	if _, apply := RequestEffortOverride(cfg, coreexecutor.Request{Model: "policy-model(low)"}, coreexecutor.Options{}); apply {
		t.Fatal("default replaced a model suffix")
	}
	cfg.ReasoningEffort = append(cfg.ReasoningEffort, config.ReasoningEffortRule{Models: []string{"policy-model"}, Mode: "follow"})
	if _, apply := RequestEffortOverride(cfg, coreexecutor.Request{Model: "policy-model"}, coreexecutor.Options{}); apply {
		t.Fatal("last matching follow rule did not override the wildcard rule")
	}
}

func TestSemanticEffortForceSuffixAndModelCapabilities(t *testing.T) {
	cfg := policyFixture("force")
	req := coreexecutor.Request{Model: "policy-model(low)", Payload: []byte(`{"reasoning_effort":"high","messages":[]}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	info := &registry.ModelInfo{ID: "policy-model", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}
	got, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, req.Payload, req, opts, "openai", "openai", "openai", info)
	if err != nil || gjson.GetBytes(got, "reasoning_effort").String() != "max" || req.Model != "policy-model(low)" {
		t.Fatalf("force/suffix: %s %v", got, err)
	}
	info.Thinking.Levels = []string{"low", "high"}
	if _, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, req.Payload, req, opts, "openai", "openai", "openai", info); err == nil {
		t.Fatal("unsupported forced level silently downgraded")
	}
}

func TestSemanticEffortCurrentControlsPreserveHistory(t *testing.T) {
	info := &registry.ModelInfo{ID: "policy-model", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}, SupportConfigurationUpdate: true}
	for _, format := range []string{"codex", "claude"} {
		t.Run(format, func(t *testing.T) {
			body := []byte(`{"reasoning":{"effort":"low","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"new turn"}]}`)
			array, field := "input", "reasoning.effort"
			if format == "claude" {
				body = []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"system","content":[],"output_config":{"effort":"low"}},{"role":"user","content":"new turn"}]}`)
				array, field = "messages", "output_config.effort"
			}
			before := gjson.GetBytes(body, array).Array()
			req := coreexecutor.Request{Model: "policy-model", Payload: body}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FromString(format)}
			got, err := ApplyConfiguredRequestThinkingWithModelInfo(policyFixture("force"), body, req, opts, format, format, format, info)
			if err != nil {
				t.Fatal(err)
			}
			after := gjson.GetBytes(got, array).Array()
			if len(after) != len(before)+1 || after[0].Raw != before[0].Raw || after[1].Raw != before[1].Raw || after[len(after)-1].Get(field).String() != "max" {
				t.Fatalf("current policy rewrote history or lost override: %s", got)
			}
			if effort := thinking.ExtractTranslatedReasoningEffort(got, format); effort != "max" {
				t.Fatalf("usage effort=%q", effort)
			}
		})
	}
}

func TestSemanticEffortNativeProtocols(t *testing.T) {
	for _, tc := range []struct{ format, path string }{
		{"openai", "reasoning_effort"}, {"codex", "reasoning.effort"}, {"xai", "reasoning.effort"},
		{"claude", "output_config.effort"}, {"kimi", "thinking.effort"},
		{"gemini", "generationConfig.thinkingConfig.thinkingLevel"},
		{"antigravity", "request.generationConfig.thinkingConfig.thinkingLevel"},
		{"interactions", "generation_config.thinking_level"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			info := &registry.ModelInfo{ID: "policy-model", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}}
			cfg := policyFixture("force")
			cfg.ReasoningEffort[0].Effort = "high"
			req := coreexecutor.Request{Model: "policy-model", Payload: []byte(`{"messages":[],"max_tokens":32000,"reasoning":{"effort":"low"}}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			got, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, req.Payload, req, opts, "openai", tc.format, tc.format, info)
			if err != nil || gjson.GetBytes(got, tc.path).String() != "high" {
				t.Fatalf("native effort: %s %v", got, err)
			}
		})
	}
}

func TestSemanticEffortBudgetAndUnsupportedModels(t *testing.T) {
	cfg := policyFixture("force")
	cfg.ReasoningEffort[0].Effort = "medium"
	req := coreexecutor.Request{Model: "policy-model", Payload: []byte(`{"max_tokens":32000,"messages":[]}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	info := &registry.ModelInfo{ID: "policy-model", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32768}}
	for _, tc := range []struct{ format, path string }{{"claude", "thinking.budget_tokens"}, {"gemini", "generationConfig.thinkingConfig.thinkingBudget"}} {
		got, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, req.Payload, req, opts, "claude", tc.format, tc.format, info)
		if err != nil || gjson.GetBytes(got, tc.path).Int() != 8192 {
			t.Fatalf("budget %s: %s %v", tc.format, got, err)
		}
	}
	info.Thinking = nil
	if _, err := ApplyConfiguredRequestThinkingWithModelInfo(cfg, req.Payload, req, opts, "claude", "claude", "claude", info); err == nil {
		t.Fatal("non-reasoning model accepted force")
	}
	if _, err := ApplyConfiguredRequestThinking(cfg, req.Payload, req, opts, "claude", "unknown-protocol", "unknown-provider"); err == nil {
		t.Fatal("unknown protocol ignored force")
	}
}

func TestSemanticEffortAliasSelectionAndNoPolicy(t *testing.T) {
	req := coreexecutor.Request{Model: "upstream-model", Payload: []byte(`{"reasoning_effort":"high"}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "policy-alias(low)"}}
	if _, matched := RequestEffortPolicy(policyFixture("force"), req, opts); !matched {
		t.Fatal("client alias did not match")
	}
	if _, active := RequestEffortOverride(policyFixture("default"), req, opts); active {
		t.Fatal("default replaced alias suffix")
	}
	for _, cfg := range []*config.Config{nil, {}, {ReasoningEffort: []config.ReasoningEffortRule{{Models: []string{"other"}, Mode: "force", Effort: "max"}}}} {
		got, err := ApplyConfiguredRequestThinking(cfg, req.Payload, req, opts, "openai", "openai", "openai")
		want, wantErr := ApplyRequestThinking(req.Payload, req, opts, "openai", "openai", "openai")
		if fmt.Sprint(err) != fmt.Sprint(wantErr) || !bytes.Equal(got, want) {
			t.Fatal("unmatched policy changed existing pipeline")
		}
	}
	if _, active := RequestEffortOverride(policyFixture("default"), coreexecutor.Request{Model: "policy-model", Payload: []byte(`{"thinking":{"type":"disabled"}}`)}, coreexecutor.Options{}); active {
		t.Fatal("default replaced explicit off without format")
	}
}

func TestSemanticEffortProtectsDynamicControls(t *testing.T) {
	for _, format := range []string{"codex", "claude"} {
		array, field := "input", "reasoning.effort"
		before := []byte(`{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hello"}]}`)
		if format == "claude" {
			array, field = "messages", "output_config.effort"
			before = []byte(`{"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hello"}]}`)
		}
		for _, mode := range []string{"follow", "default", "force"} {
			cfg := policyFixture(mode)
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "policy-model"}}, Params: map[string]any{array + ".0." + field: "low", array + ".1.content": "rewritten", "max_tokens": 24000}}}
			req := coreexecutor.Request{Model: "policy-model", Payload: before}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FromString(format)}
			got := NewPayloadFinalizer(cfg, format, req.Model, format, "", before, req, opts)(before)
			if gjson.GetBytes(got, array+".0."+field).String() != "high" || gjson.GetBytes(got, array+".1.content").String() != "rewritten" || gjson.GetBytes(got, "max_tokens").Int() != 24000 {
				t.Fatalf("%s/%s lost ownership or unrelated edits: %s", format, mode, got)
			}
			cfg.Payload.Override[0].Params = map[string]any{array: []any{map[string]any{"role": "user", "content": "replacement"}}, "max_tokens": 24000}
			got = NewPayloadFinalizer(cfg, format, req.Model, format, "", before, req, opts)(before)
			if gjson.GetBytes(got, array).Raw != gjson.GetBytes(before, array).Raw {
				t.Fatalf("dynamic history replaced: %s", got)
			}
		}
	}
}

func TestSemanticEffortSteeringPreservesFrameShape(t *testing.T) {
	cfg := policyFixture("force")
	// A known Responses model supports configuration_update. Keep policy selection
	// on the client alias while routing and validation use the actual model.
	for _, body := range []string{
		`{"type":"response.steer","previous_response_id":"r1","input":"hello"}`,
		`{"type":"response.steer","previous_response_id":"r1","input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
	} {
		req := coreexecutor.Request{Model: "gpt-6-astra", Payload: []byte(body)}
		opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatCodex, Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "policy-model"}}
		got, err := ApplyConfiguredSteerThinking(cfg, req.Payload, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(got, "reasoning").Exists() {
			t.Fatalf("steering gained top-level settings: %s", got)
		}
		if gjson.GetBytes(req.Payload, "input").IsArray() {
			if gjson.GetBytes(got, "input.0.reasoning.effort").String() != "max" {
				t.Fatalf("control not forced: %s", got)
			}
		} else if !bytes.Equal(got, req.Payload) {
			t.Fatalf("ordinary steering changed: %s", got)
		}
	}
}
