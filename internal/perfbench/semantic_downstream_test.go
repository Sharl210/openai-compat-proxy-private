package perfbench

import (
	"bytes"
	"fmt"
	"strings"
)

type semanticDownstreamResult struct {
	ResponseID       string
	NormalizedOutput string
	Reasoning        []string
	Tools            []semanticTool
	Usage            map[string]int64
	FinishReason     string
	TerminalStatus   string
}

type semanticSSEEvent struct {
	Event string
	Data  []byte
}

func parseSemanticDownstream(protocol downstreamProtocol, contentType string, body []byte) (semanticDownstreamResult, error) {
	normalized, err := normalizeSemanticOutput(contentType, body)
	if err != nil {
		return semanticDownstreamResult{}, err
	}

	var result semanticDownstreamResult
	switch protocol {
	case downstreamResponses:
		result, err = parseSemanticResponses(body)
	case downstreamChat:
		result, err = parseSemanticChat(body)
	case downstreamMessages:
		result, err = parseSemanticMessages(body)
	default:
		return semanticDownstreamResult{}, fmt.Errorf("unsupported downstream protocol %q", protocol)
	}
	if err != nil {
		return semanticDownstreamResult{}, err
	}
	result.NormalizedOutput = normalized
	return result, nil
}

func appendSemanticText(parts []string, text string) []string {
	if len(parts) == 0 {
		return []string{text}
	}
	parts[len(parts)-1] += text
	return parts
}

func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte(":"))
}

func parseSemanticSSE(body []byte) []semanticSSEEvent {
	blocks := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n")
	events := make([]semanticSSEEvent, 0, len(blocks))
	for _, block := range blocks {
		var eventName string
		var dataLines []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(dataLines) > 0 {
			events = append(events, semanticSSEEvent{
				Event: eventName,
				Data:  []byte(strings.Join(dataLines, "\n")),
			})
		}
	}
	return events
}
