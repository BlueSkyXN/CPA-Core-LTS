package auth

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// FlowModelOption identifies a known Executor target, not a public alias.
// Aliases describe entry routes; accounts scope those mappings without exposing
// any token. A plugin's later request rewrite can still introduce other targets.
type FlowModelOption struct {
	Ref      string   `json:"ref"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Aliases  []string `json:"aliases"`
	Accounts []string `json:"accounts"`
}

// FlowControlModelOptions is read-only. It deliberately avoids
// preparedExecutionModelsWithAlias/nextModelPoolOffset: opening the Panel must
// not advance pool rotation or select a credential for a real request.
func (m *Manager) FlowControlModelOptions() ([]FlowModelOption, bool) {
	const maxOptions = 4096
	out := []FlowModelOption{}
	if m == nil {
		return out, false
	}
	routing := m.loadAPIKeyModelRouting()
	byRef := map[string]*FlowModelOption{}
	truncated := false
	auths := m.List()
	sort.Slice(auths, func(i, j int) bool {
		if auths[i] == nil {
			return false
		}
		if auths[j] == nil {
			return true
		}
		return auths[i].ID < auths[j].ID
	})
	for _, a := range auths {
		if a == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(FlowAccountProvider(a)))
		if provider == "" {
			continue
		}
		registered := append([]*registry.ModelInfo(nil), registry.GetGlobalRegistry().GetModelsForClient(a.ID)...)
		sort.Slice(registered, func(i, j int) bool {
			if registered[i] == nil {
				return false
			}
			if registered[j] == nil {
				return true
			}
			return registered[i].ID < registered[j].ID
		})
		for _, info := range registered {
			if info == nil || strings.TrimSpace(info.ID) == "" {
				continue
			}
			requested := rewriteModelForAuth(info.ID, a)
			alias := m.resolveExecutionAliasResultForRequestedWithRouting(routing, a, requested)
			upstream := executionAliasPoolModel(a, requested, alias)
			targets := resolveOpenAICompatUpstreamModelPool(routing.config, a, upstream)
			if len(targets) == 0 {
				resolved := m.applyAPIKeyModelAliasWithRouting(routing, a, upstream)
				if strings.TrimSpace(resolved) == "" {
					resolved = upstream
				}
				targets = []string{resolved}
			}
			for _, target := range targets {
				model := strings.ToLower(flowModel(target))
				if model == "" {
					continue
				}
				ref := provider + "::" + model
				row := byRef[ref]
				if row == nil {
					if len(byRef) >= maxOptions {
						truncated = true
						continue
					}
					row = &FlowModelOption{Ref: ref, Provider: provider, Model: model, Aliases: []string{}, Accounts: []string{}}
					byRef[ref] = row
				}
				// The directory is advisory and bounded, including relationship lists.
				if !appendFlowModelReference(&row.Aliases, flowModel(info.ID)) {
					truncated = true
				}
				if !appendFlowModelReference(&row.Accounts, FlowAccountReference(a)) {
					truncated = true
				}
			}
		}
	}
	for _, row := range byRef {
		sort.Strings(row.Aliases)
		sort.Strings(row.Accounts)
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, truncated
}

func appendFlowModelReference(values *[]string, value string) bool {
	for _, old := range *values {
		if old == value {
			return true
		}
	}
	if len(*values) >= 256 {
		return false
	}
	*values = append(*values, value)
	return true
}
