package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestParseClaudeRetryAfterUnifiedReset(t *testing.T) {
	now := time.Unix(1_777_482_352, 0).UTC()
	header := http.Header{
		"Anthropic-Ratelimit-Unified-Reset": {"1777500000"},
		"Retry-After":                       {"30"},
	}

	got := ParseClaudeRetryAfter(header, now)
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if want := 17648 * time.Second; *got != want {
		t.Fatalf("retryAfter = %v, want %v", *got, want)
	}
}

func TestParseClaudeRetryAfterFallsBackWhenUnifiedResetIsPast(t *testing.T) {
	now := time.Unix(1_777_482_352, 0).UTC()
	header := http.Header{
		"Anthropic-Ratelimit-Unified-Reset": {"1777400000"},
		"Retry-After":                       {"45"},
	}

	got := ParseClaudeRetryAfter(header, now)
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if want := 45 * time.Second; *got != want {
		t.Fatalf("retryAfter = %v, want %v", *got, want)
	}
}

func TestParseClaudeRetryAfterUsesServerDateAnchor(t *testing.T) {
	serverNow := time.Unix(1_777_482_352, 0).UTC()
	clientNow := serverNow.Add(10 * time.Minute)
	header := http.Header{
		"Date":                              {serverNow.Format(http.TimeFormat)},
		"Anthropic-Ratelimit-Unified-Reset": {"1777482652"},
	}

	got := ParseClaudeRetryAfter(header, clientNow)
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if want := 5 * time.Minute; *got != want {
		t.Fatalf("retryAfter = %v, want %v", *got, want)
	}
}

func TestParseClaudeRetryAfterClampsLongCooldowns(t *testing.T) {
	now := time.Unix(1_777_482_352, 0).UTC()
	header := http.Header{"Retry-After": {"99999999999"}}

	got := ParseClaudeRetryAfter(header, now)
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if *got != maxClaudeRetryAfter {
		t.Fatalf("retryAfter = %v, want %v", *got, maxClaudeRetryAfter)
	}
}

func TestParseClaudeRetryAfterPreservesZero(t *testing.T) {
	got := ParseClaudeRetryAfter(http.Header{"Retry-After": {"0"}}, time.Now())
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if *got != 0 {
		t.Fatalf("retryAfter = %v, want 0", *got)
	}
}
