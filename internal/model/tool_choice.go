package model

func DecodeOpenAIToolChoice(value any) CanonicalToolChoice {
	choice := CanonicalToolChoice{Raw: map[string]any{"value": value}}
	switch typed := value.(type) {
	case string:
		choice.Mode = typed
		choice.Requirement = toolChoiceRequirement(typed)
	case map[string]any:
		choice.Mode, _ = typed["type"].(string)
		choice.Requirement = toolChoiceRequirement(choice.Mode)
		if choice.Requirement == ToolChoiceRequiredNamed {
			choice.Name, _ = typed["name"].(string)
			if function, _ := typed["function"].(map[string]any); choice.Name == "" {
				choice.Name, _ = function["name"].(string)
			}
		}
	}
	return choice
}

func toolChoiceRequirement(kind string) ToolChoiceRequirement {
	switch kind {
	case "auto":
		return ToolChoiceOptional
	case "required", "any":
		return ToolChoiceRequiredAny
	case "function", "tool":
		return ToolChoiceRequiredNamed
	case "none":
		return ToolChoiceNone
	default:
		return ""
	}
}
