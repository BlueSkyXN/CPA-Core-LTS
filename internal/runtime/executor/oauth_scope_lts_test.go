package executor

import (
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestCodexAPIKeyViewSharesAffinityWithoutCopyingOwner(t *testing.T) {
	owner := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{IdentityConfuse: true}})
	owner.affinityGeneration = 23
	store := owner.affinityStore()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view := owner.ForAPIKey().(*CodexExecutor)
			if view == owner || view.affinityStore() != store || view.affinityGeneration != 23 {
				t.Error("request view lost shared affinity ownership")
			}
			if !view.cfg.Codex.IdentityConfuse {
				t.Error("shared policy removed")
			}
		}()
	}
	wg.Wait()
	if owner.affinityStore() != store {
		t.Fatal("request views replaced owner's store")
	}
}
