package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ltsGuardModelsTierKeys mirrors the codex tier JSON keys of staticModelsJSON
// that the runtime guard merge covers, including tiers the embedded catalog
// deliberately does not ship guard entries for (codex-free).
var ltsGuardModelsTierKeys = []string{"codex-free", "codex-team", "codex-plus", "codex-pro"}

// MergeLTSModelsCatalogFile merges guard model entries missing from a raw
// models.json refresh candidate using the embedded catalog as the source of
// truth. It applies the same rule as the runtime guard merge (per-tier
// presence check, embedded source, remote same-ID entries win) but edits the
// document with raw JSON appends so unrelated remote fields survive the merge
// byte-for-byte. The returned payload equals candidate when nothing is merged.
func MergeLTSModelsCatalogFile(candidate []byte) ([]byte, []string, error) {
	return mergeGuardFileCandidate(candidate, embeddedModelsJSON, "models.json", ltsGuardModelsTierKeys, "id", ltsEmbeddedCatalogGuardModels)
}

// MergeLTSCodexClientCatalogFile merges guard entries missing from a raw
// codex_client_models.json refresh candidate using the embedded client catalog
// as the source of truth, with the same slug-keyed rule as the runtime guard
// merge for the client catalog.
func MergeLTSCodexClientCatalogFile(candidate []byte) ([]byte, []string, error) {
	return mergeGuardFileCandidate(candidate, embeddedCodexClientModelsJSON, "codex_client_models.json", []string{"models"}, "slug", ltsEmbeddedCatalogGuardModels)
}

func mergeGuardFileCandidate(candidate, embeddedSource []byte, catalogName string, sectionKeys []string, idField string, guardIDs []string) ([]byte, []string, error) {
	if len(candidate) == 0 {
		return nil, nil, errors.New("empty catalog candidate")
	}
	if !json.Valid(candidate) {
		return nil, nil, fmt.Errorf("%s: candidate is not valid JSON", catalogName)
	}
	if !json.Valid(embeddedSource) {
		return nil, nil, fmt.Errorf("%s: embedded catalog is not valid JSON", catalogName)
	}
	out := candidate
	merged := make([]string, 0)
	for _, guardID := range guardIDs {
		for _, sectionKey := range sectionKeys {
			if fileSectionHasID(out, sectionKey, idField, guardID) {
				continue
			}
			entryRaw := embeddedFileSectionEntryRaw(embeddedSource, sectionKey, idField, guardID)
			if len(entryRaw) == 0 {
				// The embedded section does not carry this guard ID (for example
				// codex-free intentionally omits Daybreak Blue); nothing to merge.
				continue
			}
			next, err := sjson.SetRawBytes(out, sectionKey+".-1", entryRaw)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: merge guard entry %s into %s: %w", catalogName, guardID, sectionKey, err)
			}
			out = next
			merged = append(merged, fmt.Sprintf("%s:%s", sectionKey, guardID))
		}
	}
	return out, merged, nil
}

func fileSectionHasID(doc []byte, sectionKey, idField, id string) bool {
	section := gjson.GetBytes(doc, sectionKey)
	if !section.IsArray() {
		return false
	}
	for _, entry := range section.Array() {
		if strings.EqualFold(strings.TrimSpace(entry.Get(idField).String()), id) {
			return true
		}
	}
	return false
}

func embeddedFileSectionEntryRaw(doc []byte, sectionKey, idField, id string) []byte {
	section := gjson.GetBytes(doc, sectionKey)
	if !section.IsArray() {
		return nil
	}
	for _, entry := range section.Array() {
		if strings.EqualFold(strings.TrimSpace(entry.Get(idField).String()), id) {
			return []byte(entry.Raw)
		}
	}
	return nil
}
