package config

import (
	"fmt"
	"strings"
)

// CodexCacheAffinityConfig controls ChatGPT OAuth outbound cache adaptation.
type CodexCacheAffinityConfig struct {
	Strategy string `yaml:"strategy" json:"strategy"`
}

func (c CodexCacheAffinityConfig) EffectiveStrategy() string {
	s := strings.ToLower(strings.TrimSpace(c.Strategy))
	if s == "" {
		return "client-aware"
	}
	return s
}

// Validate 校验配置；供文件加载和嵌入式 SDK 共用。
func (c CodexCacheAffinityConfig) Validate() error {
	switch c.EffectiveStrategy() {
	case "legacy", "stable-id", "client-aware":
		return nil
	default:
		return fmt.Errorf("codex.cache-affinity.strategy must be legacy, stable-id or client-aware")
	}
}
func (c *CodexCacheAffinityConfig) normalizeAndValidate() error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.Strategy = c.EffectiveStrategy()
	return nil
}
