package usage

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// CacheObservation keeps field presence separate from the legacy scalar totals.
// nil means that an older producer did not record this evidence.
type CacheObservation struct {
	Version uint32                `json:"version"`
	Read    CacheTokenObservation `json:"read"`
	Write   CacheTokenObservation `json:"write"`
}

type CacheTokenObservation struct {
	// State is present, absent, or invalid. An absent CacheObservation is unknown.
	State string `json:"state"`
	Value *int64 `json:"value,omitempty"`
	// Source identifies the original upstream field, never a credential or body.
	Source string `json:"source,omitempty"`
}

func (c *CacheObservation) Clone() *CacheObservation {
	if c == nil {
		return nil
	}
	out := *c
	out.Read.Value = cloneInt64Ptr(c.Read.Value)
	out.Write.Value = cloneInt64Ptr(c.Write.Value)
	return &out
}

// Valid rejects contradictory imported evidence without changing legacy totals.
func (c *CacheObservation) Valid() bool {
	if c == nil {
		return true
	}
	if c.Version != 1 {
		return false
	}
	for _, field := range []CacheTokenObservation{c.Read, c.Write} {
		switch field.State {
		case "present":
			if field.Value == nil || *field.Value < 0 {
				return false
			}
		case "absent", "invalid":
			if field.Value != nil {
				return false
			}
		default:
			return false
		}
		if len(field.Source) > 96 {
			return false
		}
	}
	return true
}

// ObserveCacheTokens classifies original wire fields before compatibility defaults are applied.
func ObserveCacheTokens(node ojson.Result, read, write []string) *CacheObservation {
	if !node.IsObject() {
		return nil
	}
	field := func(paths []string) CacheTokenObservation {
		for _, path := range paths {
			value := node.Get(path)
			if !value.Exists() {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimSpace(value.Raw), 10, 64)
			if value.Type == ojson.Number && err == nil && n >= 0 {
				return CacheTokenObservation{State: "present", Value: &n, Source: path}
			}
			return CacheTokenObservation{State: "invalid", Source: path}
		}
		return CacheTokenObservation{State: "absent"}
	}
	return &CacheObservation{Version: 1, Read: field(read), Write: field(write)}
}

func ObserveOpenAICache(node ojson.Result) *CacheObservation {
	return ObserveCacheTokens(node,
		[]string{"prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens"},
		[]string{"input_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens"},
	)
}

func ObserveClaudeCache(node ojson.Result) *CacheObservation {
	return ObserveCacheTokens(node, []string{"cache_read_input_tokens"}, []string{"cache_creation_input_tokens"})
}

func ObserveGeminiCache(node ojson.Result) *CacheObservation {
	return ObserveCacheTokens(node, []string{"cachedContentTokenCount", "cached_content_token_count"}, nil)
}

func ObserveInteractionsCache(node ojson.Result) *CacheObservation {
	return ObserveCacheTokens(node,
		[]string{"cache_read_tokens", "cacheReadTokens", "cached_tokens", "cachedContentTokenCount", "total_cached_tokens"},
		[]string{"cache_creation_tokens", "cacheCreationTokens", "cache_write_tokens", "cacheWriteTokens"},
	)
}

// MergeCacheObservations retains fields across incremental usage frames.
// Explicit zero or malformed new evidence supersedes the previous field.
func MergeCacheObservations(previous, update *CacheObservation) *CacheObservation {
	if previous == nil {
		return update
	}
	if update == nil {
		return previous
	}
	next := *update
	if update.Read.State == "absent" {
		next.Read = previous.Read
	}
	if update.Write.State == "absent" {
		next.Write = previous.Write
	}
	return &next
}
