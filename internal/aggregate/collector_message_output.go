package aggregate

func cloneMessageOutputItemForAggregation(item map[string]any, aggregatedText string, hasTextDelta bool) map[string]any {
	clonedItem := cloneOutputItem(item)
	content, _ := item["content"].([]any)
	if len(content) == 0 {
		return clonedItem
	}

	clonedContent := make([]any, len(content))
	outputTextIndex := -1
	for index, rawPart := range content {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			clonedContent[index] = rawPart
			continue
		}
		clonedPart := cloneOutputItem(part)
		clonedContent[index] = clonedPart
		if partType, _ := part["type"].(string); partType == "output_text" {
			if outputTextIndex >= 0 {
				outputTextIndex = -1
				continue
			}
			outputTextIndex = index
		}
	}
	if hasTextDelta && outputTextIndex >= 0 {
		outputText := clonedContent[outputTextIndex].(map[string]any)
		if text, _ := outputText["text"].(string); text == aggregatedText {
			outputText["text"] = aggregatedText
		}
	}
	clonedItem["content"] = clonedContent
	return clonedItem
}
