package diff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// oauthModelPolicySummary hashes the effective parts of one provider's model
// policy: only enabled lists participate in evaluation, so toggling a list off
// with its models kept must produce the same summary as removing it.
type oauthModelPolicySummary struct {
	hash  string
	count int
}

func summarizeOAuthModelPolicyList(list *config.OAuthModelPolicyList) []string {
	if list == nil || !list.Enabled || len(list.Models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(list.Models))
	models := make([]string, 0, len(list.Models))
	for _, model := range list.Models {
		if trimmed := strings.ToLower(strings.TrimSpace(model)); trimmed != "" {
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			models = append(models, trimmed)
		}
	}
	sort.Strings(models)
	return models
}

func summarizeOAuthModelPolicyEntry(entry config.OAuthModelPolicyEntry) oauthModelPolicySummary {
	parts := make([]string, 0, 2)
	if entry.Allowlist != nil && entry.Allowlist.Enabled {
		allow, _ := json.Marshal(summarizeOAuthModelPolicyList(entry.Allowlist))
		parts = append(parts, "allow="+string(allow))
	}
	if deny := summarizeOAuthModelPolicyList(entry.Denylist); deny != nil {
		raw, _ := json.Marshal(deny)
		parts = append(parts, "deny="+string(raw))
	}
	if len(parts) == 0 {
		return oauthModelPolicySummary{}
	}
	count := 0
	if entry.Allowlist != nil && entry.Allowlist.Enabled {
		count += len(entry.Allowlist.Models)
	}
	if entry.Denylist != nil && entry.Denylist.Enabled {
		count += len(entry.Denylist.Models)
	}
	return oauthModelPolicySummary{
		hash:  ComputeExcludedModelsHash(parts),
		count: count,
	}
}

// DiffOAuthModelPolicyChanges compares per-provider model policies and returns
// human-readable changes plus the affected provider keys. Reuses the legacy
// excluded-models hash so the summary format stays uniform.
func DiffOAuthModelPolicyChanges(oldMap, newMap map[string]config.OAuthModelPolicyEntry) ([]string, []string) {
	keys := make(map[string]struct{}, len(oldMap)+len(newMap))
	for k := range oldMap {
		keys[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	for k := range newMap {
		keys[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	changes := make([]string, 0, len(keys))
	affected := make([]string, 0, len(keys))
	for key := range keys {
		oldInfo, okOld := oauthPolicyLookup(oldMap, key)
		newInfo, okNew := oauthPolicyLookup(newMap, key)
		switch {
		case okOld && !okNew:
			changes = append(changes, fmt.Sprintf("oauth-model-policy[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew && newInfo.hash != "":
			changes = append(changes, fmt.Sprintf("oauth-model-policy[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-model-policy[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func oauthPolicyLookup(entries map[string]config.OAuthModelPolicyEntry, key string) (oauthModelPolicySummary, bool) {
	entry, ok := entries[key]
	if !ok {
		return oauthModelPolicySummary{}, false
	}
	return summarizeOAuthModelPolicyEntry(entry), true
}
