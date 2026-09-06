package flowcontrol

import (
	"fmt"
	"net/http"
	"time"
)

const MaxObservers = 16 // hard bound; the default configurable bound is four

// ServeEvents is an authenticated management endpoint, not a model execution.
// Subscribers share one encoded summary. No active subscriber means no sampler.
func (e *Engine) ServeEvents(w http.ResponseWriter, r *http.Request) {
	if e == nil {
		http.Error(w, "flow control unavailable", 503)
		return
	}
	e.mu.Lock()
	if !e.cfg.Observation.Realtime {
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"flow_control_observation_disabled"}`))
		return
	}
	if e.closed || e.observers >= e.cfg.Observation.MaxObservers {
		e.mu.Unlock()
		w.Header().Set("Retry-After", "2")
		http.Error(w, "too many status observers or server stopped", 503)
		return
	}
	e.observers++
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.observers--; e.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	ctl := http.NewResponseController(w)
	defer ctl.SetWriteDeadline(time.Time{})
	for seq := uint64(1); ; seq++ {
		if r.Context().Err() != nil {
			return
		}
		e.mu.Lock()
		closed := e.closed
		o := e.cfg.Observation
		changed := e.observationChanged
		e.mu.Unlock()
		if closed {
			return
		}
		_ = ctl.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if !o.Realtime {
			_, _ = fmt.Fprint(w, "event: disabled\ndata: {\"realtime-disabled\":true}\n\n")
			_ = ctl.Flush()
			return
		}
		payload, err := e.cachedSummaryJSON()
		if err != nil {
			return
		}
		if _, err = fmt.Fprintf(w, "id: %s-%d\nevent: snapshot\ndata: %s\n\n", e.processID, seq, payload); err != nil {
			return
		}
		if ctl.Flush() != nil {
			return
		}
		timer := time.NewTimer(time.Duration(o.IntervalMS) * time.Millisecond)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
