package auth

import (
	"sync"
	"testing"
	"time"
)

func TestUpstreamFailureTrackerTransitionsAndSuppresses(t *testing.T) {
	tracker := &upstreamFailureTracker{}
	key := upstreamFailureKey("codex", "auth_file=codex-pro.json", "gpt-6.1-sol")

	if !tracker.recordFailure(key) {
		t.Fatal("first failure must report a transition")
	}
	if tracker.recordFailure(key) {
		t.Fatal("repeated failure must be suppressed")
	}
	if tracker.recordFailure(key) {
		t.Fatal("third failure must stay suppressed")
	}

	recovered, _, suppressed := tracker.recordSuccess(key)
	if !recovered || suppressed != 2 {
		t.Fatalf("recordSuccess = (%v, suppressed=%d), want (true, 2)", recovered, suppressed)
	}
	if recovered, _, _ := tracker.recordSuccess(key); recovered {
		t.Fatal("second success must not report recovery")
	}
	if !tracker.recordFailure(key) {
		t.Fatal("failure after recovery must report a new transition")
	}
}

func TestUpstreamFailureTrackerKeysAreIndependent(t *testing.T) {
	tracker := &upstreamFailureTracker{}
	keyA := upstreamFailureKey("codex", "auth_file=a.json", "gpt-6.1-sol")
	keyB := upstreamFailureKey("codex", "auth_file=a.json", "gpt-5.6-luna")

	if !tracker.recordFailure(keyA) {
		t.Fatal("first failure for model A must transition")
	}
	if !tracker.recordFailure(keyB) {
		t.Fatal("failure for model B must transition independently")
	}
}

func TestUpstreamFailureTrackerReArmsAfterStaleWindow(t *testing.T) {
	tracker := &upstreamFailureTracker{}
	key := upstreamFailureKey("codex", "auth_file=codex-pro.json", "gpt-6.1-sol")

	if !tracker.recordFailure(key) {
		t.Fatal("first failure must transition")
	}
	tracker.mu.Lock()
	state := tracker.states[key]
	state.since = time.Now().Add(-2 * upstreamFailureStaleWindow)
	tracker.mu.Unlock()

	if !tracker.recordFailure(key) {
		t.Fatal("failure after the stale window must re-arm the key")
	}
}

func TestUpstreamFailureTrackerConcurrentAccess(t *testing.T) {
	tracker := &upstreamFailureTracker{}
	key := upstreamFailureKey("codex", "auth_file=codex-pro.json", "gpt-6.1-sol")

	const goroutines = 32
	transitions := make(chan bool, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transitions <- tracker.recordFailure(key)
		}()
	}
	wg.Wait()
	close(transitions)

	transitionCount := 0
	for ok := range transitions {
		if ok {
			transitionCount++
		}
	}
	if transitionCount != 1 {
		t.Fatalf("concurrent first-failure transitions = %d, want 1", transitionCount)
	}
}
