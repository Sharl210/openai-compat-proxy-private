package reasoning

import "testing"

func TestFormatTextSeparatesBoldTitleFromFollowingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain content", input: "**标题**正文", want: "**标题**\n正文"},
		{name: "title after content", input: "正文**标题**后续", want: "正文\n**标题**\n后续"},
		{name: "title without body", input: "**标题**", want: "**标题**"},
		{name: "adjacent bold content", input: "**标题****后续**", want: "**标题**\n**后续**"},
		{name: "existing newline", input: "**标题**\n正文", want: "**标题**\n正文"},
		{name: "existing newline before title", input: "正文\n**标题**\n后续", want: "正文\n**标题**\n后续"},
		{name: "inline bold title", input: "正文 **重点** 继续", want: "正文 \n**重点**\n 继续"},
		{name: "multiple complete pairs", input: "**一**和**二**正文", want: "**一**\n和\n**二**\n正文"},
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

func TestFormatTextTreatsAllBoldSpansAsTitles(t *testing.T) {
	input := "思考 **重点** 继续"
	if got := FormatText(input); got != "思考 \n**重点**\n 继续" {
		t.Fatalf("expected bold thinking title to occupy its own line, got %q", got)
	}
}

func TestFormatBlockFormatsAllReasoningText(t *testing.T) {
	block := map[string]any{
		"thinking":          "**重点**正文",
		"summary":           "**标题**正文",
		"reasoning_content": "**过程**后续",
	}
	formatted := FormatBlock(block)
	if got := formatted["thinking"]; got != "**重点**\n正文" {
		t.Fatalf("expected thinking title to be separated, got %#v", got)
	}
	if got := formatted["summary"]; got != "**标题**\n正文" {
		t.Fatalf("expected summary title to be separated, got %#v", got)
	}
	if got := formatted["reasoning_content"]; got != "**过程**\n后续" {
		t.Fatalf("expected reasoning_content title to be separated, got %#v", got)
	}
}

func TestFormatDeltaCarriesBoldTitleAcrossChunks(t *testing.T) {
	var previous string
	first, combined := FormatDelta(previous, "**标题**")
	if first != "**标题**" {
		t.Fatalf("expected title chunk unchanged, got %q", first)
	}
	previous = combined

	second, combined := FormatDelta(previous, "正文")
	if second != "\n正文" {
		t.Fatalf("expected newline before following chunk, got %q", second)
	}
	if combined != "**标题**\n正文" {
		t.Fatalf("expected combined reasoning text, got %q", combined)
	}
}
