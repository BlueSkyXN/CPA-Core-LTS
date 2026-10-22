package config

import (
	"fmt"
	"strings"
)

type CodexZcodeContextUsageMode string

const (
	ZcodeContextUsageDisabled CodexZcodeContextUsageMode = "disabled"
	ZcodeContextUsageObserve  CodexZcodeContextUsageMode = "observe"
	ZcodeContextUsageCorrect  CodexZcodeContextUsageMode = "correct"
)

func (m CodexZcodeContextUsageMode) Effective() CodexZcodeContextUsageMode {
	switch CodexZcodeContextUsageMode(strings.ToLower(strings.TrimSpace(string(m)))) {
	case ZcodeContextUsageObserve:
		return ZcodeContextUsageObserve
	case ZcodeContextUsageCorrect:
		return ZcodeContextUsageCorrect
	default:
		return ZcodeContextUsageDisabled
	}
}

func (m *CodexZcodeContextUsageMode) normalizeAndValidate() error {
	value := CodexZcodeContextUsageMode(strings.ToLower(strings.TrimSpace(string(*m))))
	switch value {
	case "", ZcodeContextUsageDisabled, ZcodeContextUsageObserve, ZcodeContextUsageCorrect:
		*m = value
		return nil
	default:
		return fmt.Errorf("invalid codex.zcode-context-usage: expected disabled, observe or correct")
	}
}
