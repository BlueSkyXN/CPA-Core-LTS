package flowcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Error is deliberately request-scoped: local backpressure is not an upstream
// quota/authentication failure and must never cool down a credential.
type Error struct {
	Code  string
	Rule  string
	retry time.Duration
}

func (e *Error) Error() string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "local_flow_control", "code": e.Code, "message": "Local flow-control capacity unavailable", "rule": e.Rule}})
	return string(b)
}
func (e *Error) StatusCode() int {
	if e.Code == "flow_control_closed" || e.Code == "flow_control_state_full" || e.Code == "flow_control_configuration" {
		return 503
	}
	return 429
}
func (e *Error) IsRequestScoped() bool { return true }
func (e *Error) RetryAfter() *time.Duration {
	v := e.retry
	if v <= 0 {
		v = time.Second
	}
	return &v
}
func (e *Error) Headers() http.Header {
	v := *e.RetryAfter()
	n := int64((v + time.Second - 1) / time.Second)
	return http.Header{"Retry-After": []string{strconv.FormatInt(n, 10)}}
}
func IsError(err error) bool { var e *Error; return errors.As(err, &e) }

type event struct {
	at     time.Time
	ticket uint64
}
type bucket struct {
	rule      Rule
	identity  Identity
	active    int
	history   []event
	retention time.Duration
}
type member struct {
	id string
	b  *bucket
}
type active struct {
	operation *operation
	identity  Identity
	members   []member
	started   time.Time
	phase     string
}
type waiter struct {
	request  *Permit
	identity Identity
	id       uint64
	enqueued time.Time
	bytes    int64
	deadline time.Time
	ctx      context.Context
	done     chan struct{}
	permit   *Permit
	err      error
	blocked  string
}

// Engine has no worker goroutine. Releases/config updates trigger dispatch;
// one stoppable timer handles the earliest rate-window expiry/queue deadline.
// Network calls and user callbacks NEVER run under mu.
type Engine struct {
	mu                 sync.Mutex
	cfg                Config
	buckets            map[string]*bucket
	active             map[uint64]*active
	queue              []*waiter
	queueBytes         int64
	sequence           uint64
	lastServed         map[string]uint64
	timer              *time.Timer
	timerGeneration    uint64
	closed             bool
	now                func() time.Time
	admitted           uint64
	rejected           uint64
	canceled           uint64
	timedOut           uint64
	waited             uint64
	historyCount       int
	activitySequence   uint64
	observers          int
	processID          string
	policyRevision     uint64
	observationChanged chan struct{}
	summaryMu          sync.Mutex
	summaryPayload     []byte
	summaryExpires     time.Time
	summaryRevision    uint64
	summaryBuilds      uint64
	resourceMu         sync.Mutex
	resources          ResourceSample
	resourceExpires    time.Time
	diskExpires        time.Time
}

func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg.Effective(), buckets: map[string]*bucket{}, active: map[uint64]*active{}, now: time.Now, lastServed: map[string]uint64{}, processID: newProcessID(), policyRevision: 1, observationChanged: make(chan struct{})}, nil
}
func (e *Engine) Enabled() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Enabled
}

// Permit owns all matched concurrency slots. Release is idempotent. Rate-window
// records count admissions and are intentionally not refunded on upstream error.
type Permit struct {
	e         *Engine
	id        uint64
	once      sync.Once
	operation *operation
}

func (p *Permit) Release() {
	if p != nil && p.e != nil {
		p.once.Do(func() {
			if p.operation != nil {
				p.e.endOperation(p.operation)
			} else {
				p.e.release(p.id, false)
			}
		})
	}
}

// CancelBeforeDispatch refunds this admission only when no executor has begun.
func (p *Permit) CancelBeforeDispatch() { p.abort() }

func (p *Permit) abort() {
	if p != nil && p.e != nil {
		p.once.Do(func() {
			if p.operation != nil {
				p.e.endOperation(p.operation)
			} else {
				p.e.release(p.id, true)
			}
		})
	}
}

func (e *Engine) Acquire(ctx context.Context, d Identity, payloadBytes int64) (*Permit, error) {
	return e.acquire(ctx, d, payloadBytes, true)
}

// AcquireImmediately performs the same atomic checks, but never reserves a
// waiting position. It is used where an existing execution state machine cannot
// safely retain a pre-dispatch reservation while waiting for capacity.
func (e *Engine) AcquireImmediately(ctx context.Context, d Identity, payloadBytes int64) (*Permit, error) {
	return e.acquire(ctx, d, payloadBytes, false)
}

func (e *Engine) acquire(ctx context.Context, d Identity, payloadBytes int64, mayWait bool) (*Permit, error) {
	return e.acquireWork(ctx, nil, d, payloadBytes, mayWait)
}
func (e *Engine) acquireWork(ctx context.Context, request *Permit, d Identity, payloadBytes int64, mayWait bool) (*Permit, error) {
	if e == nil {
		return &Permit{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if payloadBytes < 0 {
		payloadBytes = 0
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d = d.normalized()
	if (d.Stage != Request && d.Stage != Attempt) || len(d.Key) > 256 || len(d.Account) > 256 || len(d.Model) > 4096 || len(d.Provider) > 256 || len(d.Credential) > 256 || len(d.RequestID) > 128 {
		return nil, &Error{Code: "flow_control_configuration"}
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, &Error{Code: "flow_control_closed"}
	}
	now := e.now()
	e.cleanupLocked(now)
	if !e.cfg.Enabled {
		e.mu.Unlock()
		return &Permit{}, nil
	}
	// Give existing eligible waiters the first chance, instead of queue jumping.
	e.dispatchLocked(now)
	members, next, blocked, stateErr := e.checkWorkLocked(request, d, now)
	if stateErr != nil {
		e.rejected++
		e.mu.Unlock()
		return nil, stateErr
	}
	if blocked == "" {
		p := e.grantWorkLocked(request, d, members, now)
		e.scheduleLocked(now)
		e.mu.Unlock()
		if err := ctx.Err(); err != nil {
			p.abort()
			return nil, err
		}
		return p, nil
	}
	q := e.cfg.Queue
	budget := time.Duration(q.MaxWaitMS) * time.Millisecond
	if request != nil && request.operation != nil {
		op := request.operation
		budget -= op.waitUsed
		if op.attempts > 0 || op.waiting {
			mayWait = false
		}
	}

	if !mayWait || q.MaxWaiting == 0 || q.MaxWaitMS == 0 {
		e.rejected++
		e.mu.Unlock()
		return nil, &Error{Code: "flow_control_busy", Rule: blocked, retry: next.Sub(now)}
	}
	if budget <= 0 {
		e.rejected++
		e.mu.Unlock()
		return nil, &Error{Code: "flow_control_wait_timeout", Rule: blocked}
	}
	perKey := 0
	for _, w := range e.queue {
		if w.identity.Key == d.Key {
			perKey++
		}
	}
	if len(e.queue) >= q.MaxWaiting || perKey >= q.MaxWaitingPerKey || payloadBytes > q.MaxBytes-e.queueBytes {
		e.rejected++
		e.mu.Unlock()
		return nil, &Error{Code: "flow_control_queue_full", Rule: blocked, retry: next.Sub(now)}
	}
	deadline := now.Add(budget)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	e.activitySequence++
	w := &waiter{request: request, id: e.activitySequence, enqueued: now, identity: d, bytes: payloadBytes, deadline: deadline, ctx: ctx, done: make(chan struct{}), blocked: blocked}
	if request != nil && request.operation != nil {
		request.operation.waiting = true
	}
	e.queue = append(e.queue, w)
	e.queueBytes += payloadBytes
	e.waited++
	e.scheduleLocked(now)
	e.mu.Unlock()
	select {
	case <-w.done:
		if err := ctx.Err(); err != nil {
			w.permit.abort()
			return nil, err
		}
		return w.permit, w.err
	case <-ctx.Done():
		e.mu.Lock()
		found := e.removeWaiterLocked(w)
		if found {
			e.canceled++
			w.err = ctx.Err()
			close(w.done)
			e.dispatchLocked(e.now())
			e.scheduleLocked(e.now())
		}
		p := w.permit
		e.mu.Unlock()
		p.abort()
		return nil, ctx.Err()
	}
}

// Available is only a scheduling hint. Acquire is the atomic authoritative check.
// Empty account/provider dimensions must not be guessed by adapters.
func (e *Engine) Available(d Identity) bool {
	if e == nil {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return true
	}
	if e.closed {
		return false
	}
	now := e.now()
	e.cleanupLocked(now)
	_, _, blocked, err := e.checkLocked(d.normalized(), now)
	return blocked == "" && err == nil
}

// AvailableAccount ignores model-specific limits while selecting a credential.
// It must not resolve/rotate a model alias pool just to obtain a scheduling hint.
// Acquire later enforces every rule against the actual resolved model.
func (e *Engine) AvailableAccount(d Identity) bool {
	if e == nil {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return true
	}
	if e.closed {
		return false
	}
	now := e.now()
	e.cleanupLocked(now)
	_, _, blocked, err := e.checkLocked(d.normalized(), now, true)
	return blocked == "" && err == nil
}

func (e *Engine) checkLocked(d Identity, now time.Time, withoutModel ...bool) ([]member, time.Time, string, *Error) {
	var members []member
	var next time.Time
	blocked := ""
	for _, r := range e.cfg.Rules {
		if len(withoutModel) > 0 && withoutModel[0] && (r.groupsModel() || r.Models != nil || (r.Model != "" && r.Model != "*")) {
			continue
		}
		if !r.matches(d) {
			continue
		}
		id := r.bucketID(d)
		b := e.buckets[id]
		if b == nil {
			if len(e.buckets) >= e.cfg.MaxBuckets {
				return nil, next, r.ID, &Error{Code: "flow_control_state_full", Rule: r.ID}
			}
			b = &bucket{rule: r, identity: d, retention: r.retention()}
			e.buckets[id] = b
		}
		b.rule = r
		if r.retention() > b.retention {
			b.retention = r.retention()
		}
		members = append(members, member{id, b})
		if r.MaxConcurrent > 0 && b.active >= r.MaxConcurrent {
			blocked = r.ID
		}
		for _, w := range r.Windows {
			period := time.Duration(w.PeriodMS) * time.Millisecond
			cut := now.Add(-period)
			first := sort.Search(len(b.history), func(i int) bool { return b.history[i].at.After(cut) })
			count := len(b.history) - first
			if count >= w.Requests {
				blocked = r.ID
				available := b.history[first+count-w.Requests].at.Add(period)
				if available.After(next) {
					next = available
				}
			}
		}
	}
	if blocked == "" {
		// Absolute tracking bound also covers policies with only rate rules.
		if len(e.active) >= 100000 {
			return nil, next, "", &Error{Code: "flow_control_state_full"}
		}
		needed := 0
		for _, m := range members {
			if len(m.b.rule.Windows) > 0 {
				needed++
			}
		}
		if e.historyCount+needed > e.cfg.MaxHistory {
			return nil, next, "", &Error{Code: "flow_control_state_full"}
		}
	}
	return members, next, blocked, nil
}

func (e *Engine) grantLocked(d Identity, members []member, now time.Time) *Permit {
	e.sequence++
	id := e.sequence
	for _, m := range members {
		m.b.active++
		if len(m.b.rule.Windows) > 0 {
			m.b.history = append(m.b.history, event{now, id})
			e.historyCount++
		}
	}
	e.active[id] = &active{identity: d, members: members, started: now, phase: "running"}
	e.admitted++
	e.lastServed[d.Key] = e.sequence
	return &Permit{e: e, id: id}
}
func (e *Engine) release(id uint64, refund bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.releaseRecordLocked(id, refund)
	now := e.now()
	e.cleanupLocked(now)
	e.dispatchLocked(now)
	e.scheduleLocked(now)
}
func (e *Engine) releaseRecordLocked(id uint64, refund bool) {
	a := e.active[id]
	if a == nil {
		return
	}
	for _, m := range a.members {
		m.b.active--
	}
	delete(e.active, id)
	if refund {
		for _, b := range e.buckets {
			for i := 0; i < len(b.history); i++ {
				if b.history[i].ticket == id {
					b.history = append(b.history[:i], b.history[i+1:]...)
					e.historyCount--
					break
				}
			}
		}
	}
	if op := a.operation; op != nil {
		op.attempts--
		if !refund {
			op.started = true
		}
		if op.attempts == 0 && op.ticket != 0 && (op.closed || !op.started) {
			ticket := op.ticket
			op.ticket = 0
			e.releaseRecordLocked(ticket, !op.started)
		}
	}
}

func (e *Engine) removeWaiterLocked(w *waiter) bool {
	for i, v := range e.queue {
		if v == w {
			copy(e.queue[i:], e.queue[i+1:])
			e.queue[len(e.queue)-1] = nil
			e.queue = e.queue[:len(e.queue)-1]
			e.queueBytes -= w.bytes
			if w.request != nil && w.request.operation != nil {
				op := w.request.operation
				op.waiting = false
				if spent := e.now().Sub(w.enqueued); spent > 0 {
					op.waitUsed += spent
				}
			}
			return true
		}
	}
	return false
}
func (e *Engine) finishWaiterLocked(w *waiter, p *Permit, err error) {
	e.removeWaiterLocked(w)
	w.permit = p
	w.err = err
	close(w.done)
}
func (e *Engine) dispatchLocked(now time.Time) {
	for i := 0; i < len(e.queue); {
		w := e.queue[i]
		if err := w.ctx.Err(); err != nil {
			e.canceled++
			e.finishWaiterLocked(w, nil, err)
			continue
		}
		if !now.Before(w.deadline) {
			e.timedOut++
			var err error = &Error{Code: "flow_control_wait_timeout", Rule: w.blocked}
			if parent, ok := w.ctx.Deadline(); ok && !now.Before(parent) {
				err = context.DeadlineExceeded
			}
			e.finishWaiterLocked(w, nil, err)
			continue
		}
		i++
	}
	for len(e.queue) > 0 {
		var chosen *waiter
		var slots workMembers
		// Choose the eligible key least recently served, then its oldest eligible
		// waiter. Empty keys do not reset another key's place in the rotation.
		var bestRank uint64
		eligibleKeys := make(map[string]bool)
		for _, w := range e.queue {
			if eligibleKeys[w.identity.Key] {
				continue
			}
			if !e.cfg.Enabled {
				chosen = w
				break
			}
			m, _, blocked, err := e.checkWorkLocked(w.request, w.identity, now)
			if err != nil {
				w.blocked = err.Code
				continue
			}
			w.blocked = blocked
			if blocked != "" {
				continue
			}
			eligibleKeys[w.identity.Key] = true
			rank := e.lastServed[w.identity.Key]
			if chosen == nil || rank < bestRank {
				chosen = w
				slots = m
				bestRank = rank
			}
		}
		if chosen == nil {
			break
		}
		p := &Permit{}
		if e.cfg.Enabled {
			p = e.grantWorkLocked(chosen.request, chosen.identity, slots, now)
		}
		e.finishWaiterLocked(chosen, p, nil)
	}
}
func (e *Engine) cleanupLocked(now time.Time) {
	keys := make(map[string]bool)
	for _, a := range e.active {
		keys[a.identity.Key] = true
	}
	for _, w := range e.queue {
		keys[w.identity.Key] = true
	}
	for key := range e.lastServed {
		if !keys[key] {
			delete(e.lastServed, key)
		}
	}
	for id, b := range e.buckets {
		cut := now.Add(-b.retention)
		first := sort.Search(len(b.history), func(i int) bool { return b.history[i].at.After(cut) })
		if first > 0 {
			e.historyCount -= first
			if first == len(b.history) {
				b.history = nil
			} else {
				b.history = append([]event(nil), b.history[first:]...)
			}
		}
		if b.active == 0 && len(b.history) == 0 {
			delete(e.buckets, id)
		}
	}
}
func (e *Engine) scheduleLocked(now time.Time) {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.timerGeneration++
	if e.closed || len(e.queue) == 0 {
		return
	}
	earliest := e.queue[0].deadline
	for _, w := range e.queue {
		if w.deadline.Before(earliest) {
			earliest = w.deadline
		}
		if e.cfg.Enabled {
			_, next, _, _ := e.checkWorkLocked(w.request, w.identity, now)
			if next.After(now) && next.Before(earliest) {
				earliest = next
			}
		}
	}
	// If history capacity is the blocker, expiration must also wake the queue.
	for _, b := range e.buckets {
		if len(b.history) > 0 {
			at := b.history[0].at.Add(b.retention)
			if at.After(now) && at.Before(earliest) {
				earliest = at
			}
		}
	}
	delay := earliest.Sub(now)
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	generation := e.timerGeneration
	e.timer = time.AfterFunc(delay, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if generation != e.timerGeneration || e.closed {
			return
		}
		n := e.now()
		e.cleanupLocked(n)
		e.dispatchLocked(n)
		e.scheduleLocked(n)
	})
}

// Update leaves live work running and reprojects its current concurrency into
// new rules. Existing rolling records remain for stable rule IDs/dimensions.
// A newly added/lengthened window cannot reconstruct already expired history.
func (e *Engine) Update(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg = cfg.Effective()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return &Error{Code: "flow_control_closed"}
	}
	if (cfg.Version >= 3) != (e.cfg.Version >= 3) && (len(e.active) > 0 || len(e.queue) > 0) {
		return &Error{Code: "flow_control_migration_busy"}
	}
	// Preflight projected buckets before publishing; a too-small resource cap
	// must not partially replace the running configuration or discard rate state.
	now := e.now()
	e.cleanupLocked(now)
	// Retained rate timestamps lack full historical identities. Do not silently
	// reinterpret another model set's rate history under the same rule ID.
	// Concurrency-only regrouping remains fully hot-reloadable.
	if cfg.Version >= 3 {
		for _, b := range e.buckets {
			if len(b.history) == 0 {
				continue
			}
			for _, r := range cfg.Rules {
				if len(r.Windows) > 0 && r.ID == b.rule.ID && !sameRuleDomain(b.rule, r) {
					return &Error{Code: "flow_control_rate_domain_change", Rule: r.ID}
				}
			}
		}
	}
	needed := make(map[string]bool, len(e.buckets))
	for id, b := range e.buckets {
		if len(b.history) > 0 {
			needed[id] = true
		}
	}
	if cfg.Enabled {
		for _, a := range e.active {
			for _, r := range cfg.Rules {
				if r.matches(a.identity) {
					needed[r.bucketID(a.identity)] = true
				}
			}
		}
		if len(needed) > cfg.MaxBuckets || e.historyCount > cfg.MaxHistory {
			return &Error{Code: "flow_control_state_full"}
		}
	}
	e.cfg = cfg
	e.policyRevision++
	close(e.observationChanged)
	e.observationChanged = make(chan struct{})
	for _, b := range e.buckets {
		b.active = 0
	}
	for _, a := range e.active {
		a.members = nil
		if !cfg.Enabled {
			continue
		}
		for _, r := range cfg.Rules {
			if !r.matches(a.identity) {
				continue
			}
			id := r.bucketID(a.identity)
			b := e.buckets[id]
			if b == nil {
				b = &bucket{rule: r, identity: a.identity, retention: r.retention()}
				e.buckets[id] = b
			}
			b.rule = r
			if r.retention() > b.retention {
				b.retention = r.retention()
			}
			b.active++
			a.members = append(a.members, member{id, b})
		}
	}
	now = e.now()
	e.cleanupLocked(now)
	e.dispatchLocked(now)
	e.scheduleLocked(now)
	return nil
}
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	close(e.observationChanged)
	e.observationChanged = make(chan struct{})
	for len(e.queue) > 0 {
		e.finishWaiterLocked(e.queue[0], nil, &Error{Code: "flow_control_closed"})
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timerGeneration++
}
