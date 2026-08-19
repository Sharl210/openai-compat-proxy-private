package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

func TestChatEventWriterFormatsNativeReasoningSummaryPartsAcrossIndexes(t *testing.T) {
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
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "delta": "正文**标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "text": "正文**标题**"}},
		{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "正文**标题**"}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "summary_index": 1, "delta": "**后续**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "正文**标题****后续**"}},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"reasoning_content":"正文\n**标题**"`) || !strings.Contains(body, `"reasoning_content":"\n\n**后续**"`) {
		t.Fatalf("expected content and adjacent titles to remain separated across reasoning event families, got %s", body)
	}
	if strings.Contains(body, internalReasoningFormatItemIDKey) {
		t.Fatalf("internal formatting key leaked to Chat output: %s", body)
	}
	if strings.Contains(body, `"reasoning_content":"正文\n**标题**\n\n**后续**"`) {
		t.Fatalf("completed reasoning snapshot replayed after projected deltas: %s", body)
	}
}

func TestAnthropicEventWriterFormatsNativeReasoningSummaryPartsAcrossIndexes(t *testing.T) {
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
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_native", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "delta": "正文**标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "text": "正文**标题**"}},
		{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_native", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "正文**标题**"}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_native", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "summary_index": 1, "delta": "**后续**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "正文**标题****后续**"}},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"thinking":"正文\n**标题**"`) || !strings.Contains(body, `"thinking":"\n\n**后续**"`) {
		t.Fatalf("expected content and adjacent titles to remain separated across reasoning event families, got %s", body)
	}
	if strings.Contains(body, internalReasoningFormatItemIDKey) {
		t.Fatalf("internal formatting key leaked to Anthropic output: %s", body)
	}
	if strings.Contains(body, `"thinking":"正文\n**标题**\n\n**后续**"`) {
		t.Fatalf("completed reasoning snapshot replayed after projected deltas: %s", body)
	}
}

func TestChatEventWriterSeparatesTitlesWhenSummaryTextDoneHasNoPartDone(t *testing.T) {
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

	for _, event := range summaryTextDoneWithoutPartDoneEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一标题**"`,
		`"reasoning_content":"\n\n**第二标题**"`,
	)
}

func TestAnthropicEventWriterSeparatesTitlesWhenSummaryTextDoneHasNoPartDone(t *testing.T) {
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

	for _, event := range summaryTextDoneWithoutPartDoneEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"thinking":"**第一标题**"`,
		`"thinking":"\n\n**第二标题**"`,
	)
}

func TestResponsesEventWriterSeparatesTitlesWhenSummaryTextDoneHasNoPartDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, summaryTextDoneWithoutPartDoneEvents()...)

	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"\n\n**第二标题**"`,
	)
	if strings.Contains(body, "**第一标题****第二标题**") {
		t.Fatalf("completed reasoning snapshot retained adjacent titles: %s", body)
	}
}

func TestResponsesEventWriterSeparatesFragmentedTitleAfterSummaryTextDoneWithoutPartDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, summaryTextDoneWithoutPartDoneEvents("**第二", "标题**")...)

	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"\n\n**第二标题**"`,
	)
}

func TestChatEventWriterSeparatesDoneOnlySummaryTitlesAcrossIndexes(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolIDAliases: map[string]string{}, toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}, thinkingTagStyle: ""}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	for _, event := range doneOnlySummaryTitleEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	body := rec.Body.String()
	assertOrderedStreamFragments(t, body, `"reasoning_content":"**第一标题**"`, `"reasoning_content":"\n\n**第二标题**"`)
	if strings.Contains(body, `"reasoning_content":"**第一标题****第二标题**"`) {
		t.Fatalf("done-only summary titles remained adjacent: %s", body)
	}
}

func TestAnthropicEventWriterSeparatesDoneOnlySummaryTitlesAcrossIndexes(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &anthropicStreamState{pendingToolArgs: map[string]string{}, toolMeta: map[string]map[string]string{}, emittedToolItems: map[string]bool{}}
	helper := &responseEventWriterHelper{downstreamType: "anthropic", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewAnthropicEventWriter(rec, nil, state, helper, nil)
	for _, event := range doneOnlySummaryTitleEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	body := rec.Body.String()
	assertOrderedStreamFragments(t, body, `"thinking":"**第一标题**"`, `"thinking":"\n\n**第二标题**"`)
	if strings.Contains(body, `"thinking":"**第一标题****第二标题**"`) {
		t.Fatalf("done-only summary titles remained adjacent: %s", body)
	}
}
func TestChatEventWriterDoesNotSeparateTitleAfterInlineBoldSummary(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolIDAliases: map[string]string{}, toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	events := []upstream.Event{
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_inline", "summary_index": 0, "text": "正文**强调**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_inline", "summary_index": 1, "text": "**下一标题**"}},
	}
	for _, event := range events {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, `"reasoning_content":"\n\n**下一标题**"`) {
		t.Fatalf("inline bold summary incorrectly inherited title boundary: %s", body)
	}
}
func TestAnthropicEventWriterDoesNotSeparateTitleAfterInlineBoldSummary(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &anthropicStreamState{pendingToolArgs: map[string]string{}, toolMeta: map[string]map[string]string{}, emittedToolItems: map[string]bool{}}
	helper := &responseEventWriterHelper{downstreamType: "anthropic", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewAnthropicEventWriter(rec, nil, state, helper, nil)
	events := []upstream.Event{
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_inline", "summary_index": 0, "text": "正文**强调**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_inline", "summary_index": 1, "text": "**下一标题**"}},
	}
	for _, event := range events {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, `"thinking":"\n\n**下一标题**"`) {
		t.Fatalf("inline bold summary incorrectly inherited title boundary: %s", body)
	}
}

func TestResponsesEventWriterHandlesLateSummaryPartDoneAfterSummaryTextDone(t *testing.T) {
	events := summaryTextDoneWithoutPartDoneEvents()
	latePartDone := upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{
		"item_id":       "rs_missing_part_done",
		"summary_index": 0,
		"part":          map[string]any{"type": "summary_text", "text": "**第一标题**"},
	}}
	events = append(events[:6], append([]upstream.Event{latePartDone}, events[6:]...)...)
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, events...)

	if count := strings.Count(body, `"delta":"\n\n**第二标题**"`); count != 1 {
		t.Fatalf("expected one separated second-title delta after late part.done, got %d: %s", count, body)
	}
}

func TestResponsesEventWriterPreservesOpaqueReasoningWhenSummaryTextDoneHasNoPartDone(t *testing.T) {
	events := summaryTextDoneWithoutPartDoneEvents()
	for index := range events {
		events[index].Data["encrypted_content"] = "enc_payload"
		if item, _ := events[index].Data["item"].(map[string]any); item != nil {
			item["encrypted_content"] = "enc_payload"
		}
	}
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, events...)

	if strings.Contains(body, `"delta":"\n\n**第二标题**"`) {
		t.Fatalf("opaque reasoning delta was reformatted: %s", body)
	}
	if !strings.Contains(body, `"text":"**第一标题****第二标题**"`) {
		t.Fatalf("opaque reasoning snapshot changed, got %s", body)
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

func TestChatEventWriterSeparatesAlternatingReasoningHeadingDeltas(t *testing.T) {
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

	for _, event := range alternatingReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertAlternatingReasoningHeadingDeltas(t, rec.Body.String(), "reasoning_content")
}

func TestAnthropicEventWriterSeparatesAlternatingReasoningHeadingDeltas(t *testing.T) {
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

	for _, event := range alternatingReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertAlternatingReasoningHeadingDeltas(t, rec.Body.String(), "thinking")
}

func TestResponsesEventWriterSeparatesAlternatingReasoningHeadingDeltas(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, alternatingReasoningHeadingEvents()...)

	assertAlternatingReasoningHeadingDeltas(t, body, "delta")
}

func TestResponsesEventWriterSeparatesReasoningTitlesAfterContent(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "正文**第一标题****第二标题**"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)

	assertOrderedStreamFragments(t, body, `"delta":"正文\n**第一标题**\n\n**第二标题**"`)
}

func TestResponsesEventWriterSeparatesTitlesAcrossSummaryIndexesWithoutDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_indexes", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_indexes", "summary_index": 0, "delta": "**第一标题**"}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_indexes", "summary_index": 1, "delta": "**第二标题**"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_indexes", "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": "**第一标题**"},
				map[string]any{"type": "summary_text", "text": "**第二标题**"},
			},
		}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)

	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"\n\n**第二标题**"`,
	)
	if strings.Contains(body, `"delta":"**第一标题****第二标题**"`) {
		t.Fatalf("streamed summary indexes retained adjacent titles: %s", body)
	}
}

func TestReasoningTitleStatesDoNotCrossItemsOrRewriteOpaqueContent(t *testing.T) {
	helper := &responseEventWriterHelper{}
	if got := helper.formatReasoningContentDelta("rs_one", 0, "**第一标题**", false); got != "**第一标题**" {
		t.Fatalf("first stream delta=%q, want heading unchanged", got)
	}
	if got := helper.formatReasoningContentDelta("rs_two", 0, "**第二标题**", false); got != "**第二标题**" {
		t.Fatalf("separate stream delta=%q, want heading unchanged", got)
	}
	if got := helper.formatReasoningContentDelta("rs_one", 0, "**第一后续**", false); got != "\n\n**第一后续**" {
		t.Fatalf("same stream adjacent heading=%q, want separated heading", got)
	}

	state := &reasoningTextState{}
	if got := state.formatDelta("**签名标题**", true); got != "**签名标题**" {
		t.Fatalf("opaque delta=%q, want unchanged", got)
	}
	if got := state.formatDelta("**签名后续**", true); got != "**签名后续**" {
		t.Fatalf("opaque adjacent delta=%q, want unchanged", got)
	}
}

func TestReasoningTitleStateDefersCrossDeltaHeadingUntilClosed(t *testing.T) {
	state := &reasoningTextState{}
	if got := state.formatDelta("**第一标题**", false); got != "**第一标题**" {
		t.Fatalf("first title=%q, want first title unchanged", got)
	}
	if got := state.formatDelta("**第二标题", false); got != "" {
		t.Fatalf("unclosed adjacent title=%q, want deferred output", got)
	}
	if got := state.formatDelta("**", false); got != "\n\n**第二标题**" {
		t.Fatalf("closed adjacent title=%q, want separated second title", got)
	}
	if got := state.formatted.String(); got != "**第一标题**\n\n**第二标题**" {
		t.Fatalf("formatted stream=%q, want separated titles", got)
	}
}

func TestReasoningTitleStateFlushesDeferredHeadingFromSnapshot(t *testing.T) {
	state := &reasoningTextState{}
	if got := state.formatDelta("**第一标题**", false); got != "**第一标题**" {
		t.Fatalf("first title=%q, want first title unchanged", got)
	}
	if got := state.formatDelta("**第二标题", false); got != "" {
		t.Fatalf("unclosed adjacent title=%q, want deferred output", got)
	}
	if got, handled := state.formatSnapshot("**第一标题****第二标题**", false); !handled || got != "\n\n**第二标题**" {
		t.Fatalf("snapshot delta=%q handled=%t, want separated snapshot suffix", got, handled)
	}
	if got := state.finish(); got != "" {
		t.Fatalf("finish=%q, want no duplicate output", got)
	}
}

func TestReasoningTextHasTrailingBoldSpan(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "single title", text: "**标题**", want: true},
		{name: "single ASCII title", text: "**A**", want: true},
		{name: "six star formatted tail", text: "**A**\n\n****B**", want: true},
		{name: "eight star formatted tail", text: "**A**\n\n****\n\n**B**", want: true},
		{name: "empty marker after title", text: "**A**\n\n****", want: true},
		{name: "title followed by inline emphasis", text: "**标题**正文**强调**", want: false},
		{name: "plain text", text: "普通正文", want: false},
		{name: "empty marker", text: "****", want: false},
		{name: "opaque-looking stars only", text: "********", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reasoningTextHasTrailingBoldSpan(test.text); got != test.want {
				t.Fatalf("reasoningTextHasTrailingBoldSpan(%q)=%t, want %t", test.text, got, test.want)
			}
		})
	}
}

func TestResponsesEventWriterPreservesStandaloneSummaryTitleAtPartBoundary(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, standaloneSummaryTitleEvents()...)
	assertOrderedStreamFragments(t, body, `"delta":"**标题**"`)
}

func TestResponsesEventWriterFlushesDeferredReasoningTitleAtTerminal(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, deferredReasoningHeadingEvents()...)
	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"**未闭合标题"`,
		`"status":"completed"`,
	)
}

func TestChatEventWriterFlushesDeferredReasoningTitleAtTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		toolIDAliases: map[string]string{},
		toolMeta:      map[string]map[string]string{},
		toolIndex:     map[string]int{},
		toolSent:      map[string]bool{},

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
	for _, event := range deferredReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一标题**"`,
		`"reasoning_content":"**未闭合标题"`,
		`"finish_reason":"stop"`,
	)
}

func TestAnthropicEventWriterFlushesDeferredReasoningTitleBeforeSignature(t *testing.T) {
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
	for _, event := range deferredReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"thinking":"**第一标题**"`,
		`"thinking":"**未闭合标题"`,
		`"type":"signature_delta"`,
	)
}

func TestResponsesEventWriterSeparatesCrossDeltaReasoningTitles(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses, splitAdjacentReasoningHeadingEvents()...)
	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"\n\n**第二标题**"`,
	)
}

func TestChatEventWriterSeparatesCrossDeltaReasoningTitles(t *testing.T) {
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
	for _, event := range splitAdjacentReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一标题**"`,
		`"reasoning_content":"\n\n**第二标题**"`,
	)
}

func TestAnthropicEventWriterSeparatesCrossDeltaReasoningTitles(t *testing.T) {
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
	for _, event := range splitAdjacentReasoningHeadingEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"thinking":"**第一标题**"`,
		`"thinking":"\n\n**第二标题**"`,
	)
}

func TestChatEventWriterSeparatesTitleAroundEmptyBoldMarkerAcrossSummaryParts(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{
		toolIDAliases:   map[string]string{},
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "chat",
		upstreamEndpointType: config.UpstreamEndpointTypeResponses,
		toolIDAliases:        map[string]string{},
		toolItems:            map[string]*responsesToolItemState{},
	}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	for _, event := range crossPartEmptyMarkerEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一标题**"`,
		`"reasoning_content":"\n\n****"`,
		`"reasoning_content":"\n\n**第二标题**"`,
	)
}

func TestAnthropicEventWriterSeparatesTitleAroundEmptyBoldMarkerAcrossSummaryParts(t *testing.T) {
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
	for _, event := range crossPartEmptyMarkerEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	assertOrderedStreamFragments(t, rec.Body.String(),
		`"thinking":"**第一标题**"`,
		`"thinking":"\n\n****"`,
		`"thinking":"\n\n**第二标题**"`,
	)
}

func TestResponsesEventWriterFlushesDeferredReasoningTitleBeforeText(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一标题**"}},
		upstream.Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**未闭合标题"}},
		upstream.Event{Event: "response.output_text.delta", Data: map[string]any{"delta": "正文"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	assertOrderedStreamFragments(t, body,
		`"delta":"**第一标题**"`,
		`"delta":"**未闭合标题"`,
		`"delta":"正文"`,
	)
}

func TestResponsesEventWriterPreservesOpaqueReasoningSnapshot(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{
			"item": map[string]any{
				"id":                "rs_encrypted",
				"type":              "reasoning",
				"encrypted_content": "enc_payload",
				"summary": []any{map[string]any{
					"type": "summary_text",
					"text": "**第一标题****第二标题**",
				}},
			},
		}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	if !strings.Contains(body, `"text":"**第一标题****第二标题**"`) {
		t.Fatalf("expected encrypted reasoning snapshot bytes unchanged, got %s", body)
	}
	if strings.Contains(body, `"text":"**第一标题**\n\n**第二标题**"`) {
		t.Fatalf("encrypted reasoning snapshot was reformatted, got %s", body)
	}
}

func TestResponsesEventWriterFlushesReasoningStatesByOutputOrder(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_z", "output_index": 0, "summary_index": 0, "delta": "**第一未闭合"}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_a", "output_index": 1, "summary_index": 0, "delta": "**第二未闭合"}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	assertOrderedStreamFragments(t, body,
		`"delta":"**第一未闭合"`,
		`"delta":"**第二未闭合"`,
	)
}

func TestResponsesEventWriterFlushesDeferredReasoningTitleFromEmptyItemDone(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty", "summary_index": 0, "delta": "**未闭合标题"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_empty", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	if !strings.Contains(body, `"delta":"**未闭合标题"`) {
		t.Fatalf("expected empty item.done to flush deferred title, got %s", body)
	}
}

func TestChatEventWriterFlushesDeferredReasoningTitleFromEmptyItemDone(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &chatStreamState{toolIDAliases: map[string]string{}, toolMeta: map[string]map[string]string{}, toolIndex: map[string]int{}, toolSent: map[string]bool{}, pendingToolArgs: map[string]string{}}
	helper := &responseEventWriterHelper{downstreamType: "chat", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewChatEventWriter(rec, nil, state, helper, nil)
	for _, event := range emptyReasoningItemDoneEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	if !strings.Contains(rec.Body.String(), `"reasoning_content":"**未闭合标题"`) {
		t.Fatalf("expected empty item.done to flush deferred title, got %s", rec.Body.String())
	}
}

func TestAnthropicEventWriterFlushesDeferredReasoningTitleFromEmptyItemDone(t *testing.T) {
	rec := httptest.NewRecorder()
	state := &anthropicStreamState{pendingToolArgs: map[string]string{}, toolMeta: map[string]map[string]string{}, emittedToolItems: map[string]bool{}}
	helper := &responseEventWriterHelper{downstreamType: "anthropic", upstreamEndpointType: config.UpstreamEndpointTypeResponses, toolIDAliases: map[string]string{}, toolItems: map[string]*responsesToolItemState{}}
	writer := NewAnthropicEventWriter(rec, nil, state, helper, nil)
	for _, event := range emptyReasoningItemDoneEvents() {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}
	if !strings.Contains(rec.Body.String(), `"thinking":"**未闭合标题"`) {
		t.Fatalf("expected empty item.done to flush deferred title, got %s", rec.Body.String())
	}
}

func TestFinishStreamingReasoningSummaryStatesPreservesArrivalOrderAfterClear(t *testing.T) {
	states := map[reasoningSummaryKey]*reasoningSummaryState{}
	nextOrder := 0
	if got := formatStreamingReasoningSummary(&states, &nextOrder, "rs_first", 0, "**已清理", false, false); got != "" {
		t.Fatalf("first pending title=%q, want deferred output", got)
	}
	if got := formatStreamingReasoningSummary(&states, &nextOrder, "rs_z", 0, "**第一未闭合", false, false); got != "" {
		t.Fatalf("first pending title=%q, want deferred output", got)
	}
	clearStreamingReasoningSummaryStates(&states, "rs_first")
	if got := formatStreamingReasoningSummary(&states, &nextOrder, "rs_a", 0, "**第二未闭合", false, false); got != "" {
		t.Fatalf("second pending title=%q, want deferred output", got)
	}
	if got := finishStreamingReasoningSummaryStates(&states); len(got) != 2 || got[0] != "**第一未闭合" || got[1] != "**第二未闭合" {
		t.Fatalf("finished summary order=%#v, want arrival order", got)
	}
}

func TestFinishStreamingReasoningSummaryItemStatesPreservesStandaloneTitle(t *testing.T) {
	states := map[reasoningSummaryKey]*reasoningSummaryState{}
	nextOrder := 0
	if got := formatStreamingReasoningSummary(&states, &nextOrder, "rs_title", 0, "**标题**", false, false); got != "**标题**" {
		t.Fatalf("title delta=%q, want complete title", got)
	}
	if got := finishStreamingReasoningSummaryItemStates(&states, "rs_title"); len(got) != 0 {
		t.Fatalf("item boundary=%#v, want no added delimiter", got)
	}
}

func TestFormatStreamingReasoningItemSummarySupportsTypedOpaqueParts(t *testing.T) {
	states := map[reasoningSummaryKey]*reasoningSummaryState{}
	nextOrder := 0
	item := map[string]any{
		"id": "rs_typed",
		"summary": []map[string]any{{
			"type": "summary_text",
			"text": "**第一标题****第二标题**",
		}},
	}
	if got := formatStreamingReasoningItemSummary(&states, &nextOrder, item); len(got) != 1 || got[0] != "**第一标题**\n\n**第二标题**" {
		t.Fatalf("typed summary=%#v, want formatted title sequence", got)
	}

	states = map[reasoningSummaryKey]*reasoningSummaryState{}
	nextOrder = 0
	item["encrypted_content"] = "enc_payload"
	if got := formatStreamingReasoningItemSummary(&states, &nextOrder, item); len(got) != 1 || got[0] != "**第一标题****第二标题**" {
		t.Fatalf("typed opaque summary=%#v, want original bytes", got)
	}
}

func TestFormatStreamingReasoningItemSummarySeparatesTitleSequenceAcrossFinalParts(t *testing.T) {
	states := map[reasoningSummaryKey]*reasoningSummaryState{}
	nextOrder := 0
	item := map[string]any{
		"id": "rs_final_only",
		"summary": []any{
			map[string]any{"type": "summary_text", "text": "正文**第一标题**"},
			map[string]any{"type": "summary_text", "text": "**第二标题**"},
		},
	}

	if got := formatStreamingReasoningItemSummary(&states, &nextOrder, item); len(got) != 1 || got[0] != "正文\n**第一标题**\n\n**第二标题**" {
		t.Fatalf("final-only summary=%#v, want content and title sequence separated", got)
	}
}

func deferredReasoningHeadingEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**未闭合标题"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func emptyReasoningItemDoneEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty", "summary_index": 0, "delta": "**未闭合标题"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_empty", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func doneOnlySummaryTitleEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_done_only", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_done_only", "summary_index": 0, "text": "**第一标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_done_only", "summary_index": 1, "text": "**第二标题**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_done_only", "type": "reasoning", "summary": []any{
			map[string]any{"type": "summary_text", "text": "**第一标题**"},
			map[string]any{"type": "summary_text", "text": "**第二标题**"},
		}}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func standaloneSummaryTitleEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_title", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_title", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_title", "summary_index": 0, "delta": "**标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_title", "summary_index": 0, "text": "**标题**"}},
		{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_title", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "**标题**"}}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_title", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**标题**"}}}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func summaryTextDoneWithoutPartDoneEvents(secondTitleDeltas ...string) []upstream.Event {
	if len(secondTitleDeltas) == 0 {
		secondTitleDeltas = []string{"**第二标题**"}
	}
	events := []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_missing_part_done", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_missing_part_done", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_missing_part_done", "summary_index": 0, "delta": "**第一标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_missing_part_done", "summary_index": 0, "text": "**第一标题**"}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_missing_part_done", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": ""}}},
	}
	for _, delta := range secondTitleDeltas {
		events = append(events, upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_missing_part_done", "summary_index": 1, "delta": delta}})
	}
	events = append(events,
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_missing_part_done", "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": "**第一标题**"},
				map[string]any{"type": "summary_text", "text": "**第二标题**"},
			},
		}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	return events
}

func crossPartEmptyMarkerEvents(secondTitleDeltas ...string) []upstream.Event {
	if len(secondTitleDeltas) == 0 {
		secondTitleDeltas = []string{"****", "**第二标题**"}
	}
	events := []upstream.Event{
		{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_empty_marker", "type": "reasoning", "summary": []any{}}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 0, "delta": "**第一标题**"}},
		{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 0, "text": "**第一标题**"}},
		{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "**第一标题**"}}},
		{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": ""}}},
	}
	for _, delta := range secondTitleDeltas {
		events = append(events, upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty_marker", "summary_index": 1, "delta": delta}})
	}
	events = append(events, upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}})
	return events
}

func splitAdjacentReasoningHeadingEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第二标题"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func alternatingReasoningHeadingEvents() []upstream.Event {
	return []upstream.Event{
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "\n\n**第二标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第三标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "\n\n**第四标题**"}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第五标题**"}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	}
}

func assertAlternatingReasoningHeadingDeltas(t *testing.T, body, field string) {
	t.Helper()
	assertOrderedStreamFragments(t, body,
		`"`+field+`":"**第一标题**"`,
		`"`+field+`":"\n\n**第二标题**"`,
		`"`+field+`":"\n\n**第三标题**"`,
		`"`+field+`":"\n\n**第四标题**"`,
		`"`+field+`":"\n\n**第五标题**"`,
	)
	if strings.Contains(body, `"`+field+`":"**第一标题**\n\n**第二标题**"`) {
		t.Fatalf("reasoning snapshot replayed an already emitted title sequence: %s", body)
	}
}

func TestChatEventWriterAppendsUnemittedSummarySnapshotAcrossIndexes(t *testing.T) {
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
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_native", "summary_index": 1, "delta": ""}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_native", "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": "**标题**"},
				map[string]any{"type": "summary_text", "text": "**后续**"},
			},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**标题**"`,
		`"reasoning_content":"\n\n**后续**"`,
	)
}
func TestResponsesEventWriterSeparatesTitleAddedByAuthoritativeSnapshot(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_snapshot", "summary_index": 0, "delta": "**标题**"}},
		upstream.Event{Event: "response.reasoning_summary_text.done", Data: map[string]any{"item_id": "rs_snapshot", "summary_index": 0, "text": "**标题****后续**"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_snapshot", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**标题****后续**"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	assertOrderedStreamFragments(t, body, `"delta":"**标题**"`, `"delta":"\n\n**后续**"`)
	if strings.Contains(body, `"delta":"**标题****后续**"`) {
		t.Fatalf("authoritative snapshot retained adjacent titles: %s", body)
	}
}
func TestChatEventWriterResetsSummaryAliasWhenItemIDIsReused(t *testing.T) {
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
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第一标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_reused", "summary_index": 1, "delta": "**第一后续**"}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_reused", "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": "**第一标题**"},
				map[string]any{"type": "summary_text", "text": "**第一后续**"},
			},
		}}},
		{Event: "response.reasoning.delta", Data: map[string]any{"summary": "**第二标题**"}},
		{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_reused", "summary_index": 1, "delta": ""}},
		{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{
			"id": "rs_reused", "type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": "**第二标题**"},
				map[string]any{"type": "summary_text", "text": "**第二后续**"},
			},
		}}},
		{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	} {
		if err := writer.WriteEvent(event.Event, event.Data); err != nil {
			t.Fatalf("writer.WriteEvent(%s): %v", event.Event, err)
		}
	}

	assertOrderedStreamFragments(t, rec.Body.String(),
		`"reasoning_content":"**第一标题**"`,
		`"reasoning_content":"\n\n**第一后续**"`,
		`"reasoning_content":"**第二标题**"`,
		`"reasoning_content":"\n\n**第二后续**"`,
	)
}

func assertOrderedStreamFragments(t *testing.T, body string, fragments ...string) {
	t.Helper()
	searchStart := 0
	for _, fragment := range fragments {
		relativeIndex := strings.Index(body[searchStart:], fragment)
		if relativeIndex < 0 {
			t.Fatalf("missing stream fragment %q in %s", fragment, body)
		}
		searchStart += relativeIndex + len(fragment)
	}
}
func TestResponsesEventWriterSeparatesTitleAfterEmptyMarkerAtItemBoundary(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_first", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_first", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_first", "summary_index": 0, "delta": "**A**"}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_first", "summary_index": 0, "delta": "****"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_first", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**A******"}}}}},
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_second", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_second", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_second", "summary_index": 0, "delta": "**B**"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_second", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "**B**"}}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	assertOrderedStreamFragments(t, body,
		`"delta":"**A**"`,
		`"delta":"\n\n****"`,
		`"delta":"\n\n**B**"`,
	)
	if strings.Contains(body, `"delta":"******B**"`) {
		t.Fatalf("empty marker and next title remained adjacent: %s", body)
	}
}

func TestResponsesEventWriterKeepsTitleBoundaryAcrossClosedEmptyMarkerPart(t *testing.T) {
	body := renderResponsesWriterEvents(t, config.UpstreamEndpointTypeResponses,
		upstream.Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"id": "rs_empty_part", "type": "reasoning", "summary": []any{}}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 0, "delta": "**A**"}},
		upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "**A**"}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 1, "delta": "****"}},
		upstream.Event{Event: "response.reasoning_summary_part.done", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 1, "part": map[string]any{"type": "summary_text", "text": "****"}}},
		upstream.Event{Event: "response.reasoning_summary_part.added", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 2, "part": map[string]any{"type": "summary_text", "text": ""}}},
		upstream.Event{Event: "response.reasoning_summary_text.delta", Data: map[string]any{"item_id": "rs_empty_part", "summary_index": 2, "delta": "**B**"}},
		upstream.Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"id": "rs_empty_part", "type": "reasoning", "summary": []any{
			map[string]any{"type": "summary_text", "text": "**A**"},
			map[string]any{"type": "summary_text", "text": "****"},
			map[string]any{"type": "summary_text", "text": "**B**"},
		}}}},
		upstream.Event{Event: "response.completed", Data: map[string]any{"response": map[string]any{}}},
	)
	assertOrderedStreamFragments(t, body,
		`"delta":"**A**"`,
		`"delta":"\n\n****"`,
		`"delta":"\n\n**B**"`,
	)
	if strings.Contains(body, `"delta":"**B**"`) || strings.Contains(body, `"text":"**A******B**"`) {
		t.Fatalf("closed empty marker part lost the next title boundary: %s", body)
	}
}
