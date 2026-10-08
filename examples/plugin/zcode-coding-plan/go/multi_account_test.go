package main

import (
	"fmt"
	"sync"
	"testing"
)

func accountStorage(name string) []byte {
	return encode(map[string]any{"type": provider, "api_key": name + ".synthetic-secret", "device_id": "device-" + name})
}

func prepareAccount(t *testing.T, r *pluginRuntime, name string) (executorRequest, *config) {
	t.Helper()
	req := executorRequest{AuthID: name, AuthIndex: "index-" + name, RequestID: "request-" + name, CallbackID: "callback-" + name, StorageJSON: accountStorage(name), CallerScope: "caller", WorkspaceIdentity: "workspace", ExecutionSessionID: "conversation"}
	c, err := r.configurationForAuth(req.StorageJSON, req.AuthID, req.AuthIndex)
	if err != nil {
		t.Fatal(err)
	}
	return req, c
}

func TestMultiAccountConcurrentAdmissionLimit(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := map[string][]*execution{}
	for _, id := range []string{"account-a", "account-b"} {
		req, c := prepareAccount(t, r, id)
		if c.MaxInflight != 10 {
			t.Fatalf("default per-account limit = %d, want 10", c.MaxInflight)
		}
		for i := range 24 {
			wg.Add(1)
			go func(req executorRequest, c *config, i int) {
				defer wg.Done()
				req.RequestID = fmt.Sprintf("request-%d", i)
				e, err := r.admit(req, c)
				if err != nil {
					if safeError(err).Status != 429 || safeError(err).Code != "rate_limit_error" {
						t.Errorf("unexpected admission error: %v", err)
					}
					return
				}
				mu.Lock()
				accepted[req.AuthID] = append(accepted[req.AuthID], e)
				mu.Unlock()
			}(req, c, i)
		}
	}
	wg.Wait()
	defer func() {
		for _, executions := range accepted {
			for _, e := range executions {
				r.finish(e)
			}
		}
	}()
	if len(r.active) != 20 {
		t.Fatalf("total admitted = %d, want 20", len(r.active))
	}
	for _, id := range []string{"account-a", "account-b"} {
		if len(accepted[id]) != 10 || r.accounts[id].active != 10 {
			t.Fatalf("account %s admitted %d, want 10", id, len(accepted[id]))
		}
	}
	r.finish(accepted["account-a"][0])
	r.finish(accepted["account-a"][0])
	if r.accounts["account-a"].active != 9 || r.accounts["account-b"].active != 10 {
		t.Fatal("finishing a request changed another account or released capacity twice")
	}
	req, c := prepareAccount(t, r, "account-a")
	req.RequestID = "replacement"
	e, err := r.admit(req, c)
	if err != nil {
		t.Fatal("released account slot was not reusable", err)
	}
	defer r.finish(e)
}

func TestMultiAccountReadinessAndModelsDoNotConsumeCapacity(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	reqA, cA := prepareAccount(t, r, "account-a")
	var active []*execution
	defer func() {
		for _, e := range active {
			r.finish(e)
		}
	}()
	for i := range 10 {
		reqA.RequestID = fmt.Sprint(i)
		e, err := r.admit(reqA, cA)
		if err != nil {
			t.Fatal(err)
		}
		active = append(active, e)
	}
	for _, id := range []string{reqA.AuthID, "account-b"} {
		for _, purpose := range []string{"diagnostic", "admission"} {
			status, err := r.readiness(encode(map[string]any{"Purpose": purpose, "StorageJSON": accountStorage(id), "AuthID": id, "AuthIndex": "index-" + id}))
			want := id != reqA.AuthID || purpose != "admission"
			if err != nil || status.(map[string]any)["Ready"] != want {
				t.Fatalf("readiness for %s/%s must be %v: %v", id, purpose, want, err)
			}
		}
		models, err := r.dispatch("model.for_auth", encode(map[string]any{"StorageJSON": accountStorage(id), "AuthID": id}))
		if err != nil || len(models.(map[string]any)["Models"].([]any)) != 2 {
			t.Fatal("model discovery failed while another account is full", err)
		}
	}
	if r.accounts["account-b"].active != 0 || r.accounts[reqA.AuthID].active != 10 {
		t.Fatal("readiness or discovery changed execution capacity")
	}
	reqB, cB := prepareAccount(t, r, "account-b")
	e, err := r.admit(reqB, cB)
	if err != nil {
		t.Fatal("account A's limit blocked account B", err)
	}
	defer r.finish(e)
}

func TestMultiAccountRotationDoesNotBlockOrReplacePeer(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	reqA, cA := prepareAccount(t, r, "account-a")
	reqB, cB := prepareAccount(t, r, "account-b")
	accountA, accountB := r.accounts[reqA.AuthID], r.accounts[reqB.AuthID]
	oldSignerA, signerB := accountA.signer, accountB.signer
	eB, err := r.admit(reqB, cB)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(eB)
	rotated, err := r.configuration(accountStorage("rotated-a"), reqA.AuthID)
	if err != nil || rotated == cA || !oldSignerA.closed {
		t.Fatal("peer's active request blocked rotation", err)
	}
	if accountB.config != cB || accountB.signer != signerB || eB.ctx.Err() != nil {
		t.Fatal("rotation modified the peer")
	}
	if _, err := r.admit(reqA, cA); err == nil {
		t.Fatal("stale credential snapshot was admitted")
	}
	if _, err := r.admit(reqB, rotated); err == nil {
		t.Fatal("another account's credential snapshot was admitted")
	}
	if _, err := r.configuration(accountStorage("rotated-b"), reqB.AuthID); err == nil || safeError(err).Code != "busy" {
		t.Fatal("in-use credential could be replaced", err)
	}
}

func TestMultiAccountScopedCancelAndClose(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	reqA, cA := prepareAccount(t, r, "account-a")
	reqB, cB := prepareAccount(t, r, "account-b")
	reqA.RequestID, reqB.RequestID = "same-request", "same-request"
	eA, err := r.admit(reqA, cA)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(eA)
	eB, err := r.admit(reqB, cB)
	if err != nil {
		t.Fatal(err)
	}
	defer r.finish(eB)
	if _, err := r.admit(reqA, cA); err == nil || safeError(err).Code != "duplicate_execution" {
		t.Fatal("same-account duplicate execution was admitted")
	}
	for _, q := range []cancelRequest{
		{RequestID: reqA.RequestID, AuthID: reqA.AuthID, AuthIndex: reqB.AuthIndex},
		{RequestID: reqA.RequestID, AuthID: reqA.AuthID, CallerScope: "wrong"},
		{RequestID: reqA.RequestID, AuthID: reqA.AuthID, WorkspaceIdentity: "wrong"},
		{RequestID: reqA.RequestID, AuthID: reqA.AuthID, Provider: "wrong"},
	} {
		r.cancelMatching(q)
	}
	if eA.ctx.Err() != nil || eB.ctx.Err() != nil {
		t.Fatal("mismatched cancellation affected an execution")
	}
	r.cancelMatching(cancelRequest{RequestID: reqA.RequestID, AuthID: reqA.AuthID, AuthIndex: reqA.AuthIndex})
	if eA.ctx.Err() == nil || eB.ctx.Err() != nil {
		t.Fatal("request cancellation crossed account boundary")
	}
	r.finish(eA)
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: reqA.AuthID, AuthIndex: reqB.AuthIndex})
	if r.accounts[reqA.AuthID] == nil {
		t.Fatal("partially matching close removed an idle account")
	}
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: reqA.AuthID, AuthIndex: reqA.AuthIndex})
	if r.accounts[reqA.AuthID] != nil || r.accounts[reqB.AuthID] == nil || eB.ctx.Err() != nil {
		t.Fatal("account close affected its peer")
	}
	r.closeAccount(cancelRequest{Scope: "provider", Provider: provider})
	if eB.ctx.Err() == nil {
		t.Fatal("provider close did not cancel remaining account")
	}
	r.finish(eB)
	if len(r.accounts) != 0 || len(r.active) != 0 {
		t.Fatal("provider close left account credentials or executions")
	}
}

func TestMultiAccountConfigurationLifecycle(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	reqA, cA := prepareAccount(t, r, "account-a")
	_, cB := prepareAccount(t, r, "account-b")
	a, b := r.accounts["account-a"], r.accounts["account-b"]
	signerA, signerB := a.signer, b.signer
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true, "models": []string{}})); err == nil {
		t.Fatal("invalid global configuration was accepted")
	}
	if a.config != cA || b.config != cB || signerA.closed || signerB.closed {
		t.Fatal("failed configuration changed account state")
	}
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true, "upstream": "zai"})); err != nil {
		t.Fatal(err)
	}
	for id, previous := range map[string]*config{"account-a": cA, "account-b": cB} {
		next := r.accounts[id]
		if next.config == previous || next.config.Upstream != "zai" || next.config.APIKey != previous.APIKey || next.config.AccountScope != id {
			t.Fatal("global reconfiguration lost an account's identity or credentials")
		}
	}
	if !signerA.closed || !signerB.closed {
		t.Fatal("global reconfiguration retained previous signers")
	}
	if _, err := r.admit(reqA, cA); err == nil {
		t.Fatal("global reconfiguration admitted stale state")
	}
	signerA, signerB = r.accounts["account-a"].signer, r.accounts["account-b"].signer
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": false})); err != nil {
		t.Fatal(err)
	}
	if len(r.accounts) != 0 || !signerA.closed || !signerB.closed {
		t.Fatal("logging revocation left an account's cached credentials")
	}
}

func TestMultiAccountConcurrentRetirementAndReconfiguration(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			id := fmt.Sprintf("account-%d", worker%2)
			for iteration := range 80 {
				switch worker % 4 {
				case 0, 1:
					req := executorRequest{AuthID: id, AuthIndex: "index-" + id, RequestID: fmt.Sprintf("request-%d-%d", worker, iteration), CallbackID: "callback"}
					c, err := r.configurationForAuth(accountStorage(id), id, req.AuthIndex)
					if err != nil {
						if safeError(err).Code != "unavailable" {
							t.Errorf("unexpected configuration error: %v", err)
						}
						continue
					}
					e, err := r.admit(req, c)
					if err != nil {
						code := safeError(err).Code
						if code != "config_changed" && code != "unavailable" {
							t.Errorf("unexpected admission error: %v", err)
						}
						continue
					}
					r.finish(e)
				case 2:
					r.closeAccount(cancelRequest{Scope: "provider", Provider: provider})
				case 3:
					_, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true}))
					if err != nil && safeError(err).Code != "busy" {
						t.Errorf("unexpected reconfiguration error: %v", err)
					}
				}
			}
		}(worker)
	}
	wg.Wait()
	r.stop()
	if len(r.active) != 0 || len(r.accounts) != 0 {
		t.Fatal("concurrent lifecycle operations retained an execution or account")
	}
}

func TestAccountRemovalBeforeExecutionAndIndexBinding(t *testing.T) {
	r, _, _ := newAccountRuntime(t)
	c, err := r.configuration(accountStorage("discovered"), "discovered")
	if err != nil {
		t.Fatal(err)
	}
	s := r.accounts["discovered"].signer
	r.closeAccount(cancelRequest{Scope: "auth", AuthID: "discovered", AuthIndex: "host-index"})
	if len(r.accounts) != 0 || !s.closed {
		t.Fatal("model-only account could not be removed")
	}
	req, next := prepareAccount(t, r, "discovered")
	if next == c {
		t.Fatal("re-added account reused an obsolete snapshot")
	}
	if _, err := r.configurationForAuth(req.StorageJSON, req.AuthID, "wrong-index"); err == nil {
		t.Fatal("conflicting auth index was accepted")
	}
	r.stop()
	if len(r.accounts) != 0 {
		t.Fatal("shutdown retained idle credentials")
	}
	if _, err := r.configuration(req.StorageJSON, req.AuthID); err == nil {
		t.Fatal("stopped plugin admitted an account")
	}
}
