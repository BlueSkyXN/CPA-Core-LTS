package main

import "slices"

// removeGatewayVisionDeclarations 清理旧网关工具定义和指向已删定义的引用。
// 同名普通工具和独立的 MCP 引用继续按原规则转发。
func removeGatewayVisionDeclarations(body map[string]any) {
	choice, _ := body["tool_choice"].(map[string]any)
	selectedName, _ := choice["name"].(string)
	var removed, hasRetained bool
	removedNames := make(map[string]struct{})
	retainedNames := make(map[string]struct{})
	retainTool := func(tool map[string]any) {
		name, _ := tool["name"].(string)
		hasRetained = true
		if name != "" {
			retainedNames[name] = struct{}{}
		}
	}
	removeDeclaration := func(value any) bool {
		tool, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if tool["type"] != gatewayVisionToolType {
			retainTool(tool)
			return false
		}
		name, _ := tool["name"].(string)
		removed = true
		if name != "" {
			removedNames[name] = struct{}{}
		}
		return true
	}
	if tools, ok := body["tools"].([]any); ok {
		kept := slices.DeleteFunc(tools, removeDeclaration)
		if len(kept) != len(tools) {
			if len(kept) == 0 {
				delete(body, "tools")
			} else {
				body["tools"] = kept
			}
		}
	}
	filterSystemToolChanges(body, func(kind string, tool map[string]any) bool {
		if kind != "tool_addition" {
			return false
		}
		switch tool["type"] {
		case "tool_definition":
			return removeDeclaration(tool["definition"])
		case "mcp_tool_reference", "mcp_toolset_reference":
			retainTool(tool)
		}
		return false
	})
	if !removed {
		return
	}

	filterSystemToolChanges(body, func(kind string, tool map[string]any) bool {
		if tool["type"] != "tool_reference" {
			return false
		}
		name, _ := tool["name"].(string)
		_, wasRemoved := removedNames[name]
		_, hasDefinition := retainedNames[name]
		if wasRemoved && !hasDefinition {
			return true
		}
		if kind == "tool_addition" {
			retainTool(tool)
		}
		return false
	})
	_, removedSelected := removedNames[selectedName]
	_, retainedSelected := retainedNames[selectedName]
	if !hasRetained || (choice["type"] == "tool" && removedSelected && !retainedSelected) {
		delete(body, "tool_choice")
	}
}

func filterSystemToolChanges(body map[string]any, remove func(string, map[string]any) bool) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	body["messages"] = slices.DeleteFunc(messages, func(value any) bool {
		message, _ := value.(map[string]any)
		if message["role"] != "system" {
			return false
		}
		content, _ := message["content"].([]any)
		kept := slices.DeleteFunc(content, func(value any) bool {
			block, _ := value.(map[string]any)
			kind, _ := block["type"].(string)
			if kind != "tool_addition" && kind != "tool_removal" {
				return false
			}
			tool, _ := block["tool"].(map[string]any)
			return remove(kind, tool)
		})
		if len(kept) == len(content) {
			return false
		}
		message["content"] = kept
		return len(kept) == 0 && len(message) == 2
	})
}
