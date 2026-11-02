package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"
)

// ValidatePayloadExecutionScopes validates optional execution gates without changing
// rule order or weakening an invalid gate into an unrestricted rule.
func (cfg *Config) ValidatePayloadExecutionScopes() error {
	if cfg == nil {
		return nil
	}
	validate := func(section string, index int, models []PayloadModelRule) error {
		for j := range models {
			model := &models[j]
			location := fmt.Sprintf("requests.payload.%s[%d].models[%d]", section, index, j)
			switch model.AuthKind {
			case "", "apikey", "oauth":
			default:
				return fmt.Errorf("%s.auth-kind: use apikey or oauth, or omit the field", location)
			}
			if model.RequestPaths != nil && len(model.RequestPaths) == 0 {
				return fmt.Errorf("%s.request-paths: provide at least one exact path or omit the field", location)
			}
			for k, value := range model.RequestPaths {
				parsed, err := url.ParseRequestURI(value)
				if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") ||
					strings.ContainsAny(value, "*?#%\\") || strings.IndexFunc(value, unicode.IsSpace) >= 0 ||
					path.Clean(value) != value || parsed == nil || parsed.Path != value {
					return fmt.Errorf("%s.request-paths[%d]: use an exact absolute path without wildcards, query, fragment or escapes", location, k)
				}
			}
		}
		return nil
	}
	for _, section := range []struct {
		name  string
		rules []PayloadRule
	}{
		{"default", cfg.Payload.Default}, {"default-raw", cfg.Payload.DefaultRaw},
		{"override", cfg.Payload.Override}, {"override-raw", cfg.Payload.OverrideRaw},
	} {
		for i, rule := range section.rules {
			if err := validate(section.name, i, rule.Models); err != nil {
				return err
			}
		}
	}
	for i, rule := range cfg.Payload.Filter {
		if err := validate("filter", i, rule.Models); err != nil {
			return err
		}
	}
	return nil
}
