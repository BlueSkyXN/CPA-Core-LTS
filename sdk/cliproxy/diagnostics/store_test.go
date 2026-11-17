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
