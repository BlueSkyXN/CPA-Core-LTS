package flowcontrol

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHoldChannelReleasesOnlyAtProducerEnd(t *testing.T) {
	src := make(chan int)
	var releases atomic.Int32
	out := HoldChannel(context.Background(), src, func() { releases.Add(1) })
	go func() { src <- 1 }()
	if <-out != 1 || releases.Load() != 0 {
		t.Fatal("first chunk released active slot")
	}
	close(src)
	for range out {
	}
	if releases.Load() != 1 {
		t.Fatal("terminal release count")
	}
}
func TestHoldChannelCanceledProducerStaysCountedUntilStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := make(chan int)
	var released atomic.Bool
	out := HoldChannel(ctx, src, func() { released.Store(true) })
	cancel()
	time.Sleep(10 * time.Millisecond)
	if released.Load() {
		t.Fatal("released while canceled producer was still live")
	}
	close(src)
	for range out {
	}
	if !released.Load() {
		t.Fatal("did not release when producer closed")
	}
}
func TestFlowControlHTTPStreamingFixture(t *testing.T) {
	e, err := New(Config{Enabled: true, Rules: []Rule{{ID: "one", Stage: Request, Scope: "global", MaxConcurrent: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := e.Acquire(r.Context(), Identity{Stage: Request, Key: "client"}, 0)
		if err != nil {
			w.WriteHeader(429)
			return
		}
		defer p.Release()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	other, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if other.StatusCode != 429 {
		t.Fatal("slot released on headers/first chunk")
	}
	close(finish)
	resp.Body.Close()
	until := time.Now().Add(time.Second)
	for e.Snapshot().Requests != 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if e.Snapshot().Requests != 0 {
		t.Fatal("HTTP slot leaked")
	}
}
func TestHotProjectionRejectsTooSmallCapAtomically(t *testing.T) {
	c := Config{Enabled: true, MaxBuckets: 10, Rules: []Rule{{ID: "all", Stage: Request, Scope: "global", MaxConcurrent: 10}}}
	e, _ := New(c)
	defer e.Close()
	a, _ := e.Acquire(context.Background(), Identity{Stage: Request, Key: "a"}, 0)
	b, _ := e.Acquire(context.Background(), Identity{Stage: Request, Key: "b"}, 0)
	defer a.Release()
	defer b.Release()
	next := c
	next.MaxBuckets = 1
	next.Rules = []Rule{{ID: "key", Stage: Request, Scope: "key", MaxConcurrent: 10}}
	if e.Update(next) == nil {
		t.Fatal("must reject impossible live projection")
	}
	if e.Snapshot().Requests != 2 || e.Snapshot().Buckets[0].Rule != "all" {
		t.Fatal("partial config applied")
	}
}

func TestCaseFoldedModelSharesLimit(t *testing.T) {
	e, _ := New(Config{Enabled: true, Rules: []Rule{{ID: "model", Stage: Request, Scope: "model", Model: "GPT-*", MaxConcurrent: 1}}})
	defer e.Close()
	p, err := e.Acquire(context.Background(), Identity{Stage: Request, Model: "gpt-X"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	if _, err = e.Acquire(context.Background(), Identity{Stage: Request, Model: "GPT-x"}, 0); err == nil {
		t.Fatal("case bypassed model cap")
	}
}
func TestAccountAvailabilityHintDoesNotCreateUnknownModelBucket(t *testing.T) {
	e, _ := New(Config{Enabled: true, Rules: []Rule{{ID: "model", Stage: Attempt, Scope: "account-model", MaxConcurrent: 1}, {ID: "account", Stage: Attempt, Scope: "account", MaxConcurrent: 2}}})
	defer e.Close()
	d := Identity{Stage: Attempt, Account: "one", Model: "gpt-a"}
	p, _ := e.Acquire(context.Background(), d, 0)
	defer p.Release()
	d.Model = ""
	if !e.AvailableAccount(d) {
		t.Fatal("model-free account hint unexpectedly blocked")
	}
	for _, b := range e.Snapshot().Buckets {
		if b.Rule == "model" && b.Model == "" {
			t.Fatal("hint created guessed model bucket")
		}
	}
}

func TestImmediateAdmissionNeverQueuesOrSpendsWindow(t *testing.T) {
	e, err := New(Config{Enabled: true, Queue: QueueConfig{MaxWaiting: 2, MaxWaitMS: 500}, Rules: []Rule{{ID: "one", Stage: Attempt, Scope: "account", MaxConcurrent: 1, Windows: []Window{{Requests: 9, PeriodMS: 1000}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	d := Identity{Stage: Attempt, Account: "one"}
	p, err := e.Acquire(context.Background(), d, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	_, err = e.AcquireImmediately(context.Background(), d, 0)
	var busy *Error
	if !errors.As(err, &busy) || busy.Code != "flow_control_busy" {
		t.Fatalf("want immediate busy: %v", err)
	}
	s := e.Snapshot()
	if s.Waiting != 0 || s.Waited != 0 || s.Attempts != 1 || s.Buckets[0].WindowCounts[0] != 1 {
		t.Fatalf("unexpected state: %+v", s)
	}
}
