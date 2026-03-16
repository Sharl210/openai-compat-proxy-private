package httpapi

func nestedCachedTokens(usage map[string]any) any {
	if len(usage) == 0 {
		return nil
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if cachedTokens, ok := details["cached_tokens"]; ok {
			return cachedTokens
		}
	}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		if cachedTokens, ok := details["cached_tokens"]; ok {
			return cachedTokens
		}
	}
	return nil
}
