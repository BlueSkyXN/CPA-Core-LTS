package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// PrefixSnapshot contains metadata only; AuthID is for the owning manager to
// resolve to its public auth index before exposing a management response.
type PrefixSnapshot struct {
	AuthID        string
	SessionHash   string
	NamespaceHash string
	PrefixLength  int
	ExpiresAt     time.Time
	Expired       bool
}

func (m *MerklePrefixMatcher) SnapshotSession(sessionHash, namespacePrefix string, limit int) ([]PrefixSnapshot, bool) {
	if m == nil || sessionHash == "" || limit <= 0 {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]PrefixSnapshot, 0)
	truncated := false
	for ns, bucket := range m.groups {
		if namespacePrefix != "" && !strings.HasPrefix(ns, namespacePrefix) {
			continue
		}
		for _, group := range bucket.groups {
			if group == nil || snapshotHash(group.sessionID) != sessionHash {
				continue
			}
			if len(out) >= limit {
				truncated = true
				continue
			}
			out = append(out, PrefixSnapshot{AuthID: group.authID, SessionHash: sessionHash, NamespaceHash: snapshotHash(ns), PrefixLength: len(group.fingerprints), ExpiresAt: group.expiresAt, Expired: !now.Before(group.expiresAt)})
		}
	}
	return out, truncated
}
func snapshotHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
