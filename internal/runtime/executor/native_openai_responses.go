package executor

import (
	"context"
	"github.com/MoeForge/moego/ojson"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"net/url"
	"strings"
)

// 公开 API 与订阅后端不能共用删字段规则。这里只识别已确认的原厂 HTTPS
// API key 出口；自定义反代地址和 OAuth 继续遵循原有兼容策略。
func nativeOpenAIResponses(auth *cliproxyauth.Auth, endpoint string) bool {
	if auth == nil || auth.AuthKind() != cliproxyauth.AuthKindAPIKey {
		return false
	}
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "api.openai.com") &&
		(u.Port() == "" || u.Port() == "443") && u.User == nil &&
		(strings.TrimSuffix(u.Path, "/") == "/v1" || strings.TrimSuffix(u.Path, "/") == "/v1/responses" || strings.TrimSuffix(u.Path, "/") == "/v1/responses/compact")
}

func codexRequestTarget(from sdktranslator.Format, native bool) sdktranslator.Format {
	// Responses/Codex 已有 Responses 形状；其他入口仍需现有 Codex 转换器
	// 生成 input，不能落入缺少跨协议转换器时的原样透传分支。
	if native && (from == sdktranslator.FormatOpenAIResponse || from == sdktranslator.FormatCodex) {
		return sdktranslator.FormatOpenAIResponse
	}
	return sdktranslator.FormatCodex
}

func observeNativeCacheIntent(ctx context.Context, body []byte) {
	result, object := "absent", ""
	if key := ojson.GetBytesNoCopy(body, "prompt_cache_key"); key.Exists() {
		result, object = "forwarded", diagnostics.Hash(key.Raw)
	}
	diagnostics.Record(ctx, diagnostics.Event{Layer: "cache_key", Method: "native_api", Result: result, ObjectHash: object})
}
