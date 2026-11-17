// Package diagnostics records bounded, content-free request evidence.
// It does not select credentials, infer sessions, or invoke a cache matcher.
package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	MaxTraces     = 512
	MaxEvents     = 128
	MaxAttempts   = 32
	MaxTraceBytes = 64 << 10
	MaxStoreBytes = 8 << 20
	Retention     = time.Hour
)

type Event struct {
	Sequence     int       `json:"sequence"`
	DecisionID   string    `json:"decision_id,omitempty"`
	ExecutionID  string    `json:"execution_id,omitempty"`
	SelectionID  string    `json:"selection_id,omitempty"`
	At           time.Time `json:"at"`
	Layer        string    `json:"layer"`
	Method       string    `json:"method"`
	Result       string    `json:"result"`
	Reason       string    `json:"reason,omitempty"`
	AuthIndex    string    `json:"auth_index,omitempty"`
	ObjectHash   string    `json:"object_hash,omitempty"`
	IdentityHash string    `json:"identity_hash,omitempty"`
	ScopeHash    string    `json:"scope_hash,omitempty"`
	PrefixLength int       `json:"prefix_length,omitempty"`
	PrefixUnit   string    `json:"prefix_unit,omitempty"`
	Examined     *int      `json:"examined,omitempty"`
	Changes      []string  `json:"changes,omitempty"`
}

type Trace struct {
	Version          uint32    `json:"version"`
	ID               string    `json:"id"`
	InstanceID       string    `json:"instance_id"`
	LogicalRequestID string    `json:"logical_request_id"`
	SessionHash      string    `json:"session_hash,omitempty"`
	Model            string    `json:"model"`
	SourceFormat     string    `json:"source_format"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Input            Payload   `json:"input"`
	Events           []Event   `json:"events"`
	Attempts         []Attempt `json:"attempts"`
	Truncated        bool      `json:"truncated"`
}

type entry struct {
	value Trace
	bytes int
}
type Store struct {
	mu       sync.Mutex
	instance string
	entries  map[string]*entry
	bytes    int
	now      func() time.Time
}
type contextKey struct{}
type Ref struct {
	store       *Store
	id          string
	decisionID  string
	executionID string
}

func NewStore() *Store {
	return &Store{instance: uuid.NewString(), entries: make(map[string]*entry), now: time.Now}
}
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func FromContext(ctx context.Context) *Ref {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(contextKey{}).(*Ref)
	return r
}
func (r *Ref) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

func (r *Ref) InstanceID() string {
	if r == nil || r.store == nil {
		return ""
	}
	return r.store.instance
}

// 每次重新选路都创建独立决定；不把上一凭据的理由拼接到下一次执行。
func BeginSelection(ctx context.Context) context.Context {
	r := FromContext(ctx)
	if r == nil {
		return ctx
	}
	copy := *r
	copy.decisionID, copy.executionID = uuid.NewString(), ""
	return context.WithValue(ctx, contextKey{}, &copy)
}

func WithExecution(ctx context.Context, id string) context.Context {
	r := FromContext(ctx)
	if r == nil {
		return ctx
	}
	copy := *r
	copy.executionID = id
	return context.WithValue(ctx, contextKey{}, &copy)
}

func (s *Store) Begin(ctx context.Context, requestID, callerScope, session, model, format string, body []byte) context.Context {
	if s == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	id := Hash(callerScope + "\x00" + requestID)
	input := Snapshot(body)
	s.mu.Lock()
	now := s.now()
	for key, e := range s.entries {
		if now.Sub(e.value.UpdatedAt) > Retention {
			s.bytes -= e.bytes
			delete(s.entries, key)
		}
	}
	if _, ok := s.entries[id]; !ok {
		v := Trace{Version: 1, ID: id, InstanceID: s.instance, LogicalRequestID: bounded(requestID), Model: bounded(model), SourceFormat: bounded(format), CreatedAt: now, UpdatedAt: now, Input: input, Events: []Event{}, Attempts: []Attempt{}}
		if session != "" {
			v.SessionHash = Hash(session)
		}
		b, _ := json.Marshal(v)
		s.entries[id] = &entry{value: v, bytes: len(b)}
		s.bytes += len(b)
		s.evictLocked(id)
	}
	s.mu.Unlock()
	return context.WithValue(ctx, contextKey{}, &Ref{store: s, id: id})
}

func (s *Store) evictLocked(keep string) {
	for len(s.entries) > MaxTraces || s.bytes > MaxStoreBytes {
		old := ""
		var at time.Time
		for key, e := range s.entries {
			if key != keep && (old == "" || e.value.UpdatedAt.Before(at)) {
				old, at = key, e.value.UpdatedAt
			}
		}
		if old == "" {
			break
		}
		s.bytes -= s.entries[old].bytes
		delete(s.entries, old)
	}
}

func (r *Ref) update(fn func(*Trace)) bool {
	if r == nil || r.store == nil {
		return false
	}
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[r.id]
	if e == nil {
		return false
	}
	previous, _ := json.Marshal(e.value)
	fn(&e.value)
	e.value.UpdatedAt = s.now()
	b, _ := json.Marshal(e.value)
	retained := true
	if len(b) > MaxTraceBytes {
		retained = false
		_ = json.Unmarshal(previous, &e.value)
		e.value.Truncated = true
		b, _ = json.Marshal(e.value)
	}
	s.bytes += len(b) - e.bytes
	e.bytes = len(b)
	s.evictLocked(r.id)
	return retained
}

func Record(ctx context.Context, event Event) { FromContext(ctx).Record(event) }
func (r *Ref) Record(event Event) {
	if r == nil {
		return
	}
	event.DecisionID, event.ExecutionID = r.decisionID, r.executionID
	event.SelectionID = bounded(event.SelectionID)
	if event.Examined != nil {
		count := *event.Examined
		event.Examined = &count
	}
	event.Layer = bounded(event.Layer)
	event.Method = bounded(event.Method)
	event.Result = bounded(event.Result)
	event.Reason = bounded(event.Reason)
	event.AuthIndex = bounded(event.AuthIndex)
	event.ObjectHash = bounded(event.ObjectHash)
	event.IdentityHash = bounded(event.IdentityHash)
	event.ScopeHash = bounded(event.ScopeHash)
	event.Changes = append([]string(nil), event.Changes...)
	if len(event.Changes) > 32 {
		event.Changes = event.Changes[:32]
	}
	for i := range event.Changes {
		event.Changes[i] = bounded(event.Changes[i])
	}
	r.update(func(t *Trace) {
		if len(t.Events) >= MaxEvents {
			t.Truncated = true
			return
		}
		event.Sequence = len(t.Events) + 1
		event.At = r.store.now()
		t.Events = append(t.Events, event)
	})
}
func (r *Ref) SetSession(session string) {
	if session != "" {
		r.update(func(t *Trace) { t.SessionHash = Hash(session) })
	}
}

func (s *Store) Get(id string) (Trace, bool) {
	if s == nil {
		return Trace{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil || s.now().Sub(e.value.UpdatedAt) > Retention {
		return Trace{}, false
	}
	// 查询复制有限记录，不清理、不续期，也不触碰算法索引。
	b, _ := json.Marshal(e.value)
	var out Trace
	_ = json.Unmarshal(b, &out)
	return out, true
}

type Overview struct {
	InstanceID string    `json:"instance_id"`
	At         time.Time `json:"at"`
	Stored     int       `json:"stored"`
	Active     int       `json:"active"`
	Bytes      int       `json:"bytes"`
	MaxRecords int       `json:"max_records"`
	MaxBytes   int       `json:"max_bytes"`
}

func (s *Store) Overview() Overview {
	if s == nil {
		return Overview{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := Overview{InstanceID: s.instance, At: now, Stored: len(s.entries), Bytes: s.bytes, MaxRecords: MaxTraces, MaxBytes: MaxStoreBytes}
	for _, e := range s.entries {
		if now.Sub(e.value.UpdatedAt) <= Retention {
			out.Active++
		}
	}
	return out
}
func bounded(v string) string {
	if len(v) > 192 {
		v = v[:192]
	}
	// JSON 字段的短切片仍可能引用整个长请求；只保留独立的小字符串。
	return strings.Clone(v)
}
