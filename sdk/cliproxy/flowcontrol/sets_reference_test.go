package flowcontrol

import (
	"context"
	"math/rand"
	"strings"
	"testing"
)

// A simple record-scan reference, independent of the engine's buckets and
// selection helpers. It deliberately compares all intersecting model sets.
func TestV3ModelSetsReference5000Actions(t *testing.T) {
	keys := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	accounts := []string{strings.Repeat("d", 64), strings.Repeat("e", 64)}
	rules := []Rule{setRule("total", nil, nil, 8), setRule("s12", []string{"m1", "m2"}, []string{"key", "account"}, 3), setRule("s23", []string{"m2", "m3"}, []string{"key", "account"}, 3), setRule("triple", nil, []string{"key", "account", "model"}, 2)}
	rules = append(rules, Rule{ID: "chosen-users", Stage: Attempt, Scope: "custom", GroupBy: []string{"key"}, Keys: keys[:2], Accounts: accounts[:1], Models: []string{"m1", "m3"}, MaxConcurrent: 2})
	e, err := New(v3Policy(rules...))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	type record struct {
		d Identity
		p *Permit
	}
	active := []record{}
	rng := rand.New(rand.NewSource(9227))
	member := func(values []string, v string) bool {
		if values == nil {
			return true
		}
		for _, x := range values {
			if x == v {
				return true
			}
		}
		return false
	}
	matches := func(r Rule, d Identity) bool {
		return member(r.Models, d.Model) && member(r.Keys, d.Key) && member(r.Accounts, d.Account)
	}
	same := func(r Rule, a, b Identity) bool {
		for _, d := range r.GroupBy {
			switch d {
			case "key":
				if a.Key != b.Key {
					return false
				}
			case "account":
				if a.Account != b.Account {
					return false
				}
			case "model":
				if a.Model != b.Model || a.Provider != b.Provider {
					return false
				}
			}
		}
		return true
	}
	for step := 0; step < 5000; step++ {
		if len(active) > 0 && rng.Intn(3) == 0 {
			n := rng.Intn(len(active))
			active[n].p.Release()
			active[n].p.Release()
			active = append(active[:n], active[n+1:]...)
			continue
		}
		d := Identity{Stage: Attempt, Key: keys[rng.Intn(len(keys))], Account: accounts[rng.Intn(len(accounts))], Model: []string{"m1", "m2", "m3"}[rng.Intn(3)], Provider: "p"}
		allowed := true
		for _, r := range rules {
			if !matches(r, d) {
				continue
			}
			n := 0
			for _, a := range active {
				if matches(r, a.d) && same(r, d, a.d) {
					n++
				}
			}
			if n >= r.MaxConcurrent {
				allowed = false
			}
		}
		p, err := e.AcquireImmediately(context.Background(), d, 0)
		if (err == nil) != allowed {
			t.Fatalf("step=%d expected=%v err=%v", step, allowed, err)
		}
		if err == nil {
			active = append(active, record{d, p})
		}
		if e.Summary().Attempts != len(active) {
			t.Fatal("partial/double accounting")
		}
	}
	for _, a := range active {
		a.p.Release()
	}
	if e.Summary().Attempts != 0 {
		t.Fatal("leak")
	}
}
