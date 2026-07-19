package codexmetadata

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	redactedInvalidTurnMetadata = "[REDACTED INVALID CODEX TURN METADATA]"
	redactedInvalidRequestBody  = "[REDACTED CODEX REQUEST BODY WITH INVALID TURN METADATA]"
	redactedIdentityHeader      = "[REDACTED CODEX IDENTITY HEADER]"
)

type logMetadataInspection struct {
	sawClientMetadata bool
	hasCanonical      bool
	ambiguous         bool
}

// RedactRequestBodyForLog removes workspace enrichment from the canonical
// metadata copy written to logs. The original request body is never modified.
func RedactRequestBodyForLog(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if !bytes.Contains(body, []byte("x-codex-turn-metadata")) && !bytes.Contains(body, []byte(`\u`)) {
		return body
	}
	inspection, err := inspectLogMetadataCarriers(body)
	if err != nil {
		if inspection.sawClientMetadata || bytes.Contains(body, []byte("x-codex-turn-metadata")) {
			return []byte(redactedInvalidRequestBody)
		}
		return body
	}
	if !inspection.hasCanonical {
		return body
	}
	if inspection.ambiguous {
		return []byte(redactedInvalidRequestBody)
	}
	envelope, err := requestEnvelope(body)
	if err != nil {
		return []byte(redactedInvalidRequestBody)
	}
	rawClientMetadata, hasClientMetadata := envelope["client_metadata"]
	if !hasClientMetadata {
		return []byte(redactedInvalidRequestBody)
	}
	var clientMetadata map[string]json.RawMessage
	if err = json.Unmarshal(rawClientMetadata, &clientMetadata); err != nil || clientMetadata == nil {
		return []byte(redactedInvalidRequestBody)
	}
	rawCanonical, exists := clientMetadata["x-codex-turn-metadata"]
	if !exists {
		return []byte(redactedInvalidRequestBody)
	}
	var canonicalText string
	if err := json.Unmarshal(rawCanonical, &canonicalText); err != nil {
		canonicalText = redactedInvalidTurnMetadata
	} else {
		canonicalText = redactTurnMetadataForLog(canonicalText)
	}
	encodedCanonical, err := json.Marshal(canonicalText)
	if err != nil {
		return []byte("[REDACTED CODEX REQUEST BODY]")
	}
	clientMetadata["x-codex-turn-metadata"] = encodedCanonical
	clientMetadataJSON, err := json.Marshal(clientMetadata)
	if err != nil {
		return []byte("[REDACTED CODEX REQUEST BODY]")
	}
	envelope["client_metadata"] = clientMetadataJSON
	redacted, err := json.Marshal(envelope)
	if err != nil {
		return []byte("[REDACTED CODEX REQUEST BODY]")
	}
	return redacted
}

func inspectLogMetadataCarriers(body []byte) (logMetadataInspection, error) {
	var inspection logMetadataInspection
	members, err := decodeJSONObjectMembers(body)
	clientMetadataCount := 0
	canonicalCount := 0
	for _, member := range members {
		if member.key != "client_metadata" {
			continue
		}
		inspection.sawClientMetadata = true
		clientMetadataCount++
		if bytes.Equal(bytes.TrimSpace(member.value), []byte("null")) {
			continue
		}
		clientMembers, clientErr := decodeJSONObjectMembers(member.value)
		if clientErr != nil {
			return inspection, clientErr
		}
		seen := make(map[string]struct{}, len(clientMembers))
		carrierHasCanonical := false
		carrierHasDuplicate := false
		for _, clientMember := range clientMembers {
			if _, duplicate := seen[clientMember.key]; duplicate {
				carrierHasDuplicate = true
			}
			seen[clientMember.key] = struct{}{}
			if clientMember.key == "x-codex-turn-metadata" {
				carrierHasCanonical = true
				canonicalCount++
			}
		}
		if carrierHasCanonical && carrierHasDuplicate {
			inspection.ambiguous = true
		}
	}
	inspection.hasCanonical = canonicalCount > 0
	if inspection.hasCanonical && (clientMetadataCount != 1 || canonicalCount != 1) {
		inspection.ambiguous = true
	}
	if err != nil {
		return inspection, err
	}
	return inspection, nil
}

// RedactHeadersForLog clones a header map, removes workspace enrichment from
// direct canonical metadata, and masks derived identity projections.
func RedactHeadersForLog(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string][]string, len(headers))
	for key, values := range headers {
		cloned := append([]string(nil), values...)
		switch {
		case strings.EqualFold(key, "X-Codex-Turn-Metadata"):
			for index := range cloned {
				cloned[index] = redactTurnMetadataForLog(cloned[index])
			}
		case isDerivedIdentityHeader(key):
			for index := range cloned {
				cloned[index] = redactedIdentityHeader
			}
		}
		redacted[key] = cloned
	}
	return redacted
}

func isDerivedIdentityHeader(key string) bool {
	for _, candidate := range []string{
		"Session_id",
		"Session-Id",
		"X-Codex-Window-Id",
		"X-Codex-Parent-Thread-Id",
		"X-OpenAI-Subagent",
	} {
		if strings.EqualFold(key, candidate) {
			return true
		}
	}
	return false
}

func redactTurnMetadataForLog(raw string) string {
	canonical, _, err := decodeCanonicalObject(raw)
	if err != nil {
		return redactedInvalidTurnMetadata
	}
	delete(canonical, "workspaces")
	redacted, err := marshalASCIIJSON(canonical)
	if err != nil {
		return redactedInvalidTurnMetadata
	}
	return string(redacted)
}
