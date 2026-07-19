package texttail

import "testing"

func TestTrimTrailingCRLF(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "leaves text without terminal line ending", text: "plain text", want: "plain text"},
		{name: "removes terminal CRLF sequence", text: "answer\r\n", want: "answer"},
		{name: "removes all terminal CR and LF bytes", text: "answer\n\r\n", want: "answer"},
		{name: "preserves internal line ending and terminal spaces", text: "first\r\nsecond \t\r\n", want: "first\r\nsecond \t"},
		{name: "does not remove line ending before terminal whitespace", text: "answer\r\n \t", want: "answer\r\n \t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimTrailingCRLF(tt.text); got != tt.want {
				t.Fatalf("TrimTrailingCRLF(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
