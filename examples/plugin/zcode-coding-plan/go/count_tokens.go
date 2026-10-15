package main

import (
	"net/url"
	"strings"
)

// countTokensEndpoint 把配置的 /v1/messages 端点改写到同目录的 count_tokens 路由。
// 2026-10-15 真实凭据探测确认 bigmodel 与 zai 上游都实现该端点（返回 {"input_tokens":N}），
// 鉴权与 messages 共用同一套签名头，因此这里复用执行路径的 signer 与身份头。
func countTokensEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" {
		return ""
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/count_tokens"
	parsed.RawQuery = "beta=true"
	return parsed.String()
}

// transformCountTokenRequest 只保留计数契约字段并补 metadata.user_id。
// Claude Code 的 count_tokens 请求带 model/messages/tools（无 max_tokens）；
// metadata 与 messages 同线格式，上游据此归 session 统计，签名头里的 session 必须同源。
func transformCountTokenRequest(req executorRequest, c *config, session string) (map[string]any, error) {
	var body map[string]any
	if err := decode(req.Payload, &body); err != nil || body == nil {
		return nil, problem(400, "invalid_request", "Invalid token count request")
	}
	if len(req.Payload) > maxBody {
		return nil, problem(413, "request_too_large", "Request exceeds size limit")
	}
	model, ok := body["model"].(string)
	allowed := false
	for _, m := range c.Models {
		if model == m {
			allowed = true
		}
	}
	if !ok || !allowed {
		return nil, problem(400, "model_not_found", "Model is not in the configured allowlist")
	}
	if _, exists := body["messages"]; !exists {
		return nil, problem(400, "invalid_request", "messages is required")
	}
	wire := struct {
		Device  string `json:"device_id"`
		Account string `json:"account_uuid"`
		Session string `json:"session_id"`
	}{c.DeviceID, "", session}
	metadata := map[string]any{}
	if v, exists := body["metadata"]; exists {
		var ok bool
		metadata, ok = v.(map[string]any)
		if !ok {
			return nil, problem(400, "invalid_request", "metadata must be an object")
		}
	}
	metadata["user_id"] = string(encode(wire))
	body["metadata"] = metadata
	delete(body, "max_tokens")
	delete(body, "stream")
	return body, nil
}
