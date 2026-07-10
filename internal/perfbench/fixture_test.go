package perfbench

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"testing"
)

func generatedImageFixture(size int64) []byte {
	fixture := make([]byte, int(size))
	for i := range fixture {
		fixture[i] = byte((i*131 + 17) % 251)
	}
	return fixture
}

func buildScenarioRequest(item scenario) ([]byte, error) {
	var prefix string
	var suffix string
	stream := item.Delivery == deliveryStream
	switch item.Downstream {
	case downstreamResponses:
		prefix = `{"model":"perf-model","input":[{"role":"user","content":[{"type":"input_text","text":"deterministic fixture"},{"type":"input_image","image_url":"data:image/png;base64,`
		suffix = fmt.Sprintf(`"}]}],"stream":%t}`, stream)
	case downstreamChat:
		prefix = `{"model":"perf-model","messages":[{"role":"user","content":[{"type":"text","text":"deterministic fixture"},{"type":"image_url","image_url":{"url":"data:image/png;base64,`
		suffix = fmt.Sprintf(`"}}]}],"stream":%t}`, stream)
	case downstreamMessages:
		prefix = `{"model":"perf-model","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"deterministic fixture"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`
		suffix = fmt.Sprintf(`"}}]}],"stream":%t}`, stream)
	default:
		return nil, fmt.Errorf("unsupported downstream protocol %q", item.Downstream)
	}

	var body bytes.Buffer
	body.Grow(len(prefix) + base64.StdEncoding.EncodedLen(int(item.ImageBytes)) + len(suffix))
	body.WriteString(prefix)
	encoder := base64.NewEncoder(base64.StdEncoding, &body)
	if _, err := encoder.Write(generatedImageFixture(item.ImageBytes)); err != nil {
		return nil, fmt.Errorf("encode image fixture: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close image encoder: %w", err)
	}
	body.WriteString(suffix)
	return body.Bytes(), nil
}

func TestGeneratedImageFixture_is_deterministic(t *testing.T) {
	// Given
	const size = int64(1 << 20)

	// When
	first := generatedImageFixture(size)
	second := generatedImageFixture(size)

	// Then
	if int64(len(first)) != size {
		t.Fatalf("fixture size = %d, want %d", len(first), size)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generated fixture changed between calls")
	}
}

func TestProtocolRequestBuilder_is_deterministic(t *testing.T) {
	for _, downstream := range []downstreamProtocol{
		downstreamResponses,
		downstreamChat,
		downstreamMessages,
	} {
		t.Run(string(downstream), func(t *testing.T) {
			// Given
			scenario := scenarioCatalog()[0]
			scenario.Downstream = downstream

			// When
			first, err := buildScenarioRequest(scenario)
			if err != nil {
				t.Fatalf("build first request: %v", err)
			}
			second, err := buildScenarioRequest(scenario)
			if err != nil {
				t.Fatalf("build second request: %v", err)
			}

			// Then
			if !bytes.Equal(first, second) {
				t.Fatal("request body changed between builds")
			}
			if int64(len(first)) <= scenario.ImageBytes {
				t.Fatalf("request bytes = %d, image bytes = %d", len(first), scenario.ImageBytes)
			}
		})
	}
}
