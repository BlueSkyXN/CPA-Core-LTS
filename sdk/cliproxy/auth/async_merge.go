package auth

import (
	"reflect"
	"strings"
)

// mergeAsyncAuth is a three-way merge: current wins conflicts, and unchanged
// fields receive the provider's delta. Never replace the whole live credential
// with a snapshot captured before network I/O.
func mergeAsyncAuth(base, current, updated *Auth) *Auth {
	out := current.Clone()
	if base.Prefix == current.Prefix {
		out.Prefix = updated.Prefix
	}
	if base.Label == current.Label {
		out.Label = updated.Label
	}
	if base.ProxyURL == current.ProxyURL {
		out.ProxyURL = updated.ProxyURL
	}
	if base.Disabled == current.Disabled {
		out.Disabled = updated.Disabled
	}
	out.Attributes = mergeAuthMap(base.Attributes, current.Attributes, updated.Attributes)
	out.Metadata = mergeAuthMetadata(base.Metadata, current.Metadata, updated.Metadata)
	// Token families must not mix a manually replaced access token with an old
	// refresh token. A concurrent credential edit wins the entire token update.
	materialChanged := credentialMaterialChanged(base.Metadata, current.Metadata)
	if materialChanged {
		out.Metadata = current.Clone().Metadata
	}
	if !materialChanged && reflect.DeepEqual(base.Storage, current.Storage) {
		out.Storage = updated.Storage
	}
	if !materialChanged && reflect.DeepEqual(base.Runtime, current.Runtime) {
		out.Runtime = updated.Runtime
	}
	if !materialChanged && base.LastRefreshedAt == current.LastRefreshedAt {
		out.LastRefreshedAt = updated.LastRefreshedAt
	}
	if !materialChanged && base.NextRefreshAfter == current.NextRefreshAfter {
		out.NextRefreshAfter = updated.NextRefreshAfter
	}
	// Availability fields describe one state; do not partially clear a newer
	// execution failure just because only one field changed concurrently.
	if base.Status == current.Status && base.StatusMessage == current.StatusMessage &&
		base.Unavailable == current.Unavailable && base.NextRetryAfter == current.NextRetryAfter &&
		reflect.DeepEqual(base.LastError, current.LastError) && reflect.DeepEqual(base.Quota, current.Quota) {
		out.Status = updated.Status
		out.StatusMessage = updated.StatusMessage
		out.Unavailable = updated.Unavailable
		out.NextRetryAfter = updated.NextRetryAfter
		out.LastError = updated.Clone().LastError
		out.Quota = updated.Quota.Clone()
	}
	out.ModelStates = mergeAuthMap(base.ModelStates, current.ModelStates, updated.ModelStates)
	return out.Clone()
}

func mergeAuthMap[T any](base, current, updated map[string]T) map[string]T {
	if base == nil && current == nil && updated == nil {
		return nil
	}
	out := make(map[string]T, len(current))
	for key, value := range current {
		out[key] = value
	}
	keys := make(map[string]struct{}, len(base)+len(updated))
	for key := range base {
		keys[key] = struct{}{}
	}
	for key := range updated {
		keys[key] = struct{}{}
	}
	for key := range keys {
		before, was := base[key]
		latest, exists := current[key]
		if was != exists || !reflect.DeepEqual(before, latest) {
			continue
		}
		if value, ok := updated[key]; ok {
			out[key] = value
		} else {
			delete(out, key)
		}
	}
	return out
}

func mergeAuthMetadata(base, current, updated map[string]any) map[string]any {
	out := mergeAuthMap(base, current, updated)
	for key, before := range base {
		b, bok := before.(map[string]any)
		c, cok := current[key].(map[string]any)
		u, uok := updated[key].(map[string]any)
		if bok && cok && uok {
			out[key] = mergeAuthMetadata(b, c, u)
		}
	}
	return out
}

func credentialMaterialChanged(base, current map[string]any) bool {
	keys := make(map[string]struct{}, len(base)+len(current))
	for key := range base {
		keys[key] = struct{}{}
	}
	for key := range current {
		keys[key] = struct{}{}
	}
	for key := range keys {
		b, bok := base[key]
		c, cok := current[key]
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			lower == "api_key" || lower == "api-key" || lower == "cookie" || lower == "password" {
			if bok != cok || !reflect.DeepEqual(b, c) {
				return true
			}
		}
		bm, _ := b.(map[string]any)
		cm, _ := c.(map[string]any)
		if (bm != nil || cm != nil) && credentialMaterialChanged(bm, cm) {
			return true
		}
	}
	return false
}

// Only JSON-shaped values are copied recursively. Opaque provider handles are
// intentionally not reflected through or serialized.
func cloneAuthJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		if value == nil {
			return value
		}
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = cloneAuthJSONValue(child)
		}
		return out
	case []any:
		if value == nil {
			return value
		}
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = cloneAuthJSONValue(child)
		}
		return out
	case map[string]string:
		if value == nil {
			return value
		}
		out := make(map[string]string, len(value))
		for key, child := range value {
			out[key] = child
		}
		return out
	case []string:
		if value == nil {
			return value
		}
		out := make([]string, len(value))
		copy(out, value)
		return out
	case []byte:
		if value == nil {
			return value
		}
		out := make([]byte, len(value))
		copy(out, value)
		return out
	default:
		return value
	}
}
