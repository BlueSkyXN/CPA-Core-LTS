package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

const (
	usageV1MigrationName               = "v1_uncached_input_tokens_to_v2"
	usageV2TimingMigrationName         = "v2_timing_contract_to_v3"
	usageCodeVersionUnsupported        = "usage_version_unsupported"
	usageCodeShapeInvalid              = "usage_shape_invalid"
	usageCodeV1TokenContractInvalid    = "usage_v1_token_contract_invalid"
	usageCodeV1CacheSemanticsAmbiguous = "usage_v1_cache_semantics_ambiguous"
	usageCodeV2TokenContractInvalid    = "usage_v2_token_contract_invalid"
	usageCodeV3TokenContractInvalid    = "usage_v3_token_contract_invalid"
	usageCodeV1TimingAmbiguous         = "usage_v1_timing_semantics_ambiguous"
	usageCodeV2TimingAmbiguous         = "usage_v2_timing_semantics_ambiguous"
	usageCodeV3TimingInvalid           = "usage_v3_timing_contract_invalid"
	usageCodeAggregateOverflow         = "usage_aggregate_overflow"
)

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    usage.CanonicalExportVersion,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		writeUsageImportError(c, usageCodeShapeInvalid, "usage statistics unavailable")
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		writeUsageImportError(c, usageCodeShapeInvalid, "failed to read request body")
		return
	}

	version, err := usageImportVersion(data)
	if err != nil {
		writeUsageImportError(c, usageCodeShapeInvalid, "invalid json")
		return
	}
	if version != 1 && version != 2 && version != usage.CanonicalExportVersion {
		writeUsageImportError(c, usageCodeVersionUnsupported, "unsupported version")
		return
	}
	if err := validateUsageImportRawShape(data, version); err != nil {
		switch {
		case errors.Is(err, errUsageV1TimingSemanticsAmbiguous):
			writeUsageImportError(c, usageCodeV1TimingAmbiguous, err.Error())
		case errors.Is(err, errUsageV2TimingSemanticsAmbiguous):
			writeUsageImportError(c, usageCodeV2TimingAmbiguous, err.Error())
		case errors.Is(err, errUsageV3TimingContractInvalid):
			writeUsageImportError(c, usageCodeV3TimingInvalid, err.Error())
		default:
			writeUsageImportError(c, usageCodeShapeInvalid, "invalid usage shape")
		}
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		if errors.Is(err, usage.ErrInvalidLegacyTokenStats) || errors.Is(err, usage.ErrInvalidCanonicalTokenStats) {
			writeUsageImportTokenContractError(c, version)
			return
		}
		writeUsageImportError(c, usageCodeShapeInvalid, "invalid json")
		return
	}
	if payload.Usage.APIs == nil {
		writeUsageImportError(c, usageCodeShapeInvalid, "invalid usage.apis")
		return
	}

	migratedFromVersion := 0
	payload.Version = version
	if version == 1 {
		if err := payload.Usage.MigrateV1TokenStats(); err != nil {
			switch {
			case errors.Is(err, usage.ErrAmbiguousLegacyTokenStats):
				writeUsageImportError(c, usageCodeV1CacheSemanticsAmbiguous, usage.ErrAmbiguousLegacyTokenStats.Error())
			case errors.Is(err, usage.ErrInvalidLegacyTokenStats):
				writeUsageImportError(c, usageCodeV1TokenContractInvalid, usage.ErrInvalidLegacyTokenStats.Error())
			default:
				writeUsageImportError(c, usageCodeV1TokenContractInvalid, "invalid legacy usage payload")
			}
			return
		}
		if err := payload.Usage.ValidateCanonicalTokenStats(); err != nil {
			writeUsageImportError(c, usageCodeV1TokenContractInvalid, usage.ErrInvalidCanonicalTokenStats.Error())
			return
		}
		migratedFromVersion = 1
	} else if version == 2 {
		if payload.Usage.HasLegacyUncachedInputTokens() {
			writeUsageImportError(c, usageCodeV2TokenContractInvalid, "canonical usage payload must not contain uncached_input_tokens")
			return
		}
		if err := payload.Usage.ValidateCanonicalV2TokenStats(); err != nil {
			writeUsageImportError(c, usageCodeV2TokenContractInvalid, usage.ErrInvalidCanonicalTokenStats.Error())
			return
		}
		migratedFromVersion = 2
	} else {
		if payload.Usage.HasLegacyUncachedInputTokens() {
			writeUsageImportError(c, usageCodeV3TokenContractInvalid, "canonical usage payload must not contain uncached_input_tokens")
			return
		}
		if err := payload.Usage.ValidateCanonicalV3TokenStats(); err != nil {
			writeUsageImportError(c, usageCodeV3TokenContractInvalid, usage.ErrInvalidCanonicalTokenStats.Error())
			return
		}
	}
	if version == usage.CanonicalExportVersion {
		if err := payload.Usage.ValidateCanonicalTimingStats(); err != nil {
			writeUsageImportError(c, usageCodeV3TimingInvalid, err.Error())
			return
		}
	}

	result, err := h.usageStats.MergeSnapshot(payload.Usage)
	if err != nil {
		if errors.Is(err, usage.ErrUsageAggregateOverflow) {
			writeUsageImportError(c, usageCodeAggregateOverflow, usage.ErrUsageAggregateOverflow.Error())
			return
		}
		if errors.Is(err, usage.ErrInvalidCanonicalTimingStats) {
			writeUsageImportError(c, usageCodeV3TimingInvalid, err.Error())
			return
		}
		writeUsageImportError(c, usageCodeShapeInvalid, "failed to merge usage statistics")
		return
	}
	snapshot := h.usageStats.Snapshot()
	response := gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
		"schema_version":  usage.CanonicalExportVersion,
	}
	if migratedFromVersion != 0 {
		response["migrated_from_version"] = migratedFromVersion
		migrations := []string{usageV2TimingMigrationName}
		if migratedFromVersion == 1 {
			migrations = []string{usageV1MigrationName, usageV2TimingMigrationName}
		}
		response["migrations"] = migrations
	}
	c.JSON(http.StatusOK, response)
}

var (
	errUsageV1TimingSemanticsAmbiguous = errors.New("version 1 timing semantics are ambiguous")
	errUsageV2TimingSemanticsAmbiguous = errors.New("version 2 timing semantics are ambiguous")
	errUsageV3TimingContractInvalid    = errors.New("version 3 timing contract is invalid")
)

func validateUsageImportRawShape(data []byte, version int) error {
	root, err := unmarshalUsageObject(data)
	if err != nil {
		return err
	}
	if err = rejectUsageFieldAliases(root, "version", "usage"); err != nil {
		return err
	}
	usageObject, err := requiredUsageObject(root, "usage")
	if err != nil {
		return err
	}
	if err = rejectUsageFieldAliases(
		usageObject,
		"total_requests",
		"success_count",
		"failure_count",
		"total_tokens",
		"apis",
		"requests_by_day",
		"requests_by_hour",
		"tokens_by_day",
		"tokens_by_hour",
	); err != nil {
		return err
	}
	apis, err := requiredUsageObject(usageObject, "apis")
	if err != nil {
		return err
	}
	for apiName, rawAPI := range apis {
		if strings.TrimSpace(apiName) == "" {
			return errors.New("usage API identity must not be blank")
		}
		apiObject, err := unmarshalUsageObject(rawAPI)
		if err != nil {
			return err
		}
		if err = rejectUsageFieldAliases(apiObject, "total_requests", "total_tokens", "models"); err != nil {
			return err
		}
		models, err := requiredUsageObject(apiObject, "models")
		if err != nil {
			return err
		}
		for _, rawModel := range models {
			modelObject, err := unmarshalUsageObject(rawModel)
			if err != nil {
				return err
			}
			if err = rejectUsageFieldAliases(modelObject, "total_requests", "total_tokens", "details"); err != nil {
				return err
			}
			details, err := requiredUsageArray(modelObject, "details")
			if err != nil {
				return err
			}
			for _, rawDetail := range details {
				detailObject, err := unmarshalUsageObject(rawDetail)
				if err != nil {
					return err
				}
				if err = rejectUsageFieldAliases(
					detailObject,
					"timestamp",
					"latency_ms",
					"ttfb_ms",
					"source",
					"auth_index",
					"alias",
					"reasoning_effort",
					"service_tier",
					"request_service_tier",
					"outbound_service_tier",
					"response_service_tier",
					"effective_service_tier",
					"tokens",
					"failed",
					"generate",
					"failure_reason",
					"failure_status",
					"timing_version",
					"ttft_ms",
					"ttfa_ms",
				); err != nil {
					return err
				}
				if err := validateUsageTimingRawShape(detailObject, version); err != nil {
					return err
				}
				rawTokens, exists := detailObject["tokens"]
				if !exists || bytes.Equal(bytes.TrimSpace(rawTokens), []byte("null")) {
					continue
				}
				tokenObject, err := unmarshalUsageObject(rawTokens)
				if err != nil {
					return err
				}
				if err = rejectUsageFieldAliases(
					tokenObject,
					"input_tokens",
					"output_tokens",
					"reasoning_tokens",
					"cached_tokens",
					"cache_read_tokens",
					"cache_creation_tokens",
					"total_tokens",
					"uncached_input_tokens",
				); err != nil {
					return err
				}
			}
		}
	}
	for _, field := range []string{"requests_by_day", "requests_by_hour", "tokens_by_day", "tokens_by_hour"} {
		if rawMap, exists := usageObject[field]; exists {
			if _, err := unmarshalUsageObject(rawMap); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateUsageTimingRawShape(detail map[string]json.RawMessage, version int) error {
	_, hasTimingVersion := detail["timing_version"]
	_, hasTTFB := detail["ttfb_ms"]
	_, hasTTFT := detail["ttft_ms"]
	_, hasTTFA := detail["ttfa_ms"]
	if rawLatency, hasLatency := detail["latency_ms"]; hasLatency {
		if _, ok := rawNonNegativeInt64(rawLatency); !ok {
			if version == 3 {
				return errUsageV3TimingContractInvalid
			}
			return errors.New("legacy latency_ms must be a non-negative integer")
		}
	}

	if version == 1 || version == 2 {
		if hasTimingVersion || hasTTFT || hasTTFA {
			if version == 1 {
				return errUsageV1TimingSemanticsAmbiguous
			}
			return errUsageV2TimingSemanticsAmbiguous
		}
		if !hasTTFB {
			return nil
		}
		ttfb, ok := rawNonNegativeInt64(detail["ttfb_ms"])
		if !ok {
			return errors.New("legacy ttfb_ms must be a non-negative integer")
		}
		latency, latencyOK := rawNonNegativeInt64(detail["latency_ms"])
		if !latencyOK || ttfb > latency {
			return errors.New("legacy ttfb_ms must not exceed latency_ms")
		}
		return nil
	}

	if hasTimingVersion {
		var timingVersion uint32
		if !rawInteger(detail["timing_version"]) || json.Unmarshal(detail["timing_version"], &timingVersion) != nil || timingVersion != 1 {
			return errUsageV3TimingContractInvalid
		}
	}

	var ttfb, ttft, ttfa int64
	if hasTTFB {
		var ok bool
		ttfb, ok = rawNonNegativeInt64(detail["ttfb_ms"])
		if !ok {
			return errUsageV3TimingContractInvalid
		}
	}
	if hasTTFT {
		var ok bool
		ttft, ok = rawNonNegativeInt64(detail["ttft_ms"])
		if !ok {
			return errUsageV3TimingContractInvalid
		}
	}
	if hasTTFA {
		var ok bool
		ttfa, ok = rawNonNegativeInt64(detail["ttfa_ms"])
		if !ok {
			return errUsageV3TimingContractInvalid
		}
	}
	if !hasTTFB && (hasTTFT || hasTTFA) {
		return errUsageV3TimingContractInvalid
	}
	if hasTTFB || hasTTFT || hasTTFA {
		latency, ok := rawNonNegativeInt64(detail["latency_ms"])
		if !ok || ttfb > latency || ttft > latency || ttfa > latency {
			return errUsageV3TimingContractInvalid
		}
		if (hasTTFT || hasTTFA) && !hasTimingVersion {
			return errUsageV3TimingContractInvalid
		}
	}
	return nil
}

func rawInteger(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var value int64
	return json.Unmarshal(raw, &value) == nil
}

func rawNonNegativeInt64(raw json.RawMessage) (int64, bool) {
	if !rawInteger(raw) {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func rejectUsageFieldAliases(object map[string]json.RawMessage, fields ...string) error {
	for key := range object {
		for _, field := range fields {
			if key != field && strings.EqualFold(key, field) {
				return errors.New("usage field names are case-sensitive")
			}
		}
	}
	return nil
}

func requiredUsageObject(object map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	raw, exists := object[field]
	if !exists {
		return nil, errors.New("missing usage object")
	}
	return unmarshalUsageObject(raw)
}

func unmarshalUsageObject(data []byte) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, errors.New("usage value must be an object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("usage value must be an object")
	}
	return object, nil
}

func requiredUsageArray(object map[string]json.RawMessage, field string) ([]json.RawMessage, error) {
	raw, exists := object[field]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("missing usage array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("usage value must be an array")
	}
	return values, nil
}

func usageImportVersion(data []byte) (int, error) {
	if err := rejectDuplicateUsageJSONFields(data); err != nil {
		return 0, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("usage import root must be an object")
	}
	if err := rejectUsageFieldAliases(envelope, "version", "usage"); err != nil {
		return 0, err
	}
	rawVersion, exists := envelope["version"]
	if !exists {
		return 0, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawVersion), []byte("null")) {
		return 0, errors.New("usage import version must be an integer")
	}
	var version int
	if err := json.Unmarshal(rawVersion, &version); err != nil {
		return 0, err
	}
	return version, nil
}

func rejectDuplicateUsageJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("usage import must contain one JSON value")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("usage object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate usage object key")
			}
			seen[key] = struct{}{}
			if err = consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("invalid usage object")
		}
	case '[':
		for decoder.More() {
			if err = consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("invalid usage array")
		}
	default:
		return errors.New("invalid usage JSON delimiter")
	}
	return nil
}

func writeUsageImportTokenContractError(c *gin.Context, version int) {
	if version == 1 {
		writeUsageImportError(c, usageCodeV1TokenContractInvalid, usage.ErrInvalidLegacyTokenStats.Error())
		return
	}
	if version == usage.CanonicalExportVersion {
		writeUsageImportError(c, usageCodeV3TokenContractInvalid, usage.ErrInvalidCanonicalTokenStats.Error())
		return
	}
	writeUsageImportError(c, usageCodeV2TokenContractInvalid, usage.ErrInvalidCanonicalTokenStats.Error())
}

func writeUsageImportError(c *gin.Context, code, message string) {
	c.JSON(http.StatusBadRequest, gin.H{
		"error": message,
		"code":  code,
	})
}
