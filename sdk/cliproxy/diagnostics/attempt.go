package diagnostics

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const MaxResponseFrame = 2 << 20

type Attempt struct {
	Number               int                         `json:"number"`
	DecisionID           string                      `json:"decision_id,omitempty"`
	ExecutionID          string                      `json:"execution_id"`
	Provider             string                      `json:"provider"`
	Model                string                      `json:"model"`
	AuthIndex            string                      `json:"auth_index,omitempty"`
	AuthType             string                      `json:"auth_type,omitempty"`
	Transport            string                      `json:"transport"`
	Endpoint             string                      `json:"endpoint"`
	RouteScope           string                      `json:"route_scope,omitempty"`
	CacheReferences      []usage.CacheRouteReference `json:"cache_references,omitempty"`
	CacheWriteByTTL      map[string]int64            `json:"cache_write_by_ttl,omitempty"`
	CacheControl         *CacheControl               `json:"cache_control,omitempty"`
	PreviousResponse     string                      `json:"previous_response_digest,omitempty"`
	PreviousSource       string                      `json:"previous_source,omitempty"`
	ResponseIDs          []string                    `json:"response_ids,omitempty"`
	SessionHeaderHash    string                      `json:"session_header_hash,omitempty"`
	StartedAt            time.Time                   `json:"started_at"`
	Status               int                         `json:"status,omitempty"`
	Outcome              string                      `json:"outcome"`
	Input                *Payload                    `json:"input,omitempty"`
	Request              Payload                     `json:"request"`
	ResponseID           string                      `json:"response_id,omitempty"`
	ResponseModel        string                      `json:"response_model,omitempty"`
	ResponseStatus       string                      `json:"response_status,omitempty"`
	ObservationState     string                      `json:"observation_state"`
	ComparisonIDSent     string                      `json:"comparison_id_sent"`
	DiagnosticReturned   bool                        `json:"diagnostic_returned"`
	Diagnostic           *Diagnostic                 `json:"diagnostic,omitempty"`
	InputTokens          *int64                      `json:"input_tokens,omitempty"`
	CacheObservation     *usage.CacheObservation     `json:"cache_observation,omitempty"`
	InputTransformations int                         `json:"input_transformations,omitempty"`
	ResponseTruncated    bool                        `json:"response_truncated"`
}
type Diagnostic struct {
	Type                     string `json:"type"`
	Reason                   string `json:"reason,omitempty"`
	CacheMissedTokens        *int64 `json:"cache_missed_tokens,omitempty"`
	ComparisonReusableTokens *int64 `json:"comparison_reusable_tokens,omitempty"`
}

func (a Attempt) hasOfficialCacheLoss() bool {
	d := a.Diagnostic
	if !a.DiagnosticReturned || d == nil || d.Type == "comparison_response_not_found" || d.Type == "unavailable" {
		return false
	}
	return d.Type == "cache_miss" || d.CacheMissedTokens != nil && *d.CacheMissedTokens > 0
}

type Execution struct {
	Input                                    *Payload
	Ref                                      *Ref
	ID, Provider, Model, AuthIndex, AuthType string
}
type AttemptRef struct {
	ref    *Ref
	number int
}

func (e *Execution) Begin(transport, endpoint string, payload []byte, headers http.Header) *AttemptRef {
	if e == nil || e.Ref == nil {
		return nil
	}
	a := Attempt{ExecutionID: e.ID, Provider: bounded(e.Provider), Model: bounded(e.Model), AuthIndex: bounded(e.AuthIndex), AuthType: bounded(e.AuthType), Transport: transport, Endpoint: endpoint, Outcome: "started", ObservationState: "not_observed", Request: Snapshot(payload), ComparisonIDSent: "unknown"}
	if strings.Contains(endpoint, "://") {
		a.Endpoint = EndpointKind(endpoint)
	}
	a.DecisionID = e.Ref.decisionID
	a.Input = e.Input
	a.captureCacheRoute(payload, headers)
	ref := &AttemptRef{ref: e.Ref}
	retained := e.Ref.update(func(t *Trace) {
		if len(t.Attempts) >= MaxAttempts {
			t.Truncated = true
			return
		}
		a.scopeCacheRoute(t, endpoint)
		a.Number = len(t.Attempts) + 1
		a.StartedAt = e.Ref.store.now()
		ref.number = a.Number
		t.Attempts = append(t.Attempts, a)
	})
	if !retained || ref.number == 0 {
		return nil
	}
	return ref
}
func (e *Execution) BeginHTTP(req *http.Request) *AttemptRef {
	if e == nil || e.Ref == nil || req == nil {
		return nil
	}
	var body []byte
	reason := "body_unavailable"
	if req.GetBody != nil {
		if reader, err := req.GetBody(); err == nil {
			body, reason = readRequestBody(reader, req.Header.Get("Content-Encoding"))
			_ = reader.Close()
		}
	}
	endpoint := ""
	if req.URL != nil {
		endpoint = req.URL.String()
	}
	a := e.Begin("http", endpoint, body, req.Header)
	if a != nil && reason != "" {
		a.update(func(v *Attempt) { v.Request.Reason = reason })
	}
	return a
}

func readRequestBody(body io.Reader, encoding string) ([]byte, string) {
	reader := body
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		r, err := gzip.NewReader(body)
		if err != nil {
			return nil, "decode_error"
		}
		defer r.Close()
		reader = r
	case "zstd":
		r, err := zstd.NewReader(body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
		if err != nil {
			return nil, "decode_error"
		}
		defer r.Close()
		reader = r
	default:
		return nil, "unsupported_encoding"
	}
	b, err := io.ReadAll(io.LimitReader(reader, MaxPayloadBytes+1))
	if err != nil {
		return nil, "decode_error"
	}
	return b, ""
}
func (a *AttemptRef) update(fn func(*Attempt)) {
	if a == nil || a.ref == nil {
		return
	}
	a.ref.update(func(t *Trace) {
		if a.number > 0 && a.number <= len(t.Attempts) {
			fn(&t.Attempts[a.number-1])
		}
	})
}
func (a *AttemptRef) Status(status int, failed bool) {
	a.update(func(v *Attempt) {
		v.Status = status
		if failed {
			v.Outcome = "transport_error"
		} else {
			v.Outcome = "response_received"
		}
	})
}
func (a *AttemptRef) End(outcome string) { a.update(func(v *Attempt) { v.Outcome = outcome }) }
func (a *AttemptRef) Truncate()          { a.update(func(v *Attempt) { v.ResponseTruncated = true }) }

func (a *AttemptRef) Observe(data []byte) {
	if a == nil {
		return
	}
	data = bytes.TrimSpace(data)
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	if len(data) > MaxResponseFrame {
		a.Truncate()
		return
	}
	if !json.Valid(data) {
		return
	}
	root := ojson.ParseBytes(data)
	eventType := root.Get("type").String()
	if r := root.Get("response"); r.IsObject() {
		root = r
	}
	if message := root.Get("message"); eventType == "message_start" && message.IsObject() {
		root = message
	}
	if interaction := root.Get("interaction"); interaction.IsObject() {
		root = interaction
	}
	usageNode := root.Get("usage")
	gemini := root.Get("usageMetadata")
	if !gemini.Exists() {
		gemini = root.Get("usage_metadata")
	}
	diagnostic := root.Get("prompt_cache_diagnostics")
	object := root.Get("object").String()
	responseObject := object == "response" || object == "chat.completion" || object == "chat.completion.chunk"
	responseEvent := eventType == "response.created" || eventType == "response.completed" ||
		eventType == "response.done" || eventType == "response.incomplete" || eventType == "response.failed" ||
		eventType == "message" || eventType == "message_start" || eventType == "message_stop"
	if !usageNode.IsObject() && !gemini.IsObject() && !diagnostic.IsObject() && !responseObject && !responseEvent {
		return
	}
	a.update(func(v *Attempt) {
		if v.ObservationState != "observed" {
			v.ObservationState = "partial"
		}
		if gemini.IsObject() || eventType == "message" || eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed" || eventType == "response.done" || eventType == "" && (root.Get("object").String() == "response" || root.Get("object").String() == "chat.completion") {
			v.ObservationState = "observed"
		}
		id := safeToken(root.Get("id").String())
		if id == "" && gemini.IsObject() {
			id = safeToken(root.Get("responseId").String())
		}
		if id != "" {
			v.ResponseID = id
			if !slices.Contains(v.ResponseIDs, id) {
				if len(v.ResponseIDs) < 4 {
					v.ResponseIDs = append(v.ResponseIDs, id)
				} else {
					v.ResponseTruncated = true
				}
			}
		}
		if model := safeToken(root.Get("model").String()); model != "" {
			v.ResponseModel = model
		}
		if status := safeToken(root.Get("status").String()); status != "" {
			v.ResponseStatus = status
		}
		// Claude supplies the ID in message_start. A consumed partial stream
		// cannot serve as a predecessor until its message_stop is observed.
		if v.Provider == "claude" && eventType == "message_stop" && v.ResponseID != "" {
			v.ObservationState = "observed"
			v.ResponseStatus = "completed"
		}
		var observation *usage.CacheObservation
		switch {
		case gemini.IsObject():
			observation = usage.ObserveGeminiCache(gemini)
		case v.Provider == "claude" || strings.HasPrefix(eventType, "message_"):
			observation = usage.ObserveClaudeCache(usageNode)
			for _, ttl := range []string{"5m", "30m", "1h", "12h"} {
				count := numberPointer(usageNode.Get("cache_creation.ephemeral_" + ttl + "_input_tokens"))
				if count != nil {
					if v.CacheWriteByTTL == nil {
						v.CacheWriteByTTL = map[string]int64{}
					}
					v.CacheWriteByTTL[ttl] = *count
				}
			}
		case strings.Contains(eventType, "interaction.") || ojson.GetBytes(data, "interaction").IsObject():
			observation = usage.ObserveInteractionsCache(usageNode)
		default:
			observation = usage.ObserveOpenAICache(usageNode)
		}
		v.CacheObservation = usage.MergeCacheObservations(v.CacheObservation, observation)
		if diagnostic.IsObject() {
			v.DiagnosticReturned = true
			d := &Diagnostic{Type: safeToken(diagnostic.Get("type").String()), Reason: safeToken(diagnostic.Get("reason").String())}
			d.CacheMissedTokens = numberPointer(diagnostic.Get("cache_missed_tokens"))
			d.ComparisonReusableTokens = numberPointer(diagnostic.Get("comparison_reusable_tokens"))
			v.Diagnostic = d
		}
		if transforms := root.Get("input_transformations"); transforms.IsArray() {
			v.InputTransformations = len(transforms.Array())
		}
	})
}
func numberPointer(v ojson.Result) *int64 {
	if v.Type != ojson.Number {
		return nil
	}
	var n int64
	if json.Unmarshal([]byte(v.Raw), &n) != nil || n < 0 {
		return nil
	}
	return &n
}

// WrapResponse observes only bytes already consumed by the caller. It never
// pre-reads a stream, changes chunks, or buffers before downstream delivery.
func (a *AttemptRef) WrapResponse(body io.ReadCloser, contentType, encoding string) io.ReadCloser {
	if a == nil || body == nil {
		return body
	}
	if encoding != "" {
		a.update(func(v *Attempt) { v.ObservationState = "encoded_response" })
	}
	return &observedBody{ReadCloser: body, attempt: a, sse: strings.Contains(contentType, "text/event-stream"), encoded: encoding != ""}
}

type observedBody struct {
	io.ReadCloser
	mu                           sync.Mutex
	attempt                      *AttemptRef
	buf                          []byte
	sse, encoded, dropping, done bool
	handedOff                    bool
}

// Decoder handoff keeps the same attempt while parsing consumed decoded bytes.
// The raw wrapper still forwards/counts bytes, but no longer parses or ends it.
func (b *observedBody) DecodedDiagnostics() *AttemptRef {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.encoded, b.handedOff = true, true
	return b.attempt
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > 0 && !b.encoded && !b.done {
		b.consume(p[:n])
	}
	if err != nil {
		if err == io.EOF {
			b.finish("body_consumed")
		} else {
			b.finish("read_error")
		}
	}
	return n, err
}
func (b *observedBody) Close() error {
	b.mu.Lock()
	b.finish("closed")
	b.mu.Unlock()
	return b.ReadCloser.Close()
}
func (b *observedBody) finish(state string) {
	if b.done {
		return
	}
	b.done = true
	if b.handedOff {
		return
	}
	if !b.encoded && !b.dropping && len(b.buf) > 0 {
		b.attempt.Observe(b.buf)
	}
	b.attempt.End(state)
}
func (b *observedBody) consume(data []byte) {
	if !b.sse {
		if !b.dropping && len(b.buf)+len(data) <= MaxResponseFrame {
			b.buf = append(b.buf, data...)
		} else {
			b.buf = nil
			b.dropping = true
			b.attempt.Truncate()
		}
		return
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		part := data
		if i >= 0 {
			part = data[:i]
		}
		if !b.dropping {
			if len(b.buf)+len(part) <= MaxResponseFrame {
				b.buf = append(b.buf, part...)
			} else {
				b.buf = nil
				b.dropping = true
				b.attempt.Truncate()
			}
		}
		if i < 0 {
			return
		}
		if !b.dropping {
			b.attempt.Observe(b.buf)
		}
		b.buf = b.buf[:0]
		b.dropping = false
		data = data[i+1:]
	}
}

func EndpointKind(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "unknown"
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.openai.com":
		return "openai_public"
	case "chatgpt.com":
		return "chatgpt_codex"
	default:
		return "custom"
	}
}
