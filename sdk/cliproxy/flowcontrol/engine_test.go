package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func policy(r ...Rule) Config {
	return Config{Enabled: true, Queue: QueueConfig{MaxWaiting: 32, MaxWaitingPerKey: 16, MaxWaitMS: 2000, MaxBytes: 1 << 20}, Rules: r}
}
func rule(id, scope string, n int) Rule {
	return Rule{ID: id, Stage: Attempt, Scope: scope, MaxConcurrent: n}
}
func identity(key, model, account string) Identity {
	return Identity{Stage: Attempt, Key: key, Model: model, Account: account, Provider: "codex"}
}
func mustEngine(t *testing.T, c Config) *Engine {
	t.Helper()
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func mustAcquire(t *testing.T, e *Engine, d Identity) *Permit {
	t.Helper()
	p, err := e.Acquire(context.Background(), d, 10)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestAllMatchingDimensionsAreConjunctive(t *testing.T) {
	e := mustEngine(t, policy(rule("total", "global", 4), rule("keys", "key", 2), rule("models", "model", 2), rule("pair", "key-model", 1), rule("account", "account", 3), rule("am", "account-model", 2)))
	a := identity("a", "m1", "acct")
	p := mustAcquire(t, e, a)
	if e.Available(a) {
		t.Fatal("key/model limit not applied")
	}
	p2 := mustAcquire(t, e, identity("a", "m2", "acct"))
	if e.Available(identity("a", "m3", "other")) {
		t.Fatal("key aggregate not applied")
	}
	p3 := mustAcquire(t, e, identity("b", "m1", "acct"))
	if e.Available(identity("c", "m1", "other")) {
		t.Fatal("model aggregate not applied")
	}
	if e.Available(identity("c", "m3", "acct")) {
		t.Fatal("account aggregate not applied")
	}
	p.Release()
	p.Release()
	p2.Release()
	p3.Release()
	if e.Snapshot().Attempts != 0 {
		t.Fatal("leaked slots")
	}
}
func TestWindowsAreStrictRollingAndAllApply(t *testing.T) {
	r := rule("rate", "key-model", 10)
	r.Windows = []Window{{2, 1000}, {3, 10000}}
	c := policy(r)
	c.Queue = QueueConfig{}
	e := mustEngine(t, c)
	now := time.Now()
	e.now = func() time.Time { return now }
	d := identity("key", "model", "a")
	for i := 0; i < 2; i++ {
		mustAcquire(t, e, d).Release()
	}
	if _, err := e.Acquire(context.Background(), d, 0); !IsError(err) {
		t.Fatal("short window failed")
	}
	now = now.Add(time.Second)
	mustAcquire(t, e, d).Release()
	now = now.Add(time.Second)
	if _, err := e.Acquire(context.Background(), d, 0); !IsError(err) {
		t.Fatal("long window failed")
	}
	now = now.Add(8 * time.Second)
	mustAcquire(t, e, d).Release()
}
func TestRejectedAdmissionDoesNotSpendOtherRules(t *testing.T) {
	g := rule("global-rate", "global", 10)
	g.Windows = []Window{{1, 60000}}
	c := policy(rule("account", "account", 1), g)
	c.Queue = QueueConfig{}
	e := mustEngine(t, c)
	p := mustAcquire(t, e, identity("a", "m", "x"))
	_, err := e.Acquire(context.Background(), identity("b", "m", "x"), 0)
	if !IsError(err) {
		t.Fatal(err)
	}
	s := e.Snapshot()
	for _, b := range s.Buckets {
		if b.Rule == "global-rate" && b.WindowCounts[0] != 1 {
			t.Fatalf("spent on rejection: %+v", b)
		}
	}
	p.Release()
}
func TestKeyModelBucketsIndependent(t *testing.T) {
	e := mustEngine(t, policy(rule("pair", "key-model", 1)))
	a := mustAcquire(t, e, identity("a", "x", "same"))
	b := mustAcquire(t, e, identity("a", "y", "same"))
	c := mustAcquire(t, e, identity("b", "x", "same"))
	a.Release()
	b.Release()
	c.Release()
}
func TestQueueDoesNotHoldPartOfTheResources(t *testing.T) {
	e := mustEngine(t, policy(rule("global", "global", 2), rule("account", "account", 1)))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	done := make(chan *Permit, 1)
	go func() {
		q, err := e.Acquire(context.Background(), identity("b", "m", "x"), 100)
		if err == nil {
			done <- q
		}
	}()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	other := mustAcquire(t, e, identity("c", "m", "y"))
	if e.Snapshot().Attempts != 2 {
		t.Fatal("wrong counters")
	}
	other.Release()
	p.Release()
	select {
	case q := <-done:
		q.Release()
	case <-time.After(time.Second):
		t.Fatal("not woken")
	}
}
func TestWaitingLimitsAndCancellation(t *testing.T) {
	c := policy(rule("one", "global", 1))
	c.Queue = QueueConfig{MaxWaiting: 2, MaxWaitingPerKey: 1, MaxWaitMS: 1000, MaxBytes: 100}
	e := mustEngine(t, c)
	p := mustAcquire(t, e, identity("held", "m", "a"))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := e.Acquire(ctx, identity("wait", "m", "a"), 80); errCh <- err }()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	for _, d := range []Identity{identity("wait", "m", "a"), identity("other", "m", "a")} {
		_, err := e.Acquire(context.Background(), d, 30)
		var x *Error
		if !errors.As(err, &x) || x.Code != "flow_control_queue_full" {
			t.Fatal(err)
		}
	}
	cancel()
	if !errors.Is(<-errCh, context.Canceled) {
		t.Fatal("cancel")
	}
	if e.Snapshot().QueuedBytes != 0 {
		t.Fatal("byte leak")
	}
	p.Release()
}
func TestQueueDeadlineAndRateTimer(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		c := policy(rule("one", "global", 1))
		c.Queue.MaxWaitMS = 25
		e := mustEngine(t, c)
		p := mustAcquire(t, e, identity("a", "m", "a"))
		defer p.Release()
		_, err := e.Acquire(context.Background(), identity("b", "m", "a"), 1)
		var x *Error
		if !errors.As(err, &x) || x.Code != "flow_control_wait_timeout" {
			t.Fatal(err)
		}
	})
	t.Run("rate", func(t *testing.T) {
		r := rule("r", "global", 2)
		r.Windows = []Window{{1, 25}}
		e := mustEngine(t, policy(r))
		mustAcquire(t, e, identity("a", "m", "a")).Release()
		p := mustAcquire(t, e, identity("b", "m", "a"))
		p.Release()
		if e.Snapshot().Waited != 1 {
			t.Fatal("did not wait for rate")
		}
	})
}
func TestFairnessRotatesKeysAndSkipsBlockedHead(t *testing.T) {
	e := mustEngine(t, policy(rule("global", "global", 1), rule("account", "account", 1)))
	held := mustAcquire(t, e, identity("a", "m", "x"))
	ch := make(chan string, 3)
	releases := make(chan *Permit, 3)
	launch := func(k string) {
		go func() {
			p, err := e.Acquire(context.Background(), identity(k, "m", "x"), 0)
			if err == nil {
				ch <- k
				releases <- p
			}
		}()
	}
	launch("a")
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	launch("b")
	waitFor(t, func() bool { return e.Snapshot().Waiting == 2 })
	held.Release()
	if first := <-ch; first != "b" {
		t.Fatalf("expected other key first: %s", first)
	}
	(<-releases).Release()
	if <-ch != "a" {
		t.Fatal("remaining key")
	}
	(<-releases).Release()
}
func TestReconfigureKeepsInFlightAndRateHistory(t *testing.T) {
	r := rule("r", "global", 2)
	r.Windows = []Window{{2, 60000}}
	c := policy(r)
	c.Queue = QueueConfig{}
	e := mustEngine(t, c)
	a := mustAcquire(t, e, identity("a", "m", "x"))
	b := mustAcquire(t, e, identity("b", "m", "x"))
	c.Rules[0].MaxConcurrent = 1
	if err := e.Update(c); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Attempts != 2 {
		t.Fatal("lost active work")
	}
	a.Release()
	if e.Available(identity("z", "m", "x")) {
		t.Fatal("lower limit ignored")
	}
	b.Release()
	if e.Available(identity("z", "m", "x")) {
		t.Fatal("rate history reset by update")
	}
}
func TestNewConcurrencyRuleCountsExistingAdmissions(t *testing.T) {
	e := mustEngine(t, policy(rule("g", "global", 10)))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	if err := e.Update(policy(rule("a", "account", 1))); err != nil {
		t.Fatal(err)
	}
	if e.Available(identity("b", "m", "x")) {
		t.Fatal("new rule ignored existing work")
	}
	p.Release()
	if !e.Available(identity("b", "m", "x")) {
		t.Fatal("not released")
	}
}
func TestDisableReleasesWaitersButDoesNotCancelExisting(t *testing.T) {
	e := mustEngine(t, policy(rule("r", "global", 1)))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	done := make(chan error, 1)
	go func() {
		p, err := e.Acquire(context.Background(), identity("b", "m", "x"), 10)
		p.Release()
		done <- err
	}()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	if err := e.Update(Config{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p.Release()
	if e.Snapshot().Attempts != 0 {
		t.Fatal("release after disable")
	}
}
func TestConfigurationValidationAndLegacyOff(t *testing.T) {
	invalid := []Config{policy(rule("r", "bad", 1)), policy(Rule{ID: "r", Stage: Request, Scope: "account", MaxConcurrent: 1}), policy(Rule{ID: "r", Stage: Attempt, Scope: "key", Key: "raw-api-key", MaxConcurrent: 1}), policy(Rule{ID: "r", Stage: Attempt, Scope: "model", Model: "a*b", MaxConcurrent: 1})}
	for _, c := range invalid {
		if c.Validate() == nil {
			t.Fatal("accepted invalid policy")
		}
		c.Enabled = false
		if c.Validate() != nil {
			t.Fatal("cannot disable")
		}
	}
	e := mustEngine(t, Config{})
	for i := 0; i < 10; i++ {
		p := mustAcquire(t, e, identity("a", "m", "a"))
		p.Release()
	}
	if e.Snapshot().Admitted != 0 {
		t.Fatal("disabled altered accounting")
	}
}
func TestScopeTupleCollisionAndExplicitModelPattern(t *testing.T) {
	r := rule("r", "key-model", 1)
	if r.bucketID(identity("ab", "c", "")) == r.bucketID(identity("a", "bc", "")) {
		t.Fatal("tuple collision")
	}
	r.Model = "gpt-5*"
	if !r.matches(identity("a", "gpt-5.5", "")) || r.matches(identity("a", "prefix-gpt-5.5", "")) {
		t.Fatal("prefix matching")
	}
}
func TestStateCapacityNeverEvictsUsedWindow(t *testing.T) {
	r := rule("r", "key", 2)
	r.Windows = []Window{{2, 60000}}
	c := policy(r)
	c.MaxBuckets = 1
	c.MaxHistory = 2
	c.Queue = QueueConfig{}
	e := mustEngine(t, c)
	mustAcquire(t, e, identity("a", "m", "x")).Release()
	_, err := e.Acquire(context.Background(), identity("b", "m", "x"), 0)
	var x *Error
	if !errors.As(err, &x) || x.Code != "flow_control_state_full" {
		t.Fatal(err)
	}
	mustAcquire(t, e, identity("a", "m", "x")).Release()
	if e.Available(identity("a", "m", "x")) {
		t.Fatal("evicted used history")
	}
}
func TestStageSeparationAndExactSelectors(t *testing.T) {
	req := rule("req", "key", 1)
	req.Stage = Request
	e := mustEngine(t, policy(req, rule("attempt", "account", 1)))
	d := identity("a", "m", "x")
	d.Stage = Request
	p := mustAcquire(t, e, d)
	q := mustAcquire(t, e, identity("a", "m", "x"))
	if e.Snapshot().Requests != 1 || e.Snapshot().Attempts != 1 {
		t.Fatal("phases not separated")
	}
	p.Release()
	q.Release()
}
func TestCloseUnblocksQueue(t *testing.T) {
	e := mustEngine(t, policy(rule("r", "global", 1)))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	done := make(chan error, 1)
	go func() { _, err := e.Acquire(context.Background(), identity("b", "m", "x"), 0); done <- err }()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	e.Close()
	if !IsError(<-done) {
		t.Fatal("missing closed error")
	}
	p.Release()
	e.Close()
}
func TestRaceLimitAndCancellation(t *testing.T) {
	c := policy(rule("g", "global", 5), rule("a", "account", 3))
	c.Queue.MaxWaiting = 128
	c.Queue.MaxWaitingPerKey = 128
	e := mustEngine(t, c)
	var current, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p, err := e.Acquire(ctx, identity(fmt.Sprint(i%8), "m", "shared"), 1)
			if err != nil {
				return
			}
			n := current.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
			p.Release()
			p.Release()
		}(i)
	}
	wg.Wait()
	if peak.Load() > 3 {
		t.Fatalf("oversubscribed: %d", peak.Load())
	}
	s := e.Snapshot()
	if s.Attempts != 0 || s.Waiting != 0 || s.QueuedBytes != 0 {
		t.Fatalf("leaks: %+v", s)
	}
}
func TestCanceledGrantDoesNotRefundAnotherRequest(t *testing.T) {
	r := rule("r", "global", 5)
	r.Windows = []Window{{5, 60000}}
	e := mustEngine(t, policy(r))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	q := mustAcquire(t, e, identity("b", "m", "x"))
	p.abort()
	q.Release()
	s := e.Snapshot()
	if len(s.Buckets) != 1 || s.Buckets[0].WindowCounts[0] != 1 {
		t.Fatalf("wrong refund %+v", s)
	}
}
func TestParentDeadlineWins(t *testing.T) {
	e := mustEngine(t, policy(rule("r", "global", 1)))
	p := mustAcquire(t, e, identity("a", "m", "x"))
	defer p.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.Acquire(ctx, identity("b", "m", "x"), 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		var x *Error
		if !errors.As(err, &x) || x.Code != "flow_control_wait_timeout" {
			t.Fatal(err)
		}
	}
}
func BenchmarkAcquireRelease(b *testing.B) {
	c := policy(rule("g", "global", 100))
	e, _ := New(c)
	defer e.Close()
	d := identity("a", "m", "x")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := e.Acquire(context.Background(), d, 0)
		if err != nil {
			b.Fatal(err)
		}
		p.Release()
	}
}
