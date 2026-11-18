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
	"strings"
	"sync"
	"time"
)

const MaxResponseFrame = 2 << 20

type Attempt struct {
	Number               int                     `json:"number"`
	DecisionID           string                  `json:"decision_id,omitempty"`
	ExecutionID          string                  `json:"execution_id"`
	Provider             string                  `json:"provider"`
	Model                string                  `json:"model"`
	AuthIndex            string                  `json:"auth_index,omitempty"`
	AuthType             string                  `json:"auth_type,omitempty"`
	Transport            string                  `json:"transport"`
	Endpoint             string                  `json:"endpoint"`
	SessionHeaderHash    string                  `json:"session_header_hash,omitempty"`
	StartedAt            time.Time               `json:"started_at"`
	Status               int                     `json:"status,omitempty"`
	Outcome              string                  `json:"outcome"`
	Input                *Payload                `json:"input,omitempty"`
	Request              Payload                 `json:"request"`
	ResponseID           string                  `json:"response_id,omitempty"`
	ResponseModel        string                  `json:"response_model,omitempty"`
	ResponseStatus       string                  `json:"response_status,omitempty"`
	ObservationState     string                  `json:"observation_state"`
	ComparisonIDSent     string                  `json:"comparison_id_sent"`
	DiagnosticReturned   bool                    `json:"diagnostic_returned"`
	Diagnostic           *Diagnostic             `json:"diagnostic,omitempty"`
	CacheObservation     *usage.CacheObservation `json:"cache_observation,omitempty"`
	InputTransformations int                     `json:"input_transformations,omitempty"`
	ResponseTruncated    bool                    `json:"response_truncated"`
}
type Diagnostic struct {
	Type                     string `json:"type"`
	Reason                   string `json:"reason,omitempty"`
	CacheMissedTokens        *int64 `json:"cache_missed_tokens,omitempty"`
	ComparisonReusableTokens *int64 `json:"comparison_reusable_tokens,omitempty"`
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
	a.DecisionID = e.Ref.decisionID
	a.Input = e.Input
	if a.Request.Known {
		a.ComparisonIDSent = "absent"
		if _, ok := a.Request.Fields["prompt_cache_options.comparison_response_id"]; ok {
			a.ComparisonIDSent = "present"
		}
	}
	for _, key := range []string{"Session-Id", "Session_id", "X-Session-Id"} {
		if v := headers.Get(key); v != "" {
			a.SessionHeaderHash = Hash(v)
			break
		}
	}
	ref := &AttemptRef{ref: e.Ref}
	retained := e.Ref.update(func(t *Trace) {
		if len(t.Attempts) >= MaxAttempts {
			t.Truncated = true
			return
		}
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
	a := e.Begin("http", EndpointKind(endpoint), body, req.Header)
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
	usageNode := root.Get("usage")
	diagnostic := root.Get("prompt_cache_diagnostics")
	if !usageNode.IsObject() && !diagnostic.IsObject() && eventType != "response.created" && eventType != "response.completed" && eventType != "response.done" && eventType != "response.incomplete" && eventType != "response.failed" && root.Get("object").String() != "response" {
		return
	}
	a.update(func(v *Attempt) {
		v.ObservationState = "partial"
		if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed" || eventType == "response.done" || eventType == "" && (root.Get("object").String() == "response" || root.Get("object").String() == "chat.completion") {
			v.ObservationState = "observed"
		}
		if id := safeToken(root.Get("id").String()); id != "" {
			v.ResponseID = id
		}
		if model := safeToken(root.Get("model").String()); model != "" {
			v.ResponseModel = model
		}
		if status := safeToken(root.Get("status").String()); status != "" {
			v.ResponseStatus = status
		}
		if usageNode.IsObject() {
			v.CacheObservation = &usage.CacheObservation{Version: 1, Read: observedCount(usageNode, "prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens"), Write: observedCount(usageNode, "input_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens")}
		}
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
func observedCount(node ojson.Result, paths ...string) usage.CacheTokenObservation {
	for _, path := range paths {
		v := node.Get(path)
		if !v.Exists() {
			continue
		}
		out := usage.CacheTokenObservation{State: "invalid", Source: path}
		if n := numberPointer(v); n != nil {
			out.State = "present"
			out.Value = n
		}
		return out
	}
	return usage.CacheTokenObservation{State: "absent"}
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
