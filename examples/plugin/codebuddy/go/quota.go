package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type codeBuddyQuota struct {
	Status         string                  `json:"status"`
	Scope          string                  `json:"scope,omitempty"`
	Source         string                  `json:"source,omitempty"`
	Unit           string                  `json:"unit,omitempty"`
	Total          *float64                `json:"total,omitempty"`
	TotalExact     string                  `json:"total_exact,omitempty"`
	Used           *float64                `json:"used,omitempty"`
	UsedExact      string                  `json:"used_exact,omitempty"`
	Remaining      *float64                `json:"remaining,omitempty"`
	RemainingExact string                  `json:"remaining_exact,omitempty"`
	Packages       []codeBuddyQuotaPackage `json:"packages,omitempty"`
	Code           string                  `json:"code,omitempty"`
}

type codeBuddyQuotaPackage struct {
	Basis          string   `json:"basis,omitempty"`
	Name           string   `json:"name,omitempty"`
	Status         string   `json:"status,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	Total          *float64 `json:"total,omitempty"`
	TotalExact     string   `json:"total_exact,omitempty"`
	Used           *float64 `json:"used,omitempty"`
	UsedExact      string   `json:"used_exact,omitempty"`
	Remaining      *float64 `json:"remaining,omitempty"`
	RemainingExact string   `json:"remaining_exact,omitempty"`
	CycleStart     string   `json:"cycle_start,omitempty"`
	CycleEnd       string   `json:"cycle_end,omitempty"`
}

func (r *pluginRuntime) codeBuddyQuotaSummary(auth codeBuddyAuth, callbackID string) (result codeBuddyQuota) {
	// 额度视图按 2026-11-14 的 PAT 实测组合：企业成员额度（get-enterprise-user-usage）
	// 是有企业身份凭据的主额度；个人资源包（get-user-resource）单独无资源包时恒空，
	// 不是数值零。两个来源独立探测，企业额度优先展示，个人资源包有则并列。
	enterprise := r.fetchEnterpriseUsage(auth, callbackID)
	personal := r.fetchPersonalResources(auth, callbackID)
	if enterprise.Status == "available" {
		enterprise.Packages = append(personal.Packages, enterprise.Packages...)
		if personal.Status == "available" && personal.TotalExact != "" {
			// 个人资源包与企业周期额度单位一致（credits）时合并总量；否则只并列展示。
			if mergedTotal, ok := sumExactDecimals([]string{enterprise.TotalExact, personal.TotalExact}); ok {
				if mergedUsed, okUsed := sumExactDecimals([]string{enterprise.UsedExact, personal.UsedExact}); okUsed {
					if mergedRemaining, okRemaining := sumExactDecimals([]string{enterprise.RemainingExact, personal.RemainingExact}); okRemaining {
						enterprise.TotalExact, enterprise.UsedExact, enterprise.RemainingExact = mergedTotal, mergedUsed, mergedRemaining
						enterprise.Total = approximatePointer(mergedTotal)
						enterprise.Used = approximatePointer(mergedUsed)
						enterprise.Remaining = approximatePointer(mergedRemaining)
					}
				}
			}
		}
		return enterprise
	}
	if personal.Status == "available" {
		return personal
	}
	// 两个来源都不可用时报告主导原因：鉴权拒绝 > 无企业身份 > 无个人资源包。
	if enterprise.Status == "auth_rejected" || personal.Status == "auth_rejected" {
		return codeBuddyQuota{Status: "auth_rejected", Code: "billing_auth_rejected"}
	}
	if enterprise.Code == "no_enterprise_usage" {
		if personal.Status == "not_configured" {
			return codeBuddyQuota{Status: "not_configured", Code: "billing_endpoint_missing"}
		}
		return personal
	}
	if enterprise.Status == "upstream_error" || enterprise.Status == "rate_limited" {
		return enterprise
	}
	return personal
}

// fetchEnterpriseUsage 查询企业成员周期额度。凭据无企业身份或个人 key 时
// 厂商返回错误/空数据，此时降级为 no_enterprise_usage，不冒充数值结果。
func (r *pluginRuntime) fetchEnterpriseUsage(auth codeBuddyAuth, callbackID string) codeBuddyQuota {
	cfg := r.loadedConfig()
	if strings.TrimSpace(cfg.EnterpriseUsageEndpoint) == "" {
		return codeBuddyQuota{Status: "not_configured", Code: "enterprise_usage_endpoint_missing"}
	}
	if strings.TrimSpace(callbackID) == "" {
		return codeBuddyQuota{Status: "upstream_error", Code: "host_callback_missing"}
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("X-API-Key", auth.APIKey)
	headers.Set("X-Product", "SaaS")
	headers.Set("User-Agent", cfg.CatalogUserAgent)
	response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodPost,
		URL:            cfg.EnterpriseUsageEndpoint,
		Headers:        headers,
		Body:           []byte("{}"),
	})
	if errRequest != nil {
		return codeBuddyQuota{Status: "upstream_error", Code: "enterprise_usage_request_failed"}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return codeBuddyQuota{Status: "auth_rejected", Code: "billing_auth_rejected"}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return codeBuddyQuota{Status: "rate_limited", Code: "billing_rate_limited"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return codeBuddyQuota{Status: "upstream_error", Code: "enterprise_usage_http_error"}
	}
	if len(response.Body) == 0 || len(response.Body) > 64*1024 {
		return codeBuddyQuota{Status: "upstream_error", Code: "enterprise_usage_response_invalid"}
	}
	data, errParse := decodeCodeBuddyEnterpriseUsage(response.Body)
	if errParse != nil {
		return codeBuddyQuota{Status: "partial", Code: "enterprise_usage_response_invalid"}
	}
	credit := exactValue(data, "credit", "Credit")
	limit := exactValue(data, "limitNum", "limit_num")
	if credit == "" || limit == "" {
		return codeBuddyQuota{Status: "partial", Code: "no_enterprise_usage"}
	}
	remaining, _ := subtractExactDecimals(limit, credit)
	pkg := codeBuddyQuotaPackage{
		Basis:          "cycle",
		Name:           "enterprise",
		Unit:           "credits",
		Total:          approximatePointer(limit),
		TotalExact:     limit,
		Used:           approximatePointer(credit),
		UsedExact:      credit,
		Remaining:      approximatePointer(remaining),
		RemainingExact: remaining,
		CycleStart:     stringValue(data, "cycleStartTime", "cycle_start_time"),
		CycleEnd:       stringValue(data, "cycleEndTime", "cycle_end_time"),
	}
	return codeBuddyQuota{
		Status: "available",
		Scope:  "enterprise_member",
		Source: "get-enterprise-user-usage",
		Unit:   "credits",
		Total:  pkg.Total, TotalExact: pkg.TotalExact,
		Used: pkg.Used, UsedExact: pkg.UsedExact,
		Remaining: pkg.Remaining, RemainingExact: pkg.RemainingExact,
		Packages: []codeBuddyQuotaPackage{pkg},
	}
}

func decodeCodeBuddyEnterpriseUsage(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var root any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return nil, errDecode
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("enterprise usage response is not an object")
	}
	if code := stringOrNumber(rootMap, "code"); code != "" && code != "0" {
		return nil, fmt.Errorf("enterprise usage response rejected the request")
	}
	data := mapValue(rootMap, "data")
	if data == nil {
		return nil, fmt.Errorf("enterprise usage response has no data")
	}
	return data, nil
}

// fetchPersonalResources 保留原 get-user-resource 分页查询；个人无资源包时为空，
// 返回 unsupported/no_personal_resources，语义是"没有个人资源包"，不是余额为零。
func (r *pluginRuntime) fetchPersonalResources(auth codeBuddyAuth, callbackID string) (result codeBuddyQuota) {
	defer func() { result.Scope = "personal_resources"; result.Source = "get-user-resource" }()
	cfg := r.loadedConfig()
	if strings.TrimSpace(cfg.BillingEndpoint) == "" {
		return codeBuddyQuota{Status: "not_configured", Code: "billing_endpoint_missing"}
	}
	if strings.TrimSpace(callbackID) == "" {
		return codeBuddyQuota{Status: "upstream_error", Code: "host_callback_missing"}
	}
	personalHeaders := make(http.Header)
	personalHeaders.Set("Accept", "application/json")
	personalHeaders.Set("Content-Type", "application/json")
	personalHeaders.Set("X-API-Key", auth.APIKey)
	personalHeaders.Set("X-Product", "SaaS")
	personalHeaders.Set("User-Agent", cfg.CatalogUserAgent)
	// 同一个凭据分页面查询个人资源；不把 TotalDosage 当成已使用量。
	var all []any
	now := time.Now().In(time.FixedZone("CST", 8*60*60))
	seenPages := make(map[string]bool)
	for page := 1; page <= 10; page++ {
		body, _ := json.Marshal(map[string]any{
			"PageSize": 100, "PageNumber": page, "ProductCode": "p_tcaca", "Status": []int{0, 3},
			"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
			"PackageEndTimeRangeEnd":   now.AddDate(100, 0, 0).Format("2006-01-02 15:04:05"),
		})
		response, errRequest := doHostHTTP(r.caller, hostHTTPRequest{
			HostCallbackID: callbackID,
			Method:         http.MethodPost,
			URL:            cfg.BillingEndpoint,
			Headers:        personalHeaders,
			Body:           body,
		})
		if errRequest != nil {
			return codeBuddyQuota{Status: "upstream_error", Code: "billing_request_failed"}
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return codeBuddyQuota{Status: "auth_rejected", Code: "billing_auth_rejected"}
		}
		if response.StatusCode == http.StatusTooManyRequests {
			return codeBuddyQuota{Status: "rate_limited", Code: "billing_rate_limited"}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return codeBuddyQuota{Status: "upstream_error", Code: "billing_http_error"}
		}
		if len(response.Body) == 0 || len(response.Body) > 512*1024 {
			return codeBuddyQuota{Status: "upstream_error", Code: "billing_response_invalid"}
		}
		data, errParse := decodeCodeBuddyQuotaData(response.Body)
		if errParse != nil {
			return codeBuddyQuota{Status: "partial", Code: "billing_response_invalid"}
		}
		items := arrayValue(data, "Accounts")
		pageJSON, _ := json.Marshal(items)
		if len(items) > 0 && seenPages[string(pageJSON)] {
			return codeBuddyQuota{Status: "partial", Code: "billing_pagination_repeated"}
		}
		seenPages[string(pageJSON)] = true
		all = append(all, items...)
		totalCount, _ := strconv.Atoi(stringValue(data, "TotalCount"))
		if totalCount > len(all) && len(items) == 0 {
			return codeBuddyQuota{Status: "partial", Code: "billing_pagination_incomplete"}
		}
		if totalCount > 0 && len(all) >= totalCount || totalCount == 0 && len(items) < 100 {
			combined, _ := json.Marshal(map[string]any{"Accounts": all})
			quota, err := parseCodeBuddyQuota(combined)
			if err != nil {
				return codeBuddyQuota{Status: "partial", Code: "billing_response_invalid"}
			}
			return quota
		}
	}
	return codeBuddyQuota{Status: "partial", Code: "billing_pagination_limit"}
}

func decodeCodeBuddyQuotaData(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var root any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return nil, errDecode
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("billing response is not an object")
	}
	if code := stringOrNumber(rootMap, "code"); code != "" && code != "0" {
		return nil, fmt.Errorf("billing response rejected the request")
	}
	data := mapValue(rootMap, "data")
	if data == nil {
		data = rootMap
	}
	if response := mapValue(data, "Response"); response != nil {
		if nested := mapValue(response, "Data"); nested != nil {
			data = nested
		}
	}
	if nested := mapValue(data, "data"); nested != nil {
		data = nested
	}

	if !mapHasKey(data, "Accounts") && mapHasKey(rootMap, "Accounts") {
		data = rootMap
	}
	if !mapHasKey(data, "Accounts") {
		return nil, fmt.Errorf("billing response has no accounts field")
	}
	for key, value := range data {
		if strings.EqualFold(key, "Accounts") && value != nil {
			if _, ok := value.([]any); !ok {
				return nil, fmt.Errorf("billing accounts field is invalid")
			}
		}
	}
	return data, nil
}

func parseCodeBuddyQuota(raw []byte) (codeBuddyQuota, error) {
	data, err := decodeCodeBuddyQuotaData(raw)
	if err != nil {
		return codeBuddyQuota{}, err
	}
	accountValues := arrayValue(data, "Accounts")
	packages := make([]codeBuddyQuotaPackage, 0, len(accountValues))
	for _, rawPackage := range accountValues {
		item, okItem := rawPackage.(map[string]any)
		if !okItem {
			return codeBuddyQuota{}, fmt.Errorf("billing package is invalid")
		}
		packages = append(packages, parseCodeBuddyQuotaPackage(item))
	}
	if len(packages) == 0 {
		return codeBuddyQuota{
			Status:   "unsupported",
			Code:     "no_personal_resources",
			Scope:    "personal_resources",
			Source:   "get-user-resource",
			Unit:     "credits",
			Packages: []codeBuddyQuotaPackage{},
		}, nil
	}

	result := codeBuddyQuota{Status: "available", Scope: "personal_resources", Source: "get-user-resource", Unit: packages[0].Unit, Packages: packages}
	if result.Unit == "" {
		result.Unit = "credits"
	}
	for _, item := range packages {
		unit := item.Unit
		if unit == "" {
			unit = "credits"
		}
		if !strings.EqualFold(unit, result.Unit) {
			result.Status = "partial"
			result.Code = "mixed_units"
			return result, nil
		}
	}

	var total, used, remaining []string
	for _, item := range packages {
		if item.TotalExact == "" || item.UsedExact == "" || item.RemainingExact == "" {
			result.Status = "partial"
			result.Code = "invalid_package_amount"
			return result, nil
		}
		total = append(total, item.TotalExact)
		used = append(used, item.UsedExact)
		remaining = append(remaining, item.RemainingExact)
	}
	var okSum bool
	if result.TotalExact, okSum = sumExactDecimals(total); !okSum {
		result.Status = "partial"
		result.Code = "invalid_total"
		return result, nil
	}
	if result.UsedExact, okSum = sumExactDecimals(used); !okSum {
		result.Status = "partial"
		result.Code = "invalid_used"
		return result, nil
	}
	if result.RemainingExact, okSum = sumExactDecimals(remaining); !okSum {
		result.Status = "partial"
		result.Code = "invalid_remaining"
		return result, nil
	}
	result.Total = approximatePointer(result.TotalExact)
	result.Used = approximatePointer(result.UsedExact)
	result.Remaining = approximatePointer(result.RemainingExact)
	return result, nil
}

func parseCodeBuddyQuotaPackage(item map[string]any) codeBuddyQuotaPackage {
	name := stringValue(item, "PackageName", "package_name", "name")
	status := stringValue(item, "Status", "status")
	unit := stringValue(item, "Unit", "unit")
	if unit == "" {
		unit = "credits"
	}
	basis := "lifetime"
	total := exactValue(item, "CapacitySizePrecise", "capacity_size_precise", "CapacitySize", "total")
	used := exactValue(item, "CapacityUsedPrecise", "capacity_used_precise", "CapacityUsed", "used")
	remaining := exactValue(item, "CapacityRemainPrecise", "capacity_remain_precise", "CapacityRemain", "remaining")
	// 周期计数存在时整组采用，缺字段保留未知，不混入终身计数凑余额。
	for key := range item {
		if strings.HasPrefix(strings.ToLower(key), "cyclecapacity") {
			basis = "cycle"
			total = exactValue(item, "CycleCapacitySizePrecise", "CycleCapacitySize")
			used = exactValue(item, "CycleCapacityUsedPrecise", "CycleCapacityUsed")
			remaining = exactValue(item, "CycleCapacityRemainPrecise", "CycleCapacityRemain")
			break
		}
	}
	if used == "" && total != "" && remaining != "" {
		used, _ = subtractExactDecimals(total, remaining)
	}
	if remaining == "" && total != "" && used != "" {
		if value, ok := subtractExactDecimals(total, used); ok {
			remaining = value
		}
	}
	return codeBuddyQuotaPackage{
		Basis:          basis,
		Name:           name,
		Status:         status,
		Unit:           unit,
		Total:          approximatePointer(total),
		TotalExact:     total,
		Used:           approximatePointer(used),
		UsedExact:      used,
		Remaining:      approximatePointer(remaining),
		RemainingExact: remaining,
		CycleStart:     stringValue(item, "CycleStartTime", "cycle_start_time", "cycle_start"),
		CycleEnd:       stringValue(item, "CycleEndTime", "cycle_end_time", "cycle_end"),
	}
}

func mapValue(value map[string]any, key string) map[string]any {
	for candidate, raw := range value {
		if strings.EqualFold(candidate, key) {
			if result, ok := raw.(map[string]any); ok {
				return result
			}
		}
	}
	return nil
}

func arrayValue(value map[string]any, key string) []any {
	for candidate, raw := range value {
		if strings.EqualFold(candidate, key) {
			if result, ok := raw.([]any); ok {
				return result
			}
		}
	}
	return nil
}

func mapHasKey(value map[string]any, key string) bool {
	for candidate := range value {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}

func stringValue(value map[string]any, keys ...string) string {
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

func stringOrNumber(value map[string]any, key string) string {
	return stringValue(value, key)
}

func exactValue(value map[string]any, keys ...string) string {
	raw := stringValue(value, keys...)
	if raw == "" {
		return ""
	}
	if _, ok := new(big.Rat).SetString(raw); !ok {
		return ""
	}
	return normalizeDecimal(raw)
}

func normalizeDecimal(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, "eE") {
		rat, ok := new(big.Rat).SetString(value)
		if !ok {
			return ""
		}
		return ratToDecimal(rat, 12)
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

func sumExactDecimals(values []string) (string, bool) {
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
	return ratToDecimal(total, decimalScale(values)), true
}

func subtractExactDecimals(left, right string) (string, bool) {
	a, okA := new(big.Rat).SetString(left)
	b, okB := new(big.Rat).SetString(right)
	if !okA || !okB {
		return "", false
	}
	a.Sub(a, b)
	return ratToDecimal(a, maxDecimalScale(left, right)), true
}

func decimalScale(values []string) int {
	max := 0
	for _, value := range values {
		max = maxDecimalScaleValue(max, value)
	}
	return max
}

func maxDecimalScale(left, right string) int {
	return maxDecimalScaleValue(maxDecimalScaleValue(0, left), right)
}

func maxDecimalScaleValue(current int, value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(value, 'e'); index >= 0 {
		return current
	}
	if index := strings.IndexByte(value, '.'); index >= 0 && len(value)-index-1 > current {
		return len(value) - index - 1
	}
	return current
}

func ratToDecimal(value *big.Rat, scale int) string {
	if scale < 0 {
		scale = 0
	}
	return normalizeDecimal(value.FloatString(scale))
}

func approximatePointer(value string) *float64 {
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
