package diagnostics

import "time"

// AffinitySnapshot is metadata copied by the owning executor. The query keys
// come from an observed decision, never reconstructed from request contents.
type AffinitySnapshot struct {
	Status       string        `json:"status"`
	At           time.Time     `json:"at"`
	ScopeHash    string        `json:"scope_hash,omitempty"`
	IdentityHash string        `json:"identity_hash,omitempty"`
	Rows         []AffinityRow `json:"rows"`
	Truncated    bool          `json:"truncated"`
}
type AffinityRow struct {
	Kind         string    `json:"kind"`
	ObjectHash   string    `json:"object_hash"`
	GroupHash    string    `json:"group_hash"`
	PrefixLength int       `json:"prefix_length,omitempty"`
	PrefixUnit   string    `json:"prefix_unit,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	ExpiresAt    time.Time `json:"expires_at"`
	Expired      bool      `json:"expired"`
}
type AffinitySnapshotter interface {
	SnapshotCacheAffinity(scope, identity string, limit int) AffinitySnapshot
}
