package cliproxy

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// validateOAuthAliasExclusions returns warning strings for alias mappings whose
// upstream model name is blocked by a provider-wide exclusion in the same
// channel. It mirrors the runtime wildcard matching and self-alias filtering so
// warnings describe mappings that would otherwise enter the runtime alias table.
func validateOAuthAliasExclusions(aliases map[string][]config.OAuthModelAlias, excluded map[string][]string) []string {
	if len(aliases) == 0 || len(excluded) == 0 {
		return nil
	}
	var warnings []string
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		patterns := excluded[channel]
		if len(patterns) == 0 {
			continue
		}
		for _, entry := range entries {
			upstream := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if upstream == "" || alias == "" || strings.EqualFold(upstream, alias) {
				continue
			}
			upstreamLower := strings.ToLower(upstream)
			for _, pattern := range patterns {
				p := strings.TrimSpace(pattern)
				if p == "" {
					continue
				}
				if matchWildcard(strings.ToLower(p), upstreamLower) {
					warnings = append(warnings, fmt.Sprintf(
						"oauth-model-alias: alias=%q channel=%q upstream=%q matches provider-wide exclusion pattern=%q; alias will not resolve at runtime",
						alias, channel, upstream, p,
					))
				}
			}
		}
	}
	return warnings
}
