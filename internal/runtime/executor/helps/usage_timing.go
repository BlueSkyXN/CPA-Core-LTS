package helps

import (
	"bytes"
	"strings"
	"sync"
	"time"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	// UsageTimingVersionV1 is the first semantic timing contract. Keep this
	// value stable once it is emitted into usage records.
	UsageTimingVersionV1 uint32 = 1
	// responseTimingFrameMaxBytes bounds the untrusted in-flight SSE frame
	// retained by the observer. Parsed content is never retained.
	responseTimingFrameMaxBytes = 256 << 10
)

// ResponseTimingSnapshot is the immutable timing state copied into a usage
// record when the reporter publishes it.
type ResponseTimingSnapshot struct {
	TimingVersion uint32
	TTFB          time.Duration
	TTFT          time.Duration
	TTFA          time.Duration
}

// responseTimingTracker measures one upstream attempt. It deliberately keeps
// only timestamps and a bounded parser buffer; response content is discarded
// after classification.
type responseTimingTracker struct {
	mu sync.Mutex

	format           sdktranslator.Format
	semanticEnabled  bool
	semanticDisabled bool
	started          time.Time

	ttfb    time.Duration
	ttft    time.Duration
	ttfa    time.Duration
	ttfbSet bool
	ttftSet bool
	ttfaSet bool

	lineBuffer []byte
	frameData  []byte
	dropFrame  bool
}

func newResponseTimingTracker(format sdktranslator.Format, semanticEnabled bool) *responseTimingTracker {
	return &responseTimingTracker{
		format:          format,
		semanticEnabled: semanticEnabled,
	}
}

func (t *responseTimingTracker) configure(format sdktranslator.Format, semanticEnabled bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.format = format
	t.semanticEnabled = semanticEnabled
	t.mu.Unlock()
}

func (t *responseTimingTracker) start(now time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.started.IsZero() {
		t.started = now
	}
	t.mu.Unlock()
}

func (t *responseTimingTracker) markTTFB(now time.Time) {
	t.mark(now, timingMarkTTFB)
}

func (t *responseTimingTracker) markTTFT(now time.Time) {
	t.mark(now, timingMarkTTFT)
}

func (t *responseTimingTracker) markTTFA(now time.Time) {
	t.mark(now, timingMarkTTFA)
}

type timingMark uint8

const (
	timingMarkTTFB timingMark = iota + 1
	timingMarkTTFT
	timingMarkTTFA
)

func (t *responseTimingTracker) mark(now time.Time, mark timingMark) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started.IsZero() {
		return
	}
	elapsed := now.Sub(t.started)
	if elapsed < 0 {
		elapsed = 0
	}
	switch mark {
	case timingMarkTTFB:
		if !t.ttfbSet {
			t.ttfb = elapsed
			t.ttfbSet = true
		}
	case timingMarkTTFT:
		if !t.ttftSet {
			t.ttft = elapsed
			t.ttftSet = true
		}
	case timingMarkTTFA:
		if !t.ttfaSet {
			t.ttfa = elapsed
			t.ttfaSet = true
		}
	}
}

func (t *responseTimingTracker) snapshot() ResponseTimingSnapshot {
	if t == nil {
		return ResponseTimingSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return ResponseTimingSnapshot{
		TimingVersion: UsageTimingVersionV1,
		TTFB:          t.ttfb,
		TTFT:          t.ttft,
		TTFA:          t.ttfa,
	}
}

// observeBytes accepts arbitrary HTTP body chunks. It handles raw JSON and
// SSE data lines without requiring the caller to buffer or alter the stream.
func (t *responseTimingTracker) observeBytes(data []byte) {
	if t == nil || len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.semanticEnabled || t.semanticDisabled || t.format == "" {
		return
	}

	t.lineBuffer = append(t.lineBuffer, data...)
	if t.bufferSizeLocked() > responseTimingFrameMaxBytes {
		t.lineBuffer = nil
		t.frameData = nil
		t.dropFrame = true
	}

	for {
		newline := bytes.IndexByte(t.lineBuffer, '\n')
		if newline < 0 {
			break
		}
		line := bytes.Clone(t.lineBuffer[:newline])
		t.lineBuffer = t.lineBuffer[newline+1:]
		t.processLineLocked(bytes.TrimSuffix(line, []byte{'\r'}))
		if t.semanticDisabled {
			return
		}
	}

	// Most providers emit one complete SSE data line per read. Parse it now
	// when it is already valid, while retaining incomplete split JSON for the
	// next read or the terminating blank line.
	if len(t.lineBuffer) > 0 && t.isCompleteStandaloneLineLocked(t.lineBuffer) {
		line := bytes.Clone(t.lineBuffer)
		t.lineBuffer = nil
		t.processLineLocked(line)
	}
}

// observeMessage classifies one complete WebSocket or other message-bounded
// payload. Oversized messages are discarded without poisoning later messages.
func (t *responseTimingTracker) observeMessage(data []byte) {
	if t == nil || len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.semanticEnabled || t.semanticDisabled || t.format == "" || len(data) > responseTimingFrameMaxBytes {
		return
	}

	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) || !gjson.ValidBytes(trimmed) {
		return
	}
	t.classifyLocked(trimmed)
}

func (t *responseTimingTracker) bufferSizeLocked() int {
	return len(t.lineBuffer) + len(t.frameData)
}

func (t *responseTimingTracker) isCompleteStandaloneLineLocked(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	return bytes.Equal(trimmed, []byte("[DONE]")) || gjson.ValidBytes(trimmed)
}

func (t *responseTimingTracker) processLineLocked(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		t.flushFrameLocked()
		return
	}
	if t.dropFrame {
		return
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("id:")) ||
		bytes.HasPrefix(trimmed, []byte("retry:")) ||
		bytes.HasPrefix(trimmed, []byte(":")) {
		return
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			t.flushFrameLocked()
			return
		}
		if len(data) == 0 {
			return
		}
		if len(t.frameData) > 0 {
			t.frameData = append(t.frameData, '\n')
		}
		t.frameData = append(t.frameData, data...)
		if t.bufferSizeLocked() > responseTimingFrameMaxBytes {
			t.frameData = nil
			t.dropFrame = true
			return
		}
		if gjson.ValidBytes(t.frameData) {
			t.classifyLocked(t.frameData)
			t.frameData = nil
		}
		return
	}

	// WebSocket messages and non-SSE JSON responses arrive without a data:
	// prefix. Ignore malformed fragments rather than retaining them.
	if gjson.ValidBytes(trimmed) {
		t.classifyLocked(trimmed)
	}
}

func (t *responseTimingTracker) flushFrameLocked() {
	if t.dropFrame {
		t.dropFrame = false
		t.frameData = nil
		return
	}
	if len(t.frameData) > 0 && gjson.ValidBytes(t.frameData) {
		t.classifyLocked(t.frameData)
	}
	t.frameData = nil
}

func (t *responseTimingTracker) classifyLocked(payload []byte) {
	if t.semanticDisabled || !t.semanticEnabled || t.started.IsZero() {
		return
	}
	reasoning, assistant := classifySemanticTiming(t.format, payload)
	now := time.Now()
	if reasoning && !t.ttftSet {
		elapsed := now.Sub(t.started)
		if elapsed < 0 {
			elapsed = 0
		}
		t.ttft = elapsed
		t.ttftSet = true
	}
	if assistant && !t.ttfaSet {
		elapsed := now.Sub(t.started)
		if elapsed < 0 {
			elapsed = 0
		}
		t.ttfa = elapsed
		t.ttfaSet = true
	}
	if t.ttftSet && t.ttfaSet {
		t.semanticDisabled = true
		t.lineBuffer = nil
		t.frameData = nil
	}
}

func nonEmptyString(value gjson.Result) bool {
	return value.Type == gjson.String && strings.TrimSpace(value.String()) != ""
}

func anyNonEmptyText(value gjson.Result) bool {
	if !value.Exists() || value.Type == gjson.Null {
		return false
	}
	if nonEmptyString(value) {
		return true
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			found = anyNonEmptyText(item)
			return !found
		})
		return found
	}
	if value.IsObject() {
		for _, key := range []string{"text", "thinking", "delta", "content"} {
			if anyNonEmptyText(value.Get(key)) {
				return true
			}
		}
	}
	return false
}

func classifySemanticTiming(format sdktranslator.Format, payload []byte) (reasoning, assistant bool) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false, false
	}
	root := gjson.ParseBytes(payload)
	switch format {
	case sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse:
		return classifyResponsesTiming(root)
	case sdktranslator.FormatOpenAI:
		return classifyOpenAIChatTiming(root)
	case sdktranslator.FormatClaude:
		return classifyClaudeTiming(root)
	case sdktranslator.FormatGemini, sdktranslator.FormatAntigravity:
		return classifyGeminiTiming(root)
	case sdktranslator.FormatInteractions:
		return classifyInteractionsTiming(root)
	default:
		return false, false
	}
}

func classifyResponsesTiming(root gjson.Result) (reasoning, assistant bool) {
	eventType := root.Get("type").String()
	switch eventType {
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		reasoning = anyNonEmptyText(root.Get("delta"))
	case "response.output_text.delta":
		assistant = anyNonEmptyText(root.Get("delta"))
	}
	if strings.HasPrefix(eventType, "response.output_item.") {
		item := root.Get("item")
		switch item.Get("type").String() {
		case "reasoning":
			reasoning = anyNonEmptyText(item.Get("summary")) ||
				anyNonEmptyText(item.Get("content")) ||
				anyNonEmptyText(item.Get("text"))
		case "message":
			assistant = anyNonEmptyText(item.Get("content")) || anyNonEmptyText(item.Get("text"))
		}
	}
	if eventType == "response.completed" || eventType == "response.done" {
		root.Get("response.output").ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "reasoning":
				reasoning = reasoning || anyNonEmptyText(item.Get("summary")) || anyNonEmptyText(item.Get("content"))
			case "message":
				assistant = assistant || anyNonEmptyText(item.Get("content")) || anyNonEmptyText(item.Get("text"))
			}
			return true
		})
	}
	return reasoning, assistant
}

func classifyOpenAIChatTiming(root gjson.Result) (reasoning, assistant bool) {
	root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		if !delta.Exists() {
			delta = choice.Get("message")
		}
		reasoning = reasoning || anyNonEmptyText(delta.Get("reasoning_content")) || anyNonEmptyText(delta.Get("reasoning"))
		assistant = assistant || anyNonEmptyText(delta.Get("content"))
		return true
	})
	return reasoning, assistant
}

func classifyClaudeTiming(root gjson.Result) (reasoning, assistant bool) {
	switch root.Get("type").String() {
	case "content_block_delta":
		delta := root.Get("delta")
		switch delta.Get("type").String() {
		case "thinking_delta":
			reasoning = anyNonEmptyText(delta.Get("thinking"))
		case "text_delta":
			assistant = anyNonEmptyText(delta.Get("text"))
		}
	case "content_block_start":
		block := root.Get("content_block")
		switch block.Get("type").String() {
		case "thinking":
			reasoning = anyNonEmptyText(block.Get("thinking")) || anyNonEmptyText(block.Get("text"))
		case "text":
			assistant = anyNonEmptyText(block.Get("text"))
		}
	}
	return reasoning, assistant
}

func classifyGeminiTiming(root gjson.Result) (reasoning, assistant bool) {
	for _, path := range []string{"candidates", "response.candidates"} {
		root.Get(path).ForEach(func(_, candidate gjson.Result) bool {
			candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
				if !anyNonEmptyText(part.Get("text")) {
					return true
				}
				if part.Get("thought").Type == gjson.True {
					reasoning = true
				} else {
					assistant = true
				}
				return true
			})
			return true
		})
	}
	return reasoning, assistant
}

func classifyInteractionsTiming(root gjson.Result) (reasoning, assistant bool) {
	eventType := root.Get("event_type").String()
	if eventType == "step.delta" {
		delta := root.Get("delta")
		switch delta.Get("type").String() {
		case "thought_summary":
			reasoning = anyNonEmptyText(delta.Get("content.text")) || anyNonEmptyText(delta.Get("text"))
		case "text":
			assistant = anyNonEmptyText(delta.Get("text"))
		}
	}
	if eventType == "step.start" || eventType == "step.completed" || eventType == "" {
		step := root.Get("step")
		switch step.Get("type").String() {
		case "thought":
			reasoning = reasoning || anyNonEmptyText(step.Get("content"))
		case "model_output":
			assistant = assistant || anyNonEmptyText(step.Get("content"))
		}
	}
	return reasoning, assistant
}
