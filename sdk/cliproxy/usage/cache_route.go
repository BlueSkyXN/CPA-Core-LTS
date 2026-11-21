package usage

import "slices"

const (
	MaxCacheReferences  = 12
	MaxCacheResponseIDs = 32
)

type CacheRouteKey struct {
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
	Scope  string `json:"scope,omitempty"`
}

func CacheDigestValid(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (k CacheRouteKey) ValidFilter() bool {
	return k.valid() && k.Scope != ""
}

func (k CacheRouteKey) valid() bool {
	switch k.Kind {
	case "input_session", "cache_group", "pck", "outbound_session", "named_cache":
		return CacheDigestValid(k.Digest) && (k.Scope == "" || CacheDigestValid(k.Scope))
	default:
		return false
	}
}

type CacheRouteReference struct {
	CacheRouteKey
	Method string `json:"method"`
	Source string `json:"source,omitempty"`
}

func (r CacheRouteReference) valid() bool {
	switch r.Source {
	case "", "session_metadata", "strict_prefix", "prompt_cache_key", "Session-Id", "Session_id", "X-Session-Id",
		"X-Claude-Code-Session-Id", "X-Claude-Remote-Session-Id", "X-Grok-Conv-Id", "X-Tbh-Session-Id", "session_id",
		"cachedContent", "cached_content", "request.cachedContent", "request.cached_content":
	default:
		return false
	}
	switch r.Method {
	case "inbound", "client", "outbound", "strict_prefix", "retained_group":
		return r.CacheRouteKey.valid()
	default:
		return false
	}
}

type CacheRouteSelection struct {
	Method       string `json:"method"`
	Result       string `json:"result"`
	PrefixLength int    `json:"prefix_length,omitempty"`
	PrefixUnit   string `json:"prefix_unit,omitempty"`
}

type CacheRouteSummary struct {
	Version          int                   `json:"version"`
	References       []CacheRouteReference `json:"references,omitempty"`
	Selection        *CacheRouteSelection  `json:"selection,omitempty"`
	AttemptCount     int                   `json:"attempt_count"`
	NoParsedResponse bool                  `json:"no_parsed_response,omitempty"`
	Truncated        bool                  `json:"truncated,omitempty"`
	LogicalDigest    string                `json:"logical_digest,omitempty"`
	PreviousResponse string                `json:"previous_response_digest,omitempty"`
	ResponseDigests  []string              `json:"response_digests,omitempty"`
	OfficialLoss     bool                  `json:"official_loss,omitempty"`
}

func (c *CacheRouteSummary) Clone() *CacheRouteSummary {
	if c == nil {
		return nil
	}
	out := *c
	out.References = slices.Clone(c.References)
	out.ResponseDigests = slices.Clone(c.ResponseDigests)
	if c.Selection != nil {
		selection := *c.Selection
		out.Selection = &selection
	}
	return &out
}

func (c *CacheRouteSummary) Valid() bool {
	if c == nil {
		return true
	}
	if c.Version != 1 || c.AttemptCount < 0 || c.AttemptCount > MaxCacheResponseIDs ||
		len(c.References) > MaxCacheReferences || len(c.ResponseDigests) > MaxCacheResponseIDs {
		return false
	}
	if c.LogicalDigest != "" && !CacheDigestValid(c.LogicalDigest) ||
		c.PreviousResponse != "" && !CacheDigestValid(c.PreviousResponse) {
		return false
	}
	for _, digest := range c.ResponseDigests {
		if !CacheDigestValid(digest) {
			return false
		}
	}
	for _, ref := range c.References {
		if !ref.valid() {
			return false
		}
	}
	if c.Selection != nil {
		switch c.Selection.Method {
		case "selection", "session_binding", "lcp", "websocket_connection":
		default:
			return false
		}
		switch c.Selection.Result {
		case "selected", "failed", "reused", "committed", "candidate_found":
		default:
			return false
		}
		switch c.Selection.PrefixUnit {
		case "", "canonical_turns", "strict_input_items":
		default:
			return false
		}
		if c.Selection.PrefixLength < 0 {
			return false
		}
	}
	return true
}

func (c *CacheRouteSummary) Matches(key CacheRouteKey) bool {
	return c != nil && slices.ContainsFunc(c.References, func(ref CacheRouteReference) bool {
		return ref.CacheRouteKey == key
	})
}
