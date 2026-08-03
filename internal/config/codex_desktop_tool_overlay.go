package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexapptools"
)

func (c *CodexDesktopToolOverlayConfig) normalizeAndValidate() error {
	if c == nil {
		return nil
	}
	normalized, err := codexapptools.NormalizeSelection(c.Tools)
	if err != nil {
		return fmt.Errorf("codex.desktop-tool-overlay.tools: %w", err)
	}
	if c.Enabled && len(normalized) == 0 {
		return fmt.Errorf("codex.desktop-tool-overlay.tools must not be empty when enabled")
	}
	c.Tools = normalized
	return nil
}
