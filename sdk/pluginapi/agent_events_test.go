package pluginapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateAgentEventSequence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	events := []AgentEventV1{
		newAgentEventForTest(1, AgentEventSessionCreated, map[string]any{"session_id": "native-1"}, now),
		newAgentEventForTest(2, AgentEventTurnStarted, map[string]any{}, now.Add(time.Millisecond)),
		newAgentEventForTest(3, AgentEventMessageDelta, AgentTextDeltaV1{Text: "ok"}, now.Add(2*time.Millisecond)),
		newAgentEventForTest(4, AgentEventTurnCompleted, AgentTerminalPayloadV1{State: AgentTerminalCompleted}, now.Add(3*time.Millisecond)),
	}
	if errValidate := ValidateAgentEventSequence(events); errValidate != nil {
		t.Fatalf("ValidateAgentEventSequence() error = %v", errValidate)
	}

	badSequence := append([]AgentEventV1(nil), events...)
	badSequence[2].Sequence = 4
	if errValidate := ValidateAgentEventSequence(badSequence); errValidate == nil || !strings.Contains(errValidate.Error(), "contiguous") {
		t.Fatalf("non-contiguous sequence error = %v", errValidate)
	}

	earlyTerminal := append([]AgentEventV1(nil), events...)
	earlyTerminal[1] = newAgentEventForTest(2, AgentEventTurnCancelled, AgentTerminalPayloadV1{State: AgentTerminalCancelled}, now.Add(time.Millisecond))
	if errValidate := ValidateAgentEventSequence(earlyTerminal); errValidate == nil || !strings.Contains(errValidate.Error(), "terminal before") {
		t.Fatalf("early terminal error = %v", errValidate)
	}
}

func TestAgentEventTerminalStateRejectsMismatchedState(t *testing.T) {
	event := newAgentEventForTest(1, AgentEventTurnCompleted, AgentTerminalPayloadV1{State: AgentTerminalFailed}, time.Now().UTC())
	if errValidate := event.Validate(); errValidate == nil || !strings.Contains(errValidate.Error(), "invalid") {
		t.Fatalf("Validate() error = %v", errValidate)
	}
}

func TestResolveFixedAgentPermissionUsesExactOptionID(t *testing.T) {
	request := AgentPermissionRequestV1{
		PermissionID:       "permission-1",
		RequestID:          "request-1",
		ExecutionSessionID: "session-1",
		TurnID:             "turn-1",
		Provider:           "qoder",
		AuthID:             "auth-1",
		AuthIndex:          "index-1",
		ToolName:           "write_file",
		Options: []AgentPermissionOptionV1{
			{ID: "allow_once"},
			{ID: "deny"},
		},
	}
	resolution, errResolve := ResolveFixedAgentPermission(request, AgentFixedPermissionPolicyV1{Selections: []AgentFixedPermissionSelectionV1{{
		ToolName: "write_file",
		OptionID: "allow_once",
	}}})
	if errResolve != nil {
		t.Fatalf("ResolveFixedAgentPermission() error = %v", errResolve)
	}
	if resolution.OptionID != "allow_once" || resolution.AuthIndex != request.AuthIndex || resolution.ExecutionSessionID != request.ExecutionSessionID {
		t.Fatalf("resolution = %#v", resolution)
	}

	_, errPrefix := ResolveFixedAgentPermission(request, AgentFixedPermissionPolicyV1{Selections: []AgentFixedPermissionSelectionV1{{
		ToolName: "write_file",
		OptionID: "allow",
	}}})
	if !errors.Is(errPrefix, ErrAgentPermissionUnsupported) {
		t.Fatalf("prefix option error = %v, want ErrAgentPermissionUnsupported", errPrefix)
	}

	_, errOtherTool := ResolveFixedAgentPermission(request, AgentFixedPermissionPolicyV1{Selections: []AgentFixedPermissionSelectionV1{{
		ToolName: "shell",
		OptionID: "allow_once",
	}}})
	if !errors.Is(errOtherTool, ErrAgentPermissionDenied) {
		t.Fatalf("other tool error = %v, want ErrAgentPermissionDenied", errOtherTool)
	}
}

func TestAgentEventJSONUsesStableSnakeCaseEnvelope(t *testing.T) {
	event := newAgentEventForTest(1, AgentEventMessageDelta, AgentTextDeltaV1{Text: "ok"}, time.Unix(1_800_000_000, 0).UTC())
	raw, errMarshal := json.Marshal(event)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	text := string(raw)
	for _, key := range []string{"\"schema_version\"", "\"request_id\"", "\"execution_session_id\"", "\"auth_index\"", "\"sequence\""} {
		if !strings.Contains(text, key) {
			t.Fatalf("event JSON %s missing %s", text, key)
		}
	}
	if strings.Contains(text, "RequestID") || strings.Contains(text, "StorageJSON") {
		t.Fatalf("event JSON leaked Go/credential field names: %s", text)
	}
}

func newAgentEventForTest(sequence uint64, eventType AgentEventType, payload any, timestamp time.Time) AgentEventV1 {
	raw, _ := json.Marshal(payload)
	return AgentEventV1{
		SchemaVersion:      AgentEventSchemaVersionV1,
		Type:               eventType,
		RequestID:          "request-1",
		ExecutionSessionID: "session-1",
		TurnID:             "turn-1",
		Provider:           "qoder",
		AuthID:             "auth-1",
		AuthIndex:          "index-1",
		Sequence:           sequence,
		Timestamp:          timestamp,
		Payload:            raw,
	}
}

func TestAgentTerminalFinishReasonIsOptionalAndRoundTrips(t *testing.T) {
	for _, reason := range []string{"", "length", "content_filter", "tool_calls"} {
		value := AgentTerminalPayloadV1{State: AgentTerminalCompleted, FinishReason: reason}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got AgentTerminalPayloadV1
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.FinishReason != reason {
			t.Fatalf("finish reason lost: %s", raw)
		}
		if reason == "" && strings.Contains(string(raw), "finish_reason") {
			t.Fatal("optional field changed legacy payload")
		}
	}
}
