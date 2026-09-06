package flowcontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestV3ObservationSwitchIndependentOfExecution(t *testing.T) {
	cfg := v3Policy(setRule("all", nil, nil, 1))
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt}, 0)
	defer p.Release()
	w := httptest.NewRecorder()
	e.ServeEvents(w, httptest.NewRequest("GET", "/events", nil))
	if w.Code != 409 || e.Summary().Attempts != 1 {
		t.Fatal("disabled observation changed model execution")
	}
	cfg.Observation.Realtime = true
	cfg.Observation.IntervalMS = 500
	if err = e.Update(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(e.ServeEvents))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scan := bufio.NewScanner(res.Body)
	seen := false
	for scan.Scan() {
		if strings.HasPrefix(scan.Text(), "data:") {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatal("missing initial snapshot")
	}
	cfg.Observation.Realtime = false
	if err = e.Update(cfg); err != nil {
		t.Fatal(err)
	}
	disabled := false
	for scan.Scan() {
		if strings.Contains(scan.Text(), "realtime-disabled") {
			disabled = true
		}
	}
	if !disabled || e.Summary().Attempts != 1 {
		t.Fatal("turning off observer stopped work or omitted disabled event")
	}
	e.mu.Lock()
	observers := e.observers
	e.mu.Unlock()
	if observers != 0 {
		t.Fatal("observer leak", observers)
	}
}
func TestV3ObserversShareEncodedSummary(t *testing.T) {
	e, _ := New(v3Policy(setRule("r", nil, nil, 4)))
	defer e.Close()
	var wg sync.WaitGroup
	results := make(chan []byte, 16)
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := e.cachedSummaryJSON()
			if err != nil {
				t.Error(err)
			}
			results <- b
		}()
	}
	wg.Wait()
	close(results)
	var first []byte
	for b := range results {
		if first == nil {
			first = b
		}
		if !bytes.Equal(first, b) {
			t.Fatal("observers duplicated different snapshots")
		}
	}
	if e.summaryBuilds != 1 {
		t.Fatal("built per observer", e.summaryBuilds)
	}
	for _, key := range []string{`"activity"`, `"buckets"`, `"rules"`, `"policy"`} {
		if bytes.Contains(first, []byte(key)) {
			t.Fatal("heavy fields in summary", key)
		}
	}
	cfg := e.Policy()
	cfg.Observation.Realtime = true
	e.Update(cfg)
	e.cachedSummaryJSON()
	if e.summaryBuilds != 2 {
		t.Fatal("policy change did not invalidate cache")
	}
}
func TestV3ManualDetailsSelectBeforeExplain(t *testing.T) {
	cfg := v3Policy(setRule("s12", []string{"m1", "m2"}, nil, 1), setRule("s23", []string{"m2", "m3"}, nil, 1))
	cfg.Queue = QueueConfig{MaxWaiting: 2000, MaxWaitingPerKey: 2000, MaxWaitMS: 300000}
	e, _ := New(cfg)
	defer e.Close()
	p, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Model: "m2"}, 0)
	defer p.Release()
	// Synthetic wait records isolate snapshot cost. These are not real network requests.
	e.mu.Lock()
	now := e.now()
	for n := 0; n < 1000; n++ {
		e.queue = append(e.queue, &waiter{id: uint64(n + 1), identity: Identity{Stage: Attempt, Key: "u", Model: "m2"}, enqueued: now, deadline: now.Add(time.Minute), ctx: context.Background(), done: make(chan struct{}), blocked: "s23"})
	}
	e.mu.Unlock()
	page := e.Details(DetailsOptions{Offset: 990, Limit: 5, State: "waiting"})
	if len(page.Activity) != 5 || page.MatchingTotal != 1000 || !page.ActivityTruncated {
		t.Fatalf("bad page: %+v", page)
	}
	for i, row := range page.Activity {
		if row.ID != fmt.Sprintf("wait-%d", 991+i) || len(row.BlockingRules) != 2 {
			t.Fatal(row)
		}
	}
	summary := e.Summary()
	if summary.Waiting != 1000 || summary.Blocked["s23"] != 1000 {
		t.Fatal("summary basis incorrect")
	}
	e.mu.Lock()
	e.queue = nil
	e.mu.Unlock() // synthetic records own no blocked goroutines
}
func TestV3ResourcesAreOptInAndCached(t *testing.T) {
	e, _ := New(Config{})
	defer e.Close()
	if e.Summary().Resources != nil || !e.resourceExpires.IsZero() {
		t.Fatal("unexpected resource polling")
	}
	c := e.Policy()
	c.Observation.Resources = true
	if err := e.Update(c); err != nil {
		t.Fatal(err)
	}
	a := e.Summary().Resources
	b := e.Summary().Resources
	if a == nil || !a.SampledAt.Equal(b.SampledAt) || a.Goroutines < 1 {
		t.Fatal("sampling not cached")
	}
	c.Observation.Resources = false
	e.Update(c)
	if e.Summary().Resources != nil {
		t.Fatal("resource disabling ignored")
	}
}
func TestV3PreviewUsesRuntimeProjectionAndKnownUsage(t *testing.T) {
	cfg := v3Policy(setRule("r", []string{"m1", "m2"}, []string{"model", "key"}, 2))
	e, _ := New(cfg)
	defer e.Close()
	d := Identity{Stage: Attempt, Key: "u", Model: "m1", Provider: "p"}
	p, _ := e.AcquireImmediately(context.Background(), d, 0)
	defer p.Release()
	cfg.Rules[0].Scope = "key-model"
	cfg.Rules[0].GroupBy = nil
	cfg.Rules[0].MaxConcurrent = 1
	x, err := e.Preview(&cfg, []Identity{d})
	if err != nil {
		t.Fatal(err)
	}
	if !x[0].Matches[0].Known || x[0].Matches[0].Active != 1 || x[0].CanStart {
		t.Fatal("equivalent alias fabricated zero usage", x)
	}
	cfg.Rules[0].Models = []string{"m1", "m3"}
	x, err = e.Preview(&cfg, []Identity{d})
	if err != nil || x[0].Matches[0].Known {
		t.Fatal("changed selector should be unknown", x, err)
	}
	if e.Summary().Admitted != 1 {
		t.Fatal("preview reserved capacity")
	}
}
func TestV3HotUpdateEquivalentProjectionPreservesWindow(t *testing.T) {
	cfg := v3Policy(setRule("r", []string{"m1", "m2"}, []string{"key", "model"}, 0))
	cfg.Rules[0].Windows = []Window{{Requests: 1, PeriodMS: 60000}}
	e, _ := New(cfg)
	defer e.Close()
	d := Identity{Stage: Attempt, Key: "u", Model: "m1", Provider: "p"}
	p, _ := e.AcquireImmediately(context.Background(), d, 0)
	p.Release()
	cfg.Rules[0].Scope = "key-model"
	cfg.Rules[0].GroupBy = nil
	if err := e.Update(cfg); err != nil {
		t.Fatal(err)
	}
	if e.Available(d) {
		t.Fatal("changing UI projection alias reset recent admissions")
	}
}
func TestV3PreviewAndObservationBounds(t *testing.T) {
	for _, o := range []ObservationConfig{{IntervalMS: 499}, {IntervalMS: 30001}, {MaxObservers: 17}} {
		if _, err := New(Config{Observation: o}); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	e, _ := New(v3Policy(setRule("r", nil, nil, 1)))
	defer e.Close()
	for _, targets := range [][]Identity{make([]Identity, 25), {{Stage: Attempt, Model: "m*"}}, {{Stage: Attempt, Key: strings.Repeat("x", 513)}}} {
		if _, err := e.Preview(nil, targets); err == nil {
			t.Fatal("invalid preview accepted")
		}
	}
	w := httptest.NewRecorder()
	e.ServeEvents(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	cfg := e.Policy()
	cfg.Observation.Realtime = true
	cfg.Observation.MaxObservers = 1
	e.Update(cfg)
	e.mu.Lock()
	e.observers = 1
	e.mu.Unlock()
	w = httptest.NewRecorder()
	e.ServeEvents(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	e.mu.Lock()
	e.observers = 0
	e.mu.Unlock()
}
func TestV3MigrationDuringExecutionIsAtomic(t *testing.T) {
	cfg := Config{Enabled: true, Rules: []Rule{{ID: "r", Stage: Attempt, Scope: "global", MaxConcurrent: 1}}}
	e, _ := New(cfg)
	defer e.Close()
	p, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt}, 0)
	cfg.Version = 3
	if e.Update(cfg) == nil || e.Version() != 0 || e.Summary().Attempts != 1 {
		t.Fatal("version migrated under active execution")
	}
	p.Release()
	if err := e.Update(cfg); err != nil || e.Version() != 3 {
		t.Fatal("drained migration failed", err)
	}
}
func BenchmarkV3Observation(b *testing.B) {
	for _, size := range []struct {
		name                   string
		active, waiting, rules int
	}{{"small", 5, 1, 5}, {"medium", 50, 100, 20}, {"large", 200, 1000, 64}} {
		cfg := v3Policy()
		for n := 0; n < size.rules; n++ {
			cfg.Rules = append(cfg.Rules, setRule(fmt.Sprintf("rule-%d", n), nil, nil, size.active))
		}
		e, _ := New(cfg)
		for n := 0; n < size.active; n++ {
			e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Key: fmt.Sprint(n), Model: "m", Provider: "p"}, 0)
		}
		now := time.Now()
		for n := 0; n < size.waiting; n++ {
			e.queue = append(e.queue, &waiter{id: uint64(n + 1), identity: Identity{Stage: Attempt, Key: fmt.Sprint(n), Model: "m", Provider: "p"}, enqueued: now, deadline: now.Add(time.Hour), ctx: context.Background(), done: make(chan struct{}), blocked: "rule-0"})
		}
		b.Run(size.name+"/summary", func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				json.Marshal(e.Summary())
			}
		})
		b.Run(size.name+"/details100", func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				json.Marshal(e.Details(DetailsOptions{Limit: 100}))
			}
		})
		e.queue = nil
		e.Close()
	}
}

func TestV3RateDomainEditNeedsExplicitNewHistory(t *testing.T) {
	c := v3Policy(setRule("r", []string{"m1", "m2"}, nil, 3))
	c.Rules[0].Windows = []Window{{Requests: 3, PeriodMS: 1000}}
	e, _ := New(c)
	defer e.Close()
	p, _ := e.AcquireImmediately(context.Background(), Identity{Stage: Attempt, Model: "m1"}, 0)
	p.Release()
	c.Rules[0].Models = []string{"m2", "m3"}
	if e.Update(c) == nil {
		t.Fatal("old domain history silently reinterpreted")
	}
	if !strings.Contains(strings.Join(e.Policy().Rules[0].Models, ","), "m1") {
		t.Fatal("failed update partially replaced policy")
	}
	c.Rules[0].ID = "new-domain"
	if err := e.Update(c); err != nil {
		t.Fatal(err)
	}
}
func TestV3ExplicitEmptySelectionJSONCannotBecomeAll(t *testing.T) {
	r := setRule("r", []string{}, nil, 1)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"models":[]`)) {
		t.Fatal("empty selection omitted", string(raw))
	}
	var restored Rule
	if json.Unmarshal(raw, &restored) != nil || restored.Models == nil {
		t.Fatal("empty lost")
	}
	if v3Policy(restored).Validate() == nil {
		t.Fatal("empty enabled draft accepted")
	}
	r.Models = nil
	raw, _ = json.Marshal(r)
	if bytes.Contains(raw, []byte(`"models"`)) {
		t.Fatal("all should be omitted")
	}
}
func TestV3ActiveSlotsHaveNoElapsedTTL(t *testing.T) {
	c := v3Policy(Rule{ID: "call", Stage: Request, Scope: "global", MaxConcurrent: 1}, setRule("attempt", nil, nil, 1))
	e, _ := New(c)
	defer e.Close()
	call := e.BeginRequest(Identity{Stage: Request})
	p, err := e.AcquireForRequest(context.Background(), call, Identity{Stage: Attempt}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	p.CommitDispatch()
	e.mu.Lock()
	future := time.Now().Add(48 * time.Hour)
	e.now = func() time.Time { return future }
	e.mu.Unlock()
	s := e.Summary()
	if s.Requests != 1 || s.Attempts != 1 {
		t.Fatal("time expired live slot")
	}
	call.Release()
	if e.Summary().Requests != 1 {
		t.Fatal("consumer end dropped live producer")
	}
	p.Release()
	if s = e.Summary(); s.Attempts+s.Requests != 0 {
		t.Fatal("release failed")
	}
}

func TestV3ExplicitEmptyYAMLSelectorMapCannotBecomeAll(t *testing.T) {
	r := Rule{ID: "empty", Stage: Attempt, Scope: "custom", GroupBy: []string{}, Models: []string{}, MaxConcurrent: 1}
	x, err := r.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	m := x.(map[string]any)
	if v, ok := m["models"]; !ok || v == nil || len(v.([]string)) != 0 {
		t.Fatal("empty set widened to all")
	}
	if _, ok := m["accounts"]; ok {
		t.Fatal("omitted selector materialized")
	}
	if _, ok := m["group-by"]; !ok {
		t.Fatal("shared group disappeared")
	}
}
