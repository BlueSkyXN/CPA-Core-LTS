package flowcontrol

import (
	"container/heap"
	"sort"
	"unicode/utf8"
)

// Bound both the number and encoded size of observation identities. Long model
// names retain their full limiter identity; only their observation is omitted.
const MaxSummaryGroups = 128
const maxSummaryIdentityBytes = 1024

type SummaryGroupIdentity struct {
	Stage    string `json:"stage"`
	Key      string `json:"key,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
}

type SummaryGroup struct {
	SummaryGroupIdentity
	Active  int `json:"active"`
	Waiting int `json:"waiting"`
}

type SummaryOmitted struct {
	Requests int `json:"active-requests"`
	Attempts int `json:"active-attempts"`
	Waiting  int `json:"waiting"`
}

// Breakdown counts records, not rule memberships. Request and attempt records
// remain separate; a queued joint first admission contributes one waiting item
// under its already selected attempt target, just like the details endpoint.
type SummaryBreakdown struct {
	Groups    []SummaryGroup `json:"groups"`
	Omitted   SummaryOmitted `json:"omitted"`
	Truncated bool           `json:"truncated"`
}

type summaryGroups []*SummaryGroup

func (h summaryGroups) Len() int { return len(h) }
func (h summaryGroups) Less(i, j int) bool {
	return lessSummaryIdentity(h[j].SummaryGroupIdentity, h[i].SummaryGroupIdentity)
}
func (h summaryGroups) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *summaryGroups) Push(v any)   { *h = append(*h, v.(*SummaryGroup)) }
func (h *summaryGroups) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}

func lessSummaryIdentity(a, b SummaryGroupIdentity) bool {
	aa := [...]string{a.Stage, a.Model, a.Provider, a.Account, a.Key}
	bb := [...]string{b.Stage, b.Model, b.Provider, b.Account, b.Key}
	for i := range aa {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return false
}

// Conservative JSON string length, including encoding/json's HTML escapes.
func summaryIdentityFits(d SummaryGroupIdentity) bool {
	fields := [...]string{d.Stage, d.Key, d.Model, d.Provider, d.Account}
	size := 0
	for _, field := range fields {
		if len(field) > maxSummaryIdentityBytes {
			return false
		}
		for _, r := range field {
			switch {
			case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
				size += 6
			case r == '"' || r == '\\':
				size += 2
			default:
				size += utf8.RuneLen(r)
			}
			if size > maxSummaryIdentityBytes {
				return false
			}
		}
	}
	return true
}

type breakdownCollector struct {
	groups  map[SummaryGroupIdentity]*SummaryGroup
	heap    summaryGroups
	omitted SummaryOmitted
}

func (c *breakdownCollector) omit(d SummaryGroupIdentity, active, waiting int) {
	if d.Stage == Request {
		c.omitted.Requests += active
	} else {
		c.omitted.Attempts += active
	}
	c.omitted.Waiting += waiting
}

func (c *breakdownCollector) add(d Identity, active, waiting int) {
	key := SummaryGroupIdentity{Stage: d.Stage, Key: d.Key, Model: d.Model}
	if d.Stage == Attempt {
		key.Provider, key.Account = d.Provider, d.Account
		if key.Account == "" {
			key.Account = d.Credential
		}
	}
	if group := c.groups[key]; group != nil {
		group.Active += active
		group.Waiting += waiting
		return
	}
	if !summaryIdentityFits(key) {
		c.omit(key, active, waiting)
		return
	}
	if len(c.heap) == MaxSummaryGroups {
		last := c.heap[0]
		if !lessSummaryIdentity(key, last.SummaryGroupIdentity) {
			c.omit(key, active, waiting)
			return
		}
		// The lexicographic boundary only decreases: an evicted identity can
		// never re-enter, so retained counts are complete and order independent.
		delete(c.groups, last.SummaryGroupIdentity)
		c.omit(last.SummaryGroupIdentity, last.Active, last.Waiting)
		*last = SummaryGroup{SummaryGroupIdentity: key, Active: active, Waiting: waiting}
		c.groups[key] = last
		heap.Fix(&c.heap, 0)
		return
	}
	if c.groups == nil {
		c.groups = make(map[SummaryGroupIdentity]*SummaryGroup)
	}
	group := &SummaryGroup{SummaryGroupIdentity: key, Active: active, Waiting: waiting}
	c.groups[key] = group
	heap.Push(&c.heap, group)
}

func (c *breakdownCollector) finish() *SummaryBreakdown {
	out := &SummaryBreakdown{Groups: make([]SummaryGroup, 0, len(c.heap)), Omitted: c.omitted,
		Truncated: c.omitted.Requests+c.omitted.Attempts+c.omitted.Waiting > 0}
	for _, group := range c.heap {
		out.Groups = append(out.Groups, *group)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		return lessSummaryIdentity(out.Groups[i].SummaryGroupIdentity, out.Groups[j].SummaryGroupIdentity)
	})
	return out
}
