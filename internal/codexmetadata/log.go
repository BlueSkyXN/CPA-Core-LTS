package codexmetadata

import (
	"bytes"
	"encoding/json"
	"strings"
)

const redactedInvalidTurnMetadata = "[REDACTED INVALID CODEX TURN METADATA]"

// RedactRequestBodyForLog removes workspace enrichment from the canonical
// metadata copy written to logs. The original request body is never modified.
func RedactRequestBodyForLog(body []byte) []byte {
	if len(body) == 0 || !bytes.Contains(body, []byte("x-codex-turn-metadata")) {
		return body
	}
	envelope, err := requestEnvelope(body)
	if err != nil {
		return []byte("[REDACTED CODEX REQUEST BODY WITH INVALID TURN METADATA]")
	}
	rawClientMetadata, hasClientMetadata := envelope["client_metadata"]
	if !hasClientMetadata {
		return body
	}
	var clientMetadata map[string]json.RawMessage
	if err = json.Unmarshal(rawClientMetadata, &clientMetadata); err != nil || clientMetadata == nil {
		return []byte("[REDACTED CODEX REQUEST BODY WITH INVALID TURN METADATA]")
	}
	rawCanonical, exists := clientMetadata["x-codex-turn-metadata"]
	if !exists {
		return body
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

// RedactHeadersForLog clones a header map and removes workspace enrichment
// from direct X-Codex-Turn-Metadata compatibility headers.
func RedactHeadersForLog(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string][]string, len(headers))
	for key, values := range headers {
		cloned := append([]string(nil), values...)
		if strings.EqualFold(key, "X-Codex-Turn-Metadata") {
			for index := range cloned {
				cloned[index] = redactTurnMetadataForLog(cloned[index])
			}
		}
		redacted[key] = cloned
	}
	return redacted
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
