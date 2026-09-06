package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesControls describes input controls whose meaning cannot be represented
// by Chat Completions messages. It does not run tools or own conversation state.
type ResponsesControls struct {
	HasConfigurationUpdate bool
	HasCompactionTrigger   bool
	HasAsyncTools          bool
	// ConfigurationEffort is the last explicit update in the supplied input,
	// never an inference about state retained behind previous_response_id.
	ConfigurationEffort string
}

// InspectResponsesControls reads controls without rewriting the request. In
// particular, request-level reasoning.effort and input order remain unchanged.
// Unknown model-defined effort values are left to the selected upstream.
func InspectResponsesControls(body []byte) (ResponsesControls, error) {
	var controls ResponsesControls
	var request struct {
		Input json.RawMessage `json:"input"`
		Tools []struct {
			Type  string          `json:"type"`
			Async json.RawMessage `json:"async"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return controls, fmt.Errorf("invalid Responses control JSON")
	}
	for _, tool := range request.Tools {
		if (tool.Type == "function" || tool.Type == "custom") && bytes.Equal(bytes.TrimSpace(tool.Async), []byte("true")) {
			controls.HasAsyncTools = true
		}
	}
	input := bytes.TrimSpace(request.Input)
	if len(input) == 0 || input[0] != '[' {
		return controls, nil // string input is a valid Responses shorthand
	}
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return controls, fmt.Errorf("invalid Responses input array")
	}
	previousUpdate := false
	for index, raw := range items {
		// Inspect only the item discriminator; unrelated future item fields
		// must not be coerced through a fixed message schema.
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			previousUpdate = false
			continue
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		switch kind {
		case "configuration_update":
			controls.HasConfigurationUpdate = true
			if previousUpdate {
				return controls, fmt.Errorf("input[%d]: adjacent configuration_update items are not supported", index)
			}
			var reasoning struct {
				Effort string `json:"effort"`
			}
			if err := json.Unmarshal(item["reasoning"], &reasoning); err != nil || strings.TrimSpace(reasoning.Effort) == "" {
				return controls, fmt.Errorf("input[%d]: configuration_update requires a non-empty reasoning.effort string", index)
			}
			controls.ConfigurationEffort = strings.ToLower(strings.TrimSpace(reasoning.Effort))
		case "compaction_trigger":
			controls.HasCompactionTrigger = true
			controls.ConfigurationEffort = ""
		case "compaction", "context_compaction":
			// The control preceding this compacted history is not evidence of
			// the current window's effort. The caller must provide a fresh one.
			controls.ConfigurationEffort = ""
		}
		previousUpdate = kind == "configuration_update"
	}
	return controls, nil
}

// ValidateResponsesControls rejects combinations that would otherwise lose
// meaning when Codex strips unsupported automatic-compaction settings.
// Normal requests without configuration updates retain existing behavior.
func ValidateResponsesControls(body []byte, compact bool) error {
	controls, err := InspectResponsesControls(body)
	if err != nil || !controls.HasConfigurationUpdate {
		return err
	}
	if compact {
		return fmt.Errorf("configuration_update history is not supported by the standalone responses/compact endpoint")
	}
	var options struct {
		Truncation        string `json:"truncation"`
		ContextManagement []struct {
			Type string `json:"type"`
		} `json:"context_management"`
	}
	if err := json.Unmarshal(body, &options); err != nil {
		return fmt.Errorf("invalid Responses compaction options")
	}
	if options.Truncation == "auto" {
		return fmt.Errorf("configuration_update cannot be combined with automatic truncation")
	}
	for _, item := range options.ContextManagement {
		if item.Type == "compaction" {
			return fmt.Errorf("configuration_update cannot be combined with automatic compaction; use explicit compaction_trigger items")
		}
	}
	return nil
}

// RequireChatRepresentableResponses fails instead of silently losing controls
// when an executor's upstream is Chat Completions, not native Responses.
func RequireChatRepresentableResponses(body []byte) error {
	controls, err := InspectResponsesControls(body)
	if err != nil {
		return err
	}
	if controls.HasConfigurationUpdate || controls.HasCompactionTrigger || controls.HasAsyncTools {
		return fmt.Errorf("this provider uses Chat Completions; configuration_update, compaction_trigger and async tools require a native Responses provider")
	}
	return nil
}
