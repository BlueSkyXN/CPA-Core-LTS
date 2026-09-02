package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	qoderSummaryCacheTTL = time.Minute
	qoderTokenCacheTTL   = 30 * time.Second
	maxQoderAccountBody  = 256 * 1024
)

type qoderTokenState struct {
	Source       string
	Token        string
	RefreshToken string
	ExpiresAt    time.Time
}

type qoderTokenCacheEntry struct {
	FetchedAt time.Time
	State     qoderTokenState
}

type qoderSummaryCacheEntry struct {
	FetchedAt time.Time
	Summary   qoderSummary
}

type qoderSummary struct {
	Provider   string                 `json:"provider"`
	AuthIndex  string                 `json:"auth_index"`
	Name       string                 `json:"name,omitempty"`
	Label      string                 `json:"label,omitempty"`
	Credential qoderCredentialSummary `json:"credential"`
	Account    qoderAccountSummary    `json:"account"`
	Plan       *qoderPlanSummary      `json:"plan,omitempty"`
	Quota      qoderQuotaSummary      `json:"quota"`
	UpdatedAt  time.Time              `json:"updated_at"`
	Cached     bool                   `json:"cached"`
}

type qoderCredentialSummary struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type qoderAccountSummary struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Source string `json:"source,omitempty"`
}

type qoderPlanSummary struct {
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	UserType   string `json:"user_type,omitempty"`
	Name       string `json:"name,omitempty"`
	PlanTier   string `json:"plan_tier,omitempty"`
	Paid       *bool  `json:"paid,omitempty"`
	Personal   *bool  `json:"personal,omitempty"`
	CycleStart string `json:"cycle_start,omitempty"`
	CycleEnd   string `json:"cycle_end,omitempty"`
}

type qoderQuotaSummary struct {
	Status         string              `json:"status"`
	Code           string              `json:"code,omitempty"`
	Unit           string              `json:"unit,omitempty"`
	Total          *float64            `json:"total,omitempty"`
	TotalExact     string              `json:"total_exact,omitempty"`
	Used           *float64            `json:"used,omitempty"`
	UsedExact      string              `json:"used_exact,omitempty"`
	Remaining      *float64            `json:"remaining,omitempty"`
	RemainingExact string              `json:"remaining_exact,omitempty"`
	Percentage     *float64            `json:"percentage,omitempty"`
	Exceeded       bool                `json:"exceeded,omitempty"`
	ExpiresAt      string              `json:"expires_at,omitempty"`
	Packages       []qoderQuotaPackage `json:"packages,omitempty"`
}

type qoderQuotaPackage struct {
	Name           string   `json:"name,omitempty"`
	Status         string   `json:"status,omitempty"`
	Available      *bool    `json:"available,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	Total          *float64 `json:"total,omitempty"`
	TotalExact     string   `json:"total_exact,omitempty"`
	Used           *float64 `json:"used,omitempty"`
	UsedExact      string   `json:"used_exact,omitempty"`
	Remaining      *float64 `json:"remaining,omitempty"`
	RemainingExact string   `json:"remaining_exact,omitempty"`
	Percentage     *float64 `json:"percentage,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
}

func (r *pluginRuntime) qoderSummary(auth qoderAuth, callbackID string) qoderSummary {
	cfg := r.loadedConfig()
	result := qoderSummary{
		Provider:   pluginIdentifier,
		Credential: qoderCredentialSummary{Kind: qoderCredentialKind(auth), Fingerprint: qoderCredentialFingerprint(auth)},
		Account:    qoderAccountSummary{Status: "fallback", Source: "auth_label"},
		Quota:      qoderQuotaSummary{Status: "not_configured", Code: "openapi_endpoint_missing"},
		UpdatedAt:  time.Now().UTC(),
	}
	if auth.AuthMode == "local_cli" {
		result.Account = qoderAccountSummary{Status: "unsupported", Code: "local_cli_account_unavailable", Source: "auth_label"}
		result.Plan = &qoderPlanSummary{Status: "unsupported", Code: "local_cli_plan_unavailable"}
		result.Quota = qoderQuotaSummary{Status: "unsupported", Code: "local_cli_quota_unavailable"}
		return result
	}
	if !auth.isPAT() {
		result.Account = qoderAccountSummary{Status: "unsupported", Code: "legacy_token_not_pat", Source: "auth_label"}
		result.Plan = &qoderPlanSummary{Status: "unsupported", Code: "legacy_token_not_pat"}
		result.Quota = qoderQuotaSummary{Status: "unsupported", Code: "legacy_token_not_pat"}
		return result
	}
	if strings.TrimSpace(cfg.OpenAPIEndpoint) == "" {
		result.Plan = &qoderPlanSummary{Status: "not_configured", Code: "openapi_endpoint_missing"}
		return result
	}
	if strings.TrimSpace(callbackID) == "" || r.caller == nil {
		result.Plan = &qoderPlanSummary{Status: "upstream_error", Code: "host_callback_missing"}
		result.Quota = qoderQuotaSummary{Status: "upstream_error", Code: "host_callback_missing"}
		return result
	}
	state, errToken := r.qoderToken(auth, callbackID)
	if errToken != nil {
		result.Account = qoderAccountSummary{Status: "auth_rejected", Code: "pat_exchange_failed"}
		result.Plan = &qoderPlanSummary{Status: "auth_rejected", Code: "pat_exchange_failed"}
		result.Quota = qoderQuotaSummary{Status: "auth_rejected", Code: "pat_exchange_failed"}
		return result
	}

	if status, raw := r.qoderJSONWithRefresh(auth, callbackID, &state, "/api/v1/userinfo"); status == http.StatusOK {
		result.Account = parseQoderAccount(raw)
	} else {
		result.Account = qoderComponentError(status, "userinfo")
	}
	if status, raw := r.qoderJSONWithRefresh(auth, callbackID, &state, "/api/v2/user/plan"); status == http.StatusOK {
		plan := parseQoderPlan(raw)
		result.Plan = &plan
	} else {
		plan := qoderPlanComponentError(status, "plan")
		result.Plan = &plan
	}
	if status, raw := r.qoderJSONWithRefresh(auth, callbackID, &state, "/api/v2/quota/usage"); status == http.StatusOK {
		result.Quota = parseQoderQuota(raw)
	} else {
		result.Quota = qoderQuotaComponentError(status, "quota")
	}
	return result
}

func (r *pluginRuntime) qoderToken(auth qoderAuth, callbackID string) (qoderTokenState, error) {
	cfg := r.loadedConfig()
	key := qoderTokenCacheKey(auth, cfg.OpenAPIEndpoint)
	r.mu.Lock()
	cached, ok := r.tokenCache[key]
	r.mu.Unlock()
	if ok && time.Since(cached.FetchedAt) < qoderTokenCacheTTL && cached.State.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		return cached.State, nil
	}
	if !auth.isPAT() {
		return qoderTokenState{}, fmt.Errorf("Qoder summary requires a pt- PAT")
	}
	source := auth.tokenSource()
	state, errExchange := r.qoderTokenRequest(source, "", callbackID, "/api/v1/jobToken/exchange", map[string]string{"personal_token": source})
	if errExchange != nil {
		return qoderTokenState{}, errExchange
	}
	r.mu.Lock()
	if r.tokenCache == nil {
		r.tokenCache = make(map[string]qoderTokenCacheEntry)
	}
	r.tokenCache[key] = qoderTokenCacheEntry{FetchedAt: time.Now(), State: state}
	r.mu.Unlock()
	return state, nil
}

func (r *pluginRuntime) qoderJSONWithRefresh(auth qoderAuth, callbackID string, state *qoderTokenState, path string) (int, []byte) {
	status, raw := r.qoderJSON(callbackID, state.Token, path)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return status, raw
	}
	refreshed, errRefresh := r.qoderRefreshOrExchange(auth, callbackID, *state)
	if errRefresh != nil {
		return status, raw
	}
	*state = refreshed
	cfg := r.loadedConfig()
	r.mu.Lock()
	if r.tokenCache == nil {
		r.tokenCache = make(map[string]qoderTokenCacheEntry)
	}
	r.tokenCache[qoderTokenCacheKey(auth, cfg.OpenAPIEndpoint)] = qoderTokenCacheEntry{FetchedAt: time.Now(), State: refreshed}
	r.mu.Unlock()
	status, raw = r.qoderJSON(callbackID, state.Token, path)
	return status, raw
}

func (r *pluginRuntime) qoderRefreshOrExchange(auth qoderAuth, callbackID string, state qoderTokenState) (qoderTokenState, error) {
	if state.RefreshToken != "" {
		if refreshed, errRefresh := r.qoderTokenRequest("", state.RefreshToken, callbackID, "/api/v1/jobToken/refresh", map[string]string{"refresh_token": state.RefreshToken}); errRefresh == nil {
			refreshed.Source = auth.tokenSource()
			return refreshed, nil
		}
	}
	source := auth.tokenSource()
	return r.qoderTokenRequest(source, "", callbackID, "/api/v1/jobToken/exchange", map[string]string{"personal_token": source})
}

func (r *pluginRuntime) qoderTokenRequest(source, refreshToken, callbackID, path string, body map[string]string) (qoderTokenState, error) {
	cfg := r.loadedConfig()
	if strings.TrimSpace(cfg.OpenAPIEndpoint) == "" {
		return qoderTokenState{}, fmt.Errorf("openapi endpoint is not configured")
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", cfg.OpenAPIUserAgent)
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodPost,
		URL:            cfg.OpenAPIEndpoint + path,
		Headers:        headers,
		Body:           mustMarshalJSON(body),
	})
	if errRequest != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return qoderTokenState{}, fmt.Errorf("Qoder token endpoint failed")
	}
	if len(response.Body) == 0 || len(response.Body) > maxQoderAccountBody {
		return qoderTokenState{}, fmt.Errorf("Qoder token response is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.UseNumber()
	var value map[string]any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return qoderTokenState{}, fmt.Errorf("Qoder token response is not JSON")
	}
	token := stringValueFromMap(value, "token", "access_token", "device_token")
	if token == "" {
		return qoderTokenState{}, fmt.Errorf("Qoder token response has no token")
	}
	expiresAt := parseQoderTime(value, "expires_at", "expiresAt")
	if expiresAt.IsZero() {
		if seconds := numberValueFromMap(value, "expires_in"); seconds > 0 {
			expiresAt = time.Now().Add(time.Duration(seconds) * time.Second)
		}
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(5 * time.Minute)
	}
	return qoderTokenState{
		Source:       source,
		Token:        token,
		RefreshToken: stringValueFromMap(value, "refresh_token"),
		ExpiresAt:    expiresAt,
	}, nil
}

func (r *pluginRuntime) qoderJSON(callbackID, token, path string) (int, []byte) {
	cfg := r.loadedConfig()
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("User-Agent", cfg.OpenAPIUserAgent)
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodGet,
		URL:            cfg.OpenAPIEndpoint + path,
		Headers:        headers,
	})
	if errRequest != nil {
		return http.StatusBadGateway, nil
	}
	return response.StatusCode, response.Body
}

func parseQoderAccount(raw []byte) qoderAccountSummary {
	value, ok := decodeJSONMap(raw)
	if !ok {
		return qoderAccountSummary{Status: "upstream_error", Code: "userinfo_response_invalid"}
	}
	result := qoderAccountSummary{
		ID:     stringValueFromMap(value, "id", "user_id", "uid"),
		Name:   stringValueFromMap(value, "name", "username", "user_name"),
		Email:  stringValueFromMap(value, "email"),
		Source: "userinfo",
	}
	if result.ID == "" && result.Name == "" && result.Email == "" {
		return qoderAccountSummary{Status: "unsupported", Code: "userinfo_fields_missing"}
	}
	result.Status = "available"
	return result
}

func parseQoderPlan(raw []byte) qoderPlanSummary {
	value, ok := decodeJSONMap(raw)
	if !ok {
		return qoderPlanSummary{Status: "upstream_error", Code: "plan_response_invalid"}
	}
	result := qoderPlanSummary{
		Status:     "available",
		UserType:   stringValueFromMap(value, "user_type", "userType"),
		Name:       stringValueFromMap(value, "plan_tier_name", "planTierName", "plan_name", "planName"),
		PlanTier:   stringValueFromMap(value, "plan_tier", "planTier"),
		CycleStart: formatQoderEpoch(value, "start_date", "startDate"),
		CycleEnd:   formatQoderEpoch(value, "end_date", "endDate"),
	}
	result.Paid = boolPointerFromMap(value, "is_paid_plan", "isPaidPlan")
	result.Personal = boolPointerFromMap(value, "is_personal_version", "isPersonalVersion")
	if result.UserType == "" || result.Name == "" {
		result.Status = "partial"
		result.Code = "plan_fields_missing"
	}
	return result
}

func parseQoderQuota(raw []byte) qoderQuotaSummary {
	value, ok := decodeJSONMap(raw)
	if !ok {
		return qoderQuotaSummary{Status: "upstream_error", Code: "quota_response_invalid"}
	}
	quotaFieldsPresent := mapHasKeyFromMap(value, "user_quota", "userQuota") ||
		mapHasKeyFromMap(value, "add_on_quota", "addOnQuota") ||
		mapHasKeyFromMap(value, "dedicated_resource_packages", "dedicatedResourcePackages")
	if !quotaFieldsPresent {
		return qoderQuotaSummary{Status: "upstream_error", Code: "quota_fields_missing"}
	}
	result := qoderQuotaSummary{
		Status:     "available",
		Unit:       "credits",
		Percentage: numberPointerFromMap(value, "total_usage_percentage", "totalUsagePercentage"),
		Exceeded:   boolValueFromMap(value, "is_quota_exceeded", "isQuotaExceeded"),
		ExpiresAt:  formatQoderEpoch(value, "expires_at", "expiresAt"),
	}
	for _, candidate := range []struct {
		name string
		key  string
	}{{"user_quota", "user_quota"}, {"add_on_quota", "add_on_quota"}} {
		if item := mapValueFromMap(value, candidate.key, camelCaseKey(candidate.key)); item != nil {
			result.Packages = append(result.Packages, parseQoderQuotaPackage(candidate.name, item))
		}
	}
	for _, rawItem := range arrayValueFromMap(value, "dedicated_resource_packages", "dedicatedResourcePackages") {
		if item, okItem := rawItem.(map[string]any); okItem {
			name := stringValueFromMap(item, "name", "id")
			if name == "" {
				name = "dedicated_resource_package"
			}
			result.Packages = append(result.Packages, parseQoderQuotaPackage(name, item))
		}
	}
	if len(result.Packages) == 0 {
		result.Total = floatPointer(0)
		result.TotalExact = "0"
		result.Used = floatPointer(0)
		result.UsedExact = "0"
		result.Remaining = floatPointer(0)
		result.RemainingExact = "0"
		return result
	}
	return aggregateQoderQuota(result)
}

func parseQoderQuotaPackage(name string, value map[string]any) qoderQuotaPackage {
	total := exactValueFromMap(value, "total")
	used := exactValueFromMap(value, "used")
	remaining := exactValueFromMap(value, "remaining")
	if remaining == "" && total != "" && used != "" {
		remaining, _ = subtractQoderDecimals(total, used)
	}
	return qoderQuotaPackage{
		Name:           name,
		Status:         stringValueFromMap(value, "status"),
		Available:      boolPointerFromMap(value, "available"),
		Unit:           firstNonEmpty(stringValueFromMap(value, "unit"), "credits"),
		Total:          approximateQoderPointer(total),
		TotalExact:     total,
		Used:           approximateQoderPointer(used),
		UsedExact:      used,
		Remaining:      approximateQoderPointer(remaining),
		RemainingExact: remaining,
		Percentage:     numberPointerFromMap(value, "percentage"),
		ExpiresAt:      formatQoderEpoch(value, "expires_at", "expiresAt"),
	}
}

func aggregateQoderQuota(result qoderQuotaSummary) qoderQuotaSummary {
	unit := firstNonEmpty(result.Packages[0].Unit, "credits")
	totals, used, remaining := make([]string, 0), make([]string, 0), make([]string, 0)
	for _, item := range result.Packages {
		if !qoderQuotaPackageContributes(item) {
			continue
		}
		if !strings.EqualFold(firstNonEmpty(item.Unit, "credits"), unit) || item.TotalExact == "" || item.UsedExact == "" || item.RemainingExact == "" {
			result.Status = "partial"
			result.Code = "invalid_package_amount"
			result.Unit = unit
			return result
		}
		totals = append(totals, item.TotalExact)
		used = append(used, item.UsedExact)
		remaining = append(remaining, item.RemainingExact)
	}
	if len(totals) == 0 {
		result.Unit = unit
		result.Total = floatPointer(0)
		result.TotalExact = "0"
		result.Used = floatPointer(0)
		result.UsedExact = "0"
		result.Remaining = floatPointer(0)
		result.RemainingExact = "0"
		return result
	}
	var ok bool
	result.Unit = unit
	if result.TotalExact, ok = sumQoderDecimals(totals); !ok {
		result.Status = "partial"
		result.Code = "invalid_total"
		return result
	}
	if result.UsedExact, ok = sumQoderDecimals(used); !ok {
		result.Status = "partial"
		result.Code = "invalid_used"
		return result
	}
	if result.RemainingExact, ok = sumQoderDecimals(remaining); !ok {
		result.Status = "partial"
		result.Code = "invalid_remaining"
		return result
	}
	result.Total = approximateQoderPointer(result.TotalExact)
	result.Used = approximateQoderPointer(result.UsedExact)
	result.Remaining = approximateQoderPointer(result.RemainingExact)
	return result
}

func qoderQuotaPackageContributes(item qoderQuotaPackage) bool {
	if item.Available != nil {
		return *item.Available
	}
	status := strings.ToLower(strings.TrimSpace(item.Status))
	return !strings.Contains(status, "exhausted") && !strings.Contains(status, "expired") && !strings.Contains(status, "inactive")
}

func decodeJSONMap(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	result, ok := value.(map[string]any)
	return result, ok
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

func numberPointerFromMap(value map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		for candidate, raw := range value {
			if !strings.EqualFold(candidate, key) {
				continue
			}
			switch typed := raw.(type) {
			case json.Number:
				parsed, errParse := typed.Float64()
				if errParse == nil {
					return &parsed
				}
			case float64:
				return &typed
			case string:
				parsed, errParse := strconv.ParseFloat(strings.TrimSpace(typed), 64)
				if errParse == nil {
					return &parsed
				}
			}
		}
	}
	return nil
}

func boolValueFromMap(value map[string]any, keys ...string) bool {
	for _, key := range keys {
		for candidate, raw := range value {
			if strings.EqualFold(candidate, key) {
				if parsed, ok := raw.(bool); ok {
					return parsed
				}
			}
		}
	}
	return false
}

func boolPointerFromMap(value map[string]any, keys ...string) *bool {
	for _, key := range keys {
		for candidate, raw := range value {
			if strings.EqualFold(candidate, key) {
				if parsed, ok := raw.(bool); ok {
					return &parsed
				}
			}
		}
	}
	return nil
}

func mapValueFromMap(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		for candidate, raw := range value {
			if strings.EqualFold(candidate, key) {
				if parsed, ok := raw.(map[string]any); ok {
					return parsed
				}
			}
		}
	}
	return nil
}

func mapHasKeyFromMap(value map[string]any, keys ...string) bool {
	for _, key := range keys {
		for candidate := range value {
			if strings.EqualFold(candidate, key) {
				return true
			}
		}
	}
	return false
}

func arrayValueFromMap(value map[string]any, keys ...string) []any {
	for _, key := range keys {
		for candidate, raw := range value {
			if strings.EqualFold(candidate, key) {
				if parsed, ok := raw.([]any); ok {
					return parsed
				}
			}
		}
	}
	return nil
}

func parseQoderTime(value map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		for candidate, raw := range value {
			if !strings.EqualFold(candidate, key) {
				continue
			}
			switch typed := raw.(type) {
			case string:
				if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(typed)); errParse == nil {
					return parsed
				}
			case json.Number:
				seconds, errParse := typed.Float64()
				if errParse == nil {
					return qoderUnixTime(seconds)
				}
			case float64:
				return qoderUnixTime(typed)
			}
		}
	}
	return time.Time{}
}

func qoderUnixTime(value float64) time.Time {
	if value > 100_000_000_000 {
		value /= 1000
	}
	if value <= 0 || value > 10_000_000_000 {
		return time.Time{}
	}
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * 1e9)
	return time.Unix(seconds, nanos).UTC()
}

func formatQoderEpoch(value map[string]any, keys ...string) string {
	parsed := parseQoderTime(value, keys...)
	if parsed.IsZero() {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func exactValueFromMap(value map[string]any, keys ...string) string {
	raw := stringValueFromMap(value, keys...)
	if raw == "" {
		return ""
	}
	if _, ok := new(big.Rat).SetString(raw); !ok {
		return ""
	}
	return normalizeQoderDecimal(raw)
}

func normalizeQoderDecimal(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, "eE") {
		rat, ok := new(big.Rat).SetString(value)
		if !ok {
			return ""
		}
		return normalizeQoderDecimal(ratToQoderDecimal(rat, 12))
	}
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	parts := strings.SplitN(value, ".", 2)
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if fraction == "" {
		if negative && integer != "0" {
			return "-" + integer
		}
		return integer
	}
	result := integer + "." + fraction
	if negative && result != "0" {
		result = "-" + result
	}
	return result
}

func sumQoderDecimals(values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	total := new(big.Rat)
	for _, value := range values {
		rat, ok := new(big.Rat).SetString(value)
		if !ok {
			return "", false
		}
		total.Add(total, rat)
	}
	return ratToQoderDecimal(total, qoderDecimalScale(values)), true
}

func subtractQoderDecimals(left, right string) (string, bool) {
	a, okA := new(big.Rat).SetString(left)
	b, okB := new(big.Rat).SetString(right)
	if !okA || !okB {
		return "", false
	}
	a.Sub(a, b)
	return ratToQoderDecimal(a, qoderDecimalScale([]string{left, right})), true
}

func qoderDecimalScale(values []string) int {
	max := 0
	for _, value := range values {
		if index := strings.IndexByte(value, '.'); index >= 0 && len(value)-index-1 > max {
			max = len(value) - index - 1
		}
	}
	return max
}

func ratToQoderDecimal(value *big.Rat, scale int) string {
	if scale < 0 {
		scale = 0
	}
	return normalizeQoderDecimal(value.FloatString(scale))
}

func approximateQoderPointer(value string) *float64 {
	if value == "" {
		return nil
	}
	parsed, errParse := strconv.ParseFloat(value, 64)
	if errParse != nil {
		return nil
	}
	return &parsed
}

func floatPointer(value float64) *float64 {
	return &value
}

func qoderCredentialFingerprint(auth qoderAuth) string {
	source := auth.tokenSource()
	if source == "" {
		source = strings.Join([]string{"local_cli", auth.ProfileID, auth.ConfigDir}, "\x00")
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:8])
}

func qoderCredentialKind(auth qoderAuth) string {
	if auth.AuthMode == "local_cli" {
		return "local_cli"
	}
	return "pat"
}

func qoderTokenCacheKey(auth qoderAuth, endpoint string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{qoderCredentialFingerprint(auth), endpoint}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func qoderComponentError(status int, component string) qoderAccountSummary {
	return qoderAccountSummary{Status: qoderStatusForHTTP(status), Code: component + "_request_failed"}
}

func qoderPlanComponentError(status int, component string) qoderPlanSummary {
	return qoderPlanSummary{Status: qoderStatusForHTTP(status), Code: component + "_request_failed"}
}

func qoderQuotaComponentError(status int, component string) qoderQuotaSummary {
	return qoderQuotaSummary{Status: qoderStatusForHTTP(status), Code: component + "_request_failed"}
}

func qoderStatusForHTTP(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth_rejected"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusNotFound, http.StatusNotImplemented:
		return "unsupported"
	default:
		return "upstream_error"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func camelCaseKey(value string) string {
	parts := strings.Split(value, "_")
	if len(parts) == 0 {
		return value
	}
	for index := 1; index < len(parts); index++ {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, "")
}

func mustMarshalJSON(value any) []byte {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return []byte("{}")
	}
	return raw
}
