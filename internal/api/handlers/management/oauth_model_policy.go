package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// OAuth model policy (LTS): channel-level allow/deny lists under
// "oauth-model-policy". The legacy oauth-excluded-models endpoints keep
// operating only on the legacy key; this surface is the sole writer of the new
// structure.

// GetOAuthModelPolicy returns the normalized oauth-model-policy mapping.
func (h *Handler) GetOAuthModelPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"oauth-model-policy": config.NormalizeOAuthModelPolicy(h.cfg.OAuthModelPolicy)})
}

// PutOAuthModelPolicy replaces the whole oauth-model-policy mapping.
func (h *Handler) PutOAuthModelPolicy(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string]config.OAuthModelPolicyEntry
	if errUnmarshal := json.Unmarshal(data, &entries); errUnmarshal != nil {
		var wrapper struct {
			Items map[string]config.OAuthModelPolicyEntry `json:"items"`
		}
		if errWrapper := json.Unmarshal(data, &wrapper); errWrapper != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	normalized := config.NormalizeOAuthModelPolicy(entries)
	if !modelPolicyMappingEquivalent(entries, normalized) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model policy entry"})
		return
	}
	h.cfg.OAuthModelPolicy = normalized
	h.persist(c)
}

// PatchOAuthModelPolicy upserts one provider entry. An entry whose lists are
// all absent/empty removes the provider key. An explicit legacy merge flag is
// rejected: legacy exclusions always stay in effect by design.
func (h *Handler) PatchOAuthModelPolicy(c *gin.Context) {
	var body struct {
		Provider *string                       `json:"provider"`
		Policy   *config.OAuthModelPolicyEntry `json:"policy"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(*body.Provider))
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider"})
		return
	}
	var next config.OAuthModelPolicyEntry
	if body.Policy != nil {
		normalized, errNormalize := config.NormalizeOAuthModelPolicyEntry(*body.Policy)
		if errNormalize != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error()})
			return
		}
		next = normalized
	}
	if next.Allowlist == nil && next.Denylist == nil {
		if h.cfg.OAuthModelPolicy == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
			return
		}
		if _, exists := h.cfg.OAuthModelPolicy[provider]; !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
			return
		}
		delete(h.cfg.OAuthModelPolicy, provider)
		if len(h.cfg.OAuthModelPolicy) == 0 {
			h.cfg.OAuthModelPolicy = nil
		}
		h.persist(c)
		return
	}
	if h.cfg.OAuthModelPolicy == nil {
		h.cfg.OAuthModelPolicy = make(map[string]config.OAuthModelPolicyEntry)
	}
	h.cfg.OAuthModelPolicy[provider] = next
	h.persist(c)
}

// DeleteOAuthModelPolicy removes one provider entry.
func (h *Handler) DeleteOAuthModelPolicy(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing provider"})
		return
	}
	if h.cfg.OAuthModelPolicy == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	if _, exists := h.cfg.OAuthModelPolicy[provider]; !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	delete(h.cfg.OAuthModelPolicy, provider)
	if len(h.cfg.OAuthModelPolicy) == 0 {
		h.cfg.OAuthModelPolicy = nil
	}
	h.persist(c)
}

// modelPolicyMappingEquivalent reports whether normalization dropped any
// provider entry as structurally invalid. Blank-key drops are acceptable;
// value-level rejections are not.
func modelPolicyMappingEquivalent(in, normalized map[string]config.OAuthModelPolicyEntry) bool {
	if in == nil {
		return true
	}
	for provider, entry := range in {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		if entry.Allowlist == nil && entry.Denylist == nil {
			continue
		}
		if _, exists := normalized[key]; !exists {
			return false
		}
	}
	return true
}
