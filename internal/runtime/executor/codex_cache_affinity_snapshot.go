package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"sort"
	"time"
)

func (e *CodexAutoExecutor) SnapshotCacheAffinity(scope, identity string, limit int) diagnostics.AffinitySnapshot {
	if e == nil {
		return diagnostics.AffinitySnapshot{Status: "unsupported"}
	}
	return e.httpExec.SnapshotCacheAffinity(scope, identity, limit)
}

func (e *CodexExecutor) SnapshotCacheAffinity(scope, identity string, limit int) diagnostics.AffinitySnapshot {
	out := diagnostics.AffinitySnapshot{Status: "unavailable", At: time.Now(), ScopeHash: scope, IdentityHash: identity, Rows: []diagnostics.AffinityRow{}}
	// 不调用 affinityStore()，查询不创建索引，也不触发 expire()。
	if e == nil || e.affinity == nil || scope == "" || identity == "" {
		return out
	}
	if limit <= 0 || limit > 64 {
		limit = 64
	}
	s := e.affinity
	s.mu.Lock()
	out.At, out.Status = s.now(), "empty"
	appendRow := func(kind, object, group string, length int, touched time.Time) {
		if len(out.Rows) >= limit {
			out.Truncated = true
			return
		}
		expires := touched.Add(codexAffinityTTL)
		out.Rows = append(out.Rows, diagnostics.AffinityRow{Kind: kind, ObjectHash: diagnostics.Hash(object), GroupHash: diagnostics.Hash(group), PrefixLength: length, PrefixUnit: "strict_input_items", LastActivity: touched, ExpiresAt: expires, Expired: out.At.After(expires)})
	}
	if binding, ok := s.bindings[identity]; identity != "" && ok {
		appendRow("oauth_binding", identity, binding.group, 0, binding.touched)
	}
	for key, trajectory := range s.trajectories {
		if trajectory.scope == scope && trajectory.identity == identity {
			appendRow("oauth_trajectory", key, trajectory.group, len(trajectory.snapshot.items), trajectory.touched)
		}
	}
	s.mu.Unlock()
	if len(out.Rows) > 0 {
		out.Status = "available"
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].ObjectHash < out.Rows[j].ObjectHash })
	return out
}
