package flowcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSummaryBreakdownSharedPanelFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/grouped-summary.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		State Summary `json:"state"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	e, _ := New(v3Policy())
	defer e.Close()
	k, k2, a, a2 := strings.Repeat("a", 64), strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	identities := []Identity{
		{Stage: Request, Key: k, Model: "public-alpha"}, {Stage: Request, Key: k, Model: "public-alpha"},
		{Stage: Attempt, Key: k, Model: "alpha", Provider: "p", Account: a}, {Stage: Attempt, Key: k, Model: "alpha", Provider: "p", Account: a},
		{Stage: Attempt, Key: k2, Model: "alpha", Provider: "p", Account: a2}, {Stage: Attempt, Key: k, Model: "beta", Provider: "p", Account: a2},
	}
	for i, d := range identities {
		e.active[uint64(i+1)] = &active{identity: d}
	}
	for _, d := range []Identity{identities[2], identities[5]} {
		e.queue = append(e.queue, &waiter{identity: d, enqueued: time.Now()})
	}
	s := e.Summary()
	e.queue = nil
	if s.Requests != fixture.State.Requests || s.Attempts != fixture.State.Attempts || s.Waiting != fixture.State.Waiting || !reflect.DeepEqual(s.Breakdown, fixture.State.Breakdown) {
		t.Fatal("Core aggregation differs from the shared Panel wire fixture")
	}
}

func TestSummaryBreakdownCountsRecordsInsteadOfRuleMemberships(t *testing.T) {
	cfg := v3Policy(setRule("shared", nil, nil, 10), setRule("per-model", nil, []string{"model"}, 10))
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	call := e.BeginRequest(Identity{Stage: Request, Key: "caller", Model: "public"})
	defer call.Release()
	d := Identity{Stage: Attempt, Key: "caller", Model: "actual", Provider: "provider", Account: "account", RequestID: "not-in-summary"}
	p, err := e.AcquireForRequest(context.Background(), call, d, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	p.MarkPhase("draining")
	s := e.Summary()
	if s.Requests != 1 || s.Attempts != 1 || len(s.Breakdown.Groups) != 2 || s.Breakdown.Truncated {
		t.Fatal("unexpected joint-admission counts", s)
	}
	for _, group := range s.Breakdown.Groups {
		if group.Active != 1 || group.Waiting != 0 {
			t.Fatal("rule memberships duplicated the record", group)
		}
		if group.Stage == Request && (group.Model != "public" || group.Account != "" || group.Provider != "") {
			t.Fatal("request acquired an upstream identity", group)
		}
		if group.Stage == Attempt && (group.Model != "actual" || group.Account != "account") {
			t.Fatal("lost actual target", group)
		}
	}
	raw, _ := json.Marshal(s)
	if bytes.Contains(raw, []byte(d.RequestID)) || bytes.Contains(raw, []byte(`"rules"`)) {
		t.Fatal("summary contains request details")
	}
	cfg.Enabled = false
	if err := e.Update(cfg); err != nil {
		t.Fatal(err)
	}
	if after := e.Summary(); after.Attempts != 1 || !reflect.DeepEqual(after.Breakdown, s.Breakdown) {
		t.Fatal("disabling lost draining observations")
	}
}

func TestSummaryBreakdownQueuesJointFirstAdmissionOnceAtSelectedTarget(t *testing.T) {
	e, _ := New(v3Policy())
	defer e.Close()
	call := e.BeginRequest(Identity{Stage: Request, Key: "caller", Model: "public"})
	now := time.Now()
	e.queue = []*waiter{
		{request: call, identity: Identity{Stage: Attempt, Key: "caller", Model: "actual", Provider: "provider", Account: "account"}, enqueued: now},
		{identity: Identity{Stage: Attempt, Key: "caller", Model: "actual", Provider: "provider", Account: "account"}, enqueued: now},
		{identity: Identity{Stage: Request, Key: "caller", Model: "public"}, enqueued: now},
	}
	s := e.Summary()
	e.queue = nil // Synthetic waiters do not own channels or capacity.
	if s.Waiting != 3 || s.WaitingRequests != 2 || s.WaitingAttempts != 1 || len(s.Breakdown.Groups) != 2 {
		t.Fatal("queue totals changed", s)
	}
	for _, group := range s.Breakdown.Groups {
		want := 1
		if group.Stage == Attempt {
			want = 2
		}
		if group.Active != 0 || group.Waiting != want {
			t.Fatal("joint waiter duplicated or assigned to public alias", group)
		}
	}
}

func TestSummaryBreakdownBoundIsDeterministicAndConservesOmittedCounts(t *testing.T) {
	build := func(reverse bool) *SummaryBreakdown {
		var c breakdownCollector
		for i := 0; i < 600; i++ {
			n := i
			if reverse {
				n = 599 - i
			}
			d := Identity{Stage: Attempt, Model: fmt.Sprintf("model-%04d", n)}
			c.add(d, 1, 0)
			c.add(d, 0, 2)
		}
		return c.finish()
	}
	a, b := build(false), build(true)
	if !reflect.DeepEqual(a, b) || len(a.Groups) != MaxSummaryGroups || !a.Truncated {
		t.Fatal("observation selection depends on map/iteration order")
	}
	if a.Omitted.Attempts != 600-MaxSummaryGroups || a.Omitted.Waiting != 2*(600-MaxSummaryGroups) {
		t.Fatal("omitted counters lost records", a.Omitted)
	}
	for _, group := range a.Groups {
		if group.Active != 1 || group.Waiting != 2 {
			t.Fatal("retained group has partial counts", group)
		}
	}
}

func TestSummaryBreakdownBoundsEscapedIdentityBytesAndEmptyState(t *testing.T) {
	var c breakdownCollector
	c.add(Identity{Stage: Attempt, Model: strings.Repeat("<", maxSummaryIdentityBytes)}, 3, 4)
	c.add(Identity{Stage: Request, Model: strings.Repeat("x", 4096)}, 2, 0)
	for i := 0; i < MaxSummaryGroups; i++ {
		c.add(Identity{Stage: Attempt, Model: fmt.Sprintf("%04d", i) + strings.Repeat("<", 150)}, 1, 0)
	}
	b := c.finish()
	if b.Omitted.Requests != 2 || b.Omitted.Attempts != 3 || b.Omitted.Waiting != 4 || len(b.Groups) != MaxSummaryGroups {
		t.Fatal("long identities lost totals", b.Omitted)
	}
	raw, err := json.Marshal(Summary{Breakdown: b})
	if err != nil || len(raw) > 200*1024 {
		t.Fatal("breakdown exceeds bounded SSE payload", len(raw), err)
	}
	e, _ := New(Config{})
	defer e.Close()
	raw, _ = json.Marshal(e.Summary())
	if !bytes.Contains(raw, []byte(`"groups":[]`)) || e.Summary().Breakdown.Truncated {
		t.Fatal("empty observation must be a complete zero snapshot")
	}
}
