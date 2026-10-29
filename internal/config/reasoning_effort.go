package config

import (
	"fmt"
	"strings"
)

// ReasoningEffortRule matches client-requested model names. The last matching
// rule wins. Omitted policies preserve existing behavior; matched follow rules
// protect client effort from generic payload rewrites.
type ReasoningEffortRule struct {
	Models []string `yaml:"models" json:"models"`
	Mode   string   `yaml:"mode" json:"mode"`
	Effort string   `yaml:"effort,omitempty" json:"effort,omitempty"`
}

// ValidateReasoningEffort rejects ambiguous rules before a config is saved or
// activated. Model-specific support is checked against the selected route.
func (cfg *Config) ValidateReasoningEffort() error {
	if cfg == nil {
		return nil
	}
	for i, rule := range cfg.ReasoningEffort {
		prefix := fmt.Sprintf("requests.reasoning-effort[%d]", i)
		if len(rule.Models) == 0 {
			return fmt.Errorf("%s.models: specify at least one model name or wildcard", prefix)
		}
		for _, model := range rule.Models {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("%s.models: model names cannot be empty", prefix)
			}
		}
		switch rule.Mode {
		case "follow":
			if rule.Effort != "" {
				return fmt.Errorf("%s.effort: remove the effort value when mode is follow", prefix)
			}
		case "default", "force":
			switch rule.Effort {
			case "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
			default:
				return fmt.Errorf("%s.effort: choose minimal, low, medium, high, xhigh, max or ultra", prefix)
			}
		default:
			return fmt.Errorf("%s.mode: choose follow, default or force", prefix)
		}
	}
	return nil
}
