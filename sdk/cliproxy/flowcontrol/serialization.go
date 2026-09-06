package flowcontrol

import "encoding/json"

// Preserve explicitly empty selections in disabled draft/status JSON. They are
// invalid when enabled, and must not round-trip into an omitted (= all) filter.
func (r Rule) MarshalJSON() ([]byte, error) {
	type plain Rule
	pointer := func(v []string) *[]string {
		if v == nil {
			return nil
		}
		return &v
	}
	return json.Marshal(struct {
		plain
		Keys     *[]string `json:"keys,omitempty"`
		Models   *[]string `json:"models,omitempty"`
		Accounts *[]string `json:"accounts,omitempty"`
	}{plain: plain(r), Keys: pointer(r.Keys), Models: pointer(r.Models), Accounts: pointer(r.Accounts)})
}

// MarshalYAML preserves nil (= all) versus an explicitly empty selection in
// saves performed by existing configuration-management handlers. No YAML
// dependency is needed: the repository's encoder consumes this ordinary map.
func (r Rule) MarshalYAML() (any, error) {
	out := map[string]any{"id": r.ID, "stage": r.Stage, "scope": r.Scope, "max-concurrent": r.MaxConcurrent}
	for k, v := range map[string]string{
		"label": r.Label, "key": r.Key, "model": r.Model, "provider": r.Provider,
		"account": r.Account, "credential": r.Credential, "auth-kind": r.AuthKind,
	} {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range map[string][]string{"keys": r.Keys, "models": r.Models, "accounts": r.Accounts, "group-by": r.GroupBy} {
		if v != nil {
			out[k] = v
		}
	}
	if len(r.Windows) > 0 {
		out["windows"] = r.Windows
	}
	return out, nil
}
