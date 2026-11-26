package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestAttemptObserveDeduplicatesUnchangedChunks(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ctx := store.Begin(t.Context(), "req-dedup", "caller", "session", "gpt-5", "openai", []byte(`{}`))
	ref := FromContext(ctx)
	attempt := (&Execution{Ref: ref, ID: "exec-dedup", Provider: "openai"}).Begin("http", "https://api.openai.com/v1/chat/completions", []byte(`{}`), nil)

	chunk := []byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5","choices":[{"delta":{"content":"hi"}}]}`)
	now = now.Add(time.Second)
	attempt.Observe(chunk)

	store.mu.Lock()
	revAfterFirst := store.entries[ref.ID()].revision
	updatedAfterFirst := store.entries[ref.ID()].value.UpdatedAt
	store.mu.Unlock()
	_, lookupRev1 := store.Lookup(TraceQuery{ID: "exec-dedup"})

	for i := 0; i < 1000; i++ {
		now = now.Add(time.Millisecond)
		attempt.Observe(chunk)
	}

	store.mu.Lock()
	revAfterRepeats := store.entries[ref.ID()].revision
	updatedAfterRepeats := store.entries[ref.ID()].value.UpdatedAt
	store.mu.Unlock()
	_, lookupRev2 := store.Lookup(TraceQuery{ID: "exec-dedup"})

	if revAfterRepeats != revAfterFirst {
		t.Fatalf("revision bumped on unchanged chunks: got %d, want %d", revAfterRepeats, revAfterFirst)
	}
	if !updatedAfterRepeats.Equal(updatedAfterFirst) {
		t.Fatalf("UpdatedAt changed on unchanged chunks: got %v, want %v", updatedAfterRepeats, updatedAfterFirst)
	}
	if lookupRev2 != lookupRev1 {
		t.Fatalf("Lookup revision changed on unchanged chunks: %q != %q", lookupRev2, lookupRev1)
	}

	// Genuine usage delta must still advance revision and UpdatedAt.
	now = now.Add(time.Second)
	geminiChunk := []byte(`{"responseId":"resp_gem_1","usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4}}`)
	attempt.Observe(geminiChunk)

	store.mu.Lock()
	revAfterGemini := store.entries[ref.ID()].revision
	updatedAfterGemini := store.entries[ref.ID()].value.UpdatedAt
	store.mu.Unlock()
	if revAfterGemini != revAfterFirst+1 || !updatedAfterGemini.Equal(now) {
		t.Fatalf("genuine delta did not update store: rev=%d (want %d) updated=%v (want %v)", revAfterGemini, revAfterFirst+1, updatedAfterGemini, now)
	}

	for i := 0; i < 10; i++ {
		now = now.Add(time.Millisecond)
		attempt.Observe(geminiChunk)
	}
	store.mu.Lock()
	revAfterGeminiRepeats := store.entries[ref.ID()].revision
	store.mu.Unlock()
	if revAfterGeminiRepeats != revAfterGemini {
		t.Fatalf("repeated gemini chunk bumped revision: got %d, want %d", revAfterGeminiRepeats, revAfterGemini)
	}
}

func TestAttemptObserveUnchangedSkipsStoreLock(t *testing.T) {
	store := NewStore()
	ref := FromContext(store.Begin(t.Context(), "req-lock", "caller", "", "gpt-5", "openai", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-lock", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)

	chunk := []byte(`{"id":"resp_lock","object":"chat.completion.chunk","usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":2}}}`)
	attempt.Observe(chunk)
	attempt.Status(200, false)
	attempt.RecordUsage(usage.Detail{InputTokens: 5, CacheObservation: &usage.CacheObservation{Version: 1, Read: usage.CacheTokenObservation{State: "present", Value: usage.Int64Ptr(2), Source: "input_tokens_details.cached_tokens"}, Write: usage.CacheTokenObservation{State: "absent"}}})
	attempt.Truncate()

	encodedAttempt := (&Execution{Ref: ref, ID: "exec-lock-encoded", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)
	_ = encodedAttempt.WrapResponse(io.NopCloser(strings.NewReader("")), "application/json", "gzip")

	store.mu.Lock()
	done := make(chan struct{})
	go func() {
		attempt.Observe(chunk)
		attempt.Status(200, false)
		attempt.RecordUsage(usage.Detail{InputTokens: 5})
		attempt.Truncate()
		attempt.End("response_received")
		_ = encodedAttempt.WrapResponse(io.NopCloser(strings.NewReader("")), "application/json", "gzip")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		store.mu.Unlock()
		t.Fatal("unchanged attempt operations blocked on Store.mu")
	}
	store.mu.Unlock()

	attempt.End("done")
	store.mu.Lock()
	doneEnd := make(chan struct{})
	go func() {
		attempt.End("done")
		close(doneEnd)
	}()
	select {
	case <-doneEnd:
	case <-time.After(2 * time.Second):
		store.mu.Unlock()
		t.Fatal("unchanged End blocked on Store.mu")
	}
	store.mu.Unlock()
}

func TestAttemptObserveEmptyUsageTransitionsOnce(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 5, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "req-empty-usage", "caller", "", "gpt-5", "openai", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-empty-usage", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)

	chunk := []byte(`{"object":"chat.completion.chunk","id":"chat_empty","usage":{}}`)
	now = now.Add(time.Second)
	attempt.Observe(chunk)

	trace, ok := store.Get(ref.ID())
	if !ok || trace.Attempts[0].CacheObservation == nil ||
		trace.Attempts[0].CacheObservation.Read.State != "absent" ||
		trace.Attempts[0].CacheObservation.Write.State != "absent" {
		t.Fatalf("unexpected cache observation after first empty usage: %+v", trace.Attempts)
	}
	store.mu.Lock()
	rev1 := store.entries[ref.ID()].revision
	store.mu.Unlock()

	now = now.Add(time.Second)
	attempt.Observe(chunk)
	store.mu.Lock()
	rev2 := store.entries[ref.ID()].revision
	updated2 := store.entries[ref.ID()].value.UpdatedAt
	store.mu.Unlock()
	if rev2 != rev1 || !updated2.Equal(trace.UpdatedAt) {
		t.Fatalf("repeated empty usage mutated store: rev %d->%d updated %v->%v", rev1, rev2, trace.UpdatedAt, updated2)
	}
}

func TestAttemptObserveClaudeMessageStopAndTTL(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 10, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "req-claude", "caller", "", "claude-sonnet", "claude", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-claude", Provider: "claude"}).Begin("http", "custom", []byte(`{}`), nil)
	// Deliberately pass a non-"claude" observer provider to verify attemptProvider is preserved.
	attempt = attempt.WithUsageObserver("custom-observer", nil)

	msgStart := []byte(`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet","usage":{"input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0}}}}`)
	now = now.Add(time.Second)
	attempt.Observe(msgStart)
	store.mu.Lock()
	revStart := store.entries[ref.ID()].revision
	store.mu.Unlock()

	trace, _ := store.Get(ref.ID())
	val, has5m := trace.Attempts[0].CacheWriteByTTL["5m"]
	if !has5m || val != 0 || trace.Attempts[0].ObservationState != "partial" {
		t.Fatalf("unexpected message_start state: %+v", trace.Attempts[0])
	}

	now = now.Add(time.Second)
	attempt.Observe(msgStart)
	store.mu.Lock()
	revStartRepeat := store.entries[ref.ID()].revision
	store.mu.Unlock()
	if revStartRepeat != revStart {
		t.Fatalf("duplicate message_start bumped revision: %d -> %d", revStart, revStartRepeat)
	}

	msgStop := []byte(`{"type":"message_stop"}`)
	now = now.Add(time.Second)
	attempt.Observe(msgStop)
	store.mu.Lock()
	revStop := store.entries[ref.ID()].revision
	store.mu.Unlock()
	if revStop != revStart+1 {
		t.Fatalf("message_stop did not bump revision: %d -> %d", revStart, revStop)
	}
	trace, _ = store.Get(ref.ID())
	if trace.Attempts[0].ObservationState != "observed" || trace.Attempts[0].ResponseStatus != "completed" {
		t.Fatalf("message_stop did not finalize claude attempt: %+v", trace.Attempts[0])
	}

	now = now.Add(time.Second)
	attempt.Observe(msgStop)
	store.mu.Lock()
	revStopRepeat := store.entries[ref.ID()].revision
	store.mu.Unlock()
	if revStopRepeat != revStop {
		t.Fatalf("duplicate message_stop bumped revision: %d -> %d", revStop, revStopRepeat)
	}
}

func TestAttemptObserveResponseIDSwitchAndCap(t *testing.T) {
	store := NewStore()
	ref := FromContext(store.Begin(t.Context(), "req-ids", "caller", "", "gpt-5", "openai", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-ids", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)

	rev := func() uint64 {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.entries[ref.ID()].revision
	}

	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r1"}`))
	r1 := rev()
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r1"}`))
	if rev() != r1 {
		t.Fatalf("duplicate r1 bumped revision")
	}
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r2"}`))
	r2 := rev()
	if r2 != r1+1 {
		t.Fatalf("r2 did not bump revision")
	}
	// Switching back to previously seen r1 must still update ResponseID.
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r1"}`))
	r1Again := rev()
	if r1Again != r2+1 {
		t.Fatalf("switching back to r1 did not bump revision")
	}
	trace, _ := store.Get(ref.ID())
	if trace.Attempts[0].ResponseID != "r1" || len(trace.Attempts[0].ResponseIDs) != 2 {
		t.Fatalf("unexpected response IDs: %+v", trace.Attempts[0])
	}
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r1"}`))
	if rev() != r1Again {
		t.Fatalf("duplicate r1 after switch bumped revision")
	}

	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r3"}`))
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r4"}`))
	r4 := rev()
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r5"}`))
	r5 := rev()
	if r5 != r4+1 {
		t.Fatalf("r5 truncation did not bump revision")
	}
	trace, _ = store.Get(ref.ID())
	if !trace.Attempts[0].ResponseTruncated || len(trace.Attempts[0].ResponseIDs) != 4 || trace.Attempts[0].ResponseID != "r5" {
		t.Fatalf("unexpected cap state: %+v", trace.Attempts[0])
	}
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"r5"}`))
	attempt.Truncate()
	if rev() != r5 {
		t.Fatalf("repeated r5 or Truncate bumped revision: %d -> %d", r5, rev())
	}
}

func TestAttemptLifecycleDedupStatusEndTruncateRecordUsage(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 15, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "req-life", "caller", "sess-a", "gpt-5", "openai", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-life", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)

	rev := func() uint64 {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.entries[ref.ID()].revision
	}

	// SetSession deduplication
	before := rev()
	ref.SetSession("sess-a")
	if rev() != before {
		t.Fatalf("SetSession with identical session bumped revision")
	}
	ref.SetSession("sess-b")
	if rev() != before+1 {
		t.Fatalf("SetSession with new session did not bump revision")
	}

	// Status / End / Truncate / RecordUsage deduplication
	r := rev()
	attempt.Status(200, false)
	if rev() != r+1 {
		t.Fatalf("Status(200) did not bump revision")
	}
	r = rev()
	attempt.Status(200, false)
	if rev() != r {
		t.Fatalf("duplicate Status(200) bumped revision")
	}
	attempt.Status(502, true)
	if rev() != r+1 {
		t.Fatalf("Status(502, true) did not bump revision")
	}
	r = rev()
	attempt.End("transport_error")
	if rev() != r {
		t.Fatalf("End with same outcome bumped revision")
	}
	attempt.End("completed")
	if rev() != r+1 {
		t.Fatalf("End with new outcome did not bump revision")
	}
	r = rev()
	detail := usage.Detail{
		InputTokens: 42,
		CacheObservation: &usage.CacheObservation{
			Version: 1,
			Read:    usage.CacheTokenObservation{State: "present", Value: usage.Int64Ptr(12), Source: "input_tokens_details.cached_tokens"},
			Write:   usage.CacheTokenObservation{State: "absent"},
		},
	}
	attempt.RecordUsage(detail)
	if rev() != r+1 {
		t.Fatalf("RecordUsage did not bump revision")
	}
	r = rev()
	attempt.RecordUsage(detail)
	if rev() != r {
		t.Fatalf("duplicate RecordUsage bumped revision")
	}

	// Whitebox uninitialized AttemptRef construction syncs safely on first call and dedups second call.
	whitebox := &AttemptRef{ref: ref, number: 1}
	whitebox.End("completed")
	if rev() != r {
		t.Fatalf("whitebox unchanged End bumped revision")
	}
	whitebox.End("final")
	if rev() != r+1 {
		t.Fatalf("whitebox changed End did not bump revision")
	}
	r = rev()
	whitebox.End("final")
	if rev() != r {
		t.Fatalf("whitebox repeat End bumped revision")
	}

	// Capped Record and Begin do not bump revision once Truncated is already true.
	for i := 0; i < MaxEvents+5; i++ {
		ref.Record(Event{Layer: "test", Method: "m", Result: "r"})
	}
	r = rev()
	ref.Record(Event{Layer: "test", Method: "m", Result: "r"})
	if rev() != r {
		t.Fatalf("capped Record bumped revision when already Truncated")
	}
	for i := 1; i < MaxAttempts+3; i++ {
		(&Execution{Ref: ref, ID: fmt.Sprintf("exec-%d", i), Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)
	}
	r = rev()
	if got := (&Execution{Ref: ref, ID: "exec-overflow", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil); got != nil {
		t.Fatalf("expected nil beyond MaxAttempts, got %+v", got)
	}
	if rev() != r {
		t.Fatalf("capped Begin bumped revision when already Truncated")
	}
}

func TestStoreUpdateReusesRawAndPreservesRollback(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 20, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "req-raw", "caller", "", "model", "responses", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-raw"}).Begin("http", "custom", []byte(`{}`), nil)

	changes := make([]string, 32)
	for i := range changes {
		changes[i] = strings.Repeat("x", 192)
	}
	for i := 0; i < 10; i++ {
		ref.Record(Event{Layer: "filler", Changes: changes})
	}
	trace, _ := store.Get(ref.ID())
	tail := Event{Sequence: len(trace.Events) + 1, At: now, Layer: "filler", Changes: []string{}}
	candidate := trace
	candidate.Events = append(append([]Event(nil), trace.Events...), tail)
	raw, _ := json.Marshal(candidate)
	budget := MaxTraceBytes - 32 - len(raw)
	for budget > 4 && len(tail.Changes) < 32 {
		overhead := 2
		if len(tail.Changes) > 0 {
			overhead++
		}
		n := min(budget-overhead, 192)
		tail.Changes = append(tail.Changes, strings.Repeat("x", n))
		budget -= n + overhead
	}
	ref.Record(tail)

	before, _ := store.Get(ref.ID())
	now = now.Add(time.Second)

	// Oversized Observe overflows MaxTraceBytes and rolls back.
	attempt.Observe([]byte(`{"object":"response","id":"resp_overflow","model":"served-model","status":"completed","usage":{"input_tokens_details":{"cached_tokens":0}},"prompt_cache_diagnostics":{"type":"cache_miss","reason":"` + strings.Repeat("z", 120) + `"}}`))

	store.mu.Lock()
	e := store.entries[ref.ID()]
	revAfterFirstRollback := e.revision
	rawCopy := bytes.Clone(e.raw)
	valueJSON, _ := json.Marshal(e.value)
	store.mu.Unlock()
	if !bytes.Equal(rawCopy, valueJSON) {
		t.Fatalf("entry.raw diverged from entry.value after rollback")
	}

	after, _ := store.Get(ref.ID())
	if !after.Truncated || !after.UpdatedAt.Equal(before.UpdatedAt) || after.Attempts[0].ResponseID != "" {
		t.Fatalf("rollback failed to preserve UpdatedAt or clear rolled-back fields: %+v", after)
	}

	// Second oversized Observe when already Truncated must not bump revision or UpdatedAt.
	now = now.Add(time.Second)
	attempt.Observe([]byte(`{"object":"response","id":"resp_overflow","model":"served-model","status":"completed","usage":{"input_tokens_details":{"cached_tokens":0}},"prompt_cache_diagnostics":{"type":"cache_miss","reason":"` + strings.Repeat("z", 120) + `"}}`))
	store.mu.Lock()
	revAfterSecondRollback := store.entries[ref.ID()].revision
	updatedAfterSecondRollback := store.entries[ref.ID()].value.UpdatedAt
	store.mu.Unlock()
	if revAfterSecondRollback != revAfterFirstRollback || !updatedAfterSecondRollback.Equal(before.UpdatedAt) {
		t.Fatalf("repeat overflow when already Truncated mutated store: rev %d->%d", revAfterFirstRollback, revAfterSecondRollback)
	}

	// Rolled-back Observe must NOT poison AttemptRef cached state; smaller fitting update with same ID must succeed.
	attempt.End("done")
	afterEnd, _ := store.Get(ref.ID())
	if afterEnd.Attempts[0].Outcome != "done" || !afterEnd.UpdatedAt.Equal(now) {
		t.Fatalf("fitting End after rollback was skipped: %+v", afterEnd.Attempts[0])
	}
}

func TestAttemptObserveConcurrentRace(t *testing.T) {
	store := NewStore()
	ctx := store.Begin(t.Context(), "req-race", "caller", "session", "gpt-5", "openai", []byte(`{}`))
	ref := FromContext(ctx)
	var mu sync.Mutex
	var observed int
	attempt := (&Execution{Ref: ref, ID: "exec-race", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)
	attempt = attempt.WithUsageObserver("openai", func(obs *usage.CacheObservation) {
		mu.Lock()
		if obs != nil {
			observed++
		}
		mu.Unlock()
	})

	chunks := [][]byte{
		[]byte(`{"object":"chat.completion.chunk","id":"r1","model":"gpt-5"}`),
		[]byte(`{"object":"chat.completion.chunk","id":"r1","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4}}}`),
		[]byte(`{"object":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4}}}}`),
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				attempt.Observe(chunks[(id+i)%len(chunks)])
				attempt.Status(200, false)
				attempt.RecordUsage(usage.Detail{InputTokens: 10})
				if i%10 == 0 {
					_, _ = store.Get(ref.ID())
					_, _ = store.Lookup(TraceQuery{})
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := attempt.WrapResponse(io.NopCloser(strings.NewReader("data: {\"object\":\"chat.completion.chunk\",\"id\":\"r1\"}\n\n")), "text/event-stream", "")
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
	}()
	wg.Wait()
	attempt.End("done")

	trace, ok := store.Get(ref.ID())
	if !ok || len(trace.Attempts) != 1 || trace.Attempts[0].Outcome != "done" {
		t.Fatalf("unexpected concurrent trace state: ok=%t trace=%+v", ok, trace)
	}
	mu.Lock()
	obsCount := observed
	mu.Unlock()
	if obsCount == 0 {
		t.Fatal("expected usage observer callbacks to fire")
	}
}

func TestWrapResponseSSERevisionStability(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 11, 26, 8, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "req-sse", "caller", "", "gpt-5", "openai", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "exec-sse", Provider: "openai"}).Begin("http", "custom", []byte(`{}`), nil)

	pr, pw := io.Pipe()
	body := attempt.WrapResponse(pr, "text/event-stream; charset=utf-8", "")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
	}()

	writeEvent := func(payload string) {
		if _, err := fmt.Fprintf(pw, "data: %s\n\n", payload); err != nil {
			t.Fatal(err)
		}
	}

	writeEvent(`{"object":"chat.completion.chunk","id":"chat_sse_1","model":"gpt-5","choices":[{"delta":{"content":"a"}}]}`)
	// Wait briefly by polling until the first chunk is observed by the reader goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if trace, ok := store.Get(ref.ID()); ok && trace.Attempts[0].ResponseID == "chat_sse_1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for first SSE chunk observation")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, revAfterFirst := store.Lookup(TraceQuery{ID: "exec-sse"})

	for i := 0; i < 100; i++ {
		writeEvent(`{"object":"chat.completion.chunk","id":"chat_sse_1","model":"gpt-5","choices":[{"delta":{"content":"b"}}]}`)
	}
	// Send a sentinel chunk that doesn't pass the Observe filter? No, send a synchronous pipe write of a recognizable trailing frame?
	// Writing one more chunk and waiting for completion via pipe close ensures all 100 chunks were read;
	// verify stability BEFORE closing by doing a Lookup right after writing the 100th chunk, and also check revision delta after EOF.
	_, revMidStream := store.Lookup(TraceQuery{ID: "exec-sse"})
	if revMidStream != revAfterFirst {
		t.Fatalf("SSE identical chunks destabilized cursor revision: %q != %q", revMidStream, revAfterFirst)
	}

	_ = pw.Close()
	wg.Wait()

	// Exactly 1 more revision bump from End("body_consumed") (and none from the 100 identical content chunks).
	store.mu.Lock()
	finalRev := store.entries[ref.ID()].revision
	store.mu.Unlock()
	// Expected revisions: Begin (1) + first Observe (2) + End("body_consumed") (3).
	if finalRev != 3 {
		t.Fatalf("unexpected total revisions for 101 identical SSE chunks + EOF: got %d, want 3", finalRev)
	}
}
