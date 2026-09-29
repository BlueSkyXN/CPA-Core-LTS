package pluginhost

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func (a *executorAdapter) executionModelInfo(auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*registry.ModelInfo, error) {
	if resolved, ok := coreauth.ResolvedModelInfo(req); ok {
		return resolved, nil
	}
	models := a.host.modelRegistration(a.pluginID).models
	if auth != nil {
		models = registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	}
	if len(models) == 0 {
		// Legacy executors without model declarations retain metadata-free translation.
		return nil, nil
	}

	selectModel := func(matches func(*registry.ModelInfo) bool) (*registry.ModelInfo, error) {
		var selected *registry.ModelInfo
		for _, model := range models {
			if model == nil || !matches(model) {
				continue
			}
			if selected != nil {
				return nil, coreauth.NewRequestScopedError("Selected plugin model capabilities are ambiguous", http.StatusBadRequest)
			}
			selected = model
		}
		return selected, nil
	}
	requested := executorMetadataID(opts.Metadata, coreexecutor.RequestedModelMetadataKey)
	if requested == "" {
		requested = executorMetadataID(req.Metadata, coreexecutor.RequestedModelMetadataKey)
	}
	for _, candidate := range executionModelCandidates(requested) {
		for _, byID := range []bool{true, false} {
			selected, err := selectModel(func(model *registry.ModelInfo) bool {
				name := model.Name
				if byID {
					name = model.ID
				}
				return name == candidate && executionModelMatchesUpstream(model, auth, req.Model)
			})
			if err != nil || selected != nil {
				return selected, err
			}
		}
	}
	for _, candidate := range executionModelCandidates(req.Model) {
		for _, byID := range []bool{true, false} {
			selected, err := selectModel(func(model *registry.ModelInfo) bool {
				if byID {
					return model.ID == candidate
				}
				return model.Name == candidate
			})
			if err != nil || selected != nil {
				return selected, err
			}
		}
	}
	for _, candidate := range executionModelCandidates(req.Model) {
		selected, err := selectModel(func(model *registry.ModelInfo) bool {
			return model.MetadataModelID != "" && model.MetadataModelID == candidate
		})
		if err != nil || selected != nil {
			return selected, err
		}
	}
	// A declared catalog miss must not inherit another provider's same-name model.
	return &registry.ModelInfo{ID: req.Model, UserDefined: true}, nil
}

func executionModelCandidates(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	candidates := []string{model}
	if parsed := thinking.ParseSuffix(model); parsed.HasSuffix && parsed.ModelName != "" {
		candidates = append(candidates, parsed.ModelName)
	}
	return candidates
}

func executionModelMatchesUpstream(model *registry.ModelInfo, auth *coreauth.Auth, upstream string) bool {
	names := []string{model.ID, model.Name}
	if model.MetadataModelID != "" {
		names = []string{model.MetadataModelID}
	}
	for _, name := range names {
		if model.MetadataModelID == "" && auth != nil && auth.Prefix != "" {
			name = strings.TrimPrefix(name, strings.TrimSpace(auth.Prefix)+"/")
		}
		for _, candidate := range executionModelCandidates(upstream) {
			if name != "" && name == candidate {
				return true
			}
		}
	}
	return false
}
