package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// AutoModelConfig controls global selection for the public model name auto.
type AutoModelConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Candidates []string `yaml:"candidates,omitempty" json:"candidates,omitempty"`
}

// Validate individual YAML nodes so an invalid explicit list cannot decode into an empty global search.
func (a *AutoModelConfig) UnmarshalYAML(value *yaml.Node) error {
	var wire struct {
		Enabled    bool      `yaml:"enabled"`
		Candidates yaml.Node `yaml:"candidates"`
	}
	if err := value.Decode(&wire); err != nil {
		return err
	}
	next := AutoModelConfig{Enabled: wire.Enabled}
	node := &wire.Candidates
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node != nil && node.Kind != 0 && node.ShortTag() != "!!null" {
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("routing.auto-model.candidates requires a list of public model names")
		}
		for i, item := range node.Content {
			if item.Kind == yaml.AliasNode {
				item = item.Alias
			}
			if item == nil || item.Kind != yaml.ScalarNode || item.ShortTag() != "!!str" || strings.TrimSpace(item.Value) == "" {
				return fmt.Errorf("routing.auto-model.candidates[%d] requires a public model name", i)
			}
			next.Candidates = append(next.Candidates, strings.TrimSpace(item.Value))
		}
	}
	*a = next
	return nil
}

type namedModel interface {
	GetName() string
	GetAlias() string
}

func explicitAutoAlias(model namedModel) bool {
	name := strings.TrimSpace(model.GetName())
	return name != "" && !strings.EqualFold(name, "auto") && strings.EqualFold(strings.TrimSpace(model.GetAlias()), "auto")
}

// HasAutoAlias reports whether an administrator defined auto for a different upstream ID.
func HasAutoAlias[T namedModel](models []T) bool {
	for _, model := range models {
		if explicitAutoAlias(model) {
			return true
		}
	}
	return false
}

// ConfiguredModelRoutes resolves a collision within one credential without changing its stored configuration.
// Explicit auto mappings form the pool. A native auto may still be published under a different alias.
func ConfiguredModelRoutes[T namedModel](models []T) []T {
	hasNative := false
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.GetName()), "auto") {
			hasNative = true
			break
		}
	}
	if !hasNative || !HasAutoAlias(models) {
		return models
	}
	out := make([]T, 0, len(models))
	for _, model := range models {
		if explicitAutoAlias(model) {
			out = append(out, model)
		}
	}
	for _, model := range models {
		alias := strings.TrimSpace(model.GetAlias())
		if explicitAutoAlias(model) || strings.EqualFold(strings.TrimSpace(model.GetName()), "auto") && (alias == "" || strings.EqualFold(alias, "auto")) {
			continue
		}
		out = append(out, model)
	}
	return out
}
