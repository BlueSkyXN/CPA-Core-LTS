package main

import (
	"encoding/json"
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

func TestExecutionRejectsLostControlsAndModelLimits(t *testing.T) {
	cfg := &config{Models: []string{"GLM-5.3-Flash"}}
	base := map[string]any{"model": "GLM-5.3-Flash", "max_tokens": 100, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	for _, original := range []string{`{"x_coding_plan":{"prompt":{"mode":"replace"}}}`, `{"reasoning":{"effort":"high"}}`, `{"text":{"format":{"type":"json_schema","strict":true}}}`, `{"previous_response_id":"old"}`, `{"store":true}`} {
		if _, err := transformExecution(executorRequest{Payload: encode(base), OriginalRequest: []byte(original)}, cfg, "s"); err == nil {
			t.Fatalf("unsupported original control accepted: %s", original)
		}
	}
	base["x_coding_plan"] = map[string]any{"prompt": map[string]any{"mode": "preserve"}}
	if _, err := transformExecution(executorRequest{Payload: encode(base)}, cfg, "s"); err == nil {
		t.Fatal("in-body prompt override accepted")
	}
	delete(base, "x_coding_plan")
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
