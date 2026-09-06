package flowcontrol

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// Compare admissions against a deliberately simple independent reference over
// 5,000 deterministic arrivals/completions, across all seven dimensions.
func TestSevenDimensionsAgainstReferenceScheduler(t *testing.T) {
	rules := []Rule{
		{ID: "g", Stage: Attempt, Scope: "global", MaxConcurrent: 9, Windows: []Window{{Requests: 20, PeriodMS: 100}, {Requests: 100, PeriodMS: 1000}}},
		{ID: "k", Stage: Attempt, Scope: "key", MaxConcurrent: 3, Windows: []Window{{Requests: 8, PeriodMS: 100}}},
		{ID: "m", Stage: Attempt, Scope: "model", MaxConcurrent: 4},
		{ID: "km", Stage: Attempt, Scope: "key-model", MaxConcurrent: 2},
		{ID: "p", Stage: Attempt, Scope: "provider", MaxConcurrent: 8},
		{ID: "a", Stage: Attempt, Scope: "account", MaxConcurrent: 3, Windows: []Window{{Requests: 12, PeriodMS: 250}}},
		{ID: "am", Stage: Attempt, Scope: "account-model", MaxConcurrent: 1},
	}
	e := mustEngine(t, Config{Enabled: true, Rules: rules})
	now := time.Now()
	e.now = func() time.Time { return now }
	counts := map[string]int{}
	histories := map[string][]time.Time{}
	group := func(r Rule, d Identity) string {
		switch r.Scope {
		case "global":
			return r.ID
		case "key":
			return fmt.Sprintf("%s/%q", r.ID, d.Key)
		case "model":
			return fmt.Sprintf("%s/%q", r.ID, d.Model)
		case "key-model":
			return fmt.Sprintf("%s/%q/%q", r.ID, d.Key, d.Model)
		case "provider":
			return fmt.Sprintf("%s/%q", r.ID, d.Provider)
		case "account":
			return fmt.Sprintf("%s/%q", r.ID, d.Account)
		default:
			return fmt.Sprintf("%s/%q/%q", r.ID, d.Account, d.Model)
		}
	}
	type lease struct {
		p *Permit
		d Identity
	}
	var held []lease
	rng := rand.New(rand.NewSource(1701))
	for step := 0; step < 5000; step++ {
		now = now.Add(time.Duration(rng.Intn(12)) * time.Millisecond)
		if len(held) > 0 && rng.Intn(3) == 0 {
			i := rng.Intn(len(held))
			l := held[i]
			l.p.Release()
			l.p.Release()
			for _, r := range rules {
				counts[group(r, l.d)]--
			}
			held = append(held[:i], held[i+1:]...)
		}
		d := identity(fmt.Sprint(rng.Intn(4)), fmt.Sprint(rng.Intn(4)), fmt.Sprint(rng.Intn(4)))
		allowed := true
		for _, r := range rules {
			id := group(r, d)
			if counts[id] >= r.MaxConcurrent {
				allowed = false
			}
			for _, w := range r.Windows {
				cut := now.Add(-time.Duration(w.PeriodMS) * time.Millisecond)
				n := 0
				for _, at := range histories[id] {
					if at.After(cut) {
						n++
					}
				}
				if n >= w.Requests {
					allowed = false
				}
			}
		}
		p, err := e.AcquireImmediately(context.Background(), d, 0)
		if (err == nil) != allowed {
			t.Fatalf("step %d reference=%v error=%v", step, allowed, err)
		}
		if err != nil {
			if !IsError(err) {
				t.Fatal(err)
			}
			continue
		}
		for _, r := range rules {
			id := group(r, d)
			counts[id]++
			histories[id] = append(histories[id], now)
		}
		held = append(held, lease{p, d})
	}
	for _, l := range held {
		l.p.Release()
	}
	if e.Snapshot().Attempts != 0 {
		t.Fatal("outstanding leases")
	}
}
