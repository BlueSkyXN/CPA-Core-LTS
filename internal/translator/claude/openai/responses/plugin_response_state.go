package responses

import (
	"github.com/MoeForge/moego/ojson"
	claudecommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/common"
)

type PluginResponseState = claudecommon.PluginResponseState

func pluginResponseError() error { return claudecommon.PluginResponseError() }
func validatePluginMessage(message ojson.Result) error {
	return claudecommon.ValidatePluginMessage(message, false)
}
