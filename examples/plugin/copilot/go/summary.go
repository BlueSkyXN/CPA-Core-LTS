package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const copilotSummaryCacheTTL = time.Minute

type copilotSummaryCacheEntry struct {
	FetchedAt time.Time
	Summary   copilotSummary
}

type copilotSummary struct {
	Provider       string                  `json:"provider"`
	AuthIndex      string                  `json:"auth_index"`
	Name           string                  `json:"name,omitempty"`
	Label          string                  `json:"label,omitempty"`
	Credential     copilotCredentialStatus `json:"credential"`
	Account        copilotAccountSummary   `json:"account"`
	QuotaSnapshots json.RawMessage         `json:"quota_snapshots,omitempty"`
	UpdatedAt      time.Time               `json:"updated_at"`
	Cached         bool                    `json:"cached"`
}

type copilotCredentialStatus struct {
	AuthMode    string `json:"auth_mode"`
	Fingerprint string `json:"fingerprint"`
}

type copilotAccountSummary struct {
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	Login          string `json:"login,omitempty"`
	ID             string `json:"id,omitempty"`
	CopilotPlan    string `json:"copilot_plan,omitempty"`
	AccessTypeSKU  string `json:"access_type_sku,omitempty"`
	ChatEnabled    *bool  `json:"chat_enabled,omitempty"`
	AssignedDate   string `json:"assigned_date,omitempty"`
	QuotaResetDate string `json:"quota_reset_date,omitempty"`
}

func copilotSummaryCacheKey(auth copilotAuth, endpoint, authIndex string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{copilotTokenCacheKey(auth, endpoint), strings.TrimSpace(authIndex)}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func cloneCopilotSummary(input copilotSummary) copilotSummary {
	output := input
	output.QuotaSnapshots = append(json.RawMessage(nil), input.QuotaSnapshots...)
	return output
}

// copilotSummary 直接查询 copilot_internal/user：该端点接受长期 GitHub token，
// 不消耗短时 Copilot token。unlimited/percent_remaining 等配额字段原样透传，
// 不推导订阅档位。
func (r *pluginRuntime) copilotSummary(auth copilotAuth, callbackID string, cfg pluginConfig) copilotSummary {
	result := copilotSummary{
		Provider:   pluginIdentifier,
		Credential: copilotCredentialStatus{AuthMode: auth.AuthMode, Fingerprint: copilotCredentialFingerprint(auth)},
		Account:    copilotAccountSummary{Status: "upstream_error", Code: "host_callback_missing"},
		UpdatedAt:  time.Now().UTC(),
	}
	if strings.TrimSpace(callbackID) == "" || r.caller == nil {
		return result
	}
	headers := copilotIdentityHeaders(cfg)
	headers.Set("Authorization", "token "+auth.tokenSource())
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID, Method: http.MethodGet,
		URL: cfg.GitHubAPIEndpoint + "/copilot_internal/user", Headers: headers,
	})
	if errRequest != nil {
		result.Account = copilotAccountSummary{Status: "upstream_error", Code: "copilot_user_request_failed"}
		return result
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			result.Account = copilotAccountSummary{Status: "auth_rejected", Code: "copilot_user_auth_rejected"}
		case response.StatusCode == http.StatusTooManyRequests:
			result.Account = copilotAccountSummary{Status: "rate_limited", Code: "copilot_user_rate_limited"}
		default:
			result.Account = copilotAccountSummary{Status: "upstream_error", Code: "copilot_user_request_failed"}
		}
		return result
	}
	if len(response.Body) == 0 || len(response.Body) > maxCopilotAccountBody {
		result.Account = copilotAccountSummary{Status: "upstream_error", Code: "copilot_user_response_invalid"}
		return result
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(response.Body, &envelope) != nil || envelope == nil {
		result.Account = copilotAccountSummary{Status: "upstream_error", Code: "copilot_user_response_invalid"}
		return result
	}
	result.Account = copilotAccountFromEnvelope(envelope)
	// 配额快照按原始字节透传，保留 unlimited/percent_remaining 的精确语义。
	if raw, ok := envelope["quota_snapshots"]; ok && len(raw) > 0 && !bytesEqualJSONNull(raw) {
		result.QuotaSnapshots = append(json.RawMessage(nil), raw...)
	}
	return result
}

func copilotAccountFromEnvelope(envelope map[string]json.RawMessage) copilotAccountSummary {
	readString := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := envelope[key]; ok && len(raw) > 0 && !bytesEqualJSONNull(raw) {
				var value string
				if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
		return ""
	}
	account := copilotAccountSummary{Status: "available"}
	account.Login = readString("login")
	account.CopilotPlan = readString("copilot_plan")
	account.AccessTypeSKU = readString("access_type_sku")
	account.AssignedDate = readString("assigned_date")
	account.QuotaResetDate = readString("quota_reset_date")
	if raw, ok := envelope["id"]; ok && len(raw) > 0 && !bytesEqualJSONNull(raw) {
		var value json.Number
		if json.Unmarshal(raw, &value) == nil {
			account.ID = value.String()
		} else {
			account.ID = readString("id")
		}
	}
	if raw, ok := envelope["chat_enabled"]; ok && len(raw) > 0 && !bytesEqualJSONNull(raw) {
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			account.ChatEnabled = &value
		}
	}
	return account
}

func bytesEqualJSONNull(raw []byte) bool {
	return string(raw) == "null"
}

func stringValueFromMap(value map[string]any, keys ...string) string {
	for _, key := range keys {
		for candidate, raw := range value {
			if !strings.EqualFold(candidate, key) {
				continue
			}
			switch typed := raw.(type) {
			case string:
				return strings.TrimSpace(typed)
			case json.Number:
				return typed.String()
			case float64:
				return strconv.FormatFloat(typed, 'f', -1, 64)
			}
		}
	}
	return ""
}

func numberValueFromMap(value map[string]any, keys ...string) float64 {
	for _, key := range keys {
		for candidate, raw := range value {
			if !strings.EqualFold(candidate, key) {
				continue
			}
			switch typed := raw.(type) {
			case json.Number:
				parsed, _ := typed.Float64()
				return parsed
			case float64:
				return typed
			case string:
				parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
				return parsed
			}
		}
	}
	return 0
}

func mustMarshalJSON(value any) []byte {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return []byte("{}")
	}
	return raw
}
