package config

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// CompileResourceAllowlists validates the policy alongside the configured
// upstream keys and returns a snapshot for the auth manager. Tracking remains
// active through reloads after the last rule is removed so stale credentials
// cannot enter the shared pool before the watcher removes them.
func (c *Config) CompileResourceAllowlists(trackConfiguredKeys bool) (*ResourceAllowlists, error) {
	if c == nil {
		return nil, nil
	}
	if c.Home.Enabled && c.ResourceAllowlists.Enabled() {
		return nil, fmt.Errorf("access.resource-allowlists requires local Core routing")
	}
	policy, err := c.ResourceAllowlists.Normalize()
	if err != nil {
		return nil, err
	}
	var upstreamRules map[string][]string
	if policy != nil {
		upstreamRules = policy.UpstreamAPIKeys
	}
	ids := make(map[string]struct{})
	var refsByKey map[[sha256.Size]byte][]string
	if len(upstreamRules) != 0 || (trackConfiguredKeys && !c.Home.Enabled) {
		refsByKey = make(map[[sha256.Size]byte][]string)
	}
	validateKey := func(id, apiKey string) error {
		id = strings.ToLower(strings.TrimSpace(id))
		apiKey = strings.TrimSpace(apiKey)
		if id != "" {
			if !resourceCredentialIDPattern.MatchString(id) {
				return fmt.Errorf("upstream credential-id must use 1–64 letters, digits, dots, underscores or hyphens and start with a letter or digit")
			}
			if apiKey == "" {
				return fmt.Errorf("upstream credential-id requires a nonempty api-key on the same entry")
			}
			if _, duplicate := ids[id]; duplicate {
				return fmt.Errorf("upstream credential-id must be unique across API-key entries")
			}
			ids[id] = struct{}{}
		}
		if refsByKey == nil || apiKey == "" {
			return nil
		}

		digest := sha256.Sum256([]byte(apiKey))
		existing, found := refsByKey[digest]
		if refs, reserved := upstreamRules[id]; reserved {
			if existing == nil {
				refsByKey[digest] = refs
			} else {
				// The same key can appear on multiple routes. It must meet
				// every declared rule for its value.
				common := make([]string, 0, len(existing))
				for _, ref := range existing {
					if slices.Contains(refs, ref) {
						common = append(common, ref)
					}
				}
				refsByKey[digest] = common
			}
		} else if !found {
			refsByKey[digest] = nil
		}
		return nil
	}
	if err := c.visitUpstreamAPIKeys(validateKey); err != nil {
		return nil, err
	}
	for id := range upstreamRules {
		if _, found := ids[id]; !found {
			return nil, fmt.Errorf("access.resource-allowlists.upstream-api-keys references a credential-id missing from the configured API-key entries")
		}
	}
	if policy != nil || refsByKey != nil {
		if policy == nil {
			policy = &ResourceAllowlists{}
		}
		policy.upstreamKeyRefs = refsByKey
	}
	return policy, nil
}

// TracksConfiguredUpstreamAPIKeys reports whether this runtime snapshot verifies
// that a selected key is still present in the current configuration.
func (p *ResourceAllowlists) TracksConfiguredUpstreamAPIKeys() bool {
	return p != nil && p.upstreamKeyRefs != nil
}

// AllowsConfiguredUpstreamAPIKey applies the compiled policy to the actual key
// held by a config-backed auth. During reload, removed key values are excluded
// before the watcher retires their old auth records.
func (p *ResourceAllowlists) AllowsConfiguredUpstreamAPIKey(apiKey, scope string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if !p.TracksConfiguredUpstreamAPIKeys() || apiKey == "" {
		return true
	}
	refs, configured := p.upstreamKeyRefs[sha256.Sum256([]byte(apiKey))]
	return configured && (refs == nil || (scope != "" && slices.Contains(refs, scope)))
}

func (c *Config) visitUpstreamAPIKeys(visit func(id, key string) error) error {
	for _, family := range [][]GeminiKey{c.GeminiKey, c.InteractionsKey} {
		for _, entry := range family {
			if err := visit(entry.CredentialID, entry.APIKey); err != nil {
				return err
			}
		}
	}
	for _, family := range [][]CodexKey{c.CodexKey, c.XAIKey, c.MetaKey} {
		for _, entry := range family {
			if err := visit(entry.CredentialID, entry.APIKey); err != nil {
				return err
			}
		}
	}
	for _, entry := range c.ClaudeKey {
		if err := visit(entry.CredentialID, entry.APIKey); err != nil {
			return err
		}
	}
	for _, entry := range c.VertexCompatAPIKey {
		if err := visit(entry.CredentialID, entry.APIKey); err != nil {
			return err
		}
	}
	for _, group := range c.OpenAICompatibility {
		for _, entry := range group.APIKeyEntries {
			if err := visit(entry.CredentialID, entry.APIKey); err != nil {
				return err
			}
		}
	}
	return nil
}
