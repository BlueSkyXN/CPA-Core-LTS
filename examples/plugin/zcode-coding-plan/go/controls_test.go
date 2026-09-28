package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRequiresHostFeatures(t *testing.T) {
	features := []string{"anthropic-plugin-responses-v1", "plugin-model-compat-v1", "http-disable-redirects-v1", "sensitive-endpoints-v1", "plugin-management-v1"}
	for _, method := range []string{"plugin.register", "plugin.reconfigure"} {
		for missing := -1; missing < len(features); missing++ {
			r := newRuntime(nil)
			var selected []string
			for i, feature := range features {
				if missing != i {
					selected = append(selected, feature)
				}
			}
			_, err := r.dispatch(method, encode(map[string]any{"schema_version": 6, "host_features": selected, "config_json": []byte(`{}`)}))
			if (err == nil) != (missing == -1) {
				t.Fatalf("method=%s missing=%d error=%v", method, missing, err)
			}
		}
	}
}

func TestCrossProtocolPromptControls(t *testing.T) {
	cfg := &config{Models: []string{"GLM-5.3-Flash"}, DeviceID: "synthetic", Prompt: promptConfig{Mode: "preserve", AllowRequestOverride: true, Templates: map[string]string{"t": "template"}}}
	payload := []byte(`{"model":"GLM-5.3-Flash","max_tokens":100,"system":"caller","messages":[{"role":"user","content":"hello"}]}`)
	for _, mode := range []string{"preserve", "replace", "prepend", "append", "move_to_user"} {
		t.Run(mode, func(t *testing.T) {
			req := executorRequest{Payload: payload, Headers: http.Header{"X-Coding-Plan-Prompt": {string(encode(map[string]string{"mode": mode, "template": "t"}))}}}
			body, err := transformExecution(req, cfg, "session")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := body["x_coding_plan"]; ok {
				t.Fatal("control leaked")
			}
			if mode == "replace" {
				blocks, ok := body["system"].([]any)
				if !ok || len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "template" {
					t.Fatal("header prompt did not apply")
				}
			}
		})
	}
	cfg.Prompt.AllowRequestOverride = false
	if _, err := transformExecution(executorRequest{Payload: payload, Headers: http.Header{"X-Coding-Plan-Prompt": {`{"mode":"replace","template":"t"}`}}}, cfg, "s"); err == nil {
		t.Fatal("override restriction bypassed")
	}
}

func TestExecutionRejectsLostControlsAndModelLimits(t *testing.T) {
	cfg := &config{Models: []string{"GLM-5.3-Flash"}, Prompt: promptConfig{Mode: "preserve"}}
	base := map[string]any{"model": "GLM-5.3-Flash", "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	for _, original := range []string{`{"x_coding_plan":{"prompt":{"mode":"replace"}}}`, `{"reasoning":{"effort":"high"}}`, `{"text":{"format":{"type":"json_schema","strict":true}}}`, `{"previous_response_id":"old"}`, `{"store":true}`} {
		if _, err := transformExecution(executorRequest{Payload: encode(base), OriginalRequest: []byte(original)}, cfg, "s"); err == nil {
			t.Fatalf("unsupported original control accepted: %s", original)
		}
	}
	base["max_tokens"] = 128001
	if _, err := transformExecution(executorRequest{Payload: encode(base)}, cfg, "s"); err == nil {
		t.Fatal("output limit ignored")
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(envelope(nil, problem(502, "truncated_stream", "synthetic")), &env)
	if env.Error.Code != "request_scoped" {
		t.Fatal("local protocol error can retry credentials")
	}
}
