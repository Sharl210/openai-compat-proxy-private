package reasoning

import (
	"strings"
	"testing"
)

func TestFormatTextSeparatesBoldTitleFromFollowingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain content", input: "**标题**正文", want: "**标题**正文"},
		{name: "title after content", input: "正文**标题**后续", want: "正文\n**标题**后续"},
		{name: "content followed by adjacent titles", input: "正文**第一标题****第二标题**", want: "正文\n**第一标题**\n\n**第二标题**"},
		{name: "title without body", input: "**标题**", want: "**标题**"},
		{name: "adjacent bold titles", input: "**一****二**后续", want: "**一**\n\n**二**后续"},
		{name: "adjacent bold titles without body", input: "**一****二**", want: "**一**\n\n**二**"},
		{name: "six-star title boundary", input: "**A******B**", want: "**A**\n\n****B**"},
		{name: "eight-star title boundary", input: "**A********B**", want: "**A**\n\n****\n\n**B**"},
		{name: "continuous thinking titles", input: "**ssss****sssss****sdad**", want: "**ssss**\n\n**sssss**\n\n**sdad**"},
		{name: "incomplete adjacent marker", input: "**标题****", want: "**标题****"},
		{name: "existing newline", input: "**标题**\n正文", want: "**标题**\n正文"},
		{name: "existing newline before title", input: "正文\n**标题**\n后续", want: "正文\n**标题**\n后续"},
		{name: "inline bold title", input: "正文 **重点** 继续", want: "正文 **重点** 继续"},
		{name: "multiple complete pairs", input: "**一**和**二**正文", want: "**一**和**二**正文"},
		{name: "unclosed pair", input: "正文 **未闭合", want: "正文 **未闭合"},
		{name: "standalone markers", input: "正文 ** 符号", want: "正文 ** 符号"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatText(test.input); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestFormatTextPreservesNonAdjacentBoldSpans(t *testing.T) {
	input := "思考 **重点** 继续"
	if got := FormatText(input); got != input {
		t.Fatalf("expected non-adjacent bold span unchanged, got %q", got)
	}
}

func TestFormatBlockFormatsAllReasoningText(t *testing.T) {
	block := map[string]any{
		"thinking":          "**重点**正文",
		"summary":           "**标题**正文",
		"reasoning_content": "**过程**后续",
	}
	formatted := FormatBlock(block)
	if got := formatted["thinking"]; got != "**重点**正文" {
		t.Fatalf("expected thinking title to be separated, got %#v", got)
	}
	if got := formatted["summary"]; got != "**标题**正文" {
		t.Fatalf("expected summary title to be separated, got %#v", got)
	}
	if got := formatted["reasoning_content"]; got != "**过程**后续" {
		t.Fatalf("expected reasoning_content title to be separated, got %#v", got)
	}
}

func TestFormatBlockPreservesSignedReasoningText(t *testing.T) {
	block := map[string]any{
		"thinking":  "**ssss****sssss****sdad**",
		"summary":   "**标题**正文",
		"signature": "sig_123",
	}

	formatted := FormatBlock(block)
	if got, _ := formatted["thinking"].(string); got != "**ssss****sssss****sdad**" {
		t.Fatalf("expected signed thinking preserved, got %q", got)
	}
	if got, _ := formatted["summary"].(string); got != "**标题**正文" {
		t.Fatalf("expected signed summary preserved, got %q", got)
	}
	if got, _ := block["thinking"].(string); got != "**ssss****sssss****sdad**" {
		t.Fatalf("expected source block unchanged, got %q", got)
	}
}

func TestFormatBlockPreservesEncryptedAndOpaqueReasoningText(t *testing.T) {
	for _, test := range []struct {
		name       string
		protection map[string]any
	}{
		{name: "encrypted content", protection: map[string]any{"encrypted_content": "enc_payload"}},
		{name: "opaque marker", protection: map[string]any{"opaque": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := map[string]any{
				"summary": []any{map[string]any{"type": "summary_text", "text": "**第一标题****第二标题**"}},
			}
			for key, value := range test.protection {
				block[key] = value
			}

			formatted := FormatBlock(block)
			parts, _ := formatted["summary"].([]any)
			part, _ := parts[0].(map[string]any)
			if got, _ := part["text"].(string); got != "**第一标题****第二标题**" {
				t.Fatalf("opaque reasoning text=%q, want original bytes", got)
			}
		})
	}
}

func TestFormatBlockFormatsTypedSummaryParts(t *testing.T) {
	block := map[string]any{
		"summary": []map[string]any{
			{
				"type": "summary_text",
				"text": "**ssss****sssss****sdad**",
			},
			nil,
		},
	}

	formatted := FormatBlock(block)
	parts, ok := formatted["summary"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected typed summary parts, got %#v", formatted["summary"])
	}
	if got, _ := parts[0]["text"].(string); got != "**ssss**\n\n**sssss**\n\n**sdad**" {
		t.Fatalf("expected continuous thinking titles to be separated, got %#v", parts[0])
	}
	if got, _ := block["summary"].([]map[string]any)[0]["text"].(string); got != "**ssss****sssss****sdad**" {
		t.Fatalf("expected source block unchanged, got %q", got)
	}
	if parts[1] != nil {
		t.Fatalf("expected nil typed summary part preserved, got %#v", parts[1])
	}
}

func TestFormatBlockSeparatesTitleSequenceAcrossSummaryParts(t *testing.T) {
	block := map[string]any{
		"summary": []any{
			map[string]any{"type": "summary_text", "text": "正文**第一标题**"},
			map[string]any{"type": "summary_text", "text": "**第二标题**"},
		},
	}

	formatted := FormatBlock(block)
	parts := formatted["summary"].([]any)
	first := parts[0].(map[string]any)["text"].(string)
	second := parts[1].(map[string]any)["text"].(string)
	if got := first + second; got != "正文\n**第一标题**\n\n**第二标题**" {
		t.Fatalf("formatted summary=%q, want content and title sequence separated", got)
	}
}

func TestFormatBlockPreservesNilSummarySlices(t *testing.T) {
	tests := []struct {
		name    string
		summary any
		assert  func(t *testing.T, value any)
	}{
		{
			name:    "untyped",
			summary: []any(nil),
			assert: func(t *testing.T, value any) {
				t.Helper()
				parts, ok := value.([]any)
				if !ok || parts != nil {
					t.Fatalf("expected nil []any summary, got %#v", value)
				}
			},
		},
		{
			name:    "typed",
			summary: []map[string]any(nil),
			assert: func(t *testing.T, value any) {
				t.Helper()
				parts, ok := value.([]map[string]any)
				if !ok || parts != nil {
					t.Fatalf("expected nil []map[string]any summary, got %#v", value)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := map[string]any{"summary": test.summary}
			formatted := FormatBlock(block)
			test.assert(t, formatted["summary"])
			test.assert(t, block["summary"])
		})
	}
}

func TestFormatDeltaCarriesBoldTitleAcrossChunks(t *testing.T) {
	first, combined := FormatDelta("", "**标题**")
	if first != "**标题**" || combined != "**标题**" {
		t.Fatalf("expected title chunk unchanged, got first=%q combined=%q", first, combined)
	}

	second, combined := FormatDelta(combined, "正文")
	if second != "正文" {
		t.Fatalf("expected ordinary text unchanged, got %q", second)
	}
	if combined != "**标题**正文" {
		t.Fatalf("expected combined reasoning text, got %q", combined)
	}
}

func TestFormatDeltaSeparatesAdjacentBoldTitlesAcrossChunks(t *testing.T) {
	first, combined := FormatDelta("", "**标题**")
	if first != "**标题**" || combined != "**标题**" {
		t.Fatalf("expected first title unchanged, got first=%q combined=%q", first, combined)
	}

	second, combined := FormatDelta(combined, "**后续**")
	if second != "\n\n**后续**" || combined != "**标题**\n\n**后续**" {
		t.Fatalf("expected adjacent titles separated across chunks, got second=%q combined=%q", second, combined)
	}
}

func TestFormatDeltaSeparatesTitleAfterContentAcrossChunks(t *testing.T) {
	first, combined := FormatDelta("", "正文")
	if first != "正文" || combined != "正文" {
		t.Fatalf("expected content unchanged, got first=%q combined=%q", first, combined)
	}

	second, combined := FormatDelta(combined, "**标题****后续**")
	if second != "\n**标题**\n\n**后续**" || combined != "正文\n**标题**\n\n**后续**" {
		t.Fatalf("expected content and titles to be separated, got second=%q combined=%q", second, combined)
	}
}

func TestFormatDeltaSeparatesContinuousThinkingTitlesAcrossChunks(t *testing.T) {
	first, combined := FormatDelta("", "**ssss**")
	if first != "**ssss**" || combined != "**ssss**" {
		t.Fatalf("expected first thinking title unchanged, got first=%q combined=%q", first, combined)
	}

	second, combined := FormatDelta(combined, "**sssss****sdad**")
	if second != "\n\n**sssss**\n\n**sdad**" || combined != "**ssss**\n\n**sssss**\n\n**sdad**" {
		t.Fatalf("expected continuous thinking titles separated across chunks, got second=%q combined=%q", second, combined)
	}
}

func TestStreamFormatterDefersUnclosedAdjacentHeading(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**第一标题**"); got != "**第一标题**" {
		t.Fatalf("first title=%q, want first title unchanged", got)
	}
	if got := formatter.Push("**第二标题"); got != "" {
		t.Fatalf("unclosed adjacent title=%q, want deferred output", got)
	}
	if got := formatter.Push("**"); got != "\n\n**第二标题**" {
		t.Fatalf("closed adjacent title=%q, want separated second title", got)
	}
	if got := formatter.Finish(); got != "" {
		t.Fatalf("finish=%q, want no duplicate output", got)
	}
}

func TestStreamFormatterEmitsEmptyBoldMarkerAsOneUnit(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("****"); got != "****" {
		t.Fatalf("empty bold marker=%q, want one atomic marker", got)
	}
	if got := formatter.Finish(); got != "" {
		t.Fatalf("finish=%q, want no duplicate marker", got)
	}

	formatter.Reset()
	if got := formatter.Push("**第一标题**"); got != "**第一标题**" {
		t.Fatalf("first title=%q, want first title unchanged", got)
	}
	if got := formatter.Push("*"); got != "" {
		t.Fatalf("split title opener=%q, want deferred marker", got)
	}
	if got := formatter.Push("*第二标题"); got != "" {
		t.Fatalf("split title body=%q, want deferred title", got)
	}
	if got := formatter.Push("**"); got != "\n\n**第二标题**" {
		t.Fatalf("completed split title=%q, want separated title", got)
	}
}

func TestStreamFormatterSeparatesTitleAroundEmptyBoldMarker(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**A**"); got != "**A**" {
		t.Fatalf("first title=%q, want first title", got)
	}
	if got := formatter.Push("****"); got != "\n\n****" {
		t.Fatalf("empty marker=%q, want separator and marker", got)
	}
	if got := formatter.Push("**B**"); got != "\n\n**B**" {
		t.Fatalf("second title=%q, want separator and second title", got)
	}
	if got := formatter.Finish(); got != "" {
		t.Fatalf("finish=%q, want no duplicate output", got)
	}
}
func TestStreamFormatterSplitsSixStarBoundaryAcrossChunks(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**A**"); got != "**A**" {
		t.Fatalf("first title=%q, want first title", got)
	}
	if got := formatter.Push("**"); got != "" {
		t.Fatalf("deferred boundary=%q, want no output", got)
	}
	if got := formatter.Push("**B**"); got != "\n\n****B" {
		t.Fatalf("six-star boundary=%q, want separator and retained stars", got)
	}
	if got := formatter.Finish(); got != "**" {
		t.Fatalf("final marker=%q, want closing marker", got)
	}
}

func TestStreamFormatterMatchesWholeTextAcrossChunkBoundaries(t *testing.T) {
	for _, raw := range []string{"**A****B**", "**A****B****C**", "**A******B**", "**A********B**"} {
		t.Run(raw, func(t *testing.T) {
			want := FormatText(raw)
			for first := 1; first < len(raw); first++ {
				for second := first + 1; second < len(raw); second++ {
					var formatter StreamFormatter
					got := formatter.Push(raw[:first])
					got += formatter.Push(raw[first:second])
					got += formatter.Push(raw[second:])
					got += formatter.Finish()
					if got != want {
						t.Fatalf("chunks [%d:%d] produced %q, want %q", first, second, got, want)
					}
				}
			}
		})
	}
}
func TestStreamFormatterFlushesIncompleteAdjacentHeading(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**第一标题**"); got != "**第一标题**" {
		t.Fatalf("first title=%q, want first title unchanged", got)
	}
	if got := formatter.Push("**未闭合"); got != "" {
		t.Fatalf("unclosed adjacent title=%q, want deferred output", got)
	}
	if got := formatter.Finish(); got != "**未闭合" {
		t.Fatalf("finish=%q, want literal incomplete title unchanged", got)
	}
}

func TestStreamFormatterFinishesCompleteHeadingAtBoundary(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**标题**"); got != "**标题**" {
		t.Fatalf("title=%q, want complete title", got)
	}
	if got := formatter.Finish(); got != "" {
		t.Fatalf("ordinary finish=%q, want no trailing newline", got)
	}

	formatter.Reset()
	if got := formatter.Push("**标题**"); got != "**标题**" {
		t.Fatalf("boundary title=%q, want complete title", got)
	}
	if got := formatter.FinishAtBoundary(); got != "" {
		t.Fatalf("boundary finish=%q, want no lone-title separator", got)
	}
}

func TestStreamFormatterAdvancesUnclosedHeadingScanCursor(t *testing.T) {
	var formatter StreamFormatter
	if got := formatter.Push("**a"); got != "" {
		t.Fatalf("initial unclosed heading=%q, want deferred output", got)
	}
	previousScanStart := formatter.boldScanStart
	for index := 0; index < 4096; index++ {
		if got := formatter.Push("x"); got != "" {
			t.Fatalf("fragmented unclosed heading at chunk %d=%q, want deferred output", index, got)
		}
		if formatter.boldScanStart <= previousScanStart {
			t.Fatalf("scan cursor=%d, want advance beyond %d", formatter.boldScanStart, previousScanStart)
		}
		previousScanStart = formatter.boldScanStart
	}
	got := formatter.Push("**")
	if !strings.HasPrefix(got, "**a") || !strings.HasSuffix(got, "**") || len(got) != 4101 {
		t.Fatalf("closed fragmented heading=%q, want complete heading", got)
	}
	if formatter.boldScanStart != 0 {
		t.Fatalf("scan cursor=%d after complete heading, want reset", formatter.boldScanStart)
	}
}

func BenchmarkStreamFormatterFragmentedUnclosedHeading(b *testing.B) {
	fragment := strings.Repeat("x", 4096)
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		var formatter StreamFormatter
		_ = formatter.Push("**")
		for _, character := range fragment {
			_ = formatter.Push(string(character))
		}
		_ = formatter.Push("**")
		_ = formatter.Finish()
	}
}
