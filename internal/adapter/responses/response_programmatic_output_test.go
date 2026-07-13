package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/upstream"
)

func TestResponsesNonStreamOutputPreservesProgrammaticToolCallingItems(t *testing.T) {
	// Given
	const requestBody = `{
		"model":"gpt-5.6",
		"tools":[{
			"type":"programmatic_tool_calling",
			"allowed_callers":[{"type":"agent","name":"researcher","vendor_caller":{"keep":"opaque"}}],
			"vendor_tool":{"keep":true}
		}],
		"input":"execute the delegated program"
	}`
	const upstreamResponse = `{
		"id":"resp_ptc_1",
		"output":[
			{
				"type":"program",
				"id":"prog_1",
				"call_id":"call_prog_1",
				"agent_id":"agent_researcher",
				"caller":"researcher",
				"caller_id":"agent_root",
				"code":"print('hello')",
				"vendor_program":{"keep":true}
			},
			{
				"type":"program_output",
				"id":"prog_out_1",
				"program_id":"prog_1",
				"call_id":"call_prog_1",
				"agent_id":"agent_researcher",
				"caller":"researcher",
				"caller_id":"agent_root",
				"output":"hello",
				"vendor_output":{"keep":"opaque"}
			}
		]
	}`

	var expectedRequest map[string]any
	if err := json.Unmarshal([]byte(requestBody), &expectedRequest); err != nil {
		t.Fatalf("unmarshal expected request: %v", err)
	}
	var expectedResponse map[string]any
	if err := json.Unmarshal([]byte(upstreamResponse), &expectedResponse); err != nil {
		t.Fatalf("unmarshal expected response: %v", err)
	}
	var receivedRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&receivedRequest); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(upstreamResponse)); err != nil {
			t.Fatalf("write upstream response: %v", err)
		}
	}))
	defer server.Close()

	request, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	// When
	payload, err := upstream.NewClient(server.URL).Response(context.Background(), request, "")
	if err != nil {
		t.Fatalf("client.Response error: %v", err)
	}
	result, err := aggregate.ResultFromResponsePayload(payload)
	if err != nil {
		t.Fatalf("ResultFromResponsePayload error: %v", err)
	}
	response := BuildResponse(result)

	// Then
	expectedTools, _ := expectedRequest["tools"].([]any)
	receivedTools, _ := receivedRequest["tools"].([]any)
	if !reflect.DeepEqual(receivedTools, expectedTools) {
		t.Fatalf("expected native programmatic tool and allowed_callers upstream, got %#v want %#v", receivedTools, expectedTools)
	}
	expectedOutput, _ := expectedResponse["output"].([]any)
	output, _ := response["output"].([]map[string]any)
	actualOutput := make([]any, len(output))
	for index, item := range output {
		actualOutput[index] = item
	}
	if !reflect.DeepEqual(actualOutput, expectedOutput) {
		t.Fatalf("expected program items to preserve order, IDs, and opaque fields, got %#v want %#v", actualOutput, expectedOutput)
	}
}
