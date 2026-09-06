package flowcontrol

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These model IDs are fixtures, not claims about an upstream model catalogue.
func fiveRules() []Rule {
	user := strings.Repeat("a", 64)
	return []Rule{
		{ID: "special-1", Label: "指定用户的特殊模型", Stage: Request, Scope: "key", Key: user, Model: "fixture-special", MaxConcurrent: 1},
		{ID: "gpt6-total-5", Stage: Attempt, Scope: "global", Model: "fixture-gpt6*", MaxConcurrent: 5},
		{ID: "codex-account-5", Stage: Attempt, Scope: "account", Provider: "codex", MaxConcurrent: 5},
		{ID: "user-gpt6-3", Stage: Request, Scope: "key", Key: user, Model: "fixture-gpt6*", MaxConcurrent: 3},
		{ID: "user-total-4", Stage: Request, Scope: "key", Key: user, MaxConcurrent: 4},
	}
}
func TestUserScenarioOneFiveFiveThreeFour(t *testing.T) {
	e := mustEngine(t, policy(fiveRules()...))
	key := strings.Repeat("a", 64)
	d := identity(key, "fixture-gpt6", "account-a")
	d.Stage = Request
	p := []*Permit{mustAcquire(t, e, d), mustAcquire(t, e, d), mustAcquire(t, e, d)}
	x := e.Explain(d)
	if x.CanStart || len(x.BlockingRules) != 1 || x.BlockingRules[0] != "user-gpt6-3" {
		t.Fatalf("user model: %+v", x)
	}
	d.Model = "fixture-special"
	special := mustAcquire(t, e, d)
	x = e.Explain(d)
	if x.CanStart || len(x.BlockingRules) != 2 {
		t.Fatalf("special and total must both bind: %+v", x)
	}
	d.Model = "fixture-other"
	if e.Explain(d).CanStart {
		t.Fatal("user total 4 must bind for other models")
	}
	d.Key = strings.Repeat("b", 64)
	if !e.Explain(d).CanStart {
		t.Fatal("other user must not inherit selected user's limit")
	}
	for _, q := range p {
		q.Release()
	}
	special.Release()
	// Five different incoming requests on the same account: model and account
	// constraints bind together, even if an individual caller is below its cap.
	p = nil
	for i := 0; i < 5; i++ {
		d = identity(string(rune('a'+i)), "fixture-gpt6", "account-a")
		p = append(p, mustAcquire(t, e, d))
	}
	x = e.Explain(d)
	if len(x.BlockingRules) != 2 {
		t.Fatalf("expected both model/account blockers: %+v", x)
	}
	d.Model = "fixture-other"
	if e.Explain(d).CanStart {
		t.Fatal("account cap must aggregate all models")
	}
	d.Account = "account-b"
	if !e.Explain(d).CanStart {
		t.Fatal("other account unrelated model should run")
	}
	for _, q := range p {
		q.Release()
	}
}
func TestSixthStreamWaitsForProducerCompletionNotFirstChunk(t *testing.T) {
	e := mustEngine(t, policy(Rule{ID: "five", Stage: Attempt, Scope: "model", MaxConcurrent: 5}))
	var sources []chan string
	var outputs []<-chan string
	for i := 0; i < 5; i++ {
		d := identity(string(rune('a'+i)), "gpt6-fixture", "account")
		d.RequestID = string(rune('a' + i))
		p := mustAcquire(t, e, d)
		source := make(chan string, 1)
		source <- "first token"
		out := HoldChannel(context.Background(), source, p.Release)
		<-out
		sources = append(sources, source)
		outputs = append(outputs, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan *Permit, 1)
	errs := make(chan error, 1)
	go func() {
		p, err := e.Acquire(ctx, identity("sixth", "gpt6-fixture", "account"), 9)
		if err != nil {
			errs <- err
		} else {
			waiting <- p
		}
	}()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	if e.Snapshot().Attempts != 5 {
		t.Fatal("first chunks released slots")
	}
	select {
	case <-waiting:
		t.Fatal("sixth started early")
	default:
	}
	close(sources[0])
	for range outputs[0] {
	}
	select {
	case p := <-waiting:
		p.Release()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("sixth not woken")
	}
	for i := 1; i < 5; i++ {
		close(sources[i])
		for range outputs[i] {
		}
	}
	waitFor(t, func() bool { return e.Snapshot().Attempts == 0 })
}
func TestLongRunningSlotHasNoTTL(t *testing.T) {
	e := mustEngine(t, policy(rule("one", "account", 1)))
	p := mustAcquire(t, e, identity("a", "m", "same"))
	defer p.Release()
	future := time.Now().Add(48 * time.Hour)
	e.mu.Lock()
	e.now = func() time.Time { return future }
	e.mu.Unlock()
	if e.Available(identity("b", "m", "same")) {
		t.Fatal("long-running slot expired")
	}
	if s := e.Snapshot(); s.Attempts != 1 || s.Activity[0].ElapsedMS < time.Hour.Milliseconds() {
		t.Fatal(s)
	}
}
func TestCredentialAndArbitraryCrossProduct(t *testing.T) {
	c := policy(rule("acct", "account", 3), Rule{ID: "file", Stage: Attempt, Scope: "credential", MaxConcurrent: 2}, Rule{ID: "combo", Stage: Attempt, Scope: "custom", GroupBy: []string{"model", "key", "credential"}, MaxConcurrent: 1})
	e := mustEngine(t, c)
	d := identity("a", "m", "account")
	d.Credential = "cred-a"
	p := mustAcquire(t, e, d)
	if e.Available(d) {
		t.Fatal("combo not enforced")
	}
	d.Key = "b"
	q := mustAcquire(t, e, d)
	d.Key = "c"
	if e.Available(d) {
		t.Fatal("file limit not enforced")
	}
	d.Credential = "cred-b"
	z := mustAcquire(t, e, d)
	d.Credential = "cred-c"
	if e.Available(d) {
		t.Fatal("account not aggregated")
	}
	p.Release()
	q.Release()
	z.Release()
	swapped := c.Rules[2]
	swapped.GroupBy = []string{"credential", "key", "model"}
	if c.Rules[2].bucketID(d) != swapped.bucketID(d) {
		t.Fatal("dimension order must not reset counter")
	}
}
func TestAuthKindAndOldScopeKeys(t *testing.T) {
	r := Rule{ID: "oauth", Stage: Attempt, Scope: "account", AuthKind: "oauth", MaxConcurrent: 1}
	e := mustEngine(t, policy(r))
	d := identity("a", "m", "x")
	d.AuthKind = "oauth"
	p := mustAcquire(t, e, d)
	defer p.Release()
	d.AuthKind = "apikey"
	if !e.Explain(d).CanStart {
		t.Fatal("oauth rule applied to API key")
	}
	if got := rule("r", "key-model", 1).bucketID(identity("a", "b", "x")); got != "1:r7:attempt9:key-model1:a1:b" {
		t.Fatal("old bucket identity changed", got)
	}
}
func TestExplainIsReadOnlyAndReportsAllBlockers(t *testing.T) {
	e := mustEngine(t, policy(rule("a", "account", 1), rule("b", "global", 1)))
	d := identity("k", "m", "a")
	p := mustAcquire(t, e, d)
	defer p.Release()
	before := e.Snapshot()
	for i := 0; i < 10; i++ {
		x := e.Explain(d)
		if len(x.BlockingRules) != 2 || x.AdditionalSlots == nil || *x.AdditionalSlots != 0 {
			t.Fatal(x)
		}
	}
	after := e.Snapshot()
	if before.Admitted != after.Admitted || before.Rejected != after.Rejected || len(before.Buckets) != len(after.Buckets) {
		t.Fatal("explain mutated admission state")
	}
	x := e.Explain(identity("other", "m2", "unseen"))
	x.Matches[0].Rule.Label = "changed"
	if e.Snapshot().Policy.Rules[0].Label == "changed" {
		t.Fatal("config alias")
	}
}
func TestSnapshotWaitingDetailsAndDraining(t *testing.T) {
	e := mustEngine(t, policy(rule("a", "account", 1), rule("b", "global", 1)))
	d := identity("a", "m", "x")
	d.RequestID = "correlated"
	p := mustAcquire(t, e, d)
	p.MarkPhase("draining")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.Acquire(ctx, identity("b", "m", "x"), 42); done <- err }()
	waitFor(t, func() bool { return e.Snapshot().Waiting == 1 })
	s := e.Snapshot()
	if s.WaitingAttempts != 1 || len(s.Activity) != 2 || s.Activity[1].State != "draining" || s.Activity[1].RequestID != "correlated" || len(s.Activity[0].BlockingRules) != 2 {
		t.Fatalf("bad detail %+v", s)
	}
	// Summary counts the last admission blocker; selected details above show both.
	if s.Blocked["b"] != 1 {
		t.Fatal(s.Blocked)
	}
	cancel()
	<-done
	p.Release()
}
func TestCustomRuleValidationAndClone(t *testing.T) {
	for _, dims := range [][]string{{}, {"key", "key"}, {"file"}} {
		c := policy(Rule{ID: "c", Stage: Attempt, Scope: "custom", GroupBy: dims, MaxConcurrent: 1})
		if c.Validate() == nil {
			t.Fatal(dims)
		}
	}
	c := policy(Rule{ID: "c", Stage: Request, Scope: "custom", GroupBy: []string{"credential"}, MaxConcurrent: 1})
	if c.Validate() == nil {
		t.Fatal("request can't know credential")
	}
	c = policy(Rule{ID: "c", Stage: Attempt, Scope: "custom", GroupBy: []string{"credential", "model"}, MaxConcurrent: 1})
	e := mustEngine(t, c)
	c.Rules[0].GroupBy[0] = "invalid"
	if reflect.DeepEqual(c.Rules, e.Snapshot().Policy.Rules) {
		t.Fatal("rules were not copied")
	}
}
func TestStatusSSEInitialSnapshotAndDisconnect(t *testing.T) {
	cfg := policy(rule("one", "global", 1))
	cfg.Observation.Realtime = true
	e := mustEngine(t, cfg)
	server := httptest.NewServer(http.HandlerFunc(e.ServeEvents))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var frame struct {
				Schema int      `json:"schema-version"`
				State  Snapshot `json:"state"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Schema != 3 || frame.State.ProcessID == "" {
				t.Fatal(frame)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing initial frame")
	}
	cancel()
	resp.Body.Close()
	waitFor(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.observers == 0 })
	if e.Snapshot().Attempts != 0 || e.Snapshot().Requests != 0 {
		t.Fatal("observer counted as work")
	}
}
func TestStatusObserverCap(t *testing.T) {
	cfg := policy(rule("one", "global", 1))
	cfg.Observation.Realtime = true
	e := mustEngine(t, cfg)
	e.mu.Lock()
	e.observers = MaxObservers
	e.mu.Unlock()
	w := httptest.NewRecorder()
	e.ServeEvents(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Header())
	}
	e.mu.Lock()
	e.observers = 0
	e.mu.Unlock()
}

func TestExplainMissingCredentialIsUnknownNotUnused(t *testing.T) {
	e := mustEngine(t, policy(Rule{ID: "cap", Stage: Attempt, Scope: "credential", Provider: "codex", MaxConcurrent: 5}))
	x := e.Explain(Identity{Stage: Attempt, Key: "anonymous", Model: "fixture", Provider: "codex"})
	if x.Complete || x.CanStart || x.AdditionalSlots != nil || len(x.Matches) != 1 || x.Matches[0].Known || !reflect.DeepEqual(x.Unresolved, []string{"credential"}) {
		t.Fatalf("missing target must not be treated as a free bucket: %+v", x)
	}
	x = e.Explain(Identity{Stage: Attempt, Key: "anonymous", Model: "fixture", Provider: "other"})
	if !x.Complete || len(x.Matches) != 0 {
		t.Fatalf("known mismatch should not require credential: %+v", x)
	}
	x = e.Explain(Identity{Stage: Attempt, Key: "anonymous", Model: "fixture", Provider: "codex", Credential: "file-a"})
	if !x.Complete || !x.CanStart || *x.AdditionalSlots != 5 {
		t.Fatalf("complete target: %+v", x)
	}
	if len(e.buckets) != 0 {
		t.Fatal("preview created buckets")
	}
}

func TestExplainAdditionalStartsIntersectsWindowRemaining(t *testing.T) {
	e := mustEngine(t, policy(Rule{ID: "both", Stage: Attempt, Scope: "global", MaxConcurrent: 5, Windows: []Window{{Requests: 2, PeriodMS: 60000}}}))
	d := identity("key", "model", "account")
	p := mustAcquire(t, e, d)
	defer p.Release()
	x := e.Explain(d)
	if !x.Complete || !x.CanStart || x.AdditionalSlots == nil || *x.AdditionalSlots != 1 {
		t.Fatalf("4 free concurrent slots but only 1 available rate start: %+v", x)
	}
}
