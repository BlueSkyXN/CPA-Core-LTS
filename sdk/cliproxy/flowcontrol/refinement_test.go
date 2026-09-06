package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestRejectedUpdatePreflightDoesNotMutatePolicyOrCapacity(t *testing.T) {
	cfg := Config{Version: 3, Enabled: true, Rules: []Rule{{ID: "rate", Stage: Attempt, Scope: "global", Models: []string{"a"}, MaxConcurrent: 3, Windows: []Window{{Requests: 10, PeriodMS: 60000}}}}}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := mustAcquire(t, e, Identity{Stage: Attempt, Model: "a"})
	p.Release()
	before := e.Policy()
	revision := e.Summary().PolicyRevision
	changed := e.Policy()
	changed.Rules[0].Models = []string{"b"}
	for _, check := range []func(Config) error{e.CheckUpdate, e.Update} {
		var rejected *Error
		if err := check(changed); !errors.As(err, &rejected) || rejected.Code != "flow_control_rate_domain_change" {
			t.Fatalf("%v", err)
		}
	}
	if !reflect.DeepEqual(before, e.Policy()) || revision != e.Summary().PolicyRevision {
		t.Fatal("rejected update changed effective policy")
	}
	q := mustAcquire(t, e, Identity{Stage: Attempt, Model: "a"})
	q.Release()
	if e.Summary().Attempts != 0 {
		t.Fatal("leaked capacity")
	}
}

func TestDisabledDraftExplainAndPreviewNeverPanic(t *testing.T) {
	cfg := Config{Version: 3, Enabled: true, Rules: []Rule{{ID: "r", Stage: Attempt, Scope: "global", MaxConcurrent: 1, Windows: []Window{{Requests: 2, PeriodMS: 60000}}}}}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := mustAcquire(t, e, Identity{Stage: Attempt})
	p.Release()
	draft := e.Policy()
	draft.Enabled = false
	draft.Rules[0].Models = []string{"new-domain"}
	draft.Rules[0].Windows[0].Requests = 0
	if err = e.Update(draft); err != nil {
		t.Fatal("cannot switch off with unfinished draft:", err)
	}
	x := e.Explain(Identity{Stage: Attempt, Model: "new-domain"})
	if x.ConfigurationError == "" || x.CanStart || x.Complete {
		t.Fatalf("invalid draft presented as executable: %+v", x)
	}
	rows, err := e.Preview(nil, []Identity{{Stage: Attempt, Model: "new-domain"}})
	if err != nil || len(rows) != 1 || rows[0].ConfigurationError == "" {
		t.Fatalf("preview: %+v %v", rows, err)
	}
	if e.Summary().Attempts != 0 {
		t.Fatal("explain mutated occupancy")
	}
}

func TestSequentialRetryWaitsForProducerAndKeepsHedgesNoWait(t *testing.T) {
	cfg := Config{Version: 3, Enabled: true, Queue: QueueConfig{MaxWaiting: 4, MaxWaitMS: 1000}, Rules: []Rule{{ID: "account", Stage: Attempt, Scope: "account", MaxConcurrent: 1}}}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	d := Identity{Stage: Attempt, Key: "u", Account: "a"}
	request := e.BeginRequest(Identity{Stage: Request, Key: "u"})
	defer request.Release()
	first, err := e.AcquireForRequest(ctx, request, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	first.CommitDispatch()
	producer := make(chan int)
	output := HoldChannel(ctx, producer, first.Release)
	drained := make(chan struct{})
	go func() {
		for range output {
		}
		close(drained)
	}()
	completed := make(chan error, 1)
	go func() { completed <- e.WaitForProducer(ctx, request, drained) }()
	// Extra parallel work still cannot queue behind its own execution.
	other, err := e.AcquireForRequest(ctx, request, d, 0, true)
	other.Release()
	if !IsError(err) || e.Summary().Waiting != 0 {
		t.Fatalf("hedge unexpectedly queued: %v", err)
	}
	select {
	case err := <-completed:
		t.Fatal("released producer early:", err)
	default:
	}
	if e.Summary().Attempts != 1 {
		t.Fatal("unclosed producer lost its slot")
	}
	close(producer)
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
	next, err := e.AcquireForRequest(ctx, request, d, 0, true)
	if err != nil {
		t.Fatal("sequential retry rejected:", err)
	}
	next.Release()
}

func TestProducerWaitUsesCancellationAndCumulativeBudget(t *testing.T) {
	e, _ := New(Config{Version: 3, Enabled: true, Queue: QueueConfig{MaxWaiting: 2, MaxWaitMS: 30}})
	defer e.Close()
	r := e.BeginRequest(Identity{Stage: Request})
	defer r.Release()
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.WaitForProducer(ctx, r, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := e.WaitForProducer(context.Background(), r, done); !IsError(err) {
		t.Fatalf("no deadline: %v", err)
	}
	start := time.Now()
	if err := e.WaitForProducer(context.Background(), r, done); !IsError(err) {
		t.Fatal(err)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("cleanup reset its consumed wait budget")
	}
}

func TestObservationAndAdmissionSwitchesRemainIndependent(t *testing.T) {
	cfg := Config{Version: 3, Enabled: true, Rules: []Rule{{ID: "r", Stage: Attempt, Scope: "global", MaxConcurrent: 1}}}
	e, _ := New(cfg)
	defer e.Close()
	p := mustAcquire(t, e, Identity{Stage: Attempt})
	changed := e.Policy()
	changed.Observation.Realtime = true
	changed.Observation.Resources = true
	if err := e.Update(changed); err != nil {
		t.Fatal(err)
	}
	q, err := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt}, 0)
	q.Release()
	if !IsError(err) || e.Summary().Attempts != 1 {
		t.Fatal("observation changed execution limits")
	}
	changed.Observation.Realtime = false
	changed.Observation.Resources = false
	if err := e.Update(changed); err != nil {
		t.Fatal(err)
	}
	state := e.Summary()
	if state.Resources != nil || state.Attempts != 1 {
		t.Fatal("off snapshot retains resources or drops work")
	}
	changed.Enabled = false
	if err := e.Update(changed); err != nil {
		t.Fatal(err)
	}
	if e.Summary().Attempts != 1 {
		t.Fatal("disable canceled an existing attempt")
	}
	p.Release()
	if e.Summary().Attempts != 0 {
		t.Fatal("disable prevented release")
	}
}

func TestCompiledGroupKeysPreserveLegacyAndV3Identity(t *testing.T) {
	for _, version := range []int{2, 3} {
		cfg := Config{Version: version, Rules: []Rule{{ID: "r", Stage: Attempt, Scope: "custom", GroupBy: []string{"model", "account", "key"}}}}
		raw := cfg.Rules[0]
		raw.qualifiedModel = version >= 3
		compiled := cfg.Effective().Rules[0]
		for i := 0; i < 100; i++ {
			d := Identity{Stage: Attempt, Key: fmt.Sprint(i), Account: "account:with:colons", Provider: "provider", Model: "m/(x)"}
			if raw.bucketID(d) != compiled.bucketID(d) {
				t.Fatal("compiled grouping altered an ID")
			}
		}
	}
}

// Public-API benchmark fixture also asserts the scheduler drains multiple
// batches without starving callers. Wall-clock numbers are reported, not used
// as machine-dependent pass/fail thresholds.
func TestBatchAdmissionCostAndCompletion(t *testing.T) {
	for _, size := range []struct{ queue, rules int }{{64, 16}, {200, 64}} {
		t.Run(fmt.Sprintf("q%d-r%d", size.queue, size.rules), func(t *testing.T) {
			cfg := Config{Version: 3, Enabled: true, MaxBuckets: 100000, Queue: QueueConfig{MaxWaiting: size.queue + 1, MaxWaitMS: 30000}, Rules: []Rule{{ID: "global", Stage: Attempt, Scope: "global", MaxConcurrent: 1}}}
			for i := 1; i < size.rules; i++ {
				cfg.Rules = append(cfg.Rules, Rule{ID: fmt.Sprintf("r%d", i), Stage: Attempt, Scope: "custom", GroupBy: []string{"key", "account", "model"}, MaxConcurrent: 2})
			}
			e, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			hold := mustAcquire(t, e, Identity{Stage: Attempt, Key: "first", Account: "a", Model: "m"})
			defer hold.Release()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			got := make(chan *Permit, size.queue)
			failed := make(chan error, size.queue)
			for i := 0; i < size.queue; i++ {
				go func(i int) {
					p, err := e.Acquire(ctx, Identity{Stage: Attempt, Key: fmt.Sprint(i), Account: "a", Model: "m"}, 0)
					if err != nil {
						failed <- err
					} else {
						got <- p
					}
				}(i)
			}
			deadline := time.Now().Add(10 * time.Second)
			for e.Summary().Waiting != size.queue {
				if time.Now().After(deadline) {
					t.Fatal("queue did not form")
				}
				time.Sleep(time.Millisecond)
			}
			cfg.Rules[0].MaxConcurrent = size.queue + 1
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			if err = e.Update(cfg); err != nil {
				t.Fatal(err)
			}
			update := time.Since(start)
			permits := make([]*Permit, 0, size.queue)
			for len(permits) < size.queue {
				select {
				case p := <-got:
					permits = append(permits, p)
				case err := <-failed:
					t.Fatal(err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			t.Logf("queue=%d rules=%d update_ms=%.3f all_granted_ms=%.3f allocated_MiB=%.3f", size.queue, size.rules, float64(update.Microseconds())/1000, float64(elapsed.Microseconds())/1000, float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
			if e.Summary().Attempts != size.queue+1 {
				t.Fatal("not all work admitted")
			}
			for _, p := range permits {
				p.Release()
			}
		})
	}
}
