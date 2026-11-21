package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const maxCacheTracePage = 100

type cacheTraceCursor struct {
	Instance      string `json:"i"`
	Query         string `json:"q"`
	Revision      string `json:"r"`
	ActiveOffset  int    `json:"a"`
	HistoryBound  string `json:"b,omitempty"`
	HistoryCursor string `json:"c,omitempty"`
	HistoryOffset int    `json:"h,omitempty"`
	HistoryDone   bool   `json:"e,omitempty"`
}

type cacheTracePage struct {
	Version           int                    `json:"version"`
	CurrentInstanceID string                 `json:"current_instance_id"`
	Items             []diagnostics.TraceRow `json:"items"`
	HistoryIncluded   bool                   `json:"history_included"`
	NextCursor        string                 `json:"next_cursor,omitempty"`
}

func readCacheTraceQuery(values url.Values) (diagnostics.TraceQuery, int, bool) {
	q := diagnostics.TraceQuery{
		ID: strings.TrimSpace(values.Get("q")), InstanceID: values.Get("instance_id"),
		Focus: values.Get("focus"),
	}
	if !validCacheLookupID(q.ID) || !validCacheLookupID(q.InstanceID) {
		return q, 0, false
	}
	switch q.Focus {
	case "", "zero_read", "retry", "official_loss":
	default:
		return q, 0, false
	}
	if values.Has("kind") || values.Has("digest") || values.Has("scope") {
		q.Route = &coreusage.CacheRouteKey{Kind: values.Get("kind"), Digest: values.Get("digest"), Scope: values.Get("scope")}
		if !q.Route.ValidFilter() {
			return q, 0, false
		}
	}
	for name, target := range map[string]**int64{"from_ms": &q.FromMS, "to_ms": &q.ToMS} {
		if values.Has(name) {
			ms, err := strconv.ParseInt(values.Get(name), 10, 64)
			if err != nil || ms < 0 || ms > 8640000000000000 {
				return q, 0, false
			}
			*target = &ms
		}
	}
	if q.FromMS != nil && q.ToMS != nil && *q.FromMS > *q.ToMS {
		return q, 0, false
	}
	limit := 30
	if values.Has("limit") {
		var err error
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > maxCacheTracePage {
			return q, 0, false
		}
	}
	return q, limit, true
}

func validCacheLookupID(value string) bool {
	if len(value) > 192 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune("_-.:/", c)) {
			return false
		}
	}
	return true
}

func (h *Handler) QueryCacheTraces(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"code": "cache_diagnostics_unavailable"})
		return
	}
	c.Header("Cache-Control", "no-store")
	invalid := func() { c.JSON(http.StatusBadRequest, gin.H{"code": "cache_trace_query_invalid"}) }
	if len(c.Request.URL.RawQuery) > 4096 {
		invalid()
		return
	}
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	q, limit, valid := readCacheTraceQuery(params)
	if err != nil || !valid {
		invalid()
		return
	}
	for key, values := range params {
		switch key {
		case "q", "instance_id", "focus", "kind", "digest", "scope", "from_ms", "to_ms", "limit", "cursor":
			if len(values) == 1 {
				continue
			}
		}
		invalid()
		return
	}
	instance := h.authManager.CacheDiagnosticsOverview().InstanceID
	active, revision := h.authManager.LookupCacheDiagnostics(q)
	fingerprint, _ := json.Marshal(q)
	queryHash := diagnostics.Hash(string(fingerprint))
	cursor := cacheTraceCursor{Instance: instance, Query: queryHash, Revision: revision}
	expired := func() { c.JSON(http.StatusConflict, gin.H{"code": "cache_trace_query_expired"}) }
	if raw := params.Get("cursor"); raw != "" {
		data, errDecode := base64.RawURLEncoding.DecodeString(raw)
		if errDecode != nil || json.Unmarshal(data, &cursor) != nil ||
			cursor.Query != queryHash || cursor.ActiveOffset < 0 ||
			cursor.HistoryOffset < 0 || cursor.HistoryOffset >= maxCacheTracePage {
			invalid()
			return
		}
		if cursor.Instance != instance || cursor.Revision != revision {
			expired()
			return
		}
		if cursor.ActiveOffset > len(active) {
			invalid()
			return
		}
	}
	result := cacheTracePage{Version: 1, CurrentInstanceID: instance, Items: []diagnostics.TraceRow{}}
	result.HistoryIncluded = h.usageStats != nil && q.Focus == "" && (q.ID != "" || q.Route != nil)
	if result.HistoryIncluded {
		if cursor.HistoryBound == "" {
			cursor.HistoryBound = h.usageStats.QueryCapabilities().Bound
		}
	} else {
		cursor.HistoryDone = true
	}
	lookup := &usage.CacheHistoryLookup{ID: q.ID, InstanceID: q.InstanceID, Excluded: map[string]bool{}}
	if q.ID != "" {
		lookup.Digest = diagnostics.Hash(q.ID)
	}
	for _, row := range active {
		execution := row.RequestID
		if q.ID == "" || len(row.Matched) > 0 && !slicesIDMatch(row.Matched) {
			execution = ""
		}
		lookup.Excluded[usage.CacheHistoryKey(row.InstanceID, row.DiagnosticsID, execution)] = true
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	var history *usage.QueryDetails
	for len(result.Items) < limit {
		if !cursor.HistoryDone && history == nil {
			history, err = h.usageStats.QueryDetails(ctx, usage.QueryRequest{
				Bound: cursor.HistoryBound, Cursor: cursor.HistoryCursor, Limit: maxCacheTracePage,
				FromMS: q.FromMS, ToMS: q.ToMS, Filter: usage.QueryFilter{CacheHistory: lookup, CacheRoute: q.Route},
			})
			switch {
			case errors.Is(err, usage.ErrQueryExpired):
				expired()
				return
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				c.JSON(http.StatusRequestTimeout, gin.H{"code": "cache_trace_query_timeout"})
				return
			case err != nil || cursor.HistoryOffset > len(history.Items):
				invalid()
				return
			}
			if len(history.Items) == 0 {
				cursor.HistoryDone = true
			}
		}
		var old *usage.QueryItem
		if !cursor.HistoryDone && cursor.HistoryOffset < len(history.Items) {
			old = &history.Items[cursor.HistoryOffset]
		}
		var recent *diagnostics.TraceRow
		if cursor.ActiveOffset < len(active) {
			recent = &active[cursor.ActiveOffset]
		}
		if recent == nil && old == nil {
			break
		}
		if recent != nil && (old == nil || !recent.At.Before(old.Detail.Timestamp)) {
			result.Items = append(result.Items, *recent)
			cursor.ActiveOffset++
			continue
		}
		d := old.Detail
		row := diagnostics.TraceRow{
			TraceLocation: diagnostics.TraceLocation{InstanceID: d.DiagnosticsInstanceID, DiagnosticsID: d.DiagnosticsID, RequestID: d.RequestID},
			HistoryKey:    diagnostics.Hash(instance + "\x00" + old.ID),
			At:            d.Timestamp, Model: old.Model, AuthIndex: d.AuthIndex, TraceID: d.TraceID,
			Status: d.FailureStatus, Matched: lookup.Matched(d), InputTokens: &d.Tokens.InputTokens, CacheObservation: d.CacheObservation, CacheRoute: d.CacheRoute,
		}
		result.Items = append(result.Items, row)
		cursor.HistoryOffset++
		if cursor.HistoryOffset == len(history.Items) {
			cursor.HistoryCursor, cursor.HistoryOffset = history.NextCursor, 0
			cursor.HistoryDone = history.NextCursor == ""
			history = nil
		}
	}
	if cursor.ActiveOffset < len(active) || !cursor.HistoryDone {
		data, _ := json.Marshal(cursor)
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	c.JSON(http.StatusOK, result)
}

func slicesIDMatch(ids []string) bool {
	for _, id := range ids {
		if id == "request_id" || id == "response_id" {
			return true
		}
	}
	return false
}
