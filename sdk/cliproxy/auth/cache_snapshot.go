package auth

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"sort"
	"strings"
	"time"
)

type CacheBindingSnapshot struct {
	Kind          string    `json:"kind"`
	AuthIndex     string    `json:"auth_index,omitempty"`
	PrefixLength  int       `json:"prefix_length,omitempty"`
	PrefixUnit    string    `json:"prefix_unit,omitempty"`
	NamespaceHash string    `json:"namespace_hash,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	Expired       bool      `json:"expired"`
	authID        string
}
type CacheSessionSnapshot struct {
	InstanceID  string                        `json:"instance_id"`
	Status      string                        `json:"status"`
	At          time.Time                     `json:"at"`
	SessionHash string                        `json:"session_hash"`
	Bindings    []CacheBindingSnapshot        `json:"bindings"`
	Truncated   bool                          `json:"truncated"`
	OAuth       *diagnostics.AffinitySnapshot `json:"oauth,omitempty"`
}

// SnapshotCacheSession does not call Get, LookupSession, or Match: all three
// have maintenance or routing effects that a diagnostic read must avoid.
func (m *Manager) SnapshotCacheSession(provider, model, sessionHash string, diagnosticID ...string) (out CacheSessionSnapshot) {
	out = CacheSessionSnapshot{Status: "unsupported", At: time.Now(), SessionHash: sessionHash, Bindings: []CacheBindingSnapshot{}}
	if m == nil {
		return out
	}
	out.InstanceID = m.CacheDiagnosticsOverview().InstanceID
	if len(diagnosticID) > 0 {
		out.OAuth = m.snapshotObservedAffinity(provider, model, sessionHash, diagnosticID[0])
	}
	m.mu.RLock()
	selector, ok := m.selector.(*SessionAffinitySelector)
	plugin := m.pluginScheduler != nil
	indices := make(map[string]string, len(m.auths))
	for id, a := range m.auths {
		if a != nil {
			indices[id] = a.Index
		}
	}
	m.mu.RUnlock()
	if !ok || plugin || selector == nil {
		return out
	}
	out.Status = "unbound"
	model = canonicalModelKey(model)
	const limit = 64
	if c := selector.cache; c != nil {
		c.mu.RLock()
		for _, entry := range c.groups {
			if diagnostics.Hash(entry.binding.sessionID) != sessionHash {
				continue
			}
			if provider != "mixed" && entry.binding.provider != provider && entry.binding.provider != "mixed" {
				continue
			}
			matches := false
			for _, key := range entry.aliases {
				if strings.HasSuffix(key, "::"+model) {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
			if len(out.Bindings) >= limit {
				out.Truncated = true
				continue
			}
			out.Bindings = append(out.Bindings, CacheBindingSnapshot{Kind: "session_binding", authID: entry.authID, ExpiresAt: entry.expiresAt, Expired: !out.At.Before(entry.expiresAt)})
		}
		c.mu.RUnlock()
	}
	if selector.matcher != nil {
		prefix := "lcp:v1::" + canonicalLCPProvider(provider) + "::" + model + "::"
		rows, truncated := selector.matcher.SnapshotSession(sessionHash, prefix, limit)
		out.Truncated = out.Truncated || truncated
		for _, row := range rows {
			if len(out.Bindings) >= limit {
				out.Truncated = true
				break
			}
			out.Bindings = append(out.Bindings, CacheBindingSnapshot{Kind: "lcp", authID: row.AuthID, NamespaceHash: row.NamespaceHash, PrefixLength: row.PrefixLength, PrefixUnit: "canonical_turns", ExpiresAt: row.ExpiresAt, Expired: row.Expired})
		}
	}
	active := map[string]bool{}
	for i := range out.Bindings {
		b := &out.Bindings[i]
		b.AuthIndex = indices[b.authID]
		if !b.Expired {
			if _, exists := indices[b.authID]; exists {
				active[b.authID] = true
			}
		}
	}
	if len(active) == 1 {
		out.Status = "bound"
	} else if len(active) > 1 {
		out.Status = "ambiguous"
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		a, b := out.Bindings[i], out.Bindings[j]
		return a.Kind+a.AuthIndex+a.NamespaceHash < b.Kind+b.AuthIndex+b.NamespaceHash
	})
	return out
}

func (m *Manager) snapshotObservedAffinity(provider, model, sessionHash, traceID string) *diagnostics.AffinitySnapshot {
	out := &diagnostics.AffinitySnapshot{Status: "not_recorded", At: time.Now(), Rows: []diagnostics.AffinityRow{}}
	trace, ok := m.CacheDiagnostics(traceID)
	if !ok || trace.SessionHash != sessionHash || trace.Model != model {
		return out
	}
	m.mu.RLock()
	executor := m.executors[provider]
	m.mu.RUnlock()
	snapshotter, ok := executor.(diagnostics.AffinitySnapshotter)
	if !ok {
		out.Status = "unsupported"
		return out
	}
	for i := len(trace.Events) - 1; i >= 0; i-- {
		event := trace.Events[i]
		if event.Layer == "oauth_group" && event.ScopeHash != "" {
			value := snapshotter.SnapshotCacheAffinity(event.ScopeHash, event.IdentityHash, 64)
			return &value
		}
	}
	return out
}
