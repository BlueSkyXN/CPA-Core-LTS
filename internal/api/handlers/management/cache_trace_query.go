package management

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
)

const maxCacheTracePage = 100

type cacheTraceCursor struct {
	Instance     string `json:"i"`
	Query        string `json:"q"`
	Revision     string `json:"r"`
	ActiveOffset int    `json:"a"`
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
		q.Route = &diagnostics.CacheRouteKey{Kind: values.Get("kind"), Digest: values.Get("digest"), Scope: values.Get("scope")}
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
		if errDecode != nil {
			invalid()
			return
		}
		var decoded struct {
			Instance     *string `json:"i"`
			Query        *string `json:"q"`
			Revision     *string `json:"r"`
			ActiveOffset *int    `json:"a"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var trailing any
		if dec.Decode(&decoded) != nil || dec.Decode(&trailing) != io.EOF ||
			decoded.Instance == nil || *decoded.Instance == "" ||
			decoded.Query == nil || *decoded.Query != queryHash ||
			decoded.Revision == nil || *decoded.Revision == "" ||
			decoded.ActiveOffset == nil || *decoded.ActiveOffset < 0 {
			invalid()
			return
		}
		if *decoded.Instance != instance || *decoded.Revision != revision {
			expired()
			return
		}
		if *decoded.ActiveOffset > len(active) {
			invalid()
			return
		}
		cursor.ActiveOffset = *decoded.ActiveOffset
	}
	end := min(cursor.ActiveOffset+limit, len(active))
	result := cacheTracePage{Version: 1, CurrentInstanceID: instance, Items: active[cursor.ActiveOffset:end]}
	if end < len(active) {
		cursor.ActiveOffset = end
		data, _ := json.Marshal(cursor)
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	c.JSON(http.StatusOK, result)
}
