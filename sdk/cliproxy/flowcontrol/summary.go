package flowcontrol

import (
	"encoding/json"
	"time"
)

// Summary contains no request details, model catalog or rule arrays. Counts are
// exact at SampledAt; cached SSE consumers can lag by one configured interval.
type Summary struct {
	Enabled         bool              `json:"enabled"`
	Requests        int               `json:"active-requests"`
	Attempts        int               `json:"active-attempts"`
	Waiting         int               `json:"waiting"`
	WaitingRequests int               `json:"waiting-requests"`
	WaitingAttempts int               `json:"waiting-attempts"`
	QueuedBytes     int64             `json:"queued-bytes"`
	Admitted        uint64            `json:"admitted"`
	Rejected        uint64            `json:"rejected"`
	Canceled        uint64            `json:"canceled"`
	TimedOut        uint64            `json:"timed-out"`
	Waited          uint64            `json:"waited"`
	OldestWaitMS    int64             `json:"oldest-wait-ms"`
	Blocked         map[string]int    `json:"blocked-by-rule"`
	BlockedMeaning  string            `json:"blocking-count-basis"`
	SampledAt       time.Time         `json:"sampled-at"`
	ProcessID       string            `json:"process-id"`
	PolicyRevision  uint64            `json:"policy-revision"`
	Observation     ObservationConfig `json:"observation"`
	Resources       *ResourceSample   `json:"resources,omitempty"`
}

func (e *Engine) summaryLocked(now time.Time) Summary {
	s := Summary{Enabled: e.cfg.Enabled, Waiting: len(e.queue), QueuedBytes: e.queueBytes, Admitted: e.admitted, Rejected: e.rejected, Canceled: e.canceled, TimedOut: e.timedOut, Waited: e.waited, Blocked: map[string]int{}, BlockedMeaning: "last-admission-check", SampledAt: now, ProcessID: e.processID, PolicyRevision: e.policyRevision, Observation: e.cfg.Observation}
	for _, a := range e.active {
		if a.identity.Stage == Request {
			s.Requests++
		} else {
			s.Attempts++
		}
	}
	for _, w := range e.queue {
		if w.identity.Stage == Request || w.request != nil && w.request.operation != nil && w.request.operation.ticket == 0 {
			s.WaitingRequests++
		} else {
			s.WaitingAttempts++
		}
		if elapsed := now.Sub(w.enqueued).Milliseconds(); elapsed > s.OldestWaitMS {
			s.OldestWaitMS = elapsed
		}
		if w.blocked != "" {
			s.Blocked[w.blocked]++
		}
	}
	return s
}
func (e *Engine) Summary() Summary {
	if e == nil {
		return Summary{Blocked: map[string]int{}}
	}
	e.mu.Lock()
	s := e.summaryLocked(e.now())
	e.mu.Unlock()
	if s.Observation.Resources {
		sample := e.sampleResources()
		s.Resources = &sample
	}
	return s
}
func (e *Engine) Policy() Config {
	if e == nil {
		return Config{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Effective()
}

// cachedSummaryJSON has a separate lock: only one observer per interval performs
// collection+encoding. Engine.Update never waits on this lock or on network I/O.
func (e *Engine) cachedSummaryJSON() ([]byte, error) {
	e.summaryMu.Lock()
	defer e.summaryMu.Unlock()
	e.mu.Lock()
	rev := e.policyRevision
	interval := time.Duration(e.cfg.Observation.IntervalMS) * time.Millisecond
	e.mu.Unlock()
	now := time.Now()
	if e.summaryPayload != nil && e.summaryRevision == rev && now.Before(e.summaryExpires) {
		return e.summaryPayload, nil
	}
	s := e.Summary()
	payload, err := json.Marshal(struct {
		Schema int     `json:"schema-version"`
		State  Summary `json:"state"`
	}{3, s})
	if err != nil {
		return nil, err
	}
	e.summaryPayload = payload
	e.summaryRevision = s.PolicyRevision
	e.summaryExpires = now.Add(interval)
	e.summaryBuilds++
	return payload, nil
}
