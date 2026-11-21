package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAttemptsRemainSeparate(t *testing.T) {
	s := NewStore()
	ctx := s.Begin(context.Background(), "logical", "caller", "session", "model", "openai-response", []byte(`{"input":"secret prompt","prompt_cache_key":"secret-pck"}`))
	ref := FromContext(ctx)
	a := (&Execution{Ref: ref, ID: "execution-a", AuthIndex: "A"}).Begin("http", "openai_public", []byte(`{"model":"a"}`), nil)
	a.Status(503, false)
	a.End("completed")
	b := (&Execution{Ref: ref, ID: "execution-b", AuthIndex: "B"}).Begin("http", "openai_public", []byte(`{"model":"b","prompt_cache_options":{"comparison_response_id":"resp_base"}}`), nil)
	b.Status(200, false)
	body := b.WrapResponse(io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_b\",\"usage\":{\"input_tokens\":5},\"prompt_cache_diagnostics\":{\"type\":\"unavailable\"}}}\n\n")), "text/event-stream", "")
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
	got, ok := s.Get(ref.ID())
	if !ok || len(got.Attempts) != 2 {
		t.Fatalf("attempts=%+v", got)
	}
	if got.Attempts[0].AuthIndex != "A" || got.Attempts[0].Status != 503 || got.Attempts[0].DiagnosticReturned {
		t.Fatal("first attempt mixed with retry")
	}
	second := got.Attempts[1]
	if second.AuthIndex != "B" || second.ResponseID != "resp_b" || second.Diagnostic.Type != "unavailable" || second.CacheObservation.Read.State != "absent" {
		t.Fatalf("retry=%+v", second)
	}
	if got.Input.Fields["input"].Value != "" || got.Input.Fields["prompt_cache_key"].Value != "" {
		t.Fatal("content retained")
	}
}

func TestSnapshotDoesNotRenewOrAlias(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	ctx := s.Begin(context.Background(), "request", "scope", "", "model", "responses", []byte(`{}`))
	ref := FromContext(ctx)
	ref.Record(Event{Layer: "account", Result: "not_run", Reason: "explicit_identity"})
	one, _ := s.Get(ref.ID())
	one.Events[0].Reason = "mutated"
	two, _ := s.Get(ref.ID())
	if two.Events[0].Reason != "explicit_identity" {
		t.Fatal("query aliases live state")
	}
	now = now.Add(Retention + time.Second)
	if _, ok := s.Get(ref.ID()); ok {
		t.Fatal("query renewed retention")
	}
	if s.Overview().Stored != 1 {
		t.Fatal("query cleaned store")
	}
}

func TestEvidenceCapsAreExplicit(t *testing.T) {
	s := NewStore()
	ref := FromContext(s.Begin(context.Background(), "request", "scope", "", "model", "responses", []byte(`{}`)))
	for i := 0; i < MaxEvents+2; i++ {
		ref.Record(Event{Layer: "account", Result: "no_candidate"})
	}
	got, _ := s.Get(ref.ID())
	if !got.Truncated || len(got.Events) != MaxEvents {
		t.Fatalf("bounds=%+v", got)
	}
}

func TestResponseObservationConcurrentClose(t *testing.T) {
	store := NewStore()
	ref := FromContext(store.Begin(t.Context(), "request", "scope", "", "model", "responses", []byte(`{}`)))
	a := (&Execution{Ref: ref}).Begin("http", "custom", []byte(`{}`), nil)
	reader, writer := io.Pipe()
	body := a.WrapResponse(reader, "text/event-stream", "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(io.Discard, body) }()
	go func() {
		defer wg.Done()
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_one\"}}\n"))
		_ = body.Close()
		_ = writer.Close()
	}()
	wg.Wait()
}

func TestResponseObservationSkipsBinaryPayload(t *testing.T) {
	for _, contentType := range []string{"Application/Octet-Stream; charset=binary", "image/png", "audio/mpeg", "video/mp4"} {
		t.Run(contentType, func(t *testing.T) {
			// The payload deliberately resembles JSON so any accidental parse is observable.
			const payload = `{"usage":{"input_tokens_details":{"cached_tokens":9}}}`
			meter := (*AttemptRef)(nil).WithUsageObserver("openai", nil)
			raw := io.NopCloser(strings.NewReader(payload))
			if body := meter.WrapResponse(raw, contentType, ""); body != raw {
				t.Fatal("binary response was wrapped without a diagnostic record")
			}

			store := NewStore()
			ref := FromContext(store.Begin(t.Context(), "request", "scope", "", "model", "responses", []byte(`{}`)))
			attempt := (&Execution{Ref: ref}).Begin("http", "custom", []byte(`{}`), nil)
			body := attempt.WrapResponse(raw, contentType, "")
			data, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || string(data) != payload {
				t.Fatalf("binary response changed: %v", err)
			}
			trace, _ := store.Get(ref.ID())
			result := trace.Attempts[0]
			if result.Outcome != "body_consumed" || result.CacheObservation != nil {
				t.Fatalf("binary delivery or observation is incorrect: %+v", result)
			}
		})
	}
}

func TestResponseObservationEndsPendingSSEOnlyAtEOF(t *testing.T) {
	for _, terminate := range []string{"eof", "close"} {
		t.Run(terminate, func(t *testing.T) {
			store := NewStore()
			ref := FromContext(store.Begin(t.Context(), "request", "scope", "", "model", "responses", []byte(`{}`)))
			attempt := (&Execution{Ref: ref}).Begin("http", "custom", []byte(`{}`), nil)
			const event = "data: {\"usage\":{\"input_tokens_details\":{\"cached_tokens\":4}}}\n"
			body := attempt.WrapResponse(io.NopCloser(strings.NewReader(event)), "text/event-stream", "")
			if terminate == "eof" {
				if _, err := io.ReadAll(body); err != nil {
					t.Fatal(err)
				}
			} else if _, err := io.ReadFull(body, make([]byte, len(event))); err != nil {
				t.Fatal(err)
			}
			_ = body.Close()
			trace, _ := store.Get(ref.ID())
			result := trace.Attempts[0]
			if terminate == "eof" {
				if result.Outcome != "body_consumed" || result.CacheObservation == nil ||
					result.CacheObservation.Read.Value == nil || *result.CacheObservation.Read.Value != 4 {
					t.Fatalf("clean EOF did not finish usage: %+v", result)
				}
			} else if result.Outcome != "closed" || result.CacheObservation != nil || !result.ResponseTruncated {
				t.Fatalf("close did not discard pending usage: %+v", result)
			}
		})
	}
}

func TestByteLimitDoesNotReturnDanglingAttempt(t *testing.T) {
	store := NewStore()
	ref := FromContext(store.Begin(t.Context(), "request", "scope", "", "model", "responses", []byte(`{}`)))
	changes := make([]string, 32)
	for i := range changes {
		changes[i] = strings.Repeat("x", 192)
	}
	for i := 0; i < MaxEvents; i++ {
		ref.Record(Event{Layer: "test", Changes: changes})
	}
	for i := 0; i < MaxAttempts; i++ {
		a := (&Execution{Ref: ref, ID: strings.Repeat("x", 192)}).Begin("http", "custom", []byte(`{"model":"model"}`), nil)
		a.Status(200, false)
	}
	trace, _ := store.Get(ref.ID())
	raw, _ := json.Marshal(trace)
	if !trace.Truncated || len(raw) > MaxTraceBytes {
		t.Fatal("trace exceeds byte budget")
	}
	if bytes.Contains(raw, []byte("private-content")) {
		t.Fatal("raw prompt stored")
	}
}

func TestTraceByteRollbackClearsNewOptionalFields(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	ref := FromContext(store.Begin(t.Context(), "request", "caller", "", "model", "responses", []byte(`{}`)))
	other := FromContext(store.Begin(t.Context(), "other", "caller", "", "model", "responses", []byte(`{}`)))
	attempt := (&Execution{Ref: ref, ID: "execution"}).Begin("http", "custom", []byte(`{}`), nil)
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
	raw, _ = json.Marshal(before)
	if before.Truncated || MaxTraceBytes-len(raw) > 64 || len(raw) > MaxTraceBytes {
		t.Fatalf("fixture is not just below byte limit: bytes=%d truncated=%t", len(raw), before.Truncated)
	}
	now = now.Add(time.Second)
	attempt.Observe([]byte(`{"object":"response","id":"resp_optional_` + strings.Repeat("a", 100) + `","model":"served-model","status":"completed","usage":{"input_tokens_details":{"cached_tokens":0}},"prompt_cache_diagnostics":{"type":"cache_miss"}}`))
	after, _ := store.Get(ref.ID())
	raw, _ = json.Marshal(after)
	if len(raw) > MaxTraceBytes {
		t.Errorf("rollback exceeds byte limit: bytes=%d limit=%d", len(raw), MaxTraceBytes)
	}
	before.Truncated = true
	want, _ := json.Marshal(before)
	if !bytes.Equal(raw, want) {
		t.Error("rejected update retained new fields or changed previous evidence/timestamp")
	}
	otherTrace, _ := store.Get(other.ID())
	otherRaw, _ := json.Marshal(otherTrace)
	if got := store.Overview(); got.Bytes != len(raw)+len(otherRaw) || got.Stored != 2 {
		t.Fatalf("rollback corrupted store accounting: %+v", got)
	}
	// 回退后仍可接受能放得下的更新，并维持截断标记与正确字节计数。
	attempt.End("done")
	after, _ = store.Get(ref.ID())
	raw, _ = json.Marshal(after)
	if !after.Truncated || after.Attempts[0].Outcome != "done" || !after.UpdatedAt.Equal(now) || after.Attempts[0].ResponseID != "" || after.Attempts[0].CacheObservation != nil || after.Attempts[0].Diagnostic != nil {
		t.Fatalf("valid update after rollback lost consistency: %+v", after.Attempts[0])
	}
	if len(raw) > MaxTraceBytes || store.Overview().Bytes != len(raw)+len(otherRaw) {
		t.Fatal("store byte count drifted after a valid update")
	}
}
