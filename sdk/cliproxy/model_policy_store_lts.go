package cliproxy

import (
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// accountCatalogStore keeps the pre-policy model catalog (real upstream model
// IDs, before channel/account model policy filtering) per auth ID. It powers
// previews and checkbox sources so disabling a model never hides it from the
// editor, and policy-only changes can recompute without rediscovery.
//
// Snapshots are overwritten on every registration with a non-empty candidate
// catalog and removed when an auth is unregistered. An absent entry means
// "catalog unknown", not "catalog empty".
type accountCatalogStore struct {
	mu         sync.RWMutex
	catalogs   map[string][]string
	generation map[string]uint64
}

func newAccountCatalogStore() *accountCatalogStore {
	return &accountCatalogStore{
		catalogs:   make(map[string][]string),
		generation: make(map[string]uint64),
	}
}

// record stores the pre-policy candidate catalog for one auth. generation is
// the auth's registration epoch at snapshot time; snapshots from a stale
// epoch must not overwrite a newer one.
func (c *accountCatalogStore) recordIDs(authID string, generation uint64, models []string) {
	authID = strings.TrimSpace(authID)
	if authID == "" || len(models) == 0 {
		return
	}
	ids := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, id := range models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.generation[authID]; ok && generation < current {
		return
	}
	c.catalogs[authID] = ids
	c.generation[authID] = generation
}

// remove drops the snapshot for one auth.
func (c *accountCatalogStore) remove(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.catalogs, authID)
	delete(c.generation, authID)
}

// snapshot returns the stored pre-policy catalog IDs.
func (c *accountCatalogStore) snapshot(authID string) []string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := c.catalogs[authID]
	if len(ids) == 0 {
		return nil
	}
	return append([]string(nil), ids...)
}

// authModelPolicyPreview adapts PreviewModelPolicyForAuth to the management
// handler hook signature.
func (s *Service) authModelPolicyPreview(auth *coreauth.Auth) (any, bool) {
	preview, ok := s.PreviewModelPolicyForAuth(auth)
	if !ok {
		return nil, false
	}
	return preview, true
}

// ModelPolicyEvaluation is one model's policy outcome for previews.
type ModelPolicyEvaluation struct {
	ModelID string `json:"model_id"`
	Allowed bool   `json:"allowed"`
	Layer   string `json:"layer,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ModelPolicyPreview combines the stored pre-policy catalog with per-model
// decisions under a given (possibly draft) config.
type ModelPolicyPreview struct {
	AuthID      string                  `json:"auth_id"`
	Provider    string                  `json:"provider"`
	CatalogAge  string                  `json:"catalog_status"`
	Evaluations []ModelPolicyEvaluation `json:"evaluations"`
}

// PreviewModelPolicyForAuth evaluates the model policy for one auth against
// the service config. It uses the stored pre-policy catalog and performs no
// network calls. ok reports whether a catalog snapshot exists.
func (s *Service) PreviewModelPolicyForAuth(auth *coreauth.Auth) (ModelPolicyPreview, bool) {
	if s == nil || auth == nil || s.accountCatalogs == nil {
		return ModelPolicyPreview{}, false
	}
	ids := s.accountCatalogs.snapshot(auth.ID)
	if len(ids) == 0 {
		return ModelPolicyPreview{}, false
	}
	preview := ModelPolicyPreview{
		AuthID:      auth.ID,
		Provider:    strings.ToLower(strings.TrimSpace(auth.Provider)),
		CatalogAge:  "snapshot",
		Evaluations: make([]ModelPolicyEvaluation, 0, len(ids)),
	}
	for _, id := range ids {
		decision := coreauth.EvaluateModelPolicy(s.cfg, auth, preview.Provider, id)
		preview.Evaluations = append(preview.Evaluations, ModelPolicyEvaluation{
			ModelID: id,
			Allowed: decision.Allowed,
			Layer:   decision.Layer,
			Reason:  decision.String(),
		})
	}
	return preview, true
}
