package diagnostics

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

const MaxCacheReferences = 12

type CacheRouteKey struct {
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
	Scope  string `json:"scope,omitempty"`
}

func validCacheDigest(value string) bool {
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
	switch k.Kind {
	case "input_session", "cache_group", "pck", "outbound_session", "named_cache":
		return validCacheDigest(k.Digest) && validCacheDigest(k.Scope)
	default:
		return false
	}
}

type CacheRouteReference struct {
	CacheRouteKey
	Method string `json:"method"`
	Source string `json:"source,omitempty"`
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
	OfficialLoss     bool                  `json:"official_loss,omitempty"`
}

func cacheDigest(value ojson.Result) string {
	if value.Type != ojson.String || strings.TrimSpace(value.String()) == "" {
		return ""
	}
	return Hash(value.String())
}

func cacheReference(kind, digest, scope, method, source string) CacheRouteReference {
	return CacheRouteReference{
		CacheRouteKey: CacheRouteKey{Kind: kind, Digest: digest, Scope: scope},
		Method:        method, Source: source,
	}
}

func cacheEndpointScope(scope, provider, endpoint string) string {
	u, err := url.Parse(endpoint)
	if scope == "" || err != nil || u.Host == "" {
		return ""
	}
	// HTTP and WS may hit the same upstream Responses endpoint.
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	case "http", "https":
	default:
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	secure := scheme == "https"
	if port == "" && secure {
		port = "443"
	}
	if port == "" {
		port = "80"
	}
	return Hash(scope + "\x00" + provider + "\x00" + scheme + "\x00" + host + "\x00" + port + "\x00" + strings.TrimSuffix(u.EscapedPath(), "/"))
}

func (a *Attempt) captureCacheRoute(payload []byte, headers http.Header) {
	root := ojson.Result{}
	if a.Request.Known {
		root = ojson.ParseBytes(payload)
		comparisonPath := "prompt_cache_options.comparison_response_id"
		if a.Provider == "claude" {
			comparisonPath = "diagnostics.previous_message_id"
		}
		a.ComparisonIDSent = "absent"
		if cacheDigest(root.Get(comparisonPath)) != "" {
			a.ComparisonIDSent = "present"
		}
	}
	if digest := cacheDigest(root.Get("prompt_cache_key")); digest != "" {
		a.CacheReferences = append(a.CacheReferences, cacheReference("pck", digest, "", "outbound", "prompt_cache_key"))
	}
	for _, key := range []string{"X-Tbh-Session-Id", "Session-Id", "Session_id", "X-Session-Id", "X-Claude-Code-Session-Id", "X-Claude-Remote-Session-Id", "X-Grok-Conv-Id"} {
		if key == "X-Tbh-Session-Id" && a.Provider != "meta" {
			continue
		}
		if value := headers.Get(key); value != "" {
			a.SessionHeaderHash = Hash(value)
			a.CacheReferences = append(a.CacheReferences, cacheReference("outbound_session", a.SessionHeaderHash, "", "outbound", key))
			break
		}
	}
	if digest := cacheDigest(root.Get("session_id")); a.SessionHeaderHash == "" && digest != "" {
		a.CacheReferences = append(a.CacheReferences, cacheReference("outbound_session", digest, "", "outbound", "session_id"))
	}
	for _, path := range []string{"cachedContent", "cached_content", "request.cachedContent", "request.cached_content"} {
		value := root.Get(path)
		if value.Type == ojson.String && strings.Contains(value.String(), "cachedContents/") {
			a.CacheReferences = append(a.CacheReferences, cacheReference("named_cache", Hash(value.String()), "", "outbound", path))
			break
		}
	}
	if a.Provider == "claude" && root.IsObject() {
		a.CacheControl = claudeCacheControl(root)
		if root.Get("thread.type").String() == "continue" {
			a.PreviousResponse = cacheDigest(root.Get("thread.previous_message_id"))
			if a.PreviousResponse != "" {
				a.PreviousSource = "thread.previous_message_id"
			}
		}
		if a.PreviousResponse == "" {
			a.PreviousResponse = cacheDigest(root.Get("diagnostics.previous_message_id"))
			if a.PreviousResponse != "" {
				a.PreviousSource = "diagnostics.previous_message_id"
			}
		}
	} else if digest := cacheDigest(root.Get("previous_response_id")); digest != "" {
		a.PreviousResponse = digest
		a.PreviousSource = "previous_response_id"
	}
}

// The trace lock covers only the bounded identity and decision association.
func (a *Attempt) scopeCacheRoute(trace *Trace, endpoint string) {
	a.RouteScope = cacheEndpointScope(trace.RouteScope, a.Provider, endpoint)
	for i := range a.CacheReferences {
		ref := &a.CacheReferences[i]
		ref.Scope = a.RouteScope
		if ref.Kind == "pck" && ref.Digest == trace.InputPCKHash {
			ref.Method = "client"
		}
	}
	if a.SessionHeaderHash == "" {
		return
	}
	for i := len(trace.Events) - 1; i >= 0; i-- {
		e := trace.Events[i]
		if e.ExecutionID != a.ExecutionID || e.Layer != "oauth_group" || e.ObjectHash != a.SessionHeaderHash || e.ScopeHash == "" {
			continue
		}
		method := "retained_group"
		if e.Result == "adopted" {
			method = "strict_prefix"
		}
		scope := ""
		if trace.RouteScope != "" {
			scope = Hash(trace.RouteScope + "\x00" + e.ScopeHash)
		}
		a.CacheReferences = append(a.CacheReferences, cacheReference("cache_group", a.SessionHeaderHash, scope, method, "strict_prefix"))
		break
	}
}

type CacheControl struct {
	Breakpoints int `json:"breakpoints"`
	FiveMinutes int `json:"five_minutes,omitempty"`
	OneHour     int `json:"one_hour,omitempty"`
}

func claudeCacheControl(root ojson.Result) *CacheControl {
	control := &CacheControl{}
	add := func(node ojson.Result) {
		if !node.IsObject() {
			return
		}
		control.Breakpoints++
		switch node.Get("ttl").String() {
		case "5m":
			control.FiveMinutes++
		case "1h":
			control.OneHour++
		}
	}
	add(root.Get("cache_control"))
	blocks := func(list ojson.Result) {
		list.ForEach(func(_, block ojson.Result) bool {
			add(block.Get("cache_control"))
			return true
		})
	}
	blocks(root.Get("system"))
	blocks(root.Get("tools"))
	root.Get("messages").ForEach(func(_, message ojson.Result) bool {
		blocks(message.Get("content"))
		return true
	})
	if control.Breakpoints == 0 {
		return nil
	}
	return control
}

func addCacheReference(summary *CacheRouteSummary, ref CacheRouteReference) {
	if slices.ContainsFunc(summary.References, func(seen CacheRouteReference) bool {
		return seen.CacheRouteKey == ref.CacheRouteKey
	}) {
		return
	}
	if len(summary.References) == MaxCacheReferences {
		summary.Truncated = true
		return
	}
	summary.References = append(summary.References, ref)
}

func (t *Trace) cacheRoute(executionID, decisionID string) *CacheRouteSummary {
	summary := &CacheRouteSummary{Version: 1, Truncated: t.Truncated}
	if t.InputSession != nil {
		addCacheReference(summary, *t.InputSession)
	}
	for _, attempt := range t.Attempts {
		if executionID != "" && attempt.ExecutionID != executionID {
			continue
		}
		if decisionID == "" {
			decisionID = attempt.DecisionID
		}
		summary.AttemptCount++
		summary.NoParsedResponse = attempt.CacheObservation == nil &&
			(attempt.ObservationState == "not_observed" || attempt.ObservationState == "encoded_response")
		summary.OfficialLoss = summary.OfficialLoss || attempt.hasOfficialCacheLoss()
		for _, ref := range attempt.CacheReferences {
			addCacheReference(summary, ref)
		}
	}
	for _, e := range t.Events {
		if decisionID == "" || e.DecisionID != decisionID || e.Layer != "account" ||
			(e.ExecutionID != "" && executionID != "" && e.ExecutionID != executionID) {
			continue
		}
		switch {
		case e.Method == "selection" && summary.Selection == nil,
			e.Method == "session_binding" && e.Result == "reused",
			e.Method == "websocket_connection" && e.Result == "reused":
			summary.Selection = &CacheRouteSelection{Method: e.Method, Result: e.Result}
		case e.Method == "lcp" && e.Result == "candidate_found":
			// A candidate's prefix is evidence about the search, irrespective of later selection.
			if summary.Selection == nil || summary.Selection.Method == "selection" {
				summary.Selection = &CacheRouteSelection{Method: e.Method, Result: e.Result, PrefixLength: e.PrefixLength, PrefixUnit: e.PrefixUnit}
			}
		}
	}
	return summary
}
