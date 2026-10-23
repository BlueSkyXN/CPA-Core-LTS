package main

import (
	"fmt"
	"sync"
	"testing"
)

func TestManagementReconfigureUnchangedPreservesAccounts(t *testing.T) {
	for _, overrides := range []bool{false, true} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("overrides=%t/active=%t", overrides, active), func(t *testing.T) {
				r, host, req := newAccountRuntime(t)
				settings := map[string]any{"host_logging_disabled": true}
				if overrides {
					settings["upstream"] = "bigmodel"
					settings["models"] = []string{"glm-5.3", "glm-5.3-flash"}
					settings["model_limits"] = map[string]modelLimits{"glm-5.3": {Context: 10000, Output: 1000}}
				}
				if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
					t.Fatal(err)
				}
				if _, err := r.execute(req); err != nil {
					t.Fatal(err)
				}
				second := req
				second.RequestID, second.AuthID, second.AuthIndex = "second", "second-account", "second-index"
				if _, err := r.configurationForAuth(second.StorageJSON, second.AuthID, second.AuthIndex); err != nil {
					t.Fatal(err)
				}
				accounts := map[string]*accountState{}
				configs := map[string]*config{}
				signers := map[string]*signer{}
				var executions []*execution
				for _, request := range []executorRequest{req, second} {
					account := r.accounts[request.AuthID]
					accounts[request.AuthID], configs[request.AuthID], signers[request.AuthID] = account, account.config, account.signer
					if active {
						e, err := r.admit(request, account.config)
						if err != nil {
							t.Fatal(err)
						}
						defer r.finish(e)
						executions = append(executions, e)
					}
				}
				settings["priority"] = 10
				for range 3 {
					if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
						t.Fatalf("unchanged plugin settings rejected: %v", err)
					}
				}
				for id, account := range accounts {
					if r.accounts[id] != account || account.config != configs[id] || account.signer != signers[id] || account.signer.closed || account.retiring {
						t.Fatal("unchanged reconfiguration replaced account state")
					}
				}
				if len(r.active) != len(executions) {
					t.Fatal("unchanged reconfiguration changed active execution count")
				}
				for _, e := range executions {
					if e.ctx.Err() != nil || e.account.active != 1 || e.signer != e.account.signer {
						t.Fatal("unchanged reconfiguration interrupted execution")
					}
					r.finish(e)
				}
				req.RequestID = "after-reload"
				if _, err := r.execute(req); err != nil {
					t.Fatal(err)
				}
				if host.handshakes != 1 || host.models != 2 {
					t.Fatalf("calls after reload: handshakes=%d models=%d", host.handshakes, host.models)
				}
			})
		}
	}
}

func TestManagementReconfigureChangedWhileBusyPreservesState(t *testing.T) {
	for _, field := range []string{"host_logging_disabled", "upstream", "models", "model_limits"} {
		t.Run(field, func(t *testing.T) {
			r, _, req := newAccountRuntime(t)
			c, err := r.configurationForAuth(req.StorageJSON, req.AuthID, req.AuthIndex)
			if err != nil {
				t.Fatal(err)
			}
			account := r.accounts[req.AuthID]
			e, err := r.admit(req, c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.finish(e)
			settings := map[string]any{"host_logging_disabled": true}
			switch field {
			case "host_logging_disabled":
				settings[field] = false
			case "upstream":
				settings[field] = "zai"
			case "models":
				settings[field] = []string{"glm-5.3-flash"}
			case "model_limits":
				settings[field] = map[string]modelLimits{"glm-5.3": {Context: 10000, Output: 1000}}
			}
			if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err == nil || safeError(err).Code != "busy" {
				t.Fatal("changed configuration did not retain busy protection", err)
			}
			if r.accounts[req.AuthID] != account || account.config != c || account.signer != e.signer || e.ctx.Err() != nil || len(r.active) != 1 {
				t.Fatal("rejected update changed live state")
			}
			r.finish(e)
			if _, err := r.dispatch("plugin.reconfigure", managementRegistration(settings)); err != nil {
				t.Fatal("idle configuration change rejected", err)
			}
			if !e.signer.closed {
				t.Fatal("real change did not replace the old signer")
			}
			current, err := r.configurationForAuth(req.StorageJSON, req.AuthID, req.AuthIndex)
			if field == "host_logging_disabled" {
				if err == nil || safeError(err).Code != "unsafe_host_logging" || len(r.accounts) != 0 {
					t.Fatal("logging acknowledgement revocation was ignored", err)
				}
				return
			}
			if err != nil || current == c {
				t.Fatal("real change did not replace configuration", err)
			}
			switch field {
			case "upstream":
				if current.Upstream != "zai" {
					t.Fatal("upstream change was ignored")
				}
			case "models":
				if len(current.Models) != 1 || current.Models[0] != "glm-5.3-flash" {
					t.Fatal("model list change was ignored")
				}
			case "model_limits":
				if current.ModelLimits["glm-5.3"] != (modelLimits{Context: 10000, Output: 1000}) {
					t.Fatal("model limits change was ignored")
				}
			}
		})
	}
}

func TestManagementReconfigureUnchangedResumesAfterQuiesce(t *testing.T) {
	r, host, req := newAccountRuntime(t)
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	previous := r.accounts[req.AuthID].signer
	if _, err := r.dispatch("plugin.quiesce", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if r.accepting || !previous.closed || len(r.accounts) != 0 {
		t.Fatal("quiesce did not clear account state")
	}
	if _, err := r.execute(req); err == nil {
		t.Fatal("quiesced runtime accepted execution")
	}
	if _, err := r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true})); err != nil {
		t.Fatal(err)
	}
	if !r.accepting {
		t.Fatal("unchanged reconfigure did not resume quiesced runtime")
	}
	if _, err := r.execute(req); err != nil {
		t.Fatal(err)
	}
	if r.accounts[req.AuthID].signer == previous || host.handshakes != 2 {
		t.Fatal("resumed runtime did not initialize a fresh signer")
	}
}

func TestManagementReconfigureUnchangedConcurrentExecution(t *testing.T) {
	r, _, req := newAccountRuntime(t)
	c, err := r.configurationForAuth(req.StorageJSON, req.AuthID, req.AuthIndex)
	if err != nil {
		t.Fatal(err)
	}
	account := r.accounts[req.AuthID]
	previous := account.signer
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := req
			for iteration := range 20 {
				request.RequestID = fmt.Sprintf("worker-%d-%d", worker, iteration)
				e, err := r.admit(request, c)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = r.dispatch("plugin.reconfigure", managementRegistration(map[string]any{"host_logging_disabled": true}))
				if err != nil || e.ctx.Err() != nil {
					t.Error("unchanged reconfiguration interrupted execution", err)
				}
				r.finish(e)
			}
		}()
	}
	wg.Wait()
	if r.accounts[req.AuthID] != account || account.config != c || account.signer != previous || previous.closed || len(r.active) != 0 || account.active != 0 {
		t.Fatal("concurrent no-op reconfiguration lost account state or execution accounting")
	}
}
