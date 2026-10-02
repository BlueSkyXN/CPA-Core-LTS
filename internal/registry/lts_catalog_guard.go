package registry

import (
	"encoding/json"
	"strings"
)

// ltsEmbeddedCatalogGuardModels lists model IDs that must survive remote model
// catalog refreshes. Entries live in the embedded catalogs; when a remote
// candidate omits a guard model, the embedded entry is merged back before the
// candidate is accepted, so routing for approved accounts keeps working across
// the 3-hour refresh cycle and CI catalog refreshes. Once the shared upstream
// catalog ships the same model ID, the remote entry wins and the merge is a
// no-op.
var ltsEmbeddedCatalogGuardModels = []string{"gpt-daybreak-blue-latest"}

// mergeGuardModelsIntoCandidate merges guard models missing from every codex
// tier of the candidate using the embedded catalog as the source of truth.
func mergeGuardModelsIntoCandidate(candidate *staticModelsJSON) {
	if candidate == nil {
		return
	}
	embedded := embeddedModelsSnapshot()
	if embedded == nil {
		return
	}
	candidateTiers := []*[]*ModelInfo{&candidate.CodexFree, &candidate.CodexTeam, &candidate.CodexPlus, &candidate.CodexPro}
	embeddedTiers := []*[]*ModelInfo{&embedded.CodexFree, &embedded.CodexTeam, &embedded.CodexPlus, &embedded.CodexPro}
	for _, guardID := range ltsEmbeddedCatalogGuardModels {
		for i := range candidateTiers {
			if modelSectionHasID(*candidateTiers[i], guardID) {
				continue
			}
			for _, embeddedModel := range *embeddedTiers[i] {
				if embeddedModel != nil && strings.EqualFold(strings.TrimSpace(embeddedModel.ID), guardID) {
					*candidateTiers[i] = append(*candidateTiers[i], embeddedModel)
					break
				}
			}
		}
	}
}

func embeddedModelsSnapshot() *staticModelsJSON {
	var embedded staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &embedded); err != nil {
		return nil
	}
	return &embedded
}

func modelSectionHasID(models []*ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), id) {
			return true
		}
	}
	return false
}

// mergeGuardCodexClientModels merges guard client-catalog entries missing from
// the candidate using the embedded client catalog as the source of truth. The
// returned payload replaces the candidate before validation.
func mergeGuardCodexClientModels(candidate []byte) []byte {
	var payload codexClientModelsPayload
	if err := json.Unmarshal(candidate, &payload); err != nil {
		return candidate
	}
	if len(payload.Models) == 0 {
		return candidate
	}
	var embedded codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &embedded); err != nil {
		return candidate
	}
	embeddedBySlug := make(map[string]map[string]any, len(embedded.Models))
	for _, model := range embedded.Models {
		if slug, ok := model["slug"].(string); ok && strings.TrimSpace(slug) != "" {
			embeddedBySlug[strings.ToLower(strings.TrimSpace(slug))] = model
		}
	}
	present := make(map[string]struct{}, len(payload.Models))
	for _, model := range payload.Models {
		if slug, ok := model["slug"].(string); ok && strings.TrimSpace(slug) != "" {
			present[strings.ToLower(strings.TrimSpace(slug))] = struct{}{}
		}
	}
	merged := false
	for _, guardID := range ltsEmbeddedCatalogGuardModels {
		key := strings.ToLower(guardID)
		if _, exists := present[key]; exists {
			continue
		}
		if entry, ok := embeddedBySlug[key]; ok {
			payload.Models = append(payload.Models, entry)
			merged = true
		}
	}
	if !merged {
		return candidate
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return candidate
	}
	return out
}
