package flowcontrol

import "strings"

type AuthReference struct{ Legacy, Ref, Provider, AuthKind string }
type MigrationIssue struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
}
type Migration struct {
	Config Config           `json:"config"`
	Issues []MigrationIssue `json:"issues"`
	Ready  bool             `json:"ready"`
}

// Migrate only proposes a policy. It never writes files or changes an Engine.
// Any old shared-account bucket with multiple possible Auth IDs is ambiguous.
func Migrate(cfg Config, refs []AuthReference) Migration {
	out := Migration{Config: cfg.Effective(), Issues: []MigrationIssue{}, Ready: true}
	if cfg.Version >= 3 {
		if err := cfg.Validate(); err != nil {
			out.Issues = append(out.Issues, MigrationIssue{Reason: err.Error()})
			out.Ready = false
		}
		return out
	}
	out.Config.Version = 3
	for i, r := range out.Config.Rules {
		problem := func(reason string) { out.Ready = false; out.Issues = append(out.Issues, MigrationIssue{r.ID, reason}) }
		dims, err := r.Dimensions()
		if err != nil {
			problem(err.Error())
			continue
		}
		grouped := false
		for _, d := range dims {
			if d == "account" {
				grouped = true
			}
		}
		oldMap := map[string][]string{}
		for _, a := range refs {
			if exact(strings.ToLower(strings.TrimSpace(r.Provider)), strings.ToLower(a.Provider)) && exact(r.AuthKind, a.AuthKind) &&
				exact(r.Account, a.Legacy) && exact(r.Credential, a.Ref) {
				oldMap[a.Legacy] = append(oldMap[a.Legacy], a.Ref)
			}
		}
		if grouped {
			for _, ids := range oldMap {
				if len(ids) > 1 {
					problem("legacy-account-group-is-ambiguous")
					break
				}
			}
		}
		account := ""
		if r.Account != "" && r.Account != "*" {
			ids := oldMap[r.Account]
			if len(ids) != 1 {
				problem("legacy-account-reference-missing-or-ambiguous")
			} else {
				account = ids[0]
			}
		}
		if r.Credential != "" && r.Credential != "*" {
			found := false
			for _, a := range refs {
				found = found || a.Ref == r.Credential
			}
			if !found {
				problem("legacy-credential-reference-missing")
			}
			if account != "" && account != r.Credential {
				problem("legacy-account-and-credential-disagree")
			}
			account = r.Credential
		}
		r.Account = ""
		r.Credential = ""
		r.Accounts = nil
		if account != "" {
			r.Accounts = []string{account}
		}
		if r.Key != "" && r.Key != "*" {
			r.Keys = []string{r.Key}
		}
		r.Key = ""
		if r.Model != "" && r.Model != "*" {
			r.Models = []string{r.Model}
		}
		r.Model = ""
		r.Scope = "custom"
		r.GroupBy = []string{}
		seen := map[string]bool{}
		for _, d := range dims {
			d = strings.ReplaceAll(d, "credential", "account")
			if !seen[d] {
				seen[d] = true
				r.GroupBy = append(r.GroupBy, d)
			}
		}
		out.Config.Rules[i] = r
	}
	out.Config = out.Config.Effective()
	if out.Ready {
		check := out.Config
		check.Enabled = true
		if err := check.Validate(); err != nil {
			out.Ready = false
			out.Issues = append(out.Issues, MigrationIssue{Reason: err.Error()})
		}
	}
	return out
}
