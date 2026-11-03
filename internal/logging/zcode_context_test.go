package logging

import (
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterPrintsZcodeContextCounts(t *testing.T) {
	for _, delta := range []int64{-404400, 0, 100500} {
		entry := log.NewEntry(log.New()).WithFields(log.Fields{
			"request_id": "synthetic-12345678", "mode": "observe", "status": "counted",
			"provider_total_tokens": int64(505500), "original_response_total_tokens": int64(505500),
			"counted_context_tokens": int64(505500) + delta, "context_count_delta_tokens": delta,
			"client_total_tokens": int64(505500), "client_total_source": "translated_usage",
			"context_count_applied": false, "duration_ms": int64(5), "prompt": "must-not-print",
		})
		entry.Message = "zcode context usage"
		formatted, err := (&LogFormatter{}).Format(entry)
		if err != nil {
			t.Fatal(err)
		}
		line := string(formatted)
		for _, key := range []string{"mode", "status", "provider_total_tokens", "original_response_total_tokens", "counted_context_tokens", "context_count_delta_tokens", "client_total_tokens", "client_total_source", "context_count_applied", "duration_ms"} {
			if !strings.Contains(line, key+"="+formatLogFieldValue(key, entry.Data[key])) {
				t.Fatalf("formatted log missing %s: %s", key, line)
			}
		}
		if !strings.Contains(line, "[12345678]") || strings.Contains(line, "must-not-print") {
			t.Fatalf("request correlation or field filtering changed: %s", line)
		}
		entry.Message = "unrelated event"
		formatted, _ = (&LogFormatter{}).Format(entry)
		if strings.Contains(string(formatted), "client_total_tokens") {
			t.Fatal("context-specific fields escaped into unrelated logs")
		}
	}
}
