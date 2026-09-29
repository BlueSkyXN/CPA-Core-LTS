package main

func thinkingEffort(source map[string]any) (string, error) {
	effort := ""
	accept := func(value any) error {
		level, ok := value.(string)
		if !ok || (level != "low" && level != "high" && level != "max") {
			return problem(400, "unsupported_parameter", "Thinking effort must be low, high or max")
		}
		if effort != "" && effort != level {
			return problem(400, "invalid_request", "Conflicting thinking effort fields")
		}
		effort = level
		return nil
	}
	if value, exists := source["reasoning_effort"]; exists {
		if err := accept(value); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"reasoning", "output_config"} {
		value, exists := source[name]
		if !exists {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return "", problem(400, "invalid_request", "Thinking control must be an object")
		}
		if value, exists := object["effort"]; exists {
			if err := accept(value); err != nil {
				return "", err
			}
		}
	}
	return effort, nil
}

func applyThinkingControls(body, original map[string]any) error {
	effort, err := thinkingEffort(body)
	if err != nil {
		return err
	}
	requested, err := thinkingEffort(original)
	if err != nil {
		return err
	}
	// OriginalRequest is diagnostic context, never a source for restoring removed controls.
	if requested != "" && effort == "" {
		return problem(400, "unsupported_parameter", "Thinking effort was not preserved in the effective provider request")
	}
	for _, name := range []string{"output_config", "reasoning"} {
		if value, exists := body[name]; exists {
			object, ok := value.(map[string]any)
			if !ok {
				return problem(400, "invalid_request", "Thinking control must be an object")
			}
			for key := range object {
				if key != "effort" {
					return problem(400, "unsupported_parameter", "Unsupported thinking control")
				}
			}
		}
	}
	thinking, present := body["thinking"]
	if effort == "" && !present {
		return nil
	}
	model, _ := body["model"].(string)
	if _, known := builtinModelLimits(model); !known {
		return problem(400, "unsupported_parameter", "Explicit thinking controls require a built-in GLM model")
	}
	next := map[string]any{"type": "enabled"}
	if present {
		object, ok := thinking.(map[string]any)
		if !ok || (object["type"] != "enabled" && object["type"] != "adaptive") {
			return problem(400, "unsupported_parameter", "GLM thinking cannot be disabled")
		}
		for key, value := range object {
			switch key {
			case "type":
			case "clear_thinking":
				if _, ok := value.(bool); !ok {
					return problem(400, "invalid_request", "clear_thinking must be boolean")
				}
				next[key] = value
			case "display":
				return problem(400, "unsupported_parameter", "Reasoning summary display controls are not supported by this provider")
			default:
				return problem(400, "unsupported_parameter", "Manual thinking budget controls are not supported")
			}
		}
	}
	body["thinking"] = next
	if effort != "" {
		body["reasoning_effort"] = effort
	}
	delete(body, "output_config")
	delete(body, "reasoning")
	return nil
}
