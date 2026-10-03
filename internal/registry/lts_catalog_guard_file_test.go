package registry

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestMergeLTSModelsCatalogFileRestoresMissingGuardTiers(t *testing.T) {
	candidate, err := sjson.SetBytes(catalogWithoutBlue(t), "codex-pro.0.x_remote_marker", "keep-me")
	if err != nil {
		t.Fatalf("mark remote entry: %v", err)
	}

	out, merged, err := MergeLTSModelsCatalogFile(candidate)
	if err != nil {
		t.Fatalf("merge models catalog candidate: %v", err)
	}
	if len(merged) != 3 {
		t.Fatalf("merged entries = %v, want codex-team/codex-plus/codex-pro only", merged)
	}
	for _, tier := range []string{"codex-team", "codex-plus", "codex-pro"} {
		if !fileSectionHasID(out, tier, "id", "gpt-daybreak-blue-latest") {
			t.Fatalf("%s lost guard model after file merge", tier)
		}
	}
	if fileSectionHasID(out, "codex-free", "id", "gpt-daybreak-blue-latest") {
		t.Fatal("codex-free unexpectedly received guard model")
	}
	if !strings.Contains(string(out), "keep-me") {
		t.Fatal("unrelated remote field did not survive the file merge")
	}
}

func TestMergeLTSModelsCatalogFilePartialTierKeepsRemoteEntry(t *testing.T) {
	candidate := catalogWithoutBlue(t)
	remoteEntry := embeddedFileSectionEntryRaw(embeddedModelsJSON, "codex-pro", "id", "gpt-daybreak-blue-latest")
	if len(remoteEntry) == 0 {
		t.Fatal("embedded guard entry missing")
	}
	remoteEntry, err := sjson.SetBytes(remoteEntry, "description", "remote-authoritative")
	if err != nil {
		t.Fatalf("customize remote entry: %v", err)
	}
	candidate, err = sjson.SetRawBytes(candidate, "codex-pro.-1", remoteEntry)
	if err != nil {
		t.Fatalf("append remote entry: %v", err)
	}

	out, merged, err := MergeLTSModelsCatalogFile(candidate)
	if err != nil {
		t.Fatalf("merge models catalog candidate: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("merged entries = %v, want codex-team/codex-plus only", merged)
	}
	blue := 0
	for _, entry := range gjson.GetBytes(out, "codex-pro").Array() {
		if entry.Get("id").String() == "gpt-daybreak-blue-latest" {
			blue++
			if entry.Get("description").String() != "remote-authoritative" {
				t.Fatalf("remote guard entry did not win: %q", entry.Get("description").String())
			}
		}
	}
	if blue != 1 {
		t.Fatalf("codex-pro guard entry count = %d, want 1", blue)
	}
	if !fileSectionHasID(out, "codex-team", "id", "gpt-daybreak-blue-latest") ||
		!fileSectionHasID(out, "codex-plus", "id", "gpt-daybreak-blue-latest") {
		t.Fatal("tiers without remote blue were not restored from embedded catalog")
	}
}

func TestMergeLTSModelsCatalogFileCompleteCandidateIsNoOp(t *testing.T) {
	out, merged, err := MergeLTSModelsCatalogFile(embeddedModelsJSON)
	if err != nil {
		t.Fatalf("merge complete candidate: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("merged entries = %v, want none", merged)
	}
	if !bytes.Equal(out, embeddedModelsJSON) {
		t.Fatal("complete candidate should pass through unchanged")
	}
}

func TestMergeLTSModelsCatalogFileRejectsInvalidJSON(t *testing.T) {
	if _, _, err := MergeLTSModelsCatalogFile([]byte(`{"codex-pro":`)); err == nil {
		t.Fatal("invalid candidate JSON should fail the merge")
	}
}

func TestMergeLTSCodexClientCatalogFileRestoresGuardSlug(t *testing.T) {
	candidate, err := sjson.SetBytes(clientCatalogWithoutBlue(t), "models.0.x_remote_marker", "keep-me")
	if err != nil {
		t.Fatalf("mark remote entry: %v", err)
	}

	out, merged, err := MergeLTSCodexClientCatalogFile(candidate)
	if err != nil {
		t.Fatalf("merge client catalog candidate: %v", err)
	}
	if len(merged) != 1 || merged[0] != "models:gpt-daybreak-blue-latest" {
		t.Fatalf("merged entries = %v, want models guard slug only", merged)
	}
	if !fileSectionHasID(out, "models", "slug", "gpt-daybreak-blue-latest") {
		t.Fatal("client catalog lost guard slug after file merge")
	}
	if !strings.Contains(string(out), "keep-me") {
		t.Fatal("unrelated remote field did not survive the file merge")
	}
}

func TestMergeLTSCodexClientCatalogFileRemoteSlugWins(t *testing.T) {
	candidate := clientCatalogWithoutBlue(t)
	entry := embeddedFileSectionEntryRaw(embeddedCodexClientModelsJSON, "models", "slug", "gpt-daybreak-blue-latest")
	if len(entry) == 0 {
		t.Fatal("embedded guard entry missing")
	}
	entry, err := sjson.SetBytes(entry, "display_name", "Daybreak Blue (remote)")
	if err != nil {
		t.Fatalf("customize remote entry: %v", err)
	}
	candidate, err = sjson.SetRawBytes(candidate, "models.-1", entry)
	if err != nil {
		t.Fatalf("append remote entry: %v", err)
	}

	out, merged, err := MergeLTSCodexClientCatalogFile(candidate)
	if err != nil {
		t.Fatalf("merge client catalog candidate: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("merged entries = %v, want none", merged)
	}
	found := false
	for _, model := range gjson.GetBytes(out, "models").Array() {
		if model.Get("slug").String() == "gpt-daybreak-blue-latest" {
			found = true
			if model.Get("display_name").String() != "Daybreak Blue (remote)" {
				t.Fatalf("remote entry did not win: %q", model.Get("display_name").String())
			}
		}
	}
	if !found {
		t.Fatal("remote guard slug missing after merge")
	}
}
