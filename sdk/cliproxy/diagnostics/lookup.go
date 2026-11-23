package diagnostics

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type TraceLocation struct {
	InstanceID    string `json:"instance_id"`
	DiagnosticsID string `json:"diagnostics_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
}

type TraceRow struct {
	TraceLocation
	At               time.Time               `json:"at"`
	Model            string                  `json:"model"`
	AuthIndex        string                  `json:"auth_index,omitempty"`
	TraceID          string                  `json:"trace_id,omitempty"`
	LogicalRequestID string                  `json:"logical_request_id,omitempty"`
	ResponseID       string                  `json:"response_id,omitempty"`
	Available        bool                    `json:"available"`
	AttemptCount     int                     `json:"attempt_count,omitempty"`
	Status           int                     `json:"status,omitempty"`
	Outcome          string                  `json:"outcome,omitempty"`
	Matched          []string                `json:"matched,omitempty"`
	InputTokens      *int64                  `json:"input_tokens,omitempty"`
	CacheObservation *usage.CacheObservation `json:"cache_observation,omitempty"`
	CacheRoute       *CacheRouteSummary      `json:"cache_route,omitempty"`
	Previous         *TraceLocation          `json:"previous,omitempty"`
	PreviousSource   string                  `json:"previous_source,omitempty"`
}

type TraceQuery struct {
	ID         string
	InstanceID string
	Focus      string
	Route      *CacheRouteKey
	FromMS     *int64
	ToMS       *int64
}

var LookupIDTypes = []string{"request_id", "trace_id", "diagnostics_id", "logical_request_id", "response_id"}

// Lookup walks the existing bounded store, without modifying access order or retention.
func (s *Store) Lookup(query TraceQuery) ([]TraceRow, string) {
	rows := []TraceRow{}
	if s == nil {
		return rows, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	updated := map[string]uint64{}
	type responseKey struct{ scope, credential, digest string }
	keyForResponse := func(attempt Attempt, digest string) responseKey {
		key := responseKey{scope: attempt.RouteScope, digest: digest}
		if attempt.Provider == "claude" {
			key.credential = attempt.AuthIndex
			if key.credential == "" {
				key.scope = ""
			}
		}
		return key
	}
	type previous struct {
		location TraceLocation
		at       time.Time
	}
	completed := map[responseKey][]previous{}
	for _, entry := range s.entries {
		t := &entry.value
		if now.Sub(t.UpdatedAt) > Retention || query.InstanceID != "" && query.InstanceID != t.InstanceID {
			continue
		}
		updated[t.ID] = entry.revision
		for _, a := range t.Attempts {
			finished := a.ResponseStatus == "completed" || a.Outcome == "completed" ||
				(a.ObservationState == "observed" && a.Outcome == "body_consumed" && a.Status >= 200 && a.Status < 300 && a.ResponseStatus == "")
			if key := keyForResponse(a, Hash(a.ResponseID)); key.scope != "" && a.ResponseID != "" && finished && a.Status < 400 {
				completed[key] = append(completed[key], previous{t.location(a), a.StartedAt})
			}
		}
	}
	isInWindow := func(at time.Time) bool {
		ms := at.UnixMilli()
		return (query.FromMS == nil || ms >= *query.FromMS) && (query.ToMS == nil || ms <= *query.ToMS)
	}
	for _, entry := range s.entries {
		t := &entry.value
		if _, active := updated[t.ID]; !active {
			continue
		}
		common := t.matchID(query.ID)
		fastMatch := query.Route == nil || t.InputSession != nil && t.InputSession.CacheRouteKey == *query.Route
		wholeTrace := query.ID == "" || len(common) > 0
		appendRow := func(a Attempt, matched []string, responseID string) {
			row := t.row(a)
			row.Matched, row.ResponseID = matched, responseID
			if key := keyForResponse(a, a.PreviousResponse); key.scope != "" && a.PreviousResponse != "" {
				var candidates []TraceLocation
				for _, p := range completed[key] {
					if p.location.DiagnosticsID != t.ID && p.at.Before(a.StartedAt) {
						candidates = append(candidates, p.location)
					}
				}
				if len(candidates) == 1 {
					row.Previous = &candidates[0]
					row.PreviousSource = a.PreviousSource
				}
			}
			rows = append(rows, row)
		}
		var best *Attempt
		matchedAttempt := false
		matchedExecution := false
		for i := len(t.Attempts) - 1; i >= 0; i-- {
			a := &t.Attempts[i]
			if !isInWindow(a.StartedAt) || !fastMatch && !slices.ContainsFunc(a.CacheReferences, func(ref CacheRouteReference) bool {
				return ref.CacheRouteKey == *query.Route
			}) || !focusMatches(query.Focus, t, a) {
				continue
			}
			if best == nil {
				best = a
			}
			if query.ID == "" {
				continue
			}
			matched := slices.Clone(common)
			matchesResponse := query.ID == a.ResponseID || slices.Contains(a.ResponseIDs, query.ID)
			if query.ID == a.ExecutionID && (!matchedExecution || matchesResponse) {
				matched = append(matched, "request_id")
				matchedExecution = true
			}
			response := a.ResponseID
			if matchesResponse {
				matched = append(matched, "response_id")
				response = query.ID
			}
			if len(matched) > len(common) {
				appendRow(*a, matched, response)
				matchedAttempt = true
			}
		}
		if wholeTrace && !matchedAttempt {
			if best != nil {
				appendRow(*best, common, best.ResponseID)
			} else if len(t.Attempts) == 0 && fastMatch && query.Focus == "" && isInWindow(t.CreatedAt) {
				appendRow(Attempt{}, common, "")
			}
		}
	}
	slices.SortFunc(rows, func(a, b TraceRow) int {
		if byTime := b.At.Compare(a.At); byTime != 0 {
			return byTime
		}
		if byID := cmp.Compare(a.DiagnosticsID, b.DiagnosticsID); byID != 0 {
			return byID
		}
		return cmp.Compare(b.Attempt, a.Attempt)
	})
	var revision strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&revision, "%s/%d/%d\n", row.DiagnosticsID, row.Attempt, updated[row.DiagnosticsID])
	}
	return rows, Hash(revision.String())
}

func (t *Trace) location(a Attempt) TraceLocation {
	return TraceLocation{InstanceID: t.InstanceID, DiagnosticsID: t.ID, RequestID: a.ExecutionID, Attempt: a.Number}
}

func (t *Trace) row(a Attempt) TraceRow {
	at := a.StartedAt
	if at.IsZero() {
		at = t.CreatedAt
	}
	model := t.Model
	if model == "" {
		model = a.Model
	}
	return TraceRow{TraceLocation: t.location(a), At: at, Model: model, AuthIndex: a.AuthIndex, Available: true, AttemptCount: len(t.Attempts), Status: a.Status, Outcome: a.Outcome,
		TraceID: t.TraceID, LogicalRequestID: t.LogicalRequestID, InputTokens: usage.CloneInt64Ptr(a.InputTokens),
		CacheObservation: a.CacheObservation.Clone(), CacheRoute: t.cacheRoute(a.ExecutionID, a.DecisionID)}
}

func (t *Trace) matchID(id string) []string {
	if id == "" {
		return nil
	}
	var matches []string
	if id == t.ID {
		matches = append(matches, "diagnostics_id")
	}
	if id == t.TraceID {
		matches = append(matches, "trace_id")
	}
	if id == t.LogicalRequestID {
		matches = append(matches, "logical_request_id")
	}
	return matches
}

func focusMatches(focus string, trace *Trace, attempt *Attempt) bool {
	switch focus {
	case "":
		return true
	case "zero_read":
		o := attempt.CacheObservation
		return o != nil && o.Read.State == "present" && o.Read.Value != nil && *o.Read.Value == 0
	case "retry":
		return len(trace.Attempts) > 1
	case "official_loss":
		return attempt.hasOfficialCacheLoss()
	default:
		return false
	}
}

// RecordUsage adds finalized parser evidence to the latest retained attempt of this execution.
func (e *Execution) RecordUsage(detail usage.Detail) {
	if e == nil || e.Ref == nil || (detail.InputTokens <= 0 && detail.CacheObservation == nil) {
		return
	}
	n := int(e.lastAttempt.Load())
	if n <= 0 {
		return
	}
	e.Ref.update(func(t *Trace) {
		if n > len(t.Attempts) {
			return
		}
		a := &t.Attempts[n-1]
		if a.ExecutionID != e.ID {
			return
		}
		if detail.InputTokens > 0 {
			a.InputTokens = usage.Int64Ptr(detail.InputTokens)
		}
		a.CacheObservation = usage.MergeCacheObservations(detail.CacheObservation.Clone(), a.CacheObservation)
	})
}
