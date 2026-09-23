package main

import (
	"net/http"
	"strings"
	"unicode"
)

// validateCanonicalModel 保持 exact-ID 语义：动态目录可能引入新的 canonical ID，
// 只拒绝大小写/别名/显示名误用，不做任何归一化猜测。
func validateCanonicalModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return newPluginCallError("unsupported_model", "canonical Qoder model ID is required", http.StatusBadRequest, false)
	}
	normalized := normalizeModel(model)
	for _, canonical := range canonicalQoderModelIDs {
		if model == canonical {
			return nil
		}
		if normalized == normalizeModel(canonical) {
			return newPluginCallError("unsupported_model", "Qoder model aliases are not accepted; use the canonical case-sensitive model ID", http.StatusBadRequest, false)
		}
	}
	for _, displayName := range canonicalQoderModelDisplayNames {
		if normalized == normalizeModel(displayName) {
			return newPluginCallError("unsupported_model", "Qoder display names are not executable model IDs; use the exact ID returned by /v1/models", http.StatusBadRequest, false)
		}
	}
	// Live typed model discovery may introduce new canonical IDs. Preserve them
	// exactly instead of normalizing or guessing an alias.
	return nil
}

func normalizeModel(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}
