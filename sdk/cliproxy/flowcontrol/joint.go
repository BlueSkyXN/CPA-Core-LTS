package flowcontrol

import (
	"context"
	"time"
)

// All operation fields are protected by Engine.mu. Creating a call does not
// reserve running capacity. Its first attempt atomically creates both records.
type operation struct {
	identity Identity
	ticket   uint64
	attempts int
	started  bool
	closed   bool
	waiting  bool
	waitUsed time.Duration
}
type workMembers struct{ request, attempt []member }

func (e *Engine) Version() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Version
}
func (e *Engine) BeginRequest(d Identity) *Permit {
	return &Permit{e: e, operation: &operation{identity: d.normalized()}}
}
func (e *Engine) AcquireForRequest(ctx context.Context, request *Permit, d Identity, payloadBytes int64, mayWait bool) (*Permit, error) {
	if request != nil && request.operation != nil &&
		(request.e != e || request.operation.identity.Stage != Request || d.Stage != Attempt ||
			len(request.operation.identity.Key) > 256 || len(request.operation.identity.Model) > 4096 || len(request.operation.identity.RequestID) > 128) {
		return nil, &Error{Code: "flow_control_configuration"}
	}
	return e.acquireWork(ctx, request, d, payloadBytes, mayWait)
}
func (e *Engine) checkWorkLocked(request *Permit, d Identity, now time.Time) (workMembers, time.Time, string, *Error) {
	var out workMembers
	var first time.Time
	if request != nil && request.operation != nil {
		op := request.operation
		if op.closed {
			return out, first, "", &Error{Code: "flow_control_closed"}
		}
		if op.ticket == 0 {
			var blocked string
			var err *Error
			out.request, first, blocked, err = e.checkLocked(op.identity, now)
			if err != nil || blocked != "" {
				return out, first, blocked, err
			}
		}
	}
	var next time.Time
	var blocked string
	var err *Error
	out.attempt, next, blocked, err = e.checkLocked(d, now)
	if next.After(first) {
		first = next
	}
	if err == nil && blocked == "" {
		records := 1
		if request != nil && request.operation != nil && request.operation.ticket == 0 {
			records++
		}
		needed := 0
		for _, mm := range [][]member{out.request, out.attempt} {
			for _, m := range mm {
				if len(m.b.rule.Windows) > 0 {
					needed++
				}
			}
		}
		if len(e.active)+records > 100000 || e.historyCount+needed > e.cfg.MaxHistory {
			err = &Error{Code: "flow_control_state_full"}
		}
	}
	return out, first, blocked, err
}
func (e *Engine) grantWorkLocked(request *Permit, d Identity, mm workMembers, now time.Time) *Permit {
	var op *operation
	if request != nil {
		op = request.operation
	}
	if op != nil && op.ticket == 0 {
		op.ticket = e.grantLocked(op.identity, mm.request, now).id
	}
	p := e.grantLocked(d, mm.attempt, now)
	if op != nil {
		op.attempts++
		e.active[p.id].operation = op
	}
	return p
}

// CommitDispatch distinguishes a real invocation from a failed pre-dispatch
// target recheck. Only an unsent admission can refund its rolling-window record.
func (p *Permit) CommitDispatch() {
	if p == nil || p.e == nil {
		return
	}
	p.e.mu.Lock()
	defer p.e.mu.Unlock()
	if a := p.e.active[p.id]; a != nil && a.operation != nil {
		a.operation.started = true
	}
}
func (e *Engine) endOperation(op *operation) {
	e.mu.Lock()
	defer e.mu.Unlock()
	op.closed = true
	for i := 0; i < len(e.queue); {
		w := e.queue[i]
		if w.request != nil && w.request.operation == op {
			e.finishWaiterLocked(w, nil, &Error{Code: "flow_control_closed"})
			continue
		}
		i++
	}
	if op.attempts == 0 && op.ticket != 0 {
		id := op.ticket
		op.ticket = 0
		e.releaseRecordLocked(id, !op.started)
	}
	n := e.now()
	e.cleanupLocked(n)
	e.dispatchLocked(n)
	e.scheduleLocked(n)
}

// WaitForProducer lets a sequential retry wait for an already-dispatched stream
// to terminate. It never releases that producer's slots or marks a hedge as
// sequential. Cleanup time shares the logical call's cumulative queue budget.
func (e *Engine) WaitForProducer(ctx context.Context, request *Permit, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	default:
	}
	if e == nil {
		return &Error{Code: "flow_control_busy"}
	}
	e.mu.Lock()
	budget := time.Duration(e.cfg.Queue.MaxWaitMS) * time.Millisecond
	var op *operation
	if request != nil && request.operation != nil {
		if request.e != e {
			e.mu.Unlock()
			return &Error{Code: "flow_control_configuration"}
		}
		op = request.operation
		budget -= op.waitUsed
	}
	start := e.now()
	e.mu.Unlock()
	if budget <= 0 {
		return &Error{Code: "flow_control_wait_timeout"}
	}
	defer func() {
		if op != nil {
			e.mu.Lock()
			if spent := e.now().Sub(start); spent > 0 {
				op.waitUsed += spent
			}
			e.mu.Unlock()
		}
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return &Error{Code: "flow_control_wait_timeout"}
	}
}
