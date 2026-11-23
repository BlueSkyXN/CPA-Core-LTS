package main

import "slices"

// removeGatewayVisionDeclarations 清理旧客户端随请求携带的网关工具定义。
// 普通函数可以同名。工具选择只在其目标被移除或已无工具可选时清理。
func removeGatewayVisionDeclarations(body map[string]any) {
	choice, _ := body["tool_choice"].(map[string]any)
	selectedName, _ := choice["name"].(string)
	var removed, removedSelected, hasRetained, retainedSelected bool
	retainTool := func(tool map[string]any) {
		name, _ := tool["name"].(string)
		hasRetained = true
		retainedSelected = retainedSelected || name == selectedName
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
		removedSelected = removedSelected || name == selectedName
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
	if messages, ok := body["messages"].([]any); ok {
		body["messages"] = slices.DeleteFunc(messages, func(value any) bool {
			message, _ := value.(map[string]any)
			if message["role"] != "system" {
				return false
			}
			content, _ := message["content"].([]any)
			kept := slices.DeleteFunc(content, func(value any) bool {
				block, _ := value.(map[string]any)
				if block["type"] != "tool_addition" {
					return false
				}
				tool, _ := block["tool"].(map[string]any)
				switch tool["type"] {
				case "tool_definition":
					return removeDeclaration(tool["definition"])
				case "tool_reference", "mcp_tool_reference", "mcp_toolset_reference":
					retainTool(tool)
				}
				return false
			})
			if len(kept) == len(content) {
				return false
			}
			message["content"] = kept
			return len(kept) == 0 && len(message) == 2
		})
	}
	if (removed && !hasRetained) || (choice["type"] == "tool" && selectedName != "" && removedSelected && !retainedSelected) {
		delete(body, "tool_choice")
	}
}
