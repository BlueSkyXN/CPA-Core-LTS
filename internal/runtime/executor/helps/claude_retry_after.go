package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxClaudeRetryAfter = 7 * 24 * time.Hour

// ParseClaudeRetryAfter extracts an upstream cooldown duration from Claude
// rate-limit response headers. Anthropic's unified reset header wins over the
// generic Retry-After fallback, and long future values are capped to the
// longest known Anthropic quota window.
func ParseClaudeRetryAfter(headers http.Header, fallbackNow time.Time) *time.Duration {
	now := fallbackNow
	if dateStr := headers.Get("Date"); dateStr != "" {
		if serverTime, err := http.ParseTime(dateStr); err == nil {
			now = serverTime
		}
	}

	if val := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-reset")); val != "" {
		if epoch, err := strconv.ParseInt(val, 10, 64); err == nil {
			if d := clampedClaudeRetryAfter(time.Unix(epoch, 0).Sub(now)); d != nil {
				return d
			}
		}
	}

	if val := strings.TrimSpace(headers.Get("Retry-After")); val != "" {
		if secs, err := strconv.ParseFloat(val, 64); err == nil && secs >= 0 {
			maxSecs := maxClaudeRetryAfter.Seconds()
			if secs > maxSecs {
				secs = maxSecs
			}
			d := time.Duration(secs * float64(time.Second))
			return &d
		}
		if resetAt, err := http.ParseTime(val); err == nil {
			if d := clampedClaudeRetryAfter(resetAt.Sub(now)); d != nil {
				return d
			}
		}
	}

	return nil
}

func clampedClaudeRetryAfter(d time.Duration) *time.Duration {
	if d < 0 {
		return nil
	}
	if d > maxClaudeRetryAfter {
		d = maxClaudeRetryAfter
	}
	return &d
}
