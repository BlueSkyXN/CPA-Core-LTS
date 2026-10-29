package responses

import (
	claudecommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/common"
	"github.com/tidwall/gjson"
)

type PluginResponseState = claudecommon.PluginResponseState

func pluginResponseError() error { return claudecommon.PluginResponseError() }
func validatePluginMessage(message gjson.Result) error {
	return claudecommon.ValidatePluginMessage(message, false)
}
