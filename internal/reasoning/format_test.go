package reasoning

import "testing"

func TestFormatTextFormatsOnlyExactlyTwoAdjacentBoldSpans(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "exact adjacent pair", input: "前缀**一****二**后缀", want: "前缀\n**一**\n\n**二**\n后缀"},
		{name: "only pair", input: "**一****二**", want: "\n**一**\n\n**二**\n"},
		{name: "single bold span unchanged", input: "**标题**正文", want: "**标题**正文"},
		{name: "inline bold span unchanged", input: "正文 **重点** 继续", want: "正文 **重点** 继续"},
		{name: "three adjacent spans unchanged", input: "**一****二****三**", want: "**一****二****三**"},
		{name: "separate pairs retain original form", input: "**一****二** 与 **三****四**", want: "**一****二** 与 **三****四**"},
		{name: "incomplete pair unchanged", input: "**标题****", want: "**标题****"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatText(test.input); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestFormatDeltaFormatsExactAdjacentBoldPairAcrossChunks(t *testing.T) {
	first, combined := FormatDelta("", "**一**")
	if first != "**一**" || combined != "**一**" {
		t.Fatalf("expected incomplete pair to remain unchanged, got first=%q combined=%q", first, combined)
	}

	second, combined := FormatDelta(combined, "**二**")
	if second != "**二**" || combined != "**一****二**" {
		t.Fatalf("expected cross-chunk pair to preserve append-only output, got second=%q combined=%q", second, combined)
	}
	if first+second != combined {
		t.Fatalf("expected emitted deltas to reconstruct combined output, got first=%q second=%q combined=%q", first, second, combined)
	}
}

func TestFormatDeltaFormatsNewPairAfterAlreadyEmittedPrefix(t *testing.T) {
	delta, combined := FormatDelta("前缀", "**一****二**后缀")
	if delta != "\n**一**\n\n**二**\n后缀" || combined != "前缀\n**一**\n\n**二**\n后缀" {
		t.Fatalf("expected only the normalized suffix after an unchanged prefix, got delta=%q combined=%q", delta, combined)
	}
}

func TestFormatBlockFormatsOnlyExactAdjacentBoldPair(t *testing.T) {
	block := map[string]any{
		"thinking":          "**一****二**正文",
		"summary":           "**标题**正文",
		"reasoning_content": "正文 **重点** 继续",
	}
	formatted := FormatBlock(block)
	if got := formatted["thinking"]; got != "\n**一**\n\n**二**\n正文" {
		t.Fatalf("expected exact adjacent pair formatted, got %#v", got)
	}
	if got := formatted["summary"]; got != "**标题**正文" {
		t.Fatalf("expected single bold span unchanged, got %#v", got)
	}
	if got := formatted["reasoning_content"]; got != "正文 **重点** 继续" {
		t.Fatalf("expected inline bold span unchanged, got %#v", got)
	}
}
