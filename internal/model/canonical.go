package model

type CanonicalRequest struct {
	Model                    string
	Stream                   bool
	PreservedTopLevelFields  map[string]any
	IncludeUsage             bool
	ResponseStore            *bool
	ResponseInclude          []string
	Instructions             string
	InstructionParts         []CanonicalContentPart
	ResponseInputItems       []map[string]any
	Messages                 []CanonicalMessage
	Temperature              *float64
	TopP                     *float64
	MaxOutputTokens          *int
	OmitMaxOutputTokens      bool
	Stop                     []string
	Tools                    []CanonicalTool
	ToolChoice               CanonicalToolChoice
	Reasoning                *CanonicalReasoning
	RequestID                string
	AuthMode                 string
	SkipProviderSystemPrompt bool
	HasSyntheticReasoningReplay bool
}

type CanonicalMessage struct {
	Role             string
	OrderedContent   []CanonicalContentBlock
	Parts            []CanonicalContentPart
	ToolCalls        []CanonicalToolCall
	ToolCallID       string
	ReasoningContent string
	ReasoningBlocks  []map[string]any
}

type CanonicalContentBlock struct {
	Type            string
	Part            CanonicalContentPart
	ToolCall        CanonicalToolCall
	ToolCallID      string
	ToolResultParts []CanonicalContentPart
	Raw             map[string]any
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
