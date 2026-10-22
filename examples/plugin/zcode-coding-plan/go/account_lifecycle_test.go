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
			account := r.accounts[req.AuthID]
			old, signer := account.config, account.signer
			settings := map[string]any{}
			if mode == "false" {
				settings["host_logging_disabled"] = false
			}
			if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
				t.Fatal(err)
			}
			if len(r.accounts) != 0 || account.config != nil || account.signer != nil || !signer.closed {
				t.Fatal("revocation must discard cached credentials and signers")
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
	account := r.accounts[req.AuthID]
	previousSigner := account.signer
	updated := []byte(`{"type":"zcode-coding-plan","api_key":"rotated-key.rotated-secret","device_id":"rotated-device"}`)
	current, err := r.configuration(updated, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	if current == old || current.APIKey == old.APIKey || current.DeviceID == old.DeviceID || !previousSigner.closed || account.signer == previousSigner {
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
	defer r.finish(e)
	if _, err := r.configuration(inlineStorage(), req.AuthID); err == nil || safeError(err).Code != "busy" {
		t.Fatal("active execution allowed credentials to change", err)
	}
	if e.signer != account.signer || account.config != current {
		t.Fatal("active execution lost its signer or configuration")
	}
	second, err := r.configuration(updated, "second-account")
	if err != nil || second == current || r.accounts["second-account"].signer == account.signer {
		t.Fatal("another account with identical credentials was not isolated", err)
	}
	r.finish(e)
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true, "upstream": "zai"})); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{req.AuthID, "second-account"} {
		c, err := r.configuration(updated, id)
		if err != nil || c.AccountScope != id || c.Upstream != "zai" {
			t.Fatal("reconfigure lost an account or its identity", err)
		}
	}
}

func TestAccountRemovalWaitsForActiveExecution(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	c, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	account := r.accounts[req.AuthID]
	signer := account.signer
	e, err := r.admit(req, c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(e)
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: "unrelated"})
	r.closeAccount(cancelRequest{Scope: "auth", Provider: "another-provider", AuthID: req.AuthID})
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: req.AuthID, AuthIndex: "wrong-index"})
	if account.retiring || e.ctx.Err() != nil {
		t.Fatal("unrelated or partially matching account close affected the owner")
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "auth", Provider: provider, AuthID: req.AuthID, AuthIndex: req.AuthIndex})); err != nil {
		t.Fatal(err)
	}
	if e.ctx.Err() == nil || !account.retiring {
		t.Fatal("account removal did not cancel active execution")
	}
	if _, err := r.configuration(inlineStorage(), req.AuthID); err == nil {
		t.Fatal("same account admitted before old request cleanup")
	}
	if _, err := r.configuration(inlineStorage(), "replacement"); err != nil {
		t.Fatal("another account was blocked by retiring account", err)
	}
	r.finish(e)
	if account.config != nil || account.signer != nil || !signer.closed || r.accounts[req.AuthID] != nil {
		t.Fatal("account state survived request cleanup")
	}
	if r.accounts["replacement"] == nil {
		t.Fatal("old account cleanup removed another account")
	}
	if _, err := r.admit(req, c); err == nil {
		t.Fatal("old request admitted after account removal")
	}
	fresh, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil || fresh == c {
		t.Fatal("same account could not be re-added with a fresh snapshot", err)
	}
	r.finish(e)
	if r.accounts[req.AuthID].config != fresh {
		t.Fatal("late cleanup discarded the replacement account")
	}
}

func TestSessionClosurePreservesAccountAndBusyReload(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	req.ExecutionSessionID = "conversation"
	c, err := r.configuration(req.StorageJSON, req.AuthID)
	if err != nil {
		t.Fatal(err)
	}
	account := r.accounts[req.AuthID]
	e, err := r.admit(req, c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(e)
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": false})); err == nil || safeError(err).Code != "busy" {
		t.Fatal("active reconfiguration did not report busy", err)
	}
	if account.config != c || account.signer != e.signer || r.loggingAcknowledgedLocked() != nil {
		t.Fatal("failed reconfiguration changed live state")
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "session", Provider: provider, ExecutionSessionID: req.ExecutionSessionID})); err != nil {
		t.Fatal(err)
	}
	if e.ctx.Err() == nil || account.retiring {
		t.Fatal("session close must cancel only conversation work")
	}
	r.finish(e)
	if account.config != c || r.accounts[req.AuthID] != account || account.signer == nil {
		t.Fatal("ordinary session closure discarded account identity")
	}
	if _, err := r.configuration(inlineStorage(), "another-account"); err != nil {
		t.Fatal("another account was rejected", err)
	}
	if _, err := r.dispatch("executor.close_session", encode(cancelRequest{Scope: "auth", AuthIndex: req.AuthIndex})); err != nil || r.accounts[req.AuthID] != nil || r.accounts["another-account"] == nil {
		t.Fatal("auth-index-scoped removal did not isolate the account", err)
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
