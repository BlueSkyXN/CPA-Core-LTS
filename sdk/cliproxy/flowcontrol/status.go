package flowcontrol

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const MaxActivityRows = 200

func newProcessID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("process-%d", time.Now().UnixNano())
}
func (r Rule) groupsModel() bool {
	dims, _ := r.Dimensions()
	for _, d := range dims {
		if d == "model" {
			return true
		}
	}
	return false
}

type BucketSnapshot struct {
	Rule          string            `json:"rule"`
	Label         string            `json:"label,omitempty"`
	Stage         string            `json:"stage"`
	Scope         string            `json:"scope"`
	Key           string            `json:"key,omitempty"`
	Model         string            `json:"model,omitempty"`
	Account       string            `json:"account,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Credential    string            `json:"credential,omitempty"`
	AuthKind      string            `json:"auth-kind,omitempty"`
	Dimensions    map[string]string `json:"dimensions,omitempty"`
	Active        int               `json:"active"`
	MaxConcurrent int               `json:"max-concurrent"`
	WindowCounts  []int             `json:"window-counts,omitempty"`
	Retired       bool              `json:"retired,omitempty"`
}

type Activity struct {
	ID string `json:"id"`
	Identity
	State           string    `json:"state"`
	Since           time.Time `json:"since"`
	ElapsedMS       int64     `json:"elapsed-ms"`
	WaitRemainingMS int64     `json:"wait-remaining-ms,omitempty"`
	Position        int       `json:"position,omitempty"`
	BlockingRules   []string  `json:"blocking-rules,omitempty"`
	Rules           []string  `json:"rules,omitempty"`
	PayloadBytes    int64     `json:"payload-bytes,omitempty"`
}

type Snapshot struct {
	Enabled           bool             `json:"enabled"`
	Requests          int              `json:"active-requests"`
	Attempts          int              `json:"active-attempts"`
	Waiting           int              `json:"waiting"`
	WaitingRequests   int              `json:"waiting-requests"`
	WaitingAttempts   int              `json:"waiting-attempts"`
	QueuedBytes       int64            `json:"queued-bytes"`
	Admitted          uint64           `json:"admitted"`
	Rejected          uint64           `json:"rejected"`
	TimedOut          uint64           `json:"timed-out"`
	Canceled          uint64           `json:"canceled"`
	Waited            uint64           `json:"waited"`
	Buckets           []BucketSnapshot `json:"buckets"`
	Truncated         bool             `json:"truncated"`
	Blocked           map[string]int   `json:"blocked-by-rule"`
	SampledAt         time.Time        `json:"sampled-at"`
	ProcessID         string           `json:"process-id"`
	Policy            Config           `json:"policy"`
	Activity          []Activity       `json:"activity"`
	ActivityTruncated bool             `json:"activity-truncated"`
	ActivityTotal     int              `json:"activity-total"`
	PolicyRevision    uint64           `json:"policy-revision"`
	MatchingTotal     int              `json:"matching-total"`
	Offset            int              `json:"offset"`
	OldestWaitMS      int64            `json:"oldest-wait-ms"`
}

type WindowEvaluation struct {
	Requests     int   `json:"requests"`
	PeriodMS     int64 `json:"period-ms"`
	Used         int   `json:"used"`
	Remaining    int   `json:"remaining"`
	RetryAfterMS int64 `json:"retry-after-ms,omitempty"`
}
type RuleEvaluation struct {
	Delta      int                `json:"delta"`
	Known      bool               `json:"known"`
	Unresolved []string           `json:"unresolved,omitempty"`
	Rule       Rule               `json:"rule"`
	Dimensions map[string]string  `json:"dimensions"`
	Active     int                `json:"active"`
	Remaining  *int               `json:"remaining"`
	Windows    []WindowEvaluation `json:"windows"`
	BlockedBy  []string           `json:"blocked-by"`
}
type Explanation struct {
	Identity        Identity         `json:"identity"`
	Enabled         bool             `json:"enabled"`
	Complete        bool             `json:"complete"`
	Unresolved      []string         `json:"unresolved"`
	CanStart        bool             `json:"can-start"`
	AdditionalSlots *int             `json:"additional-slots"`
	Matches         []RuleEvaluation `json:"matches"`
	BlockingRules   []string         `json:"blocking-rules"`
	SampledAt       time.Time        `json:"sampled-at"`
	PolicyRevision  uint64           `json:"policy-revision"`
	Draft           bool             `json:"draft"`
	AdvisoryOnly    bool             `json:"advisory-only"`
}

// Explain reads the SAME matching/grouping code used by Acquire. It never creates
// a bucket, spends a rate record or reserves a slot. A result is advisory: other
// requests or older eligible waiters may acquire capacity immediately afterwards.
func (e *Engine) Explain(d Identity) Explanation {
	if e == nil {
		return Explanation{Identity: d, CanStart: true, Complete: true, AdvisoryOnly: true}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	n := d.normalized()
	if d.Key == "" {
		n.Key = ""
	}
	return e.explainLocked(n, e.now(), true)
}
func (e *Engine) explainLocked(d Identity, now time.Time, partial bool) Explanation {
	return e.explainPolicyLocked(d, now, partial, e.cfg, false)
}
func (e *Engine) explainPolicyLocked(d Identity, now time.Time, partial bool, cfg Config, draft bool) Explanation {

	x := Explanation{Identity: d, Enabled: cfg.Enabled, CanStart: !e.closed, SampledAt: now, AdvisoryOnly: true, Matches: []RuleEvaluation{}, BlockingRules: []string{}, Complete: true, Unresolved: []string{}}
	for _, r := range cfg.Rules {
		possible, missing := r.matches(d), []string(nil)
		if partial {
			possible, missing = explainIdentityMatch(r, d)
		}
		if !possible {
			continue
		}
		if draft {
			liveFound := false
			for _, live := range e.cfg.Rules {
				if sameCountingRule(live, r) {
					liveFound = true
					break
				}
			}
			if !liveFound {
				missing = append(missing, "draft-policy")
			}
		}
		row := RuleEvaluation{Delta: 1, Known: len(missing) == 0, Unresolved: missing, Rule: r, Dimensions: map[string]string{}, Windows: []WindowEvaluation{}, BlockedBy: []string{}}
		// Return independent slices; API callers cannot mutate the running policy.
		row.Rule.Windows = append([]Window(nil), r.Windows...)
		row.Rule.GroupBy = cloneStrings(r.GroupBy)
		row.Rule.Models = cloneStrings(r.Models)
		row.Rule.Keys = cloneStrings(r.Keys)
		row.Rule.Accounts = cloneStrings(r.Accounts)
		dims, _ := r.Dimensions()
		for _, name := range dims {
			row.Dimensions[name] = d.dimension(name)
		}
		if !row.Known {
			x.Complete = false
			for _, field := range missing {
				found := false
				for _, old := range x.Unresolved {
					found = found || old == field
				}
				if !found {
					x.Unresolved = append(x.Unresolved, field)
				}
			}
			x.Matches = append(x.Matches, row)
			continue
		}
		b := e.buckets[r.bucketID(d)]
		if b != nil {
			row.Active = b.active
		}
		if r.MaxConcurrent > 0 {
			n := r.MaxConcurrent - row.Active
			if n < 0 {
				n = 0
			}
			row.Remaining = &n
			if x.AdditionalSlots == nil || n < *x.AdditionalSlots {
				v := n
				x.AdditionalSlots = &v
			}
			if n == 0 {
				row.BlockedBy = append(row.BlockedBy, "concurrency")
			}
		}
		for _, w := range r.Windows {
			we := WindowEvaluation{Requests: w.Requests, PeriodMS: w.PeriodMS, Remaining: w.Requests}
			if b != nil {
				cut := now.Add(-time.Duration(w.PeriodMS) * time.Millisecond)
				first := sort.Search(len(b.history), func(i int) bool { return b.history[i].at.After(cut) })
				we.Used = len(b.history) - first
				we.Remaining = w.Requests - we.Used
				if we.Remaining <= 0 {
					we.Remaining = 0
					we.RetryAfterMS = b.history[first+we.Used-w.Requests].at.Add(time.Duration(w.PeriodMS) * time.Millisecond).Sub(now).Milliseconds()
					if we.RetryAfterMS < 1 {
						we.RetryAfterMS = 1
					}
					row.BlockedBy = append(row.BlockedBy, "window")
				}
			}
			// The immediate additional-admission bound must include time windows,
			// not just free concurrent slots. This does not reserve those starts.
			if x.AdditionalSlots == nil || we.Remaining < *x.AdditionalSlots {
				v := we.Remaining
				x.AdditionalSlots = &v
			}
			row.Windows = append(row.Windows, we)
		}
		if len(row.BlockedBy) > 0 {
			x.BlockingRules = append(x.BlockingRules, r.ID)
			if cfg.Enabled {
				x.CanStart = false
			}
		}
		x.Matches = append(x.Matches, row)
	}
	sort.Strings(x.Unresolved)
	if !x.Complete {
		// false means "not established as executable", not a reservation denial.
		// Clients distinguish this from full capacity via Complete/Unresolved.
		x.CanStart = false
		x.AdditionalSlots = nil
	} else if !cfg.Enabled {
		x.AdditionalSlots = nil
	} else if !x.CanStart {
		n := 0
		x.AdditionalSlots = &n
	}
	x.PolicyRevision = e.policyRevision
	x.Draft = draft
	return x
}

// explainIdentityMatch treats omitted grouping/filter fields as unknown for a
// preview. Real Acquire never uses this helper: its identity is resolved already.
// Known mismatches are excluded, unknown targets are shown without a fake count.
func explainIdentityMatch(r Rule, d Identity) (bool, []string) {
	if r.Stage != d.Stage {
		return false, nil
	}
	missing := map[string]bool{}
	for _, f := range []struct {
		name, old, value string
		values           []string
		model            bool
	}{
		{"key", r.Key, d.Key, r.Keys, false}, {"account", r.Account, d.Account, r.Accounts, false},
		{"model", r.Model, d.Model, r.Models, true}, {"provider", r.Provider, d.Provider, nil, false},
		{"credential", r.Credential, d.Credential, nil, false}, {"auth-kind", r.AuthKind, d.AuthKind, nil, false},
	} {
		constrained := f.values != nil || (f.old != "" && f.old != "*")
		if f.value == "" {
			if constrained {
				missing[f.name] = true
			}
			continue
		}
		if f.model && d.Stage == Attempt && d.Provider == "" {
			patterns := f.values
			if patterns == nil {
				patterns = []string{f.old}
			}
			possible := false
			needsProvider := false
			unqualifiedMatch := false
			for _, p := range patterns {
				if _, name, ok := strings.Cut(p, "::"); ok {
					if matchOne(name, f.value, "", true) {
						possible = true
						needsProvider = true
					}
				} else if matchOne(p, f.value, "", true) {
					possible = true
					unqualifiedMatch = true
				}
			}
			if needsProvider && !unqualifiedMatch {
				missing["provider"] = true
			}
			if !possible {
				return false, nil
			}
		} else if !selectionMatches(f.old, f.values, f.value, d.Provider, f.model) {
			return false, nil
		}
	}
	dims, _ := r.Dimensions()
	for _, field := range dims {
		if d.dimension(field) == "" {
			missing[field] = true
		}
		if field == "model" && r.qualifiedModel && d.Stage == Attempt && d.Provider == "" {
			missing["provider"] = true
		}
	}
	fields := make([]string, 0, len(missing))
	for f := range missing {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return true, fields
}

// MarkPhase is observational; it does NOT release a permit. In particular,
// canceling the consumer leaves an uncooperative producer visible as draining.
func (p *Permit) MarkPhase(phase string) {
	if p == nil || p.e == nil || (phase != "running" && phase != "draining") {
		return
	}
	p.e.mu.Lock()
	defer p.e.mu.Unlock()
	id := p.id
	if p.operation != nil {
		id = p.operation.ticket
	}
	if a := p.e.active[id]; a != nil {
		a.phase = phase
	}
}

// A changed filter/group has unknown historical usage in a draft preview. Merely
// changing a concurrency cap or label can reuse the existing counter. A longer
// or new rate window is unknown rather than incorrectly shown as unused.
func sameCountingRule(a, b Rule) bool {
	if !sameRuleDomain(a, b) {
		return false
	}
	for _, w := range b.Windows {
		found := false
		for _, v := range a.Windows {
			if v.PeriodMS == w.PeriodMS {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameRuleDomain(a, b Rule) bool {
	da, _ := a.Dimensions()
	db, _ := b.Dimensions()
	sort.Strings(da)
	sort.Strings(db)
	if (!a.qualifiedModel && a.Scope != b.Scope) || a.ID != b.ID || a.Stage != b.Stage || a.qualifiedModel != b.qualifiedModel || !reflect.DeepEqual(da, db) ||
		a.Key != b.Key || a.Account != b.Account || a.Credential != b.Credential || a.Model != b.Model || a.Provider != b.Provider || a.AuthKind != b.AuthKind ||
		!reflect.DeepEqual(a.Keys, b.Keys) || !reflect.DeepEqual(a.Accounts, b.Accounts) || !reflect.DeepEqual(a.Models, b.Models) {
		return false
	}
	return true
}

// Preview computes a bounded batch under one policy revision, using exactly the
// same rule matching as admission. Nil config means the applied policy.
func (e *Engine) Preview(config *Config, targets []Identity) ([]Explanation, error) {
	if len(targets) > 24 {
		return nil, fmt.Errorf("at most 24 preview targets")
	}
	var cfg Config
	if config != nil {
		cfg = config.Effective()
		check := cfg
		check.Enabled = true
		if err := check.Validate(); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if config == nil {
		cfg = e.cfg
	}
	out := make([]Explanation, 0, len(targets))
	now := e.now()
	for _, d := range targets {
		for _, value := range []string{d.Key, d.Account, d.Credential, d.Provider, d.AuthKind, d.Model} {
			if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
				return nil, fmt.Errorf("invalid preview identity")
			}
		}
		if strings.Contains(d.Model, "*") {
			return nil, fmt.Errorf("preview requires an exact model, not a prefix")
		}
		if d.Stage != Request && d.Stage != Attempt {
			return nil, fmt.Errorf("invalid preview stage")
		}
		key := d.Key
		d = d.normalized()
		if key == "" {
			d.Key = ""
		}
		out = append(out, e.explainPolicyLocked(d, now, true, cfg, config != nil))
	}
	return out, nil
}
