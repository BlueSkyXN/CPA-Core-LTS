package redisqueue

import (
	"testing"
	"time"
)

func TestQueueRetentionSecondsPrunesByConfiguredWindow(t *testing.T) {
	withQueueState(t, func() {
		SetRetentionSeconds(120)
		now := time.Now()
		global.mu.Lock()
		global.items = []queueItem{
			{enqueuedAt: now.Add(-3 * time.Minute), payload: []byte("old")},
			{enqueuedAt: now.Add(-30 * time.Second), payload: []byte("fresh")},
		}
		global.head = 0
		global.mu.Unlock()

		items := PopOldest(10)
		if len(items) != 1 {
			t.Fatalf("PopOldest() items = %d, want 1", len(items))
		}
		if string(items[0]) != "fresh" {
			t.Fatalf("PopOldest()[0] = %q, want fresh", items[0])
		}
	})
}

func TestQueueRetentionSecondsDefaultsAndClamps(t *testing.T) {
	withQueueState(t, func() {
		SetRetentionSeconds(0)
		now := time.Now()
		global.mu.Lock()
		global.items = []queueItem{
			{enqueuedAt: now.Add(-61 * time.Second), payload: []byte("expired")},
			{enqueuedAt: now.Add(-30 * time.Second), payload: []byte("default-window")},
		}
		global.head = 0
		global.mu.Unlock()

		items := PopOldest(10)
		if len(items) != 1 || string(items[0]) != "default-window" {
			t.Fatalf("default retention items = %q, want [default-window]", items)
		}
	})

	withQueueState(t, func() {
		SetRetentionSeconds(7200)
		now := time.Now()
		global.mu.Lock()
		global.items = []queueItem{
			{enqueuedAt: now.Add(-3700 * time.Second), payload: []byte("expired")},
			{enqueuedAt: now.Add(-3500 * time.Second), payload: []byte("clamped-window")},
		}
		global.head = 0
		global.mu.Unlock()

		items := PopOldest(10)
		if len(items) != 1 || string(items[0]) != "clamped-window" {
			t.Fatalf("clamped retention items = %q, want [clamped-window]", items)
		}
	})
}

func withQueueState(t *testing.T, fn func()) {
	t.Helper()

	prevEnabled := Enabled()
	prevRetention := retentionSeconds.Load()
	SetEnabled(false)
	SetEnabled(true)
	defer func() {
		SetEnabled(false)
		SetEnabled(prevEnabled)
		retentionSeconds.Store(prevRetention)
	}()

	fn()
}
