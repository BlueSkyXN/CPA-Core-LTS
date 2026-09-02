package helps

import (
	"testing"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClassifySemanticTimingAcrossSupportedFormats(t *testing.T) {
	tests := []struct {
		name      string
		format    sdktranslator.Format
		payload   string
		reasoning bool
		assistant bool
	}{
		{
			name:      "responses reasoning delta",
			format:    sdktranslator.FormatOpenAIResponse,
			payload:   `{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
			reasoning: true,
		},
		{
			name:      "responses answer delta",
			format:    sdktranslator.FormatCodex,
			payload:   `{"type":"response.output_text.delta","delta":"answer"}`,
			assistant: true,
		},
		{
			name:      "openai chat reasoning and answer",
			format:    sdktranslator.FormatOpenAI,
			payload:   `{"choices":[{"delta":{"reasoning_content":"think","content":"answer"}}]}`,
			reasoning: true,
			assistant: true,
		},
		{
			name:      "claude thinking",
			format:    sdktranslator.FormatClaude,
			payload:   `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"think"}}`,
			reasoning: true,
		},
		{
			name:      "claude text",
			format:    sdktranslator.FormatClaude,
			payload:   `{"type":"content_block_delta","delta":{"type":"text_delta","text":"answer"}}`,
			assistant: true,
		},
		{
			name:      "gemini thought",
			format:    sdktranslator.FormatGemini,
			payload:   `{"candidates":[{"content":{"parts":[{"thought":true,"text":"think"}]}}]}`,
			reasoning: true,
		},
		{
			name:      "antigravity answer",
			format:    sdktranslator.FormatAntigravity,
			payload:   `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}}`,
			assistant: true,
		},
		{
			name:      "interactions thought summary",
			format:    sdktranslator.FormatInteractions,
			payload:   `{"event_type":"step.delta","delta":{"type":"thought_summary","content":{"text":"think"}}}`,
			reasoning: true,
		},
		{
			name:      "interactions answer",
			format:    sdktranslator.FormatInteractions,
			payload:   `{"event_type":"step.delta","delta":{"type":"text","text":"answer"}}`,
			assistant: true,
		},
		{
			name:    "empty content ignored",
			format:  sdktranslator.FormatOpenAIResponse,
			payload: `{"type":"response.output_text.delta","delta":"  "}`,
		},
		{
			name:    "unknown format ignored",
			format:  sdktranslator.Format("unknown"),
			payload: `{"type":"response.output_text.delta","delta":"answer"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasoning, assistant := classifySemanticTiming(tt.format, []byte(tt.payload))
			if reasoning != tt.reasoning || assistant != tt.assistant {
				t.Fatalf("classification = reasoning:%v assistant:%v, want reasoning:%v assistant:%v", reasoning, assistant, tt.reasoning, tt.assistant)
			}
		})
	}
}

func TestResponseTimingTrackerHandlesSplitSSEAndStopsAfterBothSemanticMarks(t *testing.T) {
	tracker := newResponseTimingTracker(sdktranslator.FormatOpenAIResponse, true)
	tracker.start(time.Now().Add(-25 * time.Millisecond))
	tracker.observeBytes([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thi"))
	tracker.observeBytes([]byte("nk\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n"))

	snapshot := tracker.snapshot()
	if snapshot.TimingVersion != UsageTimingVersionV1 {
		t.Fatalf("timing version = %d, want %d", snapshot.TimingVersion, UsageTimingVersionV1)
	}
	if snapshot.TTFT <= 0 || snapshot.TTFA <= 0 {
		t.Fatalf("semantic timing = ttft:%v ttfa:%v, want both positive", snapshot.TTFT, snapshot.TTFA)
	}
	tracker.observeBytes([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n"))
	if got := tracker.snapshot().TTFA; got != snapshot.TTFA {
		t.Fatalf("TTFA changed after semantic observer stopped: before=%v after=%v", snapshot.TTFA, got)
	}
}

func TestResponseTimingTrackerIgnoresToolOnlyAndNonStreaming(t *testing.T) {
	tracker := newResponseTimingTracker(sdktranslator.FormatOpenAIResponse, true)
	tracker.start(time.Now().Add(-10 * time.Millisecond))
	tracker.observeBytes([]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\\"q\\":1}"}\n\n`))
	if snapshot := tracker.snapshot(); snapshot.TTFT != 0 || snapshot.TTFA != 0 {
		t.Fatalf("tool-only timing = %+v, want semantic values missing", snapshot)
	}

	nonStreaming := newResponseTimingTracker(sdktranslator.FormatOpenAIResponse, false)
	nonStreaming.start(time.Now().Add(-10 * time.Millisecond))
	nonStreaming.observeBytes([]byte(`{"type":"response.output_text.delta","delta":"answer"}`))
	if snapshot := nonStreaming.snapshot(); snapshot.TTFT != 0 || snapshot.TTFA != 0 {
		t.Fatalf("non-streaming timing = %+v, want semantic values missing", snapshot)
	}
}
