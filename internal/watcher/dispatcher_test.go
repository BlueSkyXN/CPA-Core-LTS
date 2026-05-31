package watcher

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPrepareAuthUpdatesLockedNilQueueInitialAdd(t *testing.T) {
	w := &Watcher{}

	updates := w.prepareAuthUpdatesLocked([]*coreauth.Auth{{ID: "a", Provider: "p"}}, false)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d: %+v", len(updates), updates)
	}
	if updates[0].Action != AuthUpdateActionAdd || updates[0].ID != "a" {
		t.Fatalf("expected add for auth a, got %+v", updates[0])
	}
	if w.currentAuths["a"] == nil {
		t.Fatal("expected current auth state to be initialized")
	}
}

func TestPrepareAuthUpdatesLockedNilQueueSubsequentDiff(t *testing.T) {
	w := &Watcher{
		currentAuths: map[string]*coreauth.Auth{
			"a": {ID: "a", Provider: "old"},
			"c": {ID: "c", Provider: "p"},
		},
	}

	updates := w.prepareAuthUpdatesLocked([]*coreauth.Auth{
		{ID: "a", Provider: "new"},
		{ID: "b", Provider: "p"},
	}, false)

	got := map[string]AuthUpdateAction{}
	for _, update := range updates {
		got[update.ID] = update.Action
	}
	want := map[string]AuthUpdateAction{
		"a": AuthUpdateActionModify,
		"b": AuthUpdateActionAdd,
		"c": AuthUpdateActionDelete,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d updates, got %d: %+v", len(want), len(got), updates)
	}
	for id, action := range want {
		if got[id] != action {
			t.Fatalf("expected %s for auth %s, got %s in %+v", action, id, got[id], updates)
		}
	}
	if w.currentAuths["a"].Provider != "new" || w.currentAuths["b"] == nil {
		t.Fatalf("expected current auth state to update, got %+v", w.currentAuths)
	}
	if _, ok := w.currentAuths["c"]; ok {
		t.Fatalf("expected deleted auth c to be removed, got %+v", w.currentAuths)
	}
}
