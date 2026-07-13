package model

import "encoding/json"

type CanonicalRequest struct {
	Model                          string
	Stream                         bool
	PreservedTopLevelFields        map[string]any
	IncludeUsage                   bool
	ResponseStore                  *bool
	ResponseInclude                []string
	ResponsePromptCacheKey         json.RawMessage
	ResponsePromptCacheOptions     json.RawMessage
	ResponseMultiAgent             json.RawMessage
	ResponsesOpenAIBeta            string
	Instructions                   string
	InstructionParts               []CanonicalContentPart
	ResponseInputItems             []map[string]any
	ResponseInputItemsAreOriginal  bool
	ResponseItemReferencesByCallID map[string]string
	Messages                       []CanonicalMessage
	Temperature                    *float64
	TopP                           *float64
	MaxOutputTokens                *int
	OmitMaxOutputTokens            bool
	Stop                           []string
	Tools                          []CanonicalTool
	ToolChoice                     CanonicalToolChoice
	ParallelToolCalls              *bool
	Reasoning                      *CanonicalReasoning
	ReasoningModeOrigin            ReasoningModeOrigin
	PassThroughRawReasoning        bool
	RequestID                      string
	AuthMode                       string
	SkipProviderSystemPrompt       bool
	HasSyntheticReasoningReplay    bool
	ClaudeMetadata                 *CanonicalClaudeMetadata
}

type CanonicalClaudeMetadata struct {
	DeviceID    string
	AccountUUID string
	SessionID   string
}

type CanonicalMessage struct {
	Role              string
	OrderedContent    []CanonicalContentBlock
	Parts             []CanonicalContentPart
	ToolCalls         []CanonicalToolCall
	ToolCallID        string
	RecoveredToolCall *CanonicalToolCall
	ReasoningContent  string
	ReasoningBlocks   []map[string]any
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
	ID             string
	ResponseItemID string
	Type           string
	Name           string
	Arguments      string
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
	Mode        string
	Name        string
	Requirement ToolChoiceRequirement
	Raw         map[string]any
}

type ToolChoiceRequirement string

const (
	ToolChoiceOptional      ToolChoiceRequirement = "optional"
	ToolChoiceRequiredAny   ToolChoiceRequirement = "required_any"
	ToolChoiceRequiredNamed ToolChoiceRequirement = "required_named"
	ToolChoiceNone          ToolChoiceRequirement = "none"
)

type CanonicalReasoning struct {
	Effort  string
	Summary string
	Mode    ReasoningMode
	Raw     map[string]any
}

type ReasoningMode string

const ReasoningModePro ReasoningMode = "pro"

type ReasoningModeOrigin string

const (
	ReasoningModeOriginNone         ReasoningModeOrigin = "none"
	ReasoningModeOriginBody         ReasoningModeOrigin = "body"
	ReasoningModeOriginProxyDefault ReasoningModeOrigin = "proxy_default"
	ReasoningModeOriginSuffix       ReasoningModeOrigin = "suffix"
)
