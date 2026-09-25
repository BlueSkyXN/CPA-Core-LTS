package main

import (
	"net/http"
	"strings"
)

// Copilot 模型 ID 完全来自账号实时目录，执行侧再做一次精确匹配；这里只拒绝
// 明显的空值与控制字符，不做任何别名或大小写归一猜测。
func validateCanonicalModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return newPluginCallError("unsupported_model", "Copilot model ID is required", http.StatusBadRequest, false)
	}
	if len(model) > 512 || strings.ContainsAny(model, "\x00\r\n") {
		return newPluginCallError("unsupported_model", "Copilot model ID is invalid", http.StatusBadRequest, false)
	}
	return nil
}
