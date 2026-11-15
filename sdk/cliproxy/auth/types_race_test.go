package auth

import (
	"sync"
	"testing"
)

// TestAuthEnsureIndexConcurrentWithClone pins the read-only steady state of
// EnsureIndex: executor usage reporters derive the credential index on request
// goroutines while the conductor Clones the same shared Auth, so the fast path
// must not write (go test -race).
func TestAuthEnsureIndexConcurrentWithClone(t *testing.T) {
	auth := &Auth{ID: "race-index-auth", Provider: "codex"}
	if idx := auth.EnsureIndex(); idx == "" {
		t.Fatal("EnsureIndex returned an empty index")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if auth.EnsureIndex() == "" {
					t.Error("EnsureIndex lost the assigned index")
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				clone := auth.Clone()
				if clone.ID != auth.ID {
					t.Error("Clone observed a torn Auth")
					return
				}
			}
		}()
	}
	wg.Wait()
}
