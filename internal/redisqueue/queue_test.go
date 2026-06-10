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

func TestEnqueueBroadcastsToUsageSubscribersAndSkipsQueue(t *testing.T) {
	withQueueState(t, func() {
		first, unsubscribeFirst := SubscribeUsage()
		defer unsubscribeFirst()
		second, unsubscribeSecond := SubscribeUsage()
		defer unsubscribeSecond()

		requireUsageSubscriberPayload(t, first, usageSupportRefreshPayload)
		requireUsageSubscriberPayload(t, second, usageSupportRefreshPayload)

		Enqueue([]byte("usage-record"))

		requireUsageSubscriberPayload(t, first, "usage-record")
		requireUsageSubscriberPayload(t, second, "usage-record")

		if items := PopOldest(1); len(items) != 0 {
			t.Fatalf("PopOldest() items = %q, want empty after subscriber broadcast", items)
		}

		unsubscribeFirst()
		unsubscribeSecond()

		Enqueue([]byte("queued-record"))
		items := PopOldest(1)
		if len(items) != 1 || string(items[0]) != "queued-record" {
			t.Fatalf("PopOldest() items = %q, want queued record after unsubscribe", items)
		}
	})
}

func TestSetEnabledFalseClosesUsageSubscribers(t *testing.T) {
	withQueueState(t, func() {
		subscriber, unsubscribe := SubscribeUsage()
		defer unsubscribe()
		errorSubscriber, unsubscribeErrors := SubscribeErrors()
		defer unsubscribeErrors()

		requireUsageSubscriberPayload(t, subscriber, usageSupportRefreshPayload)

		SetEnabled(false)

		select {
		case _, ok := <-subscriber:
			if ok {
				t.Fatalf("subscriber channel remained open after SetEnabled(false)")
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for subscriber close")
		}

		select {
		case _, ok := <-errorSubscriber:
			if ok {
				t.Fatalf("error subscriber channel remained open after SetEnabled(false)")
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for error subscriber close")
		}
	})
}

func TestEnqueueErrorBroadcastsToErrorSubscribersAndDiscardsWithoutSubscribers(t *testing.T) {
	withEnabledQueue(t, func() {
		subscriber, unsubscribe := SubscribeErrors()
		defer unsubscribe()

		EnqueueError([]byte("error-record"))
		requireUsageSubscriberPayload(t, subscriber, "error-record")

		unsubscribe()

		EnqueueError([]byte("discarded-error"))
		requireErrorQueueEmpty(t)
	})
}

func TestNotifyUsageRefreshBroadcastsOnlyToUsageSubscribers(t *testing.T) {
	withEnabledQueue(t, func() {
		subscriber, unsubscribe := SubscribeUsage()
		defer unsubscribe()
		errorSubscriber, unsubscribeErrors := SubscribeErrors()
		defer unsubscribeErrors()

		requireUsageSubscriberPayload(t, subscriber, usageSupportRefreshPayload)

		NotifyUsageRefresh()
		requireUsageSubscriberPayload(t, subscriber, usageRefreshPayload)

		select {
		case got := <-errorSubscriber:
			t.Fatalf("error subscriber received usage refresh payload %q", string(got))
		default:
		}

		unsubscribe()
		NotifyUsageRefresh()
		if items := PopOldest(1); len(items) != 0 {
			t.Fatalf("PopOldest() items = %q, want empty after refresh notification without subscribers", items)
		}
	})
}

func requireUsageSubscriberPayload(t *testing.T, subscriber <-chan []byte, want string) {
	t.Helper()

	select {
	case got, ok := <-subscriber:
		if !ok {
			t.Fatalf("subscriber closed before receiving %q", want)
		}
		if string(got) != want {
			t.Fatalf("subscriber payload = %q, want %q", string(got), want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for subscriber payload %q", want)
	}
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

func requireErrorQueueEmpty(t *testing.T) {
	t.Helper()

	errorGlobal.mu.Lock()
	defer errorGlobal.mu.Unlock()

	if len(errorGlobal.items)-errorGlobal.head != 0 {
		t.Fatalf("error queue retained %d item(s), want none", len(errorGlobal.items)-errorGlobal.head)
	}
}
