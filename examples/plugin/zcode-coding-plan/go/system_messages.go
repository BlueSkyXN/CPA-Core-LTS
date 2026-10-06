package main

import "strings"

func validateSystemMessageContent(content any) error {
	if _, ok := content.(string); ok {
		return nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return problem(400, "unsupported_content", "Unsupported system message content")
	}
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			return problem(400, "unsupported_content", "Invalid system message block")
		}
		switch block["type"] {
		case "text":
			if _, ok := block["text"].(string); !ok {
				return problem(400, "invalid_request", "Invalid system message text")
			}
		case "tool_addition", "tool_removal":
			if err := validateSystemToolChange(block); err != nil {
				return err
			}
		default:
			return problem(400, "unsupported_content", "System messages support text and tool changes")
		}
	}
	return nil
}

func validateSystemToolChange(block map[string]any) error {
	tool, ok := block["tool"].(map[string]any)
	if !ok {
		return problem(400, "invalid_request", "System tool change requires a tool object")
	}
	require := func(name string) error {
		value, ok := tool[name].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return problem(400, "invalid_request", "System tool reference requires non-empty "+name)
		}
		return nil
	}
	switch tool["type"] {
	case "tool_definition":
		if block["type"] != "tool_addition" {
			return problem(400, "invalid_request", "Tool definitions require tool_addition")
		}
		// Inline definitions must not bypass the same tool policy as top-level tools.
		if err := validateTools(map[string]any{"tools": []any{tool["definition"]}}); err != nil {
			api := safeError(err)
			return problem(api.Status, api.Code, "System tool_addition: "+api.Message)
		}
		return nil
	case "tool_reference":
		return require("name")
	case "mcp_tool_reference":
		if err := require("name"); err != nil {
			return err
		}
		return require("server_name")
	case "mcp_toolset_reference":
		return require("server_name")
	default:
		return problem(400, "unsupported_content", "Unsupported system tool reference")
	}
}
