package codexmetadata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactRequestBodyForLogRemovesWorkspacesWithoutMutatingInput(t *testing.T) {
	body := requestBodyWithMetadata(t, sampleTurnMetadata, map[string]any{"transport_marker": true})
	original := string(body)
	redacted := RedactRequestBodyForLog(body)
	if string(body) != original {
		t.Fatal("log sanitizer mutated input body")
	}
	if strings.Contains(string(redacted), "secret") || strings.Contains(string(redacted), "/Users/example/project") || strings.Contains(string(redacted), "01234567") {
		t.Fatalf("redacted body leaked workspace metadata: %s", redacted)
	}
	clientMetadata := decodeClientMetadata(t, redacted)
	var canonical string
	if err := json.Unmarshal(clientMetadata["x-codex-turn-metadata"], &canonical); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(canonical, `"workspaces"`) {
		t.Fatalf("canonical log metadata retained workspaces: %s", canonical)
	}
	if _, ok := clientMetadata["transport_marker"]; !ok {
		t.Fatal("log sanitizer dropped unrelated client_metadata")
	}
}

func TestRedactRequestBodyForLogFailsClosedForMalformedCanonical(t *testing.T) {
	body := requestBodyWithRawCanonical(t, `{"workspaces":{"x":"token-secret"}`)
	redacted := RedactRequestBodyForLog(body)
	if strings.Contains(string(redacted), "token-secret") {
		t.Fatalf("malformed canonical value leaked: %s", redacted)
	}
	if !strings.Contains(string(redacted), redactedInvalidTurnMetadata) {
		t.Fatalf("redaction marker missing: %s", redacted)
	}
}

func TestRedactHeadersForLogClonesAndSanitizesCanonicalHeader(t *testing.T) {
	headers := map[string][]string{
		"X-Codex-Turn-Metadata": {sampleTurnMetadata},
		"X-Test":                {"keep"},
	}
	redacted := RedactHeadersForLog(headers)
	if strings.Contains(redacted["X-Codex-Turn-Metadata"][0], "secret") || strings.Contains(redacted["X-Codex-Turn-Metadata"][0], `"workspaces"`) {
		t.Fatalf("header was not sanitized: %s", redacted["X-Codex-Turn-Metadata"][0])
	}
	if headers["X-Codex-Turn-Metadata"][0] != sampleTurnMetadata {
		t.Fatal("header sanitizer mutated input map")
	}
	if redacted["X-Test"][0] != "keep" {
		t.Fatal("unrelated header changed")
	}
}

func TestRedactHeadersForLogMasksDerivedIdentityHeaders(t *testing.T) {
	headers := map[string][]string{
		"Session_id":               {"sensitive-session"},
		"X-Codex-Window-Id":        {"sensitive-window"},
		"X-Codex-Parent-Thread-Id": {"sensitive-parent"},
		"X-OpenAI-Subagent":        {"sensitive-subagent"},
		"X-Unrelated":              {"keep"},
	}

	redacted := RedactHeadersForLog(headers)
	for _, key := range []string{"Session_id", "X-Codex-Window-Id", "X-Codex-Parent-Thread-Id", "X-OpenAI-Subagent"} {
		if got := redacted[key]; len(got) != 1 || got[0] != redactedIdentityHeader {
			t.Fatalf("%s = %#v, want fixed marker", key, got)
		}
		if strings.Contains(strings.Join(redacted[key], ""), "sensitive") {
			t.Fatalf("%s leaked sensitive value: %#v", key, redacted[key])
		}
	}
	if got := redacted["X-Unrelated"]; len(got) != 1 || got[0] != "keep" {
		t.Fatalf("X-Unrelated = %#v, want keep", got)
	}
	if headers["Session_id"][0] != "sensitive-session" {
		t.Fatalf("live headers mutated: %#v", headers)
	}
}

func TestRedactRequestBodyForLogRedactsEscapedCanonicalKey(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","clie\u006et_metadata":{"x-codex-turn-metad\u0061ta":"{\"request_kind\":\"turn\",\"thread_id\":\"thread-1\",\"workspaces\":{\"/Users/private/project\":{\"associated_remote_urls\":{\"origin\":\"https://user:credential-sentinel@example.com/repo.git\"}}}}"}}`)
	redacted := RedactRequestBodyForLog(body)
	if strings.Contains(string(redacted), "credential-sentinel") || strings.Contains(string(redacted), `"workspaces"`) {
		t.Fatalf("escaped canonical key leaked workspace details: %s", redacted)
	}
	if !strings.Contains(string(redacted), "thread-1") {
		t.Fatalf("escaped canonical key did not preserve safe metadata: %s", redacted)
	}
}

func TestRedactRequestBodyForLogFailsClosedForDuplicateClientMetadataCarriers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"thread_id\":\"thread-1\",\"workspaces\":{\"/Users/private/project\":{\"associated_remote_urls\":{\"origin\":\"https://user:credential-sentinel@example.com/repo.git\"}}}}"},"client_metadata":{"transport_marker":"last"}}`)
	redacted := RedactRequestBodyForLog(body)
	if strings.Contains(string(redacted), "credential-sentinel") || strings.Contains(string(redacted), "/Users/private/project") {
		t.Fatalf("duplicate client metadata leaked workspace details: %s", redacted)
	}
	if !strings.Contains(string(redacted), "[REDACTED CODEX REQUEST BODY WITH INVALID TURN METADATA]") {
		t.Fatalf("duplicate client metadata did not fail closed: %s", redacted)
	}
}
