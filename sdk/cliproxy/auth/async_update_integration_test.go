package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAsyncUpdateKeepsLiveEditsAndRejectsReregister(t *testing.T) {
	m := NewManager(nil, nil, nil)
	base, err := m.Register(context.Background(), &Auth{ID: "async-regression", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	edited := base.Clone()
	edited.ProxyURL = "http://operator-proxy"
	edited.Label = "operator"
	if _, err = m.Update(context.Background(), edited); err != nil {
		t.Fatal(err)
	}
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "rotated"
	got, applied, err := m.updateFromAsync(context.Background(), base, refreshed)
	if err != nil || !applied {
		t.Fatalf("update: applied=%v err=%v", applied, err)
	}
	if got.ProxyURL != edited.ProxyURL || got.Label != "operator" || got.Metadata["access_token"] != "rotated" {
		t.Fatalf("merged auth = %#v", got)
	}
	if _, err = m.Register(context.Background(), &Auth{ID: base.ID, Provider: "codex", Metadata: map[string]any{"access_token": "replacement"}}); err != nil {
		t.Fatal(err)
	}
	_, applied, _ = m.updateFromAsync(context.Background(), base, refreshed)
	if applied {
		t.Fatal("stale refresh changed reregistered credential")
	}
}

type asyncPersistTestStore struct {
	mu      sync.Mutex
	count   int
	last    *Auth
	started chan struct{}
	release chan struct{}
}

func (s *asyncPersistTestStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *asyncPersistTestStore) Delete(context.Context, string) error  { return nil }
func (s *asyncPersistTestStore) Save(_ context.Context, a *Auth) (string, error) {
	s.mu.Lock()
	s.count++
	first := s.count == 1
	s.mu.Unlock()
	if first {
		close(s.started)
		<-s.release
	}
	s.mu.Lock()
	s.last = a.Clone()
	s.mu.Unlock()
	return a.ID, nil
}
func TestAsyncPersistenceKeepsNewestSnapshot(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	old, err := m.Register(ctx, &Auth{ID: "persist-regression", Provider: "codex", Metadata: map[string]any{"access_token": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &asyncPersistTestStore{started: make(chan struct{}), release: make(chan struct{})}
	m.store = store
	var once sync.Once
	unblock := func() { once.Do(func() { close(store.release) }) }
	defer unblock()
	firstDone := make(chan error, 1)
	go func() { firstDone <- m.persist(ctx, old) }()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("old save did not start")
	}
	updated := old.Clone()
	updated.Metadata["access_token"] = "new"
	updateDone := make(chan error, 1)
	go func() { _, err := m.Update(ctx, updated); updateDone <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.RLock()
		latest := m.auths[old.ID].Clone()
		m.mu.RUnlock()
		if latest.Metadata["access_token"] == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update blocked global auth state")
		}
		time.Sleep(time.Millisecond)
	}
	unblock()
	for _, done := range []<-chan error{firstDone, updateDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("save did not finish")
		}
	}
	store.mu.Lock()
	last := store.last.Clone()
	store.mu.Unlock()
	if last.Metadata["access_token"] != "new" {
		t.Fatalf("old write replaced newer credential: %#v", last.Metadata)
	}
	// A delayed caller with the old public generation must also save the newest
	// snapshot, never roll the persistent store back.
	if err = m.persist(ctx, old); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.last.Metadata["access_token"] != "new" {
		t.Fatal("late stale persistence reverted token")
	}
}
