package responses

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

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

func largeResponsesDecodeRequestBody(itemCount, textSize int) []byte {
	text := strings.Repeat("a", textSize)
	var builder strings.Builder
	builder.Grow(itemCount * (textSize + 64))
	builder.WriteString(`{"model":"gpt-5.4","stream":true,"previous_response_id":"resp_123","reasoning":{"effort":"high"},"tools":[{"type":"function","name":"search_web","parameters":{"type":"object"}}],"extension":{"nested":{"value":"kept"}},"input":[`)
	for i := 0; i < itemCount; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, `{"role":"user","content":"%s"}`, text)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}
