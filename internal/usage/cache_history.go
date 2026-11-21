package usage

import "slices"

// CacheHistoryLookup is supplied internally by the Management trace query.
type CacheHistoryLookup struct {
	ID         string
	Digest     string
	InstanceID string
	Excluded   map[string]bool
}

func CacheHistoryKey(instance, diagnostic, execution string) string {
	return instance + "\x00" + diagnostic + "\x00" + execution
}

func (q CacheHistoryLookup) matches(d RequestDetail) bool {
	if q.InstanceID != "" && d.DiagnosticsInstanceID != q.InstanceID {
		return false
	}
	if d.DiagnosticsID != "" && (q.Excluded[CacheHistoryKey(d.DiagnosticsInstanceID, d.DiagnosticsID, "")] ||
		q.Excluded[CacheHistoryKey(d.DiagnosticsInstanceID, d.DiagnosticsID, d.RequestID)]) {
		return false
	}
	return q.ID == "" || len(q.Matched(d)) > 0
}

func (q CacheHistoryLookup) Matched(d RequestDetail) []string {
	if q.ID == "" {
		return nil
	}
	var types []string
	if q.ID == d.RequestID {
		types = append(types, "request_id")
	}
	if q.ID == d.TraceID {
		types = append(types, "trace_id")
	}
	if q.ID == d.DiagnosticsID {
		types = append(types, "diagnostics_id")
	}
	if d.CacheRoute != nil && q.Digest != "" {
		if q.Digest == d.CacheRoute.LogicalDigest {
			types = append(types, "logical_request_id")
		}
		if slices.Contains(d.CacheRoute.ResponseDigests, q.Digest) {
			types = append(types, "response_id")
		}
	}
	return types
}
