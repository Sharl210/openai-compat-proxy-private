package httpapi

import "openai-compat-proxy/internal/texttail"

// visibleTextTailBuffer withholds only terminal CR/LF runs until the next text
// fragment proves that they are internal rather than trailing output.
type visibleTextTailBuffer struct {
	pending string
}

func (b *visibleTextTailBuffer) Push(text string) string {
	if text == "" {
		return ""
	}

	combined := b.pending + text
	b.pending = ""
	visible := trimTrailingTextLineEndings(combined)
	b.pending = combined[len(visible):]
	return visible
}

func (b *visibleTextTailBuffer) Discard() {
	b.pending = ""
}

func trimTrailingTextLineEndings(text string) string {
	return texttail.TrimTrailingCRLF(text)
}
