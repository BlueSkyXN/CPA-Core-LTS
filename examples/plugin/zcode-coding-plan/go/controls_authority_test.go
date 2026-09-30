package main

import (
	"testing"
)

func TestEffectivePayloadAuthority(t *testing.T) {
	c := &config{Models: []string{"m"}}
	base := []byte(`{"model":"m","max_tokens":10,"system":"effective","messages":[{"role":"user","content":"hello"}]}`)
	req := executorRequest{Payload: base, OriginalRequest: []byte(`{"system":"original","thinking":{"type":"enabled","budget_tokens":100}}`)}
	body, err := transformExecution(req, c, "s")
	if err != nil {
		t.Fatal(err)
	}
	if body["system"] != "effective" || body["thinking"] != nil {
		t.Fatal("removed controls restored from original")
	}
	req.Payload = []byte(`{"model":"m","max_tokens":10,"system":"effective","x_coding_plan":{"prompt":{"mode":"preserve"}},"messages":[{"role":"user","content":"hello"}]}`)
	if _, err = transformExecution(req, c, "s"); err == nil {
		t.Fatal("in-body prompt entry accepted")
	}
}
