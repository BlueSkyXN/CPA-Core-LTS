package main

import (
	"fmt"
	"strings"
	"testing"
)

const imageFixture = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="

func TestThinkingEffortReachesProvider(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "GLM-5.3-Flash"} {
		for _, effort := range builtinThinkingLevels {
			for _, field := range []string{"reasoning_effort", "output_config", "reasoning"} {
				t.Run(model+"/"+effort+"/"+field, func(t *testing.T) {
					body := map[string]any{"model": model, "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
					if field == "reasoning_effort" {
						body[field] = effort
					} else {
						body[field] = map[string]any{"effort": effort}
						body["thinking"] = map[string]any{"type": "adaptive"}
					}
					cfg := defaultConfig()
					cfg.Models = []string{model}
					result, err := transformExecution(executorRequest{Payload: encode(body), OriginalRequest: []byte(fmt.Sprintf(`{"reasoning":{"effort":%q}}`, effort))}, cfg, "session")
					if err != nil {
						t.Fatal(err)
					}
					if result["reasoning_effort"] != effort || result["thinking"].(map[string]any)["type"] != "enabled" || result["output_config"] != nil || result["reasoning"] != nil {
						t.Fatal("effort was not normalized to the GLM provider fields")
					}
				})
			}
		}
	}
}

func TestThinkingRejectsUnsupportedControls(t *testing.T) {
	for _, extra := range []string{
		`"reasoning_effort":"none"`, `"reasoning_effort":"medium"`, `"reasoning_effort":42`,
		`"thinking":{"type":"disabled"}`, `"thinking":{"type":"enabled","budget_tokens":100}`,
		`"thinking":{"type":"enabled","display":"omitted"}`,
		`"thinking":{"type":"enabled","display":"raw"}`,
		`"thinking":{"type":"enabled","display":null}`,
		`"thinking":{"type":"enabled","display":42}`,
		`"thinking":{"type":"enabled","display":true}`,
		`"output_config":{"effort":"high","format":{"type":"json_schema"}}`,
		`"reasoning_effort":"low","output_config":{"effort":"high"}`,
	} {
		body := []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"hello"}],` + extra + `}`)
		if _, err := transformExecution(executorRequest{Payload: body}, defaultConfig(), "session"); err == nil {
			t.Fatalf("unsupported thinking control accepted: %s", extra)
		}
	}
	body := []byte(`{"model":"glm-5.3","max_tokens":100,"reasoning_effort":"low","messages":[{"role":"user","content":"hello"}]}`)
	result, err := transformExecution(executorRequest{Payload: body, OriginalRequest: []byte(`{"reasoning":{"effort":"max"}}`)}, defaultConfig(), "session")
	if err != nil || result["reasoning_effort"] != "low" {
		t.Fatal("original request overrode the effective Core payload", err)
	}
	summaryBody := []byte(`{"model":"glm-5.3","max_tokens":100,"reasoning_effort":"low","thinking":{"type":"enabled","display":"omitted"},"messages":[{"role":"user","content":"hello"}]}`)
	_, err = transformExecution(executorRequest{Payload: summaryBody}, defaultConfig(), "session")
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatal("summary display rejection must point at the reasoning summary control", err)
	}
}

func TestFlashImageInputValidation(t *testing.T) {
	sources := []map[string]any{
		{"type": "base64", "media_type": "image/png", "data": imageFixture},
		{"type": "url", "url": "https://images.example.invalid/synthetic.png"},
	}
	for _, model := range builtinModels {
		for _, source := range sources {
			body := map[string]any{"model": model, "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": source}}}}}
			result, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
			if model == "glm-5.3" {
				if err == nil {
					t.Fatal("text-only model accepted an image")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			part := result["messages"].([]any)[0].(map[string]any)["content"].([]any)[0]
			if string(encode(part)) != string(encode(map[string]any{"type": "image", "source": source})) {
				t.Fatal("image payload changed")
			}
		}
	}
	for _, source := range []any{nil, map[string]any{"type": "base64", "media_type": "image/png", "data": "!invalid"}, map[string]any{"type": "base64", "media_type": "image/svg+xml", "data": imageFixture}, map[string]any{"type": "url", "url": "file:///synthetic.png"}, map[string]any{"type": "url", "url": "https://user:pass@example.invalid/image.png"}} {
		if err := validateImage(map[string]any{"source": source}, "glm-5.3-flash"); err == nil {
			t.Fatal("invalid image source accepted")
		}
	}
}

func TestCustomModelDoesNotAdvertiseUnverifiedThinking(t *testing.T) {
	c := defaultConfig()
	c.Models = []string{"custom-model"}
	m := modelList(c)[0].(map[string]any)
	if m["Thinking"] != nil || len(m["SupportedInputModalities"].([]string)) != 1 {
		t.Fatal("custom model inherited built-in-only capabilities")
	}
}

func TestThinkingSummaryDisplay(t *testing.T) {
	for _, model := range builtinModels {
		for _, effort := range append([]string{""}, builtinThinkingLevels...) {
			for _, clear := range []bool{false, true} {
				body := map[string]any{"model": model, "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "thinking": map[string]any{"type": "adaptive", "display": "summarized", "clear_thinking": clear}}
				if effort != "" {
					body["output_config"] = map[string]any{"effort": effort}
				}
				result, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
				if err != nil {
					t.Fatal(err)
				}
				think := result["thinking"].(map[string]any)
				if len(think) != 2 || think["type"] != "enabled" || think["clear_thinking"] != clear {
					t.Fatal("display leaked or effective thinking changed")
				}
				if effort != "" && result["reasoning_effort"] != effort {
					t.Fatal("effort lost")
				}
			}
		}
	}
	for _, original := range []string{`{"reasoning":{"summary":"auto"}}`, `{"reasoning":{"summary":"none"}}`, `{"thinking":{"type":"enabled","display":"omitted"}}`} {
		body := []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)
		result, err := transformExecution(executorRequest{Payload: body, OriginalRequest: []byte(original)}, defaultConfig(), "session")
		if err != nil || result["thinking"] != nil {
			t.Fatal("original display restored removed controls", err)
		}
	}
}
