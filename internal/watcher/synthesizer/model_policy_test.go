package synthesizer

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func synthesizeForModelPolicy(t *testing.T, raw []byte) []*coreauth.Auth {
	t.Helper()
	ctx := &SynthesisContext{
		Config:      nil,
		AuthDir:     t.TempDir(),
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synthesizeFileAuths(ctx, "x.json", raw)
	if err != nil {
		t.Fatalf("synthesizeFileAuths() error = %v", err)
	}
	return auths
}

func TestSynthesizeModelPolicyAttribute(t *testing.T) {
	auths := synthesizeForModelPolicy(t, []byte(`{
		"type": "codex",
		"email": "a@b.c",
		"model_policy": {
			"allowlist": {"enabled": true, "models": ["gpt-5-codex"]},
			"denylist": {"enabled": false, "models": ["gpt-4"]}
		}
	}`))
	if len(auths) != 1 {
		t.Fatalf("auths = %#v", auths)
	}
	attr := auths[0].Attributes[coreauth.AttributeModelPolicy]
	if attr != "allow=gpt-5-codex" {
		t.Fatalf("model_policy attribute = %q, want allow=gpt-5-codex", attr)
	}
}

func TestSynthesizeModelPolicyAbsentLeavesNoAttribute(t *testing.T) {
	auths := synthesizeForModelPolicy(t, []byte(`{"type":"codex","email":"a@b.c"}`))
	if len(auths) != 1 {
		t.Fatalf("auths = %#v", auths)
	}
	if _, exists := auths[0].Attributes[coreauth.AttributeModelPolicy]; exists {
		t.Fatal("absent model_policy must not set the attribute")
	}
}

func TestSynthesizeModelPolicyInvalidFailsFile(t *testing.T) {
	ctx := &SynthesisContext{
		AuthDir:     t.TempDir(),
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	_, err := synthesizeFileAuths(ctx, "x.json", []byte(`{
		"type": "codex",
		"email": "a@b.c",
		"model_policy": {"allowlist": {"enabled": "yes"}}
	}`))
	if err == nil {
		t.Fatal("invalid model_policy shape must fail the auth file")
	}
}
