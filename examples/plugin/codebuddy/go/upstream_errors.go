package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

const maxCodeBuddyErrorBytes = 64 * 1024

// 错误分类参考 WorkBuddy 5.8.2 的错误枚举；只返回固定描述和数值码。
func codeBuddyUpstreamError(status, businessCode int) error {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	// HTTP 鉴权拒绝优先，避免错误体中的其他码掩盖失效凭据。
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return newPluginCallError("upstream_auth_rejected", fmt.Sprintf("CodeBuddy upstream rejected the credential (HTTP %d)", status), status, false)
	}
	code := "upstream_error"
	message := fmt.Sprintf("CodeBuddy upstream returned HTTP %d", status)
	switch {
	case businessCode >= 6000 && businessCode <= 6008 || businessCode == 14003:
		code, status, message = "upstream_rate_limited", http.StatusTooManyRequests, "CodeBuddy rate limit exceeded"
	case businessCode == 14001 || businessCode == 14012 || businessCode == 14013 || businessCode == 14014 || businessCode == 14018 || businessCode == 14019:
		code, status, message = "upstream_credits_exhausted", http.StatusPaymentRequired, "CodeBuddy credits or token budget exhausted"
	case businessCode == 14015:
		code, status, message = "upstream_license_expired", http.StatusForbidden, "CodeBuddy license expired"
	case businessCode == 14016 || businessCode == 14017:
		code, status, message = "upstream_account_not_activated", http.StatusForbidden, "CodeBuddy account is not activated"
	case businessCode == 11115:
		code, status, message = "upstream_context_too_long", http.StatusBadRequest, "CodeBuddy context exceeds the model limit"
	}
	if businessCode != 0 {
		message = fmt.Sprintf("%s (code %d)", message, businessCode)
	}
	return newPluginCallError(code, message, status, status == http.StatusTooManyRequests || status >= 500)
}

func codeBuddyBusinessCode(value any) int {
	var raw string
	switch value := value.(type) {
	case json.Number:
		raw = value.String()
	case float64:
		raw = strconv.FormatFloat(value, 'f', -1, 64)
	case string:
		raw = value
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

func codeBuddyFrameError(frame map[string]any, errorEvent bool) error {
	if code := codeBuddyBusinessCode(frame["code"]); code != 0 {
		return codeBuddyUpstreamError(0, code)
	}
	if value := frame["error"]; value != nil {
		status, code := 0, 0
		if detail, ok := value.(map[string]any); ok {
			code = codeBuddyBusinessCode(detail["code"])
			status = codeBuddyBusinessCode(detail["status"])
			if status == 0 {
				status = codeBuddyBusinessCode(detail["status_code"])
			}
		}
		return codeBuddyUpstreamError(status, code)
	}
	if errorEvent {
		return codeBuddyUpstreamError(0, 0)
	}
	if choices, ok := frame["choices"].([]any); ok {
		for _, value := range choices {
			if choice, ok := value.(map[string]any); ok && choice["finish_reason"] == "error" {
				return newPluginCallError("upstream_model_error", "CodeBuddy model ended with finish_reason error", http.StatusBadGateway, true)
			}
		}
	}
	return nil
}

func (r *pluginRuntime) codeBuddyHTTPError(execution *activeExecution, status int) error {
	// 鉴权状态已足够分类，直接交回 Core，避免等待停滞的错误正文。
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return codeBuddyUpstreamError(status, 0)
	}
	var body []byte
	for reads := 0; reads < 64 && len(body) < maxCodeBuddyErrorBytes; reads++ {
		if execution.canceled() {
			return newPluginCallError(codeBuddyConnectionLifecycleErrorCode, "CodeBuddy request canceled", 0, true)
		}
		chunk, err := readHostHTTPStream(r.caller, execution.upstreamID())
		if err != nil {
			break
		}
		remaining := maxCodeBuddyErrorBytes - len(body)
		if len(chunk.Payload) > remaining {
			// 截断的错误体不能用来重新分类 HTTP 状态。
			return codeBuddyUpstreamError(status, 0)
		}
		body = append(body, chunk.Payload...)
		if chunk.Error != "" {
			break
		}
		if chunk.Done {
			var frame map[string]any
			if json.Valid(body) {
				decoder := json.NewDecoder(bytes.NewReader(body))
				decoder.UseNumber()
				if decoder.Decode(&frame) == nil {
					code := codeBuddyBusinessCode(frame["code"])
					if detail, ok := frame["error"].(map[string]any); ok && code == 0 {
						code = codeBuddyBusinessCode(detail["code"])
					}
					return codeBuddyUpstreamError(status, code)
				}
			}
			break
		}
	}
	return codeBuddyUpstreamError(status, 0)
}
