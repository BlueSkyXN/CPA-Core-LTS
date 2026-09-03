package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

type codeBuddyQuota struct {
	Status         string                  `json:"status"`
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

func (r *pluginRuntime) codeBuddyQuotaSummary(auth codeBuddyAuth, callbackID string) codeBuddyQuota {
	cfg := r.loadedConfig()
	if strings.TrimSpace(cfg.BillingEndpoint) == "" {
		return codeBuddyQuota{Status: "not_configured", Code: "billing_endpoint_missing"}
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
		URL:            cfg.BillingEndpoint,
		Headers:        headers,
		Body:           []byte("{}"),
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
	quota, errParse := parseCodeBuddyQuota(response.Body)
	if errParse != nil {
		return codeBuddyQuota{Status: "partial", Code: "billing_response_invalid"}
	}
	return quota
}

func parseCodeBuddyQuota(raw []byte) (codeBuddyQuota, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var root any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return codeBuddyQuota{}, errDecode
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return codeBuddyQuota{}, fmt.Errorf("billing response is not an object")
	}
	if code := stringOrNumber(rootMap, "code"); code != "" && code != "0" {
		return codeBuddyQuota{}, fmt.Errorf("billing response returned code %s", code)
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

	accountsPresent := mapHasKey(data, "Accounts") || mapHasKey(rootMap, "Accounts")
	if !accountsPresent {
		return codeBuddyQuota{}, fmt.Errorf("billing response has no accounts field")
	}
	accountValues := arrayValue(data, "Accounts")
	if len(accountValues) == 0 {
		accountValues = arrayValue(rootMap, "Accounts")
	}
	packages := make([]codeBuddyQuotaPackage, 0, len(accountValues))
	for _, rawPackage := range accountValues {
		item, okItem := rawPackage.(map[string]any)
		if !okItem {
			continue
		}
		packages = append(packages, parseCodeBuddyQuotaPackage(item))
	}
	if len(packages) == 0 {
		return codeBuddyQuota{
			Status:         "available",
			Unit:           "credits",
			Total:          floatPointer(0),
			TotalExact:     "0",
			Used:           floatPointer(0),
			UsedExact:      "0",
			Remaining:      floatPointer(0),
			RemainingExact: "0",
			Packages:       []codeBuddyQuotaPackage{},
		}, nil
	}

	result := codeBuddyQuota{Status: "available", Unit: packages[0].Unit, Packages: packages}
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
	total := exactValue(item, "CapacitySizePrecise", "capacity_size_precise", "total")
	used := exactValue(item, "CapacityUsedPrecise", "capacity_used_precise", "used")
	remaining := exactValue(item, "CapacityRemainPrecise", "capacity_remain_precise", "remaining")
	if remaining == "" && total != "" && used != "" {
		if value, ok := subtractExactDecimals(total, used); ok {
			remaining = value
		}
	}
	return codeBuddyQuotaPackage{
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
