package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

func TestChatEventWriterFormatsAdjacentReasoningTitlesAcrossEventFamilies(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		toolIDAliases:    map[string]string{},
		toolMeta:         map[string]map[string]string{},
		toolIndex:        map[string]int{},
		toolSent:         map[string]bool{},
		pendingToolArgs:  map[string]string{},
		thinkingTagStyle: "",
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "chat",
		upstreamEndpointType: config.UpstreamEndpointTypeResponses,
		toolIDAliases:        map[string]string{},
		toolItems:            map[string]*responsesToolItemState{},
	}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)

	nativeSummary := map[string]any{"item_id": "rs_native", "summary_index": 0, "delta": "**后续**"}
	for _, event := range []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: nativeSummary},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**标题****后续**"}},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"**标题**"`) || !strings.Contains(body, `"reasoning_content":"\n\n**后续**"`) {
		t.Fatalf("expected adjacent titles to remain separated across reasoning event families, got %s", body)
	}
	if got := stringValue(nativeSummary["item_id"]); got != "rs_native" {
		t.Fatalf("native item_id changed: got %q", got)
	}
	if strings.Contains(body, internalReasoningFormatItemIDKey) {
		t.Fatalf("internal formatting key leaked to Chat output: %s", body)
	}
	if strings.Contains(body, `"reasoning_content":"**标题**\n\n**后续**"`) {
		t.Fatalf("completed reasoning snapshot replayed after projected deltas: %s", body)
	}
}

func TestAnthropicEventWriterFormatsAdjacentReasoningTitlesAcrossEventFamilies(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &anthropicStreamState{
		pendingToolArgs:  map[string]string{},
		toolMeta:         map[string]map[string]string{},
		emittedToolItems: map[string]bool{},
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "anthropic",
		upstreamEndpointType: config.UpstreamEndpointTypeResponses,
		toolIDAliases:        map[string]string{},
		toolItems:            map[string]*responsesToolItemState{},
	}
	writer := NewAnthropicEventWriter(rec, nil, state, helper, nil)

	nativeSummary := map[string]any{"item_id": "rs_native", "summary_index": 0, "delta": "**后续**"}
	for _, event := range []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: nativeSummary},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**标题****后续**"}},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"**标题**"`) || !strings.Contains(body, `"thinking":"\n\n**后续**"`) {
		t.Fatalf("expected adjacent titles to remain separated across reasoning event families, got %s", body)
	}
	if got := stringValue(nativeSummary["item_id"]); got != "rs_native" {
		t.Fatalf("native item_id changed: got %q", got)
	}
	if strings.Contains(body, internalReasoningFormatItemIDKey) {
		t.Fatalf("internal formatting key leaked to Anthropic output: %s", body)
	}
	if strings.Contains(body, `"thinking":"**标题**\n\n**后续**"`) {
		t.Fatalf("completed reasoning snapshot replayed after projected deltas: %s", body)
	}
}

func TestChatEventWriterFormatsSeparateReasoningPhases(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		toolIDAliases:    map[string]string{},
		toolMeta:         map[string]map[string]string{},
		toolIndex:        map[string]int{},
		toolSent:         map[string]bool{},
		pendingToolArgs:  map[string]string{},
		thinkingTagStyle: "",
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "chat",
		upstreamEndpointType: config.UpstreamEndpointTypeResponses,
		toolIDAliases:        map[string]string{},
		toolItems:            map[string]*responsesToolItemState{},
	}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)

	for _, event := range []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一段标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_one", "summary_index": 0, "delta": "**第一段后续**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_one", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**第一段标题****第一段后续**"}},
		}}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第二段标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_two", "summary_index": 0, "delta": "**第二段后续**"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一段标题**"`,
		`"reasoning_content":"\n\n**第一段后续**"`,
		`"reasoning_content":"**第二段标题**"`,
		`"reasoning_content":"\n\n**第二段后续**"`,
	)
}

func TestAnthropicEventWriterFormatsSeparateReasoningPhases(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &anthropicStreamState{
		pendingToolArgs:  map[string]string{},
		toolMeta:         map[string]map[string]string{},
		emittedToolItems: map[string]bool{},
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "anthropic",
		upstreamEndpointType: config.UpstreamEndpointTypeResponses,
		toolIDAliases:        map[string]string{},
		toolItems:            map[string]*responsesToolItemState{},
	}
	writer := NewAnthropicEventWriter(rec, nil, state, helper, nil)

	for _, event := range []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一段标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_one", "summary_index": 0, "delta": "**第一段后续**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_one", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**第一段标题****第一段后续**"}},
		}}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第二段标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_two", "summary_index": 0, "delta": "**第二段后续**"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"thinking":"**第一段标题**"`,
		`"thinking":"\n\n**第一段后续**"`,
		`"thinking":"**第二段标题**"`,
		`"thinking":"\n\n**第二段后续**"`,
	)
}

func assertOrderedStreamFragments(t *testing.T, body string, fragments ...string) {
	t.Helper()
	previousIndex := -1
	for _, fragment := range fragments {
		index := strings.Index(body, fragment)
		if index < 0 {
			t.Fatalf("missing stream fragment %q in %s", fragment, body)
		}
		if index <= previousIndex {
			t.Fatalf("stream fragments are out of order at %q in %s", fragment, body)
		}
		previousIndex = index
	}
}
