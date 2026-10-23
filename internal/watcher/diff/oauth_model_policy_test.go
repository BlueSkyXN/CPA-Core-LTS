package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func entry(allowOn bool, allow []string, denyOn bool, deny []string) config.OAuthModelPolicyEntry {
	return config.OAuthModelPolicyEntry{
		Allowlist: &config.OAuthModelPolicyList{Enabled: allowOn, Models: allow},
		Denylist:  &config.OAuthModelPolicyList{Enabled: denyOn, Models: deny},
	}
}

func TestDiffOAuthModelPolicyChanges(t *testing.T) {
	oldMap := map[string]config.OAuthModelPolicyEntry{
		"codex": entry(false, nil, true, []string{"*-preview"}),
		"kimi":  entry(true, []string{"kimi-*"}, false, nil),
	}
	newMap := map[string]config.OAuthModelPolicyEntry{
		"codex": entry(true, []string{"gpt-*"}, true, []string{"*-preview"}),
		"qoder": entry(true, []string{"qmodel_*"}, false, nil),
	}
	changes, affected := DiffOAuthModelPolicyChanges(oldMap, newMap)
	if len(changes) != 3 {
		t.Fatalf("changes = %#v, want codex updated + kimi removed + qoder added", changes)
	}
	affectedSet := map[string]bool{}
	for _, provider := range affected {
		affectedSet[provider] = true
	}
	if !affectedSet["codex"] || !affectedSet["kimi"] || !affectedSet["qoder"] {
		t.Fatalf("affected = %#v", affected)
	}
}

func TestDiffOAuthModelPolicyTreatsDisabledToggleAsNoEffect(t *testing.T) {
	// A denylist toggled off with models kept has no runtime effect: it must not
	// report the provider as affected (the enabled evaluation is unchanged).
	oldMap := map[string]config.OAuthModelPolicyEntry{
		"codex": entry(false, nil, false, []string{"*-preview"}),
	}
	newMap := map[string]config.OAuthModelPolicyEntry{
		"codex": entry(false, nil, false, []string{"*-preview", "other"}),
	}
	if changes, affected := DiffOAuthModelPolicyChanges(oldMap, newMap); len(changes) != 0 || len(affected) != 0 {
		t.Fatalf("disabled-list model edits must not affect runtime: %v %v", changes, affected)
	}
}

func TestDiffOAuthModelPolicyEmptyEntryNotReported(t *testing.T) {
	// An entry whose lists are all disabled/empty evaluates to no restriction;
	// adding or removing it must not touch the provider's accounts.
	newMap := map[string]config.OAuthModelPolicyEntry{
		"codex": entry(false, nil, false, nil),
	}
	if changes, affected := DiffOAuthModelPolicyChanges(nil, newMap); len(changes) != 0 || len(affected) != 0 {
		t.Fatalf("empty entry reported: %v %v", changes, affected)
	}
}

func TestDiffOAuthModelPolicyEmptyAllowlistToggle(t *testing.T) {
	disabled := map[string]config.OAuthModelPolicyEntry{"codex": entry(false, nil, false, nil)}
	enabled := map[string]config.OAuthModelPolicyEntry{"codex": entry(true, nil, false, nil)}
	for _, pair := range [][2]map[string]config.OAuthModelPolicyEntry{{nil, enabled}, {disabled, enabled}, {enabled, disabled}} {
		_, affected := DiffOAuthModelPolicyChanges(pair[0], pair[1])
		if len(affected) != 1 || affected[0] != "codex" {
			t.Fatalf("empty allowlist toggle did not refresh models: %v", affected)
		}
	}
}
