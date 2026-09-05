package pluginapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const AgentEventSchemaVersionV1 uint32 = 1

// AgentEventType identifies one normalized runner event. These events are an
// internal Plugin/Runner contract; defining them does not expose a public
// native Agent API.
type AgentEventType string

const (
	AgentEventSessionCreated     AgentEventType = "session.created"
	AgentEventTurnStarted        AgentEventType = "turn.started"
	AgentEventMessageDelta       AgentEventType = "message.delta"
	AgentEventReasoningDelta     AgentEventType = "reasoning.delta"
	AgentEventToolStarted        AgentEventType = "tool.started"
	AgentEventToolUpdated        AgentEventType = "tool.updated"
	AgentEventToolCompleted      AgentEventType = "tool.completed"
	AgentEventPermissionRequired AgentEventType = "permission.required"
	AgentEventUsageUpdated       AgentEventType = "usage.updated"
	AgentEventWarning            AgentEventType = "warning"
	AgentEventTurnCompleted      AgentEventType = "turn.completed"
	AgentEventTurnFailed         AgentEventType = "turn.failed"
	AgentEventTurnCancelled      AgentEventType = "turn.cancelled"
	AgentEventSessionClosed      AgentEventType = "session.closed"
)

// AgentTerminalState preserves runner lifecycle outcomes that must not be
// collapsed into a generic provider failure.
type AgentTerminalState string

const (
	AgentTerminalCompleted             AgentTerminalState = "completed"
	AgentTerminalFailed                AgentTerminalState = "failed"
	AgentTerminalCancelled             AgentTerminalState = "cancelled"
	AgentTerminalSessionClosed         AgentTerminalState = "session_closed"
	AgentTerminalRunnerLost            AgentTerminalState = "runner_lost"
	AgentTerminalPermissionDenied      AgentTerminalState = "permission_denied"
	AgentTerminalPermissionUnsupported AgentTerminalState = "permission_unsupported"
)

// AgentEventV1 is the stable, provider-neutral event envelope shared by Agent
// Plugins and external Runners. Payload is event-specific and must remain
// secret-safe before it crosses the Runner boundary.
type AgentEventV1 struct {
	SchemaVersion      uint32          `json:"schema_version"`
	Type               AgentEventType  `json:"type"`
	RequestID          string          `json:"request_id"`
	ExecutionSessionID string          `json:"execution_session_id,omitempty"`
	TurnID             string          `json:"turn_id,omitempty"`
	Provider           string          `json:"provider"`
	AuthID             string          `json:"auth_id,omitempty"`
	AuthIndex          string          `json:"auth_index,omitempty"`
	Sequence           uint64          `json:"sequence"`
	Timestamp          time.Time       `json:"timestamp"`
	Payload            json.RawMessage `json:"payload,omitempty"`
}

type AgentTextDeltaV1 struct {
	Text string `json:"text"`
}

type AgentTerminalPayloadV1 struct {
	// FinishReason preserves a model's stop/length/content_filter/tool_calls
	// result when an adapter represents a model completion as an agent turn.
	// It is optional; older runners keep the existing inferred behavior.
	FinishReason string             `json:"finish_reason,omitempty"`
	State        AgentTerminalState `json:"state"`
	Code         string             `json:"code,omitempty"`
	Message      string             `json:"message,omitempty"`
	Retryable    bool               `json:"retryable,omitempty"`
}

type AgentUsageV1 struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
	// Provenance distinguishes provider-reported values from estimates. The
	// exact values are intentionally open strings so the usage/LTS layer can
	// evolve additively without changing this event envelope.
	Provenance string `json:"provenance,omitempty"`
}

func (event AgentEventV1) Validate() error {
	if event.SchemaVersion != AgentEventSchemaVersionV1 {
		return fmt.Errorf("agent event schema_version %d is unsupported", event.SchemaVersion)
	}
	if !isKnownAgentEventType(event.Type) {
		return fmt.Errorf("agent event type %q is unsupported", event.Type)
	}
	if strings.TrimSpace(event.RequestID) == "" {
		return errors.New("agent event request_id is required")
	}
	if strings.TrimSpace(event.Provider) == "" {
		return errors.New("agent event provider is required")
	}
	if event.Sequence == 0 {
		return errors.New("agent event sequence must start at 1")
	}
	if event.Timestamp.IsZero() {
		return errors.New("agent event timestamp is required")
	}
	if event.IsTerminal() {
		_, errTerminal := event.TerminalState()
		return errTerminal
	}
	return nil
}

func (event AgentEventV1) IsTerminal() bool {
	switch event.Type {
	case AgentEventTurnCompleted, AgentEventTurnFailed, AgentEventTurnCancelled, AgentEventSessionClosed:
		return true
	default:
		return false
	}
}

func (event AgentEventV1) TerminalState() (AgentTerminalState, error) {
	if !event.IsTerminal() {
		return "", fmt.Errorf("agent event %q is not terminal", event.Type)
	}
	var payload AgentTerminalPayloadV1
	if errUnmarshal := json.Unmarshal(event.Payload, &payload); errUnmarshal != nil {
		return "", fmt.Errorf("decode agent terminal payload: %w", errUnmarshal)
	}
	if !terminalStateAllowedForEvent(event.Type, payload.State) {
		return "", fmt.Errorf("terminal state %q is invalid for event %q", payload.State, event.Type)
	}
	return payload.State, nil
}

func ValidateAgentEventSequence(events []AgentEventV1) error {
	if len(events) == 0 {
		return errors.New("agent event sequence is empty")
	}
	first := events[0]
	for i, event := range events {
		if errValidate := event.Validate(); errValidate != nil {
			return fmt.Errorf("agent event %d: %w", i, errValidate)
		}
		wantSequence := uint64(i + 1)
		if event.Sequence != wantSequence {
			return fmt.Errorf("agent event sequence %d is not contiguous, want %d", event.Sequence, wantSequence)
		}
		if event.RequestID != first.RequestID || event.Provider != first.Provider {
			return fmt.Errorf("agent event %d changed request/provider correlation", i)
		}
		if first.ExecutionSessionID != "" && event.ExecutionSessionID != "" && event.ExecutionSessionID != first.ExecutionSessionID {
			return fmt.Errorf("agent event %d changed execution_session_id", i)
		}
		if first.AuthID != "" && event.AuthID != "" && event.AuthID != first.AuthID {
			return fmt.Errorf("agent event %d changed auth_id", i)
		}
		if first.AuthIndex != "" && event.AuthIndex != "" && event.AuthIndex != first.AuthIndex {
			return fmt.Errorf("agent event %d changed auth_index", i)
		}
		if i < len(events)-1 && event.IsTerminal() {
			return fmt.Errorf("agent event %d is terminal before the end of the sequence", i)
		}
	}
	if !events[len(events)-1].IsTerminal() {
		return errors.New("agent event sequence has no terminal event")
	}
	return nil
}

func isKnownAgentEventType(eventType AgentEventType) bool {
	switch eventType {
	case AgentEventSessionCreated,
		AgentEventTurnStarted,
		AgentEventMessageDelta,
		AgentEventReasoningDelta,
		AgentEventToolStarted,
		AgentEventToolUpdated,
		AgentEventToolCompleted,
		AgentEventPermissionRequired,
		AgentEventUsageUpdated,
		AgentEventWarning,
		AgentEventTurnCompleted,
		AgentEventTurnFailed,
		AgentEventTurnCancelled,
		AgentEventSessionClosed:
		return true
	default:
		return false
	}
}

func terminalStateAllowedForEvent(eventType AgentEventType, state AgentTerminalState) bool {
	switch eventType {
	case AgentEventTurnCompleted:
		return state == AgentTerminalCompleted
	case AgentEventTurnCancelled:
		return state == AgentTerminalCancelled
	case AgentEventSessionClosed:
		return state == AgentTerminalSessionClosed
	case AgentEventTurnFailed:
		switch state {
		case AgentTerminalFailed, AgentTerminalRunnerLost, AgentTerminalPermissionDenied, AgentTerminalPermissionUnsupported:
			return true
		}
	}
	return false
}

type AgentPermissionOptionV1 struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

type AgentPermissionRequestV1 struct {
	PermissionID       string                    `json:"permission_id"`
	RequestID          string                    `json:"request_id"`
	ExecutionSessionID string                    `json:"execution_session_id,omitempty"`
	TurnID             string                    `json:"turn_id,omitempty"`
	Provider           string                    `json:"provider"`
	AuthID             string                    `json:"auth_id,omitempty"`
	AuthIndex          string                    `json:"auth_index,omitempty"`
	ToolName           string                    `json:"tool_name"`
	Options            []AgentPermissionOptionV1 `json:"options"`
}

// AgentFixedPermissionSelectionV1 names one exact vendor option ID for one
// exact tool. Prefix, substring, and arbitrary allow_* fallback matching are
// intentionally unsupported.
type AgentFixedPermissionSelectionV1 struct {
	ToolName string `json:"tool_name"`
	OptionID string `json:"option_id"`
}

type AgentFixedPermissionPolicyV1 struct {
	Selections []AgentFixedPermissionSelectionV1 `json:"selections"`
}

type AgentPermissionResolutionV1 struct {
	PermissionID       string `json:"permission_id"`
	RequestID          string `json:"request_id"`
	ExecutionSessionID string `json:"execution_session_id,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	Provider           string `json:"provider"`
	AuthID             string `json:"auth_id,omitempty"`
	AuthIndex          string `json:"auth_index,omitempty"`
	OptionID           string `json:"option_id"`
}

var (
	ErrAgentPermissionDenied      = errors.New("agent permission denied by fixed policy")
	ErrAgentPermissionUnsupported = errors.New("agent permission policy option is unavailable")
)

func ResolveFixedAgentPermission(request AgentPermissionRequestV1, policy AgentFixedPermissionPolicyV1) (AgentPermissionResolutionV1, error) {
	if strings.TrimSpace(request.PermissionID) == "" || strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.Provider) == "" || strings.TrimSpace(request.ToolName) == "" {
		return AgentPermissionResolutionV1{}, errors.New("agent permission correlation and tool_name are required")
	}
	options := make(map[string]struct{}, len(request.Options))
	for _, option := range request.Options {
		if id := strings.TrimSpace(option.ID); id != "" {
			options[id] = struct{}{}
		}
	}
	matchedRule := false
	for _, selection := range policy.Selections {
		if selection.ToolName != request.ToolName {
			continue
		}
		matchedRule = true
		optionID := strings.TrimSpace(selection.OptionID)
		if _, ok := options[optionID]; !ok {
			continue
		}
		return AgentPermissionResolutionV1{
			PermissionID:       request.PermissionID,
			RequestID:          request.RequestID,
			ExecutionSessionID: request.ExecutionSessionID,
			TurnID:             request.TurnID,
			Provider:           request.Provider,
			AuthID:             request.AuthID,
			AuthIndex:          request.AuthIndex,
			OptionID:           optionID,
		}, nil
	}
	if matchedRule {
		return AgentPermissionResolutionV1{}, ErrAgentPermissionUnsupported
	}
	return AgentPermissionResolutionV1{}, ErrAgentPermissionDenied
}
