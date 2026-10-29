package helps

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"testing"
	"time"
)

func TestOutputTimingExcludesPreparationAndResetsOnRetry(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "test", nil)
	r.requestedAt = time.Now().Add(-5 * time.Second)
	r.EnableSemanticTiming("codex")
	r.BeginResponseTiming()
	r.MarkFirstResponseByte()
	r.ObserveTimingMessage("codex", []byte(`{"type":"response.output_text.delta","delta":"first"}`))
	time.Sleep(20 * time.Millisecond)
	r.ObserveTimingMessage("codex", []byte(`{"type":"response.completed","response":{"output":[]}}`))
	record := r.buildRecord(usage.Detail{OutputTokens: 10}, false)
	measured := record.OutputTiming
	if measured == nil || !measured.Streamed || measured.DurationMS == nil || *measured.DurationMS < 20 {
		t.Fatalf("window=%+v", measured)
	}
	if *measured.DurationMS >= (record.Latency-record.TTFB).Milliseconds()-4000 {
		t.Fatal("window includes pre-dispatch preparation")
	}
	time.Sleep(10 * time.Millisecond)
	if got := r.buildRecord(usage.Detail{}, false).OutputTiming; *got.DurationMS != *measured.DurationMS {
		t.Fatal("publication delay changed observed window")
	}
	r.BeginResponseTiming()
	r.MarkFirstResponseByte()
	r.ObserveTimingMessage("codex", []byte(`{"type":"response.output_text.delta","delta":"retry"}`))
	if got := r.buildRecord(usage.Detail{}, false).OutputTiming; got.DurationMS != nil {
		t.Fatal("retry reused previous window")
	}
}

func TestOutputTimingSingleBufferedReadAndNonStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		tracker := newResponseTimingTracker(translator.FormatOpenAIResponse, stream)
		tracker.start(time.Now())
		tracker.markTTFB(time.Now())
		payload := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		if !stream {
			payload = []byte(`{"output":[{"type":"message","content":"answer"}]}`)
		}
		tracker.observeBytes(payload)
		timing := tracker.snapshot().OutputTiming
		if timing.Streamed != stream || timing.DurationMS != nil {
			t.Fatalf("single read fabricated duration: %+v", timing)
		}
	}
}
