package registry

import (
	"encoding/json"
	"testing"
)

func withCatalogStores(t *testing.T) {
	t.Helper()
	priorModels := getModels()
	priorClient, priorRevision := GetCodexClientModelsSnapshot()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = priorModels
		modelsCatalogStore.mu.Unlock()
		codexClientCatalogStore.mu.Lock()
		codexClientCatalogStore.data = priorClient
		codexClientCatalogStore.revision = priorRevision
		codexClientCatalogStore.mu.Unlock()
	})
}

func catalogWithoutBlue(t *testing.T) []byte {
	t.Helper()
	embedded := embeddedModelsSnapshot()
	for _, tier := range []*[]*ModelInfo{&embedded.CodexFree, &embedded.CodexTeam, &embedded.CodexPlus, &embedded.CodexPro} {
		kept := (*tier)[:0]
		for _, model := range *tier {
			if model != nil && model.ID == "gpt-daybreak-blue-latest" {
				continue
			}
			kept = append(kept, model)
		}
		*tier = kept
	}
	data, err := json.Marshal(embedded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func clientCatalogWithoutBlue(t *testing.T) []byte {
	t.Helper()
	var payload codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &payload); err != nil {
		t.Fatal(err)
	}
	kept := payload.Models[:0]
	for _, model := range payload.Models {
		if slug, _ := model["slug"].(string); slug == "gpt-daybreak-blue-latest" {
			continue
		}
		kept = append(kept, model)
	}
	payload.Models = kept
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestModelsCatalogRefreshMergesGuardModels(t *testing.T) {
	withCatalogStores(t)

	if err := loadModelsFromBytes(catalogWithoutBlue(t), "test-remote-without-blue"); err != nil {
		t.Fatalf("load remote catalog without blue: %v", err)
	}
	for _, tier := range []struct {
		name   string
		models []*ModelInfo
	}{
		{"codex-team", getModels().CodexTeam},
		{"codex-plus", getModels().CodexPlus},
		{"codex-pro", getModels().CodexPro},
	} {
		if !modelSectionHasID(tier.models, "gpt-daybreak-blue-latest") {
			t.Fatalf("%s lost guard model after refresh", tier.name)
		}
	}
	if modelSectionHasID(getModels().CodexFree, "gpt-daybreak-blue-latest") {
		t.Fatal("codex-free unexpectedly received guard model")
	}
}

func TestModelsCatalogRemoteGuardModelWins(t *testing.T) {
	withCatalogStores(t)

	remote := catalogWithoutBlue(t)
	var parsed staticModelsJSON
	if err := json.Unmarshal(remote, &parsed); err != nil {
		t.Fatal(err)
	}
	remoteEntry := *modelSectionFind(t, parsed.CodexPro, "gpt-6-astra")
	remoteEntry.ID = "gpt-daybreak-blue-latest"
	remoteEntry.Description = "remote-authoritative"
	parsed.CodexPro = append(parsed.CodexPro, &remoteEntry)
	data, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadModelsFromBytes(data, "test-remote-with-blue"); err != nil {
		t.Fatalf("load remote catalog with blue: %v", err)
	}
	got := modelSectionFind(t, getModels().CodexPro, "gpt-daybreak-blue-latest")
	if got == nil {
		t.Fatal("remote blue entry missing")
	}
	if got.Description != "remote-authoritative" {
		t.Fatalf("remote guard entry did not win: %q", got.Description)
	}
}

func TestCodexClientCatalogRefreshMergesGuardModels(t *testing.T) {
	withCatalogStores(t)

	if _, err := loadCodexClientModelsFromBytes(clientCatalogWithoutBlue(t), "test-remote-without-blue"); err != nil {
		t.Fatalf("load client catalog without blue: %v", err)
	}
	var payload codexClientModelsPayload
	if err := json.Unmarshal(GetCodexClientModelsJSON(), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range payload.Models {
		if slug, _ := model["slug"].(string); slug == "gpt-daybreak-blue-latest" {
			found = true
		}
	}
	if !found {
		t.Fatal("client catalog lost guard model after refresh")
	}
}

func TestCodexClientCatalogRemoteGuardModelWins(t *testing.T) {
	withCatalogStores(t)

	var payload codexClientModelsPayload
	if err := json.Unmarshal(clientCatalogWithoutBlue(t), &payload); err != nil {
		t.Fatal(err)
	}
	var embedded codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &embedded); err != nil {
		t.Fatal(err)
	}
	var remoteEntry map[string]any
	for _, model := range embedded.Models {
		if slug, _ := model["slug"].(string); slug == "gpt-daybreak-blue-latest" {
			clone := make(map[string]any, len(model))
			for key, value := range model {
				clone[key] = value
			}
			remoteEntry = clone
			break
		}
	}
	if remoteEntry == nil {
		t.Fatal("embedded blue entry missing")
	}
	remoteEntry["display_name"] = "Daybreak Blue (remote)"
	remoteEntry["description"] = "remote-authoritative"
	payload.Models = append(payload.Models, remoteEntry)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadCodexClientModelsFromBytes(data, "test-remote-with-blue"); err != nil {
		t.Fatalf("load client catalog with remote blue: %v", err)
	}
	var stored codexClientModelsPayload
	if err := json.Unmarshal(GetCodexClientModelsJSON(), &stored); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, model := range stored.Models {
		if slug, _ := model["slug"].(string); slug == "gpt-daybreak-blue-latest" {
			count++
			if name, _ := model["display_name"].(string); name != "Daybreak Blue (remote)" {
				t.Fatalf("remote entry did not win: %v", model["display_name"])
			}
		}
	}
	if count != 1 {
		t.Fatalf("guard slug count = %d, want 1", count)
	}
}

func modelSectionFind(t *testing.T, models []*ModelInfo, id string) *ModelInfo {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	t.Fatalf("model %q not found", id)
	return nil
}
