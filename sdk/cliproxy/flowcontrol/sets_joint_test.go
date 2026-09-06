package flowcontrol

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func v3Policy(rules ...Rule) Config {
	return Config{Version: 3, Enabled: true, Rules: rules, Queue: QueueConfig{MaxWaiting: 32, MaxWaitingPerKey: 16, MaxWaitMS: 1000}}
}
func setRule(id string, models []string, group []string, n int) Rule {
	return Rule{ID: id, Stage: Attempt, Scope: "custom", Models: models, GroupBy: group, MaxConcurrent: n}
}
func waitCount(t *testing.T, e *Engine, n int) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		if e.Snapshot().Waiting == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting=%d want %d", e.Snapshot().Waiting, n)
}
func TestV3EightProjectionCombinations(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		dims := []string{}
		for i, v := range []string{"key", "account", "model"} {
			if mask&(1<<i) != 0 {
				dims = append(dims, v)
			}
		}
		e, err := New(v3Policy(setRule("r", []string{"m1", "m2"}, dims, 1)))
		if err != nil {
			t.Fatal(err)
		}
		d := Identity{Stage: Attempt, Key: "u1", Account: "a1", Model: "m1", Provider: "p"}
		p, err := e.AcquireImmediately(context.Background(), d, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			x := d
			switch i {
			case 0:
				x.Key = "u2"
			case 1:
				x.Account = "a2"
			case 2:
				x.Model = "m2"
			}
			q, err := e.AcquireImmediately(context.Background(), x, 0)
			if (err == nil) != (mask&(1<<i) != 0) {
				t.Fatalf("mask=%d dim=%d err=%v", mask, i, err)
			}
			q.Release()
		}
		p.Release()
		e.Close()
	}
}
func TestV3IntersectingModelSets(t *testing.T) {
	e, _ := New(v3Policy(setRule("a", nil, []string{"account"}, 5), setRule("s12", []string{"m1", "m2"}, []string{"key", "account"}, 3), setRule("s23", []string{"m2", "m3"}, []string{"key", "account"}, 3), setRule("individual", nil, []string{"key", "account", "model"}, 2)))
	defer e.Close()
	get := func(m string) *Permit {
		p, err := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Key: "u", Account: "a", Provider: "p", Model: m}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b, c, d := get("m1"), get("m1"), get("m2"), get("m3")
	defer b.Release()
	defer c.Release()
	defer d.Release()
	blocked := func() {
		p, err := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Key: "u", Account: "a", Provider: "p", Model: "m2"}, 0)
		p.Release()
		if err == nil {
			t.Fatal("intersection must block")
		}
	}
	blocked()
	f := get("m3")
	a.Release()
	blocked()
	f.Release()
	get("m2").Release()
}
func TestV3SelectionCopiesEmptyAndConflicts(t *testing.T) {
	for _, r := range []Rule{setRule("r", []string{}, nil, 1), {ID: "r", Stage: Attempt, Scope: "global", Model: "m1", Models: []string{"m2"}, MaxConcurrent: 1}} {
		if v3Policy(r).Validate() == nil {
			t.Fatal("expected invalid selection")
		}
	}
	cfg := v3Policy(setRule("r", []string{"M2", "m1", "m1", "m*"}, nil, 3))
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	cfg.Rules[0].Models[0] = "other"
	d := Identity{Stage: Attempt, Model: "m1"}
	p, _ := e.AcquireImmediately(context.Background(), d, 0)
	defer p.Release()
	x := e.Explain(d)
	if len(x.Matches) != 1 || x.Matches[0].Active != 1 {
		t.Fatalf("double count: %+v", x)
	}
	x.Matches[0].Rule.Models[0] = "bad"
	if !e.Available(d) {
		t.Fatal("explanation mutated config")
	}
	legacy := v3Policy(setRule("r", []string{"m1"}, nil, 1))
	legacy.Version = 2
	if legacy.Validate() == nil {
		t.Fatal("old reader cannot enable collections")
	}
}
func TestV3QualifiedModels(t *testing.T) {
	e, _ := New(v3Policy(setRule("r", []string{"p1::m", "p2::m"}, []string{"model"}, 1)))
	defer e.Close()
	p, err := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Provider: "p1", Model: "m"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	q, err := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Provider: "p2", Model: "m"}, 0)
	if err != nil {
		t.Fatal("different providers should not share model counter", err)
	}
	q.Release()
	if x := e.Explain(Identity{Stage: Attempt, Model: "m"}); x.Complete {
		t.Fatal("missing provider shown as known")
	}
}
func TestV3JointFirstWaitDoesNotHoldRequest(t *testing.T) {
	cfg := v3Policy(Rule{ID: "user", Stage: Request, Scope: "key", MaxConcurrent: 1}, setRule("account", nil, []string{"account"}, 1))
	e, _ := New(cfg)
	defer e.Close()
	ctx := context.Background()
	occupied, _ := e.AcquireImmediately(ctx, Identity{Stage: Attempt, Account: "a", Key: "other"}, 0)
	call := e.BeginRequest(Identity{Stage: Request, Key: "u"})
	defer call.Release()
	ch := make(chan *Permit, 1)
	errch := make(chan error, 1)
	go func() {
		p, err := e.AcquireForRequest(ctx, call, Identity{Stage: Attempt, Key: "u", Account: "a"}, 4, true)
		ch <- p
		errch <- err
	}()
	waitCount(t, e, 1)
	if s := e.Snapshot(); s.Requests != 0 || s.Attempts != 1 {
		t.Fatalf("premature request slot %+v", s)
	}
	occupied.Release()
	p := <-ch
	if err := <-errch; err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot(); s.Requests != 1 || s.Attempts != 1 || s.Waiting != 0 {
		t.Fatalf("not joint %+v", s)
	}
	p.CommitDispatch()
	p.Release()
	if e.Snapshot().Requests != 1 {
		t.Fatal("logical call lost during retry")
	}
	p, err := e.AcquireForRequest(ctx, call, Identity{Stage: Attempt, Key: "u", Account: "a"}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	p.Release()
	call.Release()
	if s := e.Snapshot(); s.Requests+s.Attempts != 0 {
		t.Fatal("leak")
	}
}
func TestV3JointAbortRefundAndParallelClose(t *testing.T) {
	cfg := v3Policy(Rule{ID: "user", Stage: Request, Scope: "key", MaxConcurrent: 1, Windows: []Window{{1, 60000}}}, setRule("a", nil, nil, 3))
	e, _ := New(cfg)
	defer e.Close()
	call := e.BeginRequest(Identity{Stage: Request, Key: "u"})
	d := Identity{Stage: Attempt, Key: "u"}
	p, err := e.AcquireForRequest(context.Background(), call, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	p.CancelBeforeDispatch()
	if s := e.Snapshot(); s.Requests+s.Attempts != 0 {
		t.Fatal("unsent request not refunded")
	}
	p, err = e.AcquireForRequest(context.Background(), call, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	p.CommitDispatch()
	q, err := e.AcquireForRequest(context.Background(), call, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	q.CommitDispatch()
	if s := e.Snapshot(); s.Requests != 1 || s.Attempts != 2 {
		t.Fatal("parallel logical counted twice")
	}
	call.Release()
	p.Release()
	if e.Snapshot().Requests != 1 {
		t.Fatal("request released while producer alive")
	}
	q.Release()
	if e.Snapshot().Requests != 0 {
		t.Fatal("request leak")
	}
}
func TestV3JointConcurrentAtomicity(t *testing.T) {
	e, _ := New(v3Policy(Rule{ID: "u", Stage: Request, Scope: "key", MaxConcurrent: 3}, setRule("shared", []string{"m1", "m2"}, nil, 5), setRule("triple", nil, []string{"key", "account", "model"}, 2)))
	defer e.Close()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			call := e.BeginRequest(Identity{Stage: Request, Key: string(rune('a' + i%4))})
			defer call.Release()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			p, err := e.AcquireForRequest(ctx, call, Identity{Stage: Attempt, Key: string(rune('a' + i%4)), Account: "a", Model: []string{"m1", "m2"}[i%2]}, 1, true)
			if err != nil {
				return
			}
			p.CommitDispatch()
			s := e.Snapshot()
			if s.Attempts > 5 {
				t.Errorf("over-admission %d", s.Attempts)
			}
			for _, b := range s.Buckets {
				if b.Active > b.MaxConcurrent {
					t.Errorf("over-admission %s", b.Rule)
				}
			}
			p.Release()
		}(i)
	}
	wg.Wait()
	s := e.Snapshot()
	if s.Requests+s.Attempts+s.Waiting != 0 {
		t.Fatal("leaked state")
	}
}
func TestV3HotReprojectionThenRelease(t *testing.T) {
	e, _ := New(v3Policy(setRule("r", []string{"m1", "m2"}, []string{"model"}, 5)))
	defer e.Close()
	p, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Key: "u", Model: "m1"}, 0)
	q, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Key: "u", Model: "m2"}, 0)
	cfg := v3Policy(setRule("r", []string{"m1", "m2"}, nil, 1))
	if err := e.Update(cfg); err != nil {
		t.Fatal(err)
	}
	if x := e.Explain(Identity{Stage: Attempt, Model: "m1"}); x.Matches[0].Active != 2 || x.CanStart {
		t.Fatalf("bad projection %+v", x)
	}
	p.Release()
	if e.Available(Identity{Stage: Attempt, Model: "m1"}) {
		t.Fatal("still full")
	}
	q.Release()
	if !e.Available(Identity{Stage: Attempt, Model: "m1"}) {
		t.Fatal("did not release reprojected counters")
	}
}
func TestV3MigrationExplicitAndAmbiguous(t *testing.T) {
	ref := strings.Repeat("a", 64)
	old := strings.Repeat("b", 64)
	c := Config{Enabled: true, Rules: []Rule{{ID: "r", Stage: Attempt, Scope: "key-credential-model", Credential: ref, Model: "M1", MaxConcurrent: 2}}}
	m := Migrate(c, []AuthReference{{Legacy: old, Ref: ref}})
	if !m.Ready || m.Config.Version != 3 || m.Config.Rules[0].Credential != "" || len(m.Config.Rules[0].Accounts) != 1 {
		t.Fatalf("%+v", m)
	}
	c.Rules[0].Scope = "account"
	c.Rules[0].Credential = ""
	m = Migrate(c, []AuthReference{{Legacy: old, Ref: ref}, {Legacy: old, Ref: strings.Repeat("c", 64)}})
	if m.Ready {
		t.Fatal("ambiguous split allowed")
	}
}
func TestV3BudgetSurvivesRetry(t *testing.T) {
	cfg := v3Policy(setRule("a", nil, []string{"account"}, 1))
	cfg.Queue.MaxWaitMS = 25
	e, _ := New(cfg)
	defer e.Close()
	ctx := context.Background()
	call := e.BeginRequest(Identity{Stage: Request, Key: "u"})
	defer call.Release()
	occupied, _ := e.Acquire(ctx, Identity{Stage: Attempt, Account: "a"}, 0)
	// A spent operation budget must not reset on another acquisition.
	e.mu.Lock()
	call.operation.waitUsed = 30 * time.Millisecond
	e.mu.Unlock()
	p, err := e.AcquireForRequest(ctx, call, Identity{Stage: Attempt, Key: "u", Account: "a"}, 1, true)
	p.Release()
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != "flow_control_wait_timeout" {
		t.Fatalf("%v", err)
	}
	occupied.Release()
}

func TestV3OverlappingExactPrefixDoesNotRequireSpuriousProvider(t *testing.T) {
	e, _ := New(v3Policy(setRule("r", []string{"m2", "p::m2"}, nil, 2)))
	defer e.Close()
	x := e.Explain(Identity{Stage: Attempt, Model: "m2"})
	if !x.Complete || len(x.Matches) != 1 || !x.Matches[0].Known {
		t.Fatal(x)
	}
}
