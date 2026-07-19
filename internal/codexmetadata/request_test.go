package codexmetadata

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const sampleTurnMetadata = `{"installation_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","thread_id":"22222222-2222-4222-8222-222222222222","turn_id":"33333333-3333-4333-8333-333333333333","window_id":"22222222-2222-4222-8222-222222222222:1","request_kind":"turn","workspaces":{"/Users/example/project":{"associated_remote_urls":{"origin":"https://user:secret@example.com/org/repo.git?token=leak#fragment","upstream":"git@example.com:upstream/repo.git"},"latest_git_commit_hash":"0123456789abcdef0123456789abcdef01234567","has_changes":false}},"turn_started_at_unix_ms":1700000000000,"workspace_kind":"项目"}`

func TestNormalizeRequestRepairSanitizesWorkspaceAndRegeneratesProjections(t *testing.T) {
	body := requestBodyWithMetadata(t, sampleTurnMetadata, map[string]any{
		"thread_id":               "conflicting-thread",
		"session_id":              "conflicting-session",
		"turn_id":                 "conflicting-turn",
		"x-codex-window-id":       "conflicting-window:0",
		"x-codex-installation-id": "conflicting-installation",
		"x-openai-subagent":       "collab_spawn",
		"transport_marker":        true,
	})

	updated, state, err := NormalizeRequest(body, `{"thread_id":"header-conflict"}`, Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyPassthrough,
	})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if !state.CanonicalPresent || !state.Normalized {
		t.Fatalf("state = %+v, want canonical normalized", state)
	}
	if !state.HasSessionID || state.SessionID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("canonical session state = %+v", state)
	}
	if !isASCII([]byte(state.TurnMetadata)) {
		t.Fatalf("turn metadata is not ASCII-safe: %q", state.TurnMetadata)
	}
	if strings.Contains(state.TurnMetadata, "secret") || strings.Contains(state.TurnMetadata, "token=leak") || strings.Contains(state.TurnMetadata, "#fragment") {
		t.Fatalf("turn metadata leaked remote credential: %s", state.TurnMetadata)
	}
	if !strings.Contains(state.TurnMetadata, `"origin":"https://example.com/org/repo.git"`) {
		t.Fatalf("sanitized origin missing: %s", state.TurnMetadata)
	}
	if !strings.Contains(state.TurnMetadata, `"upstream":"ssh://example.com/upstream/repo.git"`) {
		t.Fatalf("sanitized scp remote missing: %s", state.TurnMetadata)
	}
	if !strings.Contains(state.TurnMetadata, `\u9879\u76ee`) {
		t.Fatalf("non-ASCII workspace_kind was not escaped: %s", state.TurnMetadata)
	}

	clientMetadata := decodeClientMetadata(t, updated)
	assertJSONString(t, clientMetadata, "x-codex-installation-id", "11111111-1111-4111-8111-111111111111")
	assertJSONString(t, clientMetadata, "session_id", "22222222-2222-4222-8222-222222222222")
	assertJSONString(t, clientMetadata, "thread_id", "22222222-2222-4222-8222-222222222222")
	assertJSONString(t, clientMetadata, "turn_id", "33333333-3333-4333-8333-333333333333")
	assertJSONString(t, clientMetadata, "x-codex-window-id", "22222222-2222-4222-8222-222222222222:1")
	if _, ok := clientMetadata["transport_marker"]; !ok {
		t.Fatal("transport-specific client_metadata field was dropped")
	}

	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"thread_id":"old"}`)
	headers.Set("X-Codex-Window-Id", "old:0")
	headers.Set("X-Codex-Parent-Thread-Id", "old-parent")
	headers.Set("X-OpenAI-Subagent", "old-subagent")
	headers.Set("X-ResponsesAPI-Test-Marker", "keep")
	state.ApplyHeaders(headers)
	if got := headers.Get("X-Codex-Turn-Metadata"); got != state.TurnMetadata {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want canonical state", got)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != "22222222-2222-4222-8222-222222222222:1" {
		t.Fatalf("X-Codex-Window-Id = %q", got)
	}
	if got := headers.Get("X-Codex-Parent-Thread-Id"); got != "" {
		t.Fatalf("X-Codex-Parent-Thread-Id = %q, want deleted", got)
	}
	if got := headers.Get("X-OpenAI-Subagent"); got != "collab_spawn" {
		t.Fatalf("X-OpenAI-Subagent = %q, want body compatibility projection", got)
	}
	if got := headers.Get("X-ResponsesAPI-Test-Marker"); got != "keep" {
		t.Fatalf("transport header = %q, want keep", got)
	}
}

func TestNormalizeRequestUsesDirectCanonicalHeaderAsLegacyFallback(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","input":"hello"}`)
	updated, state, err := NormalizeRequest(body, sampleTurnMetadata, Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyDrop,
	})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if !state.CanonicalPresent || !state.Normalized {
		t.Fatalf("state = %+v, want normalized header fallback", state)
	}
	clientMetadata := decodeClientMetadata(t, updated)
	assertJSONString(t, clientMetadata, "thread_id", "22222222-2222-4222-8222-222222222222")
	if strings.Contains(state.TurnMetadata, `"workspaces"`) {
		t.Fatalf("workspace drop was not applied to header fallback: %s", state.TurnMetadata)
	}
}

func TestNormalizeRequestUsesThreadIDAsSessionFallback(t *testing.T) {
	body := requestBodyWithMetadata(t, `{"request_kind":"turn","thread_id":"thread-only"}`, nil)
	_, state, err := NormalizeRequest(body, "", Policy{Mode: ModeRepair})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if !state.HasSessionID || state.SessionID != "thread-only" {
		t.Fatalf("canonical session fallback = %+v", state)
	}
}

func TestNormalizeRequestPreservesFlatIdentityWhenCanonicalOmitsConditionalFields(t *testing.T) {
	canonical := `{"request_kind":"memory","workspace_kind":"memory"}`
	body := requestBodyWithMetadata(t, canonical, map[string]any{
		"x-codex-installation-id": "install-flat",
		"session_id":              "session-flat",
		"thread_id":               "thread-flat",
		"turn_id":                 "turn-flat",
		"x-codex-window-id":       "thread-flat:3",
	})
	updated, state, err := NormalizeRequest(body, "", Policy{Mode: ModeRepair})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if state.HasWindowID {
		t.Fatalf("state unexpectedly derived a window from conditional canonical metadata: %+v", state)
	}
	clientMetadata := decodeClientMetadata(t, updated)
	assertJSONString(t, clientMetadata, "x-codex-installation-id", "install-flat")
	assertJSONString(t, clientMetadata, "session_id", "session-flat")
	assertJSONString(t, clientMetadata, "thread_id", "thread-flat")
	assertJSONString(t, clientMetadata, "turn_id", "turn-flat")
	assertJSONString(t, clientMetadata, "x-codex-window-id", "thread-flat:3")

	headers := http.Header{"X-Codex-Window-Id": {"thread-flat:3"}}
	state.ApplyHeaders(headers)
	if got := headers.Get("X-Codex-Window-Id"); got != "thread-flat:3" {
		t.Fatalf("conditional flat window header = %q, want preserved", got)
	}
}

func TestNormalizeRequestWorkspaceRedactAndDrop(t *testing.T) {
	body := requestBodyWithMetadata(t, sampleTurnMetadata, nil)

	redacted, state, err := NormalizeRequest(body, "", Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyRedact,
		Scope:           "credential:stable-account",
	})
	if err != nil {
		t.Fatalf("NormalizeRequest(redact) error = %v", err)
	}
	if strings.Contains(state.TurnMetadata, "/Users/example/project") || strings.Contains(state.TurnMetadata, "example.com") || strings.Contains(state.TurnMetadata, "01234567") {
		t.Fatalf("redacted metadata leaked workspace identity: %s", state.TurnMetadata)
	}
	if !strings.Contains(state.TurnMetadata, `"workspace:`) || !strings.Contains(state.TurnMetadata, `"has_changes":false`) {
		t.Fatalf("redacted workspace shape missing: %s", state.TurnMetadata)
	}
	redactedAgain, stateAgain, err := NormalizeRequest(body, "", Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyRedact,
		Scope:           "credential:stable-account",
	})
	if err != nil || stateAgain.TurnMetadata != state.TurnMetadata || string(redactedAgain) != string(redacted) {
		t.Fatal("workspace redaction is not deterministic")
	}
	_, differentCredential, err := NormalizeRequest(body, "", Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyRedact,
		Scope:           "credential:other-account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if differentCredential.TurnMetadata == state.TurnMetadata {
		t.Fatal("workspace pseudonym did not change across credential scopes")
	}
	differentInstallationBody := bytes.Replace(body, []byte("11111111-1111-4111-8111-111111111111"), []byte("44444444-4444-4444-8444-444444444444"), -1)
	_, differentInstallation, err := NormalizeRequest(differentInstallationBody, "", Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyRedact,
		Scope:           "credential:stable-account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if differentInstallation.TurnMetadata == state.TurnMetadata {
		t.Fatal("workspace pseudonym did not change across client installations")
	}

	_, droppedState, err := NormalizeRequest(body, "", Policy{
		Mode:            ModeRepair,
		WorkspacePolicy: WorkspacePolicyDrop,
	})
	if err != nil {
		t.Fatalf("NormalizeRequest(drop) error = %v", err)
	}
	if strings.Contains(droppedState.TurnMetadata, `"workspaces"`) {
		t.Fatalf("drop policy retained workspaces: %s", droppedState.TurnMetadata)
	}
}

func TestNormalizeRequestStrictRejectsConflictAndMalformedCanonical(t *testing.T) {
	body := requestBodyWithMetadata(t, sampleTurnMetadata, map[string]any{"thread_id": "wrong"})
	if _, _, err := NormalizeRequest(body, "", Policy{Mode: ModeStrict}); err == nil {
		t.Fatal("strict mode accepted conflicting flat projection")
	}

	malformedBody := requestBodyWithRawCanonical(t, `{"thread_id":"a","thread_id":"b"}`)
	if _, _, err := NormalizeRequest(malformedBody, "", Policy{Mode: ModeRepair}); err == nil {
		t.Fatal("repair mode accepted duplicate canonical key")
	}

	wrongTypeBody := requestBodyWithRawCanonical(t, `{"request_kind":"turn","thread_id":123}`)
	if _, _, err := NormalizeRequest(wrongTypeBody, "", Policy{Mode: ModeRepair}); err == nil {
		t.Fatal("repair mode accepted non-string canonical thread_id")
	}
}

func TestNormalizeRequestStrictComparesCanonicalMetadataSemantically(t *testing.T) {
	bodyCanonical := `{"request_kind":"turn","thread_id":"thread-1","turn_started_at_unix_ms":1700000000000}`
	headerCanonical := `{"turn_started_at_unix_ms":1700000000000,"thread_id":"thread-1","request_kind":"turn"}`
	body := requestBodyWithMetadata(t, bodyCanonical, map[string]any{"thread_id": "thread-1"})
	if _, _, err := NormalizeRequest(body, headerCanonical, Policy{Mode: ModeStrict}); err != nil {
		t.Fatalf("strict mode rejected semantically equal canonical metadata: %v", err)
	}
}

func TestNormalizeRequestOffSkipsMutationButMarksCanonical(t *testing.T) {
	body := requestBodyWithMetadata(t, sampleTurnMetadata, map[string]any{"thread_id": "wrong"})
	updated, state, err := NormalizeRequest(body, "", Policy{Mode: ModeOff, WorkspacePolicy: WorkspacePolicyDrop})
	if err != nil {
		t.Fatalf("NormalizeRequest(off) error = %v", err)
	}
	if !state.CanonicalPresent || state.Normalized {
		t.Fatalf("state = %+v, want canonical present without normalization", state)
	}
	if string(updated) != string(body) {
		t.Fatal("off mode mutated request body")
	}
}

func TestNormalizeRequestOffDoesNotRejectMalformedMetadata(t *testing.T) {
	body := requestBodyWithRawCanonical(t, `{"request_kind":"turn"`)
	updated, state, err := NormalizeRequest(body, "", Policy{Mode: ModeOff})
	if err != nil {
		t.Fatalf("NormalizeRequest(off) error = %v", err)
	}
	if state.CanonicalPresent {
		t.Fatalf("state = %+v, malformed metadata must not be classified as canonical", state)
	}
	if !bytes.Equal(updated, body) {
		t.Fatal("off mode mutated malformed metadata")
	}
}

func TestNormalizeRequestIgnoresUnrelatedNonObjectClientMetadata(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","client_metadata":"legacy-client-value"}`)
	updated, state, err := NormalizeRequest(body, "", Policy{Mode: ModeRepair})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if state.CanonicalPresent || !bytes.Equal(updated, body) {
		t.Fatalf("unrelated client_metadata changed: state=%+v body=%s", state, updated)
	}
}

func TestNormalizeRequestRejectsDuplicateClientMetadataCarriers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"thread_id\":\"thread-1\"}"},"client_metadata":{"transport_marker":"last"}}`)
	if _, _, err := NormalizeRequest(body, "", Policy{Mode: ModeRepair}); err == nil {
		t.Fatal("repair mode accepted ambiguous duplicate client_metadata carriers")
	}
}

func TestNormalizeRequestOffMarksCanonicalAcrossDuplicateClientMetadataCarriers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"thread_id\":\"thread-1\"}"},"client_metadata":{"transport_marker":"last"}}`)
	updated, state, err := NormalizeRequest(body, "", Policy{Mode: ModeOff})
	if err != nil {
		t.Fatalf("NormalizeRequest(off) error = %v", err)
	}
	if !state.CanonicalPresent || !bytes.Equal(updated, body) {
		t.Fatalf("off mode lost duplicate-carrier canonical detection: state=%+v body=%s", state, updated)
	}
}

func requestBodyWithMetadata(t *testing.T, canonical string, extra map[string]any) []byte {
	t.Helper()
	clientMetadata := map[string]any{"x-codex-turn-metadata": canonical}
	for key, value := range extra {
		clientMetadata[key] = value
	}
	body, err := json.Marshal(map[string]any{
		"model":           "gpt-5.6",
		"client_metadata": clientMetadata,
		"input":           "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func requestBodyWithRawCanonical(t *testing.T, canonical string) []byte {
	t.Helper()
	return requestBodyWithMetadata(t, canonical, nil)
}

func decodeClientMetadata(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var clientMetadata map[string]json.RawMessage
	if err := json.Unmarshal(envelope["client_metadata"], &clientMetadata); err != nil {
		t.Fatal(err)
	}
	return clientMetadata
}

func assertJSONString(t *testing.T, object map[string]json.RawMessage, key, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(object[key], &got); err != nil {
		t.Fatalf("%s is not a string: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
