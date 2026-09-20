package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	session "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexAffinityMaxTrajectories = 4096
	codexAffinityMaxPrefixes     = 262144
	codexAffinityMaxItems        = 1024
	codexAffinityMaxCandidates   = 64
	codexAffinityMaxBytes        = 32 << 20
	codexAffinityTTL             = time.Hour
)

type codexAffinitySnapshot struct {
	settings string
	items    []string
	anchors  []bool
	coarse   string
	known    bool
	hasUser  bool
	scanned  int
}

// The existing fast fingerprint is only a candidate hint. Strict SHA-256
// digests retain every effective input field, array order, timestamp and UUID.
func codexAffinitySnapshotOf(body []byte) codexAffinitySnapshot {
	var out codexAffinitySnapshot
	if len(body) > codexAffinityMaxBytes || !json.Valid(body) {
		return out
	}
	root := util.ParseGJSONBytesNoCopy(body)
	if !root.IsObject() {
		return out
	}
	if prev := root.Get("previous_response_id"); prev.Exists() && prev.Type != gjson.Null && prev.String() != "" {
		return out
	}
	input := root.Get("input")
	var items []gjson.Result
	if input.IsArray() {
		input.ForEach(func(_, v gjson.Result) bool { items = append(items, v); return len(items) <= codexAffinityMaxItems })
	} else if input.Type == gjson.String {
		items = []gjson.Result{input}
	} else {
		return out
	}
	if len(items) == 0 || len(items) > codexAffinityMaxItems {
		return out
	}
	budget := affinityHashBudget{}
	// Build settings from small field digests; never decode/serialize the whole
	// multi-megabyte prompt into a second object tree.
	settings := map[string]string{}
	seen := map[string]bool{}
	valid := true
	root.ForEach(func(k, v gjson.Result) bool {
		key := k.String()
		if seen[key] {
			valid = false
			return false
		}
		seen[key] = true
		switch key {
		case "input", "prompt_cache_key", "client_metadata", "stream", "store", "previous_response_id":
			return true
		}
		digest, ok := budget.digest(v, 0)
		if !ok {
			valid = false
			return false
		}
		settings[key] = digest
		return true
	})
	if !valid {
		return out
	}
	encoded, _ := json.Marshal(settings)
	out.settings = affinityBytesDigest(encoded)
	chain := out.settings
	user, anchor := input.Type == gjson.String, false
	for _, item := range items {
		digest, ok := budget.digest(item, 0)
		if !ok {
			return codexAffinitySnapshot{}
		}
		chain = affinityDigest(chain, digest)
		out.items = append(out.items, chain)
		role, kind := item.Get("role").String(), item.Get("type").String()
		if user && (role == "assistant" || kind == "function_call_output") {
			anchor = true
		}
		if role == "user" {
			user = true
		}
		out.anchors = append(out.anchors, anchor)
	}
	// The generic candidate normalizer performs regex/string copies. Large
	// requests already have a strict prefix index and skip that optional hint.
	if len(body) <= 64<<10 {
		turns := session.ExtractCanonicalTurns(translator.FormatCodex, body)
		if len(turns) > 0 {
			out.coarse = session.FastTurnFingerprint(turns[0])
		}
	}
	out.hasUser = user
	out.scanned = len(body)
	out.known = true
	return out
}
func affinityBytesDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

type affinityHashBudget struct{ nodes int }

// 严格摘要只规范化 JSON 对象字段顺序，保留数组顺序和每个标量；
// 重复键、不可验证的远端媒体、深度/节点超限均返回 unknown。
func (b *affinityHashBudget) digest(v gjson.Result, depth int) (string, bool) {
	b.nodes++
	if depth > 64 || b.nodes > codexAffinityMaxPrefixes {
		return "", false
	}
	if v.IsObject() {
		fields := map[string]string{}
		ok := true
		v.ForEach(func(k, child gjson.Result) bool {
			key := k.String()
			if _, exists := fields[key]; exists {
				ok = false
				return false
			}
			switch key {
			case "file_id":
				ok = false
				return false
			case "image_url", "file_url", "audio_url", "video_url":
				media := child
				if child.IsObject() {
					media = child.Get("url")
				}
				if media.Type != gjson.String || !strings.HasPrefix(media.String(), "data:") {
					ok = false
					return false
				}
			}
			digest, valid := b.digest(child, depth+1)
			if !valid {
				ok = false
				return false
			}
			fields[key] = digest
			return true
		})
		if !ok {
			return "", false
		}
		raw, _ := json.Marshal(fields)
		return affinityDigest("object", affinityBytesDigest(raw)), true
	}
	if v.IsArray() {
		chain := affinityDigest("array")
		ok := true
		v.ForEach(func(_, child gjson.Result) bool {
			digest, valid := b.digest(child, depth+1)
			if !valid {
				ok = false
				return false
			}
			chain = affinityDigest(chain, digest)
			return true
		})
		return chain, ok
	}
	// gjson decodes JSON string escaping; control characters in prompt content
	// remain meaningful. Large scalars are hashed without constructing JSON.
	if v.Type == gjson.String {
		return affinityDigest("string", affinityStringDigest(v.Str)), true
	}
	return affinityDigest("scalar", v.Raw), true
}

type codexAffinityTrajectory struct {
	scope, identity, group string
	snapshot               codexAffinitySnapshot
	touched                time.Time
}
type codexAffinityBinding struct {
	group   string
	touched time.Time
}
type codexAffinityFrozen struct {
	pck          string
	fieldsFrozen bool
	live         bool
	coldKey      string
	group        string
	adopted      bool
	refs         int
	touched      time.Time
}
type codexAffinityStore struct {
	mu           sync.Mutex
	generation   uint64
	trajectories map[string]codexAffinityTrajectory
	prefixes     map[string]map[string]struct{}
	bindings     map[string]codexAffinityBinding
	requests     map[string]*codexAffinityFrozen
	cold         map[string]*codexAffinityFrozen
	prefixCount  int
	now          func() time.Time
}

func newCodexAffinityStore() *codexAffinityStore {
	return &codexAffinityStore{trajectories: map[string]codexAffinityTrajectory{}, prefixes: map[string]map[string]struct{}{}, bindings: map[string]codexAffinityBinding{}, requests: map[string]*codexAffinityFrozen{}, cold: map[string]*codexAffinityFrozen{}, now: time.Now}
}

type codexAffinityDecision struct {
	closed                                      bool
	store                                       *codexAffinityStore
	generation                                  uint64
	scope, identity, group, requestKey, coldKey string
	snapshot                                    codexAffinitySnapshot
	adopted                                     bool
	ctx                                         context.Context
	once                                        sync.Once
	frozen                                      *codexAffinityFrozen
	coldFrozen                                  *codexAffinityFrozen
	check                                       string
}

func (s *codexAffinityStore) resetGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.trajectories = map[string]codexAffinityTrajectory{}
	s.prefixes = map[string]map[string]struct{}{}
	s.bindings = map[string]codexAffinityBinding{}
	s.cold = map[string]*codexAffinityFrozen{}
	s.prefixCount = 0
	return s.generation
}
func (s *codexAffinityStore) removeTrajectory(k string) {
	t := s.trajectories[k]
	for _, p := range t.snapshot.items {
		pk := affinityDigest(t.scope, p)
		delete(s.prefixes[pk], k)
		s.prefixCount--
		if len(s.prefixes[pk]) == 0 {
			delete(s.prefixes, pk)
		}
	}
	delete(s.trajectories, k)
}
func (s *codexAffinityStore) expire(now time.Time) {
	for k, t := range s.trajectories {
		if now.Sub(t.touched) > codexAffinityTTL {
			s.removeTrajectory(k)
		}
	}
	for k, b := range s.bindings {
		if now.Sub(b.touched) > codexAffinityTTL {
			delete(s.bindings, k)
		}
	}
	for k, r := range s.requests {
		if !r.live && r.refs == 0 && now.Sub(r.touched) > codexAffinityTTL {
			delete(s.requests, k)
		}
	}
}
func (s *codexAffinityStore) decide(ctx context.Context, scope, identity, parent, requestID, fallback, seed string, reliable bool, snap codexAffinitySnapshot) *codexAffinityDecision {
	d := &codexAffinityDecision{store: s, scope: scope, identity: identity, group: fallback, snapshot: snap, ctx: ctx, check: "unknown"}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	d.generation = s.generation
	if requestID != "" {
		d.requestKey = affinityDigest("logical-request", requestID)
		if r := s.requests[d.requestKey]; r != nil {
			r.refs++
			r.touched = now
			d.group, d.adopted, d.frozen = r.group, r.adopted, r
			d.coldKey = r.coldKey
			if c := s.cold[d.coldKey]; c != nil {
				c.refs++
				d.coldFrozen = c
			}
			return d
		}
	}
	established := false
	bindingKey := identity
	if identity != "" {
		if b, ok := s.bindings[bindingKey]; ok {
			d.group = b.group
			b.touched = now
			s.bindings[bindingKey] = b
			established = true
		}
	}
	if reliable && identity != "" && !established {
		d.coldKey = affinityDigest("identity-reservation", identity)
		if pending := s.cold[d.coldKey]; pending != nil {
			d.group = pending.group
			established = true
		}
	}
	// Once the request budget is full, retain the deterministic temporary
	// fallback instead of allocating an unrecorded random decision on retries.
	if (d.requestKey != "" && len(s.requests) >= codexAffinityMaxTrajectories) || len(s.cold) >= codexAffinityMaxTrajectories || (reliable && !established && len(s.bindings) >= codexAffinityMaxTrajectories) {
		return s.freeze(d, now)
	}
	// Unknown is never treated as mismatch or an empty/full prefix.
	if !snap.known {
		return s.freeze(d, now)
	}
	d.check = "mismatch"
	best := 0
	groups := map[string]struct{}{}
	checked := map[string]struct{}{}
	// 从最长前缀向前检索；先扫公共短前缀会让长对话的旧轨迹
	// 提前耗尽 64 个候选预算，掩盖已有的唯一长前缀。
	for index := len(snap.items) - 1; index >= 0; index-- {
		for k := range s.prefixes[affinityDigest(scope, snap.items[index])] {
			if _, seen := checked[k]; seen {
				continue
			}
			if len(checked) >= codexAffinityMaxCandidates {
				d.check = "unknown"
				return s.freeze(d, now)
			}
			checked[k] = struct{}{}
			t := s.trajectories[k]
			if snap.coarse != "" && t.snapshot.coarse != "" && snap.coarse != t.snapshot.coarse {
				continue
			}
			n := 0
			for n < len(snap.items) && n < len(t.snapshot.items) && snap.items[n] == t.snapshot.items[n] {
				n++
			}
			exact := n == len(snap.items) && n == len(t.snapshot.items)
			if n == 0 || (!exact && parent == "" && !snap.anchors[n-1]) {
				continue
			}
			d.check = "eligible"
			if reliable && (established || parent == "" || t.identity != parent) {
				continue
			}
			if n > best {
				best = n
				groups = map[string]struct{}{t.group: {}}
			} else if n == best {
				groups[t.group] = struct{}{}
			}
		}
		// 当前 bucket 已包含所有该长度的并列候选，较短前缀不能胜出。
		if best > 0 || (reliable && (established || parent == "") && d.check == "eligible") {
			break
		}
	}
	if established {
		return s.freeze(d, now)
	}
	if len(groups) == 1 {
		for g := range groups {
			d.group = g
		}
		d.adopted = true
	} else if !reliable {
		d.coldKey = affinityDigest(scope, snap.settings, snap.items[len(snap.items)-1])
		if c := s.cold[d.coldKey]; c != nil {
			d.group = c.group
		} else {
			// A legacy seed is usable only while no incompatible group owns it.
			compatible := seed != "" && len(groups) == 0
			for _, cold := range s.cold {
				if cold.group == seed {
					compatible = false
					break
				}
			}
			for _, t := range s.trajectories {
				if t.scope == scope && t.group == seed {
					compatible = false
					break
				}
			}
			if compatible {
				d.group = seed
			} else {
				d.group = uuid.NewString()
			}
		}
	}
	return s.freeze(d, now)
}
func (s *codexAffinityStore) freeze(d *codexAffinityDecision, now time.Time) *codexAffinityDecision {
	// Admission failure uses the caller's frozen fallback; active entries are
	// never evicted just to make room for another request.
	r := &codexAffinityFrozen{group: d.group, adopted: d.adopted, refs: 1, touched: now, coldKey: d.coldKey}
	d.frozen = r
	if d.requestKey != "" && len(s.requests) < codexAffinityMaxTrajectories {
		s.requests[d.requestKey] = r
		if d.ctx.Done() != nil {
			r.live = true
			context.AfterFunc(d.ctx, func() { s.mu.Lock(); r.live = false; r.touched = s.now(); s.mu.Unlock() })
		}
	}
	if d.coldKey != "" {
		if c := s.cold[d.coldKey]; c != nil {
			c.refs++
			d.coldFrozen = c
		} else if len(s.cold) < codexAffinityMaxTrajectories {
			c := &codexAffinityFrozen{group: d.group, refs: 1, touched: now}
			s.cold[d.coldKey] = c
			d.coldFrozen = c
		}
	}
	return d
}
func (d *codexAffinityDecision) close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		s := d.store
		s.mu.Lock()
		defer s.mu.Unlock()
		d.closed = true
		if d.frozen != nil {
			d.frozen.refs--
			d.frozen.touched = s.now()

		}
		if d.coldFrozen != nil {
			d.coldFrozen.refs--
			if d.coldFrozen.refs == 0 && s.cold[d.coldKey] == d.coldFrozen {
				delete(s.cold, d.coldKey)
			}
		}
	})
}
func (d *codexAffinityDecision) complete(payload []byte) {
	if d == nil || d.ctx.Err() != nil || gjson.GetBytes(payload, "type").String() != "response.completed" || !d.snapshot.known {
		return
	}
	extended := d.snapshot.withOutput(payload)
	s := d.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.generation != s.generation || d.closed || d.ctx.Err() != nil {
		return
	}
	now := s.now()
	s.expire(now)
	if d.identity != "" && len(s.bindings) < codexAffinityMaxTrajectories {
		k := d.identity
		if _, ok := s.bindings[k]; !ok {
			s.bindings[k] = codexAffinityBinding{d.group, now}
		}
	}
	s.publish(d.scope, d.identity, d.group, d.snapshot, now)
	if extended.known {
		s.publish(d.scope, d.identity, d.group, extended, now)
	}
}

func (s *codexAffinityStore) publish(scope, identity, group string, snapshot codexAffinitySnapshot, now time.Time) {
	k := affinityDigest(scope, group, snapshot.items[len(snapshot.items)-1])
	if _, ok := s.trajectories[k]; ok {
		s.removeTrajectory(k)
	}
	for len(s.trajectories) >= codexAffinityMaxTrajectories || s.prefixCount+len(snapshot.items) > codexAffinityMaxPrefixes {
		oldest := ""
		var when time.Time
		for key, t := range s.trajectories {
			if oldest == "" || t.touched.Before(when) {
				oldest, when = key, t.touched
			}
		}
		if oldest == "" {
			return
		}
		s.removeTrajectory(oldest)
	}
	s.trajectories[k] = codexAffinityTrajectory{scope, identity, group, snapshot, now}
	for _, p := range snapshot.items {
		pk := affinityDigest(scope, p)
		if s.prefixes[pk] == nil {
			s.prefixes[pk] = map[string]struct{}{}
		}
		s.prefixes[pk][k] = struct{}{}
		s.prefixCount++
	}
}

// Publish a completed output as a continuation anchor only when its exact
// effective representation can be verified on the next request.
func (s codexAffinitySnapshot) withOutput(payload []byte) codexAffinitySnapshot {
	if len(payload)+s.scanned > codexAffinityMaxBytes || !json.Valid(payload) {
		return codexAffinitySnapshot{}
	}
	raw := util.GetGJSONBytesNoCopy(payload, "response.output")
	if !raw.IsArray() {
		return codexAffinitySnapshot{}
	}
	var items []gjson.Result
	raw.ForEach(func(_, v gjson.Result) bool {
		items = append(items, v)
		return len(items)+len(s.items) <= codexAffinityMaxItems
	})
	if len(items) == 0 || len(items)+len(s.items) > codexAffinityMaxItems {
		return codexAffinitySnapshot{}
	}
	out := s
	out.items = append([]string(nil), s.items...)
	out.anchors = append([]bool(nil), s.anchors...)
	chain := out.items[len(out.items)-1]
	anchor := out.anchors[len(out.anchors)-1]
	budget := affinityHashBudget{}
	for _, item := range items {
		digest, ok := budget.digest(item, 0)
		if !ok {
			return codexAffinitySnapshot{}
		}
		chain = affinityDigest(chain, digest)
		out.items = append(out.items, chain)
		if out.hasUser && (item.Get("role").String() == "assistant" || item.Get("type").String() == "function_call") {
			anchor = true
		}
		out.anchors = append(out.anchors, anchor)
	}
	return out
}

func affinityStringDigest(value string) string {
	if len(value) <= 32<<10 {
		return affinityBytesDigest([]byte(value))
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	for len(value) > 0 {
		n := copy(buffer, value)
		_, _ = hash.Write(buffer[:n])
		value = value[n:]
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// 同一逻辑请求同时冻结 H/K，策略热更新不能只保留 header 而改掉 pck。
func (d *codexAffinityDecision) freezeFields(body []byte, pck string) ([]byte, string) {
	if d == nil || d.frozen == nil {
		return body, pck
	}
	d.store.mu.Lock()
	if !d.frozen.fieldsFrozen {
		d.frozen.pck = pck
		d.frozen.fieldsFrozen = true
	}
	frozen := d.frozen.pck
	d.store.mu.Unlock()
	if frozen == pck {
		return body, pck
	}
	if frozen == "" {
		body, _ = sjson.DeleteBytes(body, "prompt_cache_key")
	} else {
		body, _ = sjson.SetBytes(body, "prompt_cache_key", frozen)
	}
	return body, frozen
}
