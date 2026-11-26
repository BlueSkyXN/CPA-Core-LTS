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

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	logging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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
	Version          uint32               `json:"version"`
	ID               string               `json:"id"`
	InstanceID       string               `json:"instance_id"`
	LogicalRequestID string               `json:"logical_request_id"`
	TraceID          string               `json:"trace_id,omitempty"`
	RouteScope       string               `json:"route_scope,omitempty"`
	InputSession     *CacheRouteReference `json:"input_session,omitempty"`
	InputPCKHash     string               `json:"input_pck_hash,omitempty"`
	PreviousResponse string               `json:"previous_response_digest,omitempty"`
	SessionHash      string               `json:"session_hash,omitempty"`
	Model            string               `json:"model"`
	SourceFormat     string               `json:"source_format"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
	Input            Payload              `json:"input"`
	Events           []Event              `json:"events"`
	Attempts         []Attempt            `json:"attempts"`
	Truncated        bool                 `json:"truncated"`
}

type entry struct {
	value    Trace
	raw      []byte
	bytes    int
	revision uint64
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
	root := ojson.Result{}
	if input.Known {
		root = ojson.ParseBytes(body)
	}
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
		v.TraceID = bounded(usage.TraceIDFromContext(ctx))
		if v.TraceID == "" {
			v.TraceID = bounded(logging.GetRequestID(ctx))
		}
		if callerScope != "" {
			v.RouteScope = Hash(s.instance + "\x00" + callerScope)
		}
		v.InputPCKHash = cacheDigest(root.Get("prompt_cache_key"))
		v.PreviousResponse = cacheDigest(root.Get("previous_response_id"))
		if session != "" {
			v.SessionHash = Hash(session)
			// These affinity namespaces describe a cache key or caller, not a conversation.
			if !strings.HasPrefix(session, "pck:") && !strings.HasPrefix(session, "geminicache:") && !strings.HasPrefix(session, "user:") {
				inputSession := cacheReference("input_session", v.SessionHash, v.RouteScope, "inbound", "session_metadata")
				v.InputSession = &inputSession
			}
		}
		b, _ := json.Marshal(v)
		s.entries[id] = &entry{value: v, raw: b, bytes: len(b)}
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
			if key != keep && (old == "" || e.value.UpdatedAt.Before(at) || e.value.UpdatedAt.Equal(at) && key < old) {
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

func (r *Ref) update(fn func(*Trace) bool) bool {
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
	if !fn(&e.value) {
		return true
	}
	previous := e.raw
	if previous == nil {
		previous, _ = json.Marshal(e.value)
	}
	e.value.UpdatedAt = s.now()
	b, _ := json.Marshal(e.value)
	retained := true
	if len(b) > MaxTraceBytes {
		retained = false
		// 恢复到零值再替换，清除被拒绝更新新增的可选字段、指针和 map 项。
		var restored Trace
		_ = json.Unmarshal(previous, &restored)
		e.value = restored
		if !e.value.Truncated {
			e.value.Truncated = true
			b, _ = json.Marshal(e.value)
			s.bytes += len(b) - e.bytes
			e.bytes = len(b)
			e.raw = b
			e.revision++
		} else if e.raw == nil {
			e.raw = previous
		}
		s.evictLocked(r.id)
		return retained
	}
	s.bytes += len(b) - e.bytes
	e.bytes = len(b)
	e.raw = b
	e.revision++
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
	r.update(func(t *Trace) bool {
		if len(t.Events) >= MaxEvents {
			if t.Truncated {
				return false
			}
			t.Truncated = true
			return true
		}
		event.Sequence = len(t.Events) + 1
		event.At = r.store.now()
		t.Events = append(t.Events, event)
		return true
	})
}
func (r *Ref) SetSession(session string) {
	if r != nil && session != "" {
		hash := Hash(session)
		r.update(func(t *Trace) bool {
			if t.SessionHash == hash {
				return false
			}
			t.SessionHash = hash
			return true
		})
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
	raw := e.raw
	if raw == nil {
		raw, _ = json.Marshal(e.value)
	}
	var out Trace
	_ = json.Unmarshal(raw, &out)
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
