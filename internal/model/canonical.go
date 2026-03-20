package model

type CanonicalRequest struct {
	Model           string
	Stream          bool
	IncludeUsage    bool
	Instructions    string
	Messages        []CanonicalMessage
	Temperature     *float64
	TopP            *float64
	MaxOutputTokens *int
	Stop            []string
	Tools           []CanonicalTool
	ToolChoice      CanonicalToolChoice
	Reasoning       *CanonicalReasoning
	RequestID       string
	AuthMode        string
}

type CanonicalMessage struct {
	Role             string
	Parts            []CanonicalContentPart
	ToolCalls        []CanonicalToolCall
	ToolCallID       string
	ReasoningContent string
}

type CanonicalToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

type CanonicalContentPart struct {
	Type     string
	Text     string
	ImageURL string
	MimeType string
	Raw      map[string]any
}

type CanonicalTool struct {
	Type        string
	Name        string
	Description string
	Parameters  map[string]any
	Raw         map[string]any
}

type CanonicalToolChoice struct {
	Mode string
	Name string
	Raw  map[string]any
}

type CanonicalReasoning struct {
	Effort  string
	Summary string
	Raw     map[string]any
}
