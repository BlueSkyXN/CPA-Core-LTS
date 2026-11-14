package helps

import (
	"testing"
	"time"
)

// TestPluginBareJSONTimingWindow locks the plugin executor stream shape:
// plugins deframe upstream SSE and emit bare JSON chunks (no "data:" prefix,
// no trailing newline). Those frames must mark the stream observed so the
// output window (duration_ms) is measurable, and the first content delta must
// record TTFA. Regression: PRO deepseek-v4.1-flash records all had
// streamed:false and empty OutputTPS because of this gap (verified end-to-end
// 2026-11-15 with a real CodeBuddy stream through the plugin host).
func TestPluginBareJSONTimingWindow(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.EnableSemanticTiming("openai")
	reporter.StartResponseTiming()
	frames := []string{
		`{"choices":[{"delta":{"content":"","role":"assistant"},"index":0}]}`,
		`{"choices":[{"delta":{"content":"18"},"index":0}]}`,
		`{"choices":[{"delta":{"content":"2"},"index":0}]}`,
		`{"choices":[{"delta":{"content":""},"finish_reason":"stop","index":0}],"usage":{"total_tokens":9}}`,
	}
	for _, frame := range frames {
		reporter.MarkFirstResponseByte()
		reporter.ObserveTimingPayload("openai", []byte(frame))
		time.Sleep(20 * time.Millisecond)
	}
	snapshot := reporter.timingSnapshot()
	if snapshot.OutputTiming == nil || !snapshot.OutputTiming.Streamed {
		t.Fatalf("bare JSON frames did not mark stream observed: %+v", snapshot.OutputTiming)
	}
	if snapshot.OutputTiming.DurationMS == nil || *snapshot.OutputTiming.DurationMS <= 0 {
		t.Fatalf("output window missing: %+v", snapshot.OutputTiming)
	}
	if snapshot.TTFA <= 0 {
		t.Fatalf("TTFA not captured from content delta: %+v", snapshot)
	}
}
