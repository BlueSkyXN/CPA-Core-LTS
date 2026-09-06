package flowcontrol

import (
	"container/heap"
	"fmt"
	"sort"
	"time"
)

// DetailsOptions selects a bounded page BEFORE constructing rule explanations.
// Offset is a display convenience, not a strict queue execution position.
type DetailsOptions struct {
	Offset, Limit                     int
	Stage, State, Key, Account, Model string
}
type activitySeed struct {
	id               uint64
	waiting          bool
	identity         Identity
	since, timeLimit time.Time
	phase            string
	bytes            int64
	position         int
	requestIdentity  *Identity
}
type seedHeap []activitySeed

func (s seedHeap) Len() int { return len(s) }
func before(a, b activitySeed) bool {
	if a.waiting != b.waiting {
		return a.waiting
	}
	return a.id < b.id
}
func (s seedHeap) Less(i, j int) bool { return before(s[j], s[i]) }
func (s seedHeap) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s *seedHeap) Push(v any)        { *s = append(*s, v.(activitySeed)) }
func (s *seedHeap) Pop() any          { old := *s; n := len(old); v := old[n-1]; *s = old[:n-1]; return v }

func (e *Engine) Snapshot() Snapshot { return e.Details(DetailsOptions{Limit: MaxActivityRows}) }
func (e *Engine) Details(q DetailsOptions) Snapshot {
	if e == nil {
		return Snapshot{Buckets: []BucketSnapshot{}, Activity: []Activity{}, Blocked: map[string]int{}}
	}
	if q.Limit < 1 || q.Limit > MaxActivityRows {
		q.Limit = MaxActivityRows
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > 10000 {
		q.Offset = 10000
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	s := e.summaryLocked(now)
	out := Snapshot{Enabled: s.Enabled, Requests: s.Requests, Attempts: s.Attempts, Waiting: s.Waiting, WaitingRequests: s.WaitingRequests, WaitingAttempts: s.WaitingAttempts, QueuedBytes: s.QueuedBytes, Admitted: s.Admitted, Rejected: s.Rejected, Canceled: s.Canceled, TimedOut: s.TimedOut, Waited: s.Waited, Blocked: s.Blocked, SampledAt: now, ProcessID: e.processID, Policy: e.cfg.Effective(), PolicyRevision: e.policyRevision, ActivityTotal: len(e.active) + len(e.queue), OldestWaitMS: s.OldestWaitMS, Activity: []Activity{}, Buckets: []BucketSnapshot{}, Offset: q.Offset}
	selected := &seedHeap{}
	capacity := q.Offset + q.Limit
	add := func(v activitySeed) {
		if (q.Stage != "" && v.identity.Stage != q.Stage) || (q.State != "" && v.phase != q.State) || (q.Key != "" && q.Key != v.identity.Key) || (q.Account != "" && q.Account != v.identity.Account) || (q.Model != "" && !matchOne(q.Model, v.identity.Model, v.identity.Provider, true)) {
			return
		}
		out.MatchingTotal++
		if selected.Len() < capacity {
			heap.Push(selected, v)
		} else if before(v, (*selected)[0]) {
			(*selected)[0] = v
			heap.Fix(selected, 0)
		}
	}
	for i, w := range e.queue {
		var requestIdentity *Identity
		if w.request != nil && w.request.operation != nil && w.request.operation.ticket == 0 {
			requestIdentity = &w.request.operation.identity
		}
		add(activitySeed{requestIdentity: requestIdentity, id: w.id, waiting: true, identity: w.identity, since: w.enqueued, timeLimit: w.deadline, phase: "waiting", bytes: w.bytes, position: i + 1})
	}
	for id, a := range e.active {
		add(activitySeed{id: id, identity: a.identity, since: a.started, phase: a.phase})
	}
	seeds := []activitySeed(*selected)
	sort.Slice(seeds, func(i, j int) bool { return before(seeds[i], seeds[j]) })
	for index := q.Offset; index < len(seeds); index++ {
		v := seeds[index]
		row := Activity{ID: fmt.Sprintf("run-%d", v.id), Identity: v.identity, State: v.phase, Since: v.since, ElapsedMS: now.Sub(v.since).Milliseconds(), Position: v.position, PayloadBytes: v.bytes}
		if v.waiting {
			row.ID = fmt.Sprintf("wait-%d", v.id)
			row.WaitRemainingMS = v.timeLimit.Sub(now).Milliseconds()
			if row.WaitRemainingMS < 0 {
				row.WaitRemainingMS = 0
			}
			// Only selected waiters get detailed matching. The live summary never does.
			x := e.explainLocked(v.identity, now, false)
			row.BlockingRules = x.BlockingRules
			// A joint first attempt may be blocked by a request-stage rule.
			if v.requestIdentity != nil {
				rx := e.explainLocked(*v.requestIdentity, now, false)
				row.BlockingRules = append(row.BlockingRules, rx.BlockingRules...)
			}
		} else if a := e.active[v.id]; a != nil {
			for _, m := range a.members {
				row.Rules = append(row.Rules, m.b.rule.ID)
			}
		}
		out.Activity = append(out.Activity, row)
	}
	out.ActivityTruncated = q.Offset+len(out.Activity) < out.MatchingTotal
	// Bucket output is bounded during selection too; no unbounded history copy.
	ids := make([]string, 0, 1001)
	for id := range e.buckets {
		ids = append(ids, id)
		if len(ids) > 1000 {
			out.Truncated = true
			break
		}
	}
	sort.Strings(ids)
	current := map[string]bool{}
	for _, r := range e.cfg.Rules {
		current[r.ID] = true
	}
	for _, id := range ids {
		if len(out.Buckets) >= 1000 {
			break
		}
		b := e.buckets[id]
		row := BucketSnapshot{Rule: b.rule.ID, Label: b.rule.Label, Stage: b.rule.Stage, Scope: b.rule.Scope, Active: b.active, MaxConcurrent: b.rule.MaxConcurrent, Dimensions: map[string]string{}, Retired: !current[b.rule.ID]}
		dims, _ := b.rule.Dimensions()
		for _, dim := range dims {
			v := b.identity.dimension(dim)
			row.Dimensions[dim] = v
			switch dim {
			case "key":
				row.Key = v
			case "model":
				row.Model = v
				if b.rule.qualifiedModel && b.identity.Stage == Attempt {
					row.Provider = b.identity.Provider
				}
			case "provider":
				row.Provider = v
			case "account":
				row.Account = v
			case "credential":
				row.Credential = v
			case "auth-kind":
				row.AuthKind = v
			}
		}
		for _, w := range b.rule.Windows {
			cut := now.Add(-time.Duration(w.PeriodMS) * time.Millisecond)
			first := sort.Search(len(b.history), func(i int) bool { return b.history[i].at.After(cut) })
			row.WindowCounts = append(row.WindowCounts, len(b.history)-first)
		}
		out.Buckets = append(out.Buckets, row)
	}
	return out
}
