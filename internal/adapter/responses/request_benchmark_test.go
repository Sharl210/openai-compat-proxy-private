package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

var benchmarkFunctionCallOutputPartsSink []model.CanonicalContentPart

func BenchmarkDecodeRequestLargeInput(b *testing.B) {
	body := largeResponsesDecodeRequestBody(160, 8<<10)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for range b.N {
		if _, err := DecodeRequest(bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeRequestRichDynamicInput(b *testing.B) {
	body := richResponsesDecodeRequestBody(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for range b.N {
		if _, err := DecodeRequest(bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeRequestMixedContentSlowPath(b *testing.B) {
	body := mixedContentResponsesDecodeRequestBody(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for range b.N {
		if _, err := DecodeRequest(bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeFunctionCallOutputPartsStructured(b *testing.B) {
	output := map[string]any{
		"ok":      true,
		"payload": strings.Repeat("result ", 128<<10),
		"metadata": map[string]any{
			"source": "benchmark",
			"nested": []any{"keep", float64(7), nil},
		},
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(encoded)))
	b.ResetTimer()

	for range b.N {
		parts, err := decodeFunctionCallOutputParts(output)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkFunctionCallOutputPartsSink = parts
	}
}

func largeResponsesDecodeRequestBody(itemCount, textSize int) []byte {
	text := strings.Repeat("a", textSize)
	var builder strings.Builder
	builder.Grow(itemCount * (textSize + 64))
	builder.WriteString(`{"model":"gpt-5.4","stream":true,"previous_response_id":"resp_123","reasoning":{"effort":"high"},"tools":[{"type":"function","name":"search_web","parameters":{"type":"object"}}],"extension":{"nested":{"value":"kept"}},"input":[`)
	for i := range itemCount {
		if i > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, `{"role":"user","content":"%s"}`, text)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func richResponsesDecodeRequestBody(b *testing.B) []byte {
	b.Helper()
	input := make([]map[string]any, 0, 195)
	for index := range 192 {
		input = append(input, map[string]any{
			"type": "message",
			"id":   fmt.Sprintf("msg_%03d", index),
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": strings.Repeat(fmt.Sprintf("item-%03d ", index), 512),
			}},
			"vendor_message": map[string]any{"index": index, "keep": true},
		})
	}
	input = append(input,
		map[string]any{"type": "reasoning", "id": "rs_bench", "summary": []map[string]any{{"type": "summary_text", "text": strings.Repeat("reasoning ", 512)}}, "encrypted_content": "enc_bench"},
		map[string]any{"type": "function_call", "id": "fc_bench", "call_id": "call_bench", "name": "lookup", "arguments": `{"query":"benchmark"}`},
		map[string]any{"type": "function_call_output", "id": "fco_bench", "call_id": "call_bench", "output": map[string]any{"ok": true, "payload": strings.Repeat("result ", 512)}},
	)
	body, err := json.Marshal(map[string]any{
		"model":                "gpt-5.6",
		"previous_response_id": "resp_bench",
		"instructions":         strings.Repeat("instruction ", 512),
		"reasoning":            map[string]any{"effort": "high", "summary": "auto", "vendor_reasoning": map[string]any{"keep": true}},
		"parallel_tool_calls":  false,
		"tools":                []map[string]any{{"type": "function", "name": "lookup", "description": strings.Repeat("description ", 64), "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}, "vendor_tool": map[string]any{"keep": true}}},
		"input":                input,
		"vendor_top_level":     map[string]any{"keep": true},
	})
	if err != nil {
		b.Fatal(err)
	}
	return body
}

func mixedContentResponsesDecodeRequestBody(b *testing.B) []byte {
	b.Helper()
	input := make([]map[string]any, 0, 96)
	for index := range 96 {
		input = append(input, map[string]any{
			"type": "message",
			"id":   fmt.Sprintf("mixed_%03d", index),
			"role": "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": strings.Repeat(fmt.Sprintf("answer-%03d ", index), 256), "vendor_text": map[string]any{"keep": true}},
				{"type": "reasoning", "id": fmt.Sprintf("rs_%03d", index), "summary": []map[string]any{{"type": "summary_text", "text": strings.Repeat("thinking ", 128)}}, "encrypted_content": fmt.Sprintf("enc_%03d", index), "vendor_reasoning": map[string]any{"keep": true}},
				{"type": "input_image", "image_url": map[string]any{"url": "https://example.test/image.png", "detail": "high", "vendor_image": map[string]any{"keep": true}}},
				{"type": "input_file", "input_file": map[string]any{"file_id": fmt.Sprintf("file_%03d", index), "vendor_file": []any{"keep", nil}}},
				{"type": "input_audio", "input_audio": map[string]any{"data": "YWJj", "format": "wav", "vendor_audio": map[string]any{"keep": true}}},
			},
			"tool_calls": []map[string]any{{"id": fmt.Sprintf("call_%03d", index), "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"query":"benchmark"}`}}},
		})
	}
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6",
		"input": input,
	})
	if err != nil {
		b.Fatal(err)
	}
	return body
}
