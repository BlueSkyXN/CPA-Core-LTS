package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func newAccountRuntime(t *testing.T) (*pluginRuntime, *fakeHost, executorRequest) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHost{private: priv, public: pub, reads: map[string]int{}, streamDone: make(chan struct{})}
	r := newRuntime(f)
	t.Cleanup(r.stop)
	if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": true})); err != nil {
		t.Fatal(err)
	}
	req := executorRequest{RequestID: "first", CallbackID: "callback", AuthID: "account", AuthIndex: "index", Format: "claude", StorageJSON: inlineStorage(), Payload: []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)}
	return r, f, req
}

func TestLoggingAcknowledgementRevocation(t *testing.T) {
	for _, mode := range []string{"false", "inherit"} {
		t.Run(mode, func(t *testing.T) {
			r, host, req := newAccountRuntime(t)
			if _, err := r.execute(req); err != nil {
				t.Fatal(err)
			}
			old, signer := r.config, r.signer
			settings := map[string]any{}
			if mode == "false" {
				settings["host_logging_disabled"] = false
			}
			if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
				t.Fatal(err)
			}
			if r.config != nil || r.signer != nil || !signer.closed || r.authOwner != req.AuthID {
				t.Fatal("revocation must discard credentials without releasing account ownership")
			}
			if _, err := r.execute(req); err == nil || safeError(err).Code != "unsafe_host_logging" {
				t.Fatal("revoked acknowledgement did not block execution", err)
			}
			if _, err := r.admit(req, old); err == nil || safeError(err).Code != "unsafe_host_logging" {
				t.Fatal("revoked acknowledgement admitted an old snapshot", err)
			}
			status, err := r.readiness(encode(map[string]any{"StorageJSON": inlineStorage(), "AuthID": req.AuthID}))
			if err != nil || status.(map[string]any)["Ready"] != false || host.models != 1 {
				t.Fatal("revocation did not stop readiness and upstream dispatch", err)
			}
			if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true})); err != nil {
				t.Fatal(err)
			}
			if _, err := r.execute(req); err != nil || host.models != 2 {
				t.Fatal("re-acknowledgement did not recover the account", err)
			}
		})
	}
}

func TestAccountCredentialRotationAndStaleAdmission(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	old, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	previousSigner := r.signer
	updated := []byte(`{"type":"zcode-coding-plan","api_key":"rotated-key.rotated-secret","device_id":"rotated-device"}`)
	current, err := r.configuration(updated, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	if current == old || current.APIKey == old.APIKey || current.DeviceID == old.DeviceID || !previousSigner.closed || r.signer == previousSigner {
		t.Fatal("rotation did not replace the credential and signer snapshot")
	}
	if _, err := r.admit(req, old); err == nil || safeError(err).Code != "config_changed" {
		t.Fatal("old credential snapshot admitted", err)
	}
	req.StorageJSON = updated
	e, err := r.admit(req, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.configuration(inlineStorage(), req.AuthID); err == nil || safeError(err).Code != "busy" {
		t.Fatal("active execution allowed credentials to change", err)
	}
	if e.signer != r.signer || r.config != current {
		t.Fatal("active execution lost its signer or configuration")
	}
	r.finish(e)
	if _, err := r.configuration(updated, "second-account"); err == nil || safeError(err).Code != "single_account_only" {
		t.Fatal("another account with identical credentials bypassed ownership", err)
	}
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true, "upstream": "zai"})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.configuration(updated, "second-account"); err == nil {
		t.Fatal("reconfigure released account ownership")
	}
}

func TestAccountRemovalWaitsForActiveExecution(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	c, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.admit(req, c)
	if err != nil {
		t.Fatal(err)
	}
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: "unrelated"})
	r.closeAccount(cancelRequest{Scope: "auth", Provider: "another-provider", AuthID: req.AuthID})
	if r.retiring || e.ctx.Err() != nil {
		t.Fatal("unrelated account close affected the owner")
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "auth", Provider: provider, AuthID: req.AuthID, AuthIndex: req.AuthIndex})); err != nil {
		t.Fatal(err)
	}
	if e.ctx.Err() == nil || !r.retiring {
		t.Fatal("account removal did not cancel active execution")
	}
	if _, err := r.configuration(inlineStorage(), "replacement"); err == nil {
		t.Fatal("replacement admitted before old request cleanup")
	}
	r.finish(e)
	if r.config != nil || r.signer != nil || r.authOwner != "" || r.retiring {
		t.Fatal("account state survived request cleanup")
	}
	if _, err := r.configuration(inlineStorage(), "replacement"); err != nil {
		t.Fatal("replacement account rejected after removal", err)
	}
	if _, err := r.admit(req, c); err == nil {
		t.Fatal("old request admitted after account removal")
	}
}

func TestSessionClosurePreservesAccountAndBusyReload(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	req.ExecutionSessionID = "conversation"
	c, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.admit(req, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": false})); err == nil || safeError(err).Code != "busy" {
		t.Fatal("active reconfiguration did not report busy", err)
	}
	if r.config != c || r.signer != e.signer || r.loggingAcknowledgedLocked() != nil {
		t.Fatal("failed reconfiguration changed live state")
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "session", Provider: provider, ExecutionSessionID: req.ExecutionSessionID})); err != nil {
		t.Fatal(err)
	}
	if e.ctx.Err() == nil || r.retiring {
		t.Fatal("session close must cancel only conversation work")
	}
	r.finish(e)
	if r.config != c || r.authOwner != req.AuthID || r.signer == nil {
		t.Fatal("ordinary session closure discarded account identity")
	}
	if _, err := r.configuration(inlineStorage(), "another-account"); err == nil {
		t.Fatal("session close released the single-account binding")
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "auth", AuthIndex: req.AuthIndex})); err != nil || r.authOwner != "" {
		t.Fatal("auth-index-scoped removal did not release the binding", err)
	}
}

func TestAuthRejectsRemovedReferenceField(t *testing.T) {
	for _, value := range []any{nil, "", 42, true, "/retired/config.json"} {
		raw := encode(map[string]any{"type": provider, "api_key": "synthetic-key.synthetic-secret", "device_id": "synthetic-device", "config_file": value})
		if _, err := parseAuth(raw); err == nil || safeError(err).Code != "invalid_auth" {
			t.Fatal("removed config_file field was accepted", err)
		}
	}
}

func TestManagementRejectsRemovedFields(t *testing.T) {
	for _, name := range []string{"config_file", "credential", "identity", "prompt", "prompt_mode", "prompt_template", "prompt_move_position", "allow_request_override"} {
		if _, err := parseManagementConfig(encode(map[string]any{name: nil})); err == nil {
			t.Fatalf("removed management field %s accepted", name)
		}
	}
	if _, err := parseManagementConfig([]byte(`null`)); err == nil {
		t.Fatal("null configuration accepted")
	}
}
