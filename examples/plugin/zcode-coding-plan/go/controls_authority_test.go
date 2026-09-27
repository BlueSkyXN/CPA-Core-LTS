package main

import (
	"net/http"
	"testing"
)

func TestEffectivePromptAndPayloadAuthority(t *testing.T) {
	c := &config{Models: []string{"m"}, Prompt: promptConfig{Mode: "preserve", AllowRequestOverride: true, Templates: map[string]string{"t": "template"}}}
	base := []byte(`{"model":"m","max_tokens":10,"system":"effective","messages":[{"role":"user","content":"hello"}]}`)
	req := executorRequest{Payload: base, OriginalRequest: []byte(`{"system":"original","thinking":{"type":"enabled","budget_tokens":100}}`)}
	body, err := transformExecution(req, c, "s")
	if err != nil {
		t.Fatal(err)
	}
	if body["system"] != "effective" || body["thinking"] != nil {
		t.Fatal("removed controls restored from original")
	}
	for _, header := range []string{`{"path":"/not-allowed"}`, `{"template":"https://example.invalid"}`, `{}`, `null`} {
		req.Headers = http.Header{promptHeader: {header}}
		if _, err = transformExecution(req, c, "s"); err == nil {
			t.Fatal("invalid prompt selection accepted")
		}
	}
	req.Headers = http.Header{promptHeader: {`{"mode":"replace","template":"t"}`, `{"mode":"preserve"}`}}
	if _, err = transformExecution(req, c, "s"); err == nil {
		t.Fatal("duplicate header accepted")
	}
	req.Headers = http.Header{promptHeader: {`{"mode":"replace","template":"t"}`}}
	req.Payload = []byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hello"}],"x_coding_plan":{"prompt":{"mode":"preserve"}}}`)
	if _, err = transformExecution(req, c, "s"); err == nil {
		t.Fatal("dual prompt sources accepted")
	}
	req.Payload = base
	req.Headers = nil
	body, err = transformExecution(req, c, "s")
	if err != nil || body["system"] != "effective" {
		t.Fatal("removed header affected prompt")
	}
}
