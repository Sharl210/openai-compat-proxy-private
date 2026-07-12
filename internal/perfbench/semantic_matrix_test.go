package perfbench

type semanticRecord struct {
	ScenarioID            string                          `json:"scenario_id"`
	Downstream            downstreamProtocol              `json:"downstream"`
	Upstream              upstreamProtocol                `json:"upstream"`
	Delivery              deliveryMode                    `json:"delivery"`
	ImageBytes            int64                           `json:"image_bytes"`
	Profile               scenarioProfile                 `json:"profile"`
	FinalEndpoint         string                          `json:"final_endpoint"`
	Method                string                          `json:"method"`
	ObservedBodySHA256    string                          `json:"observed_body_sha256"`
	ObservedBodyBytes     int64                           `json:"observed_body_bytes"`
	ContentLength         int64                           `json:"content_length"`
	DecodedImageSHA256    string                          `json:"decoded_image_sha256"`
	DecodedImageBytes     int64                           `json:"decoded_image_bytes"`
	PromptCacheKey        string                          `json:"prompt_cache_key"`
	DownstreamStatus      int                             `json:"downstream_status"`
	DownstreamContentType string                          `json:"downstream_content_type"`
	UpstreamContentType   string                          `json:"upstream_content_type"`
	UpstreamResponseMode  string                          `json:"upstream_response_mode"`
	ProxyHeaders          map[string]string               `json:"proxy_headers"`
	NormalizedOutput      string                          `json:"normalized_output"`
	Reasoning             []string                        `json:"reasoning"`
	Tools                 []semanticTool                  `json:"tools"`
	Usage                 map[string]int64                `json:"usage"`
	FinishReason          string                          `json:"finish_reason"`
	TerminalStatus        string                          `json:"terminal_status,omitempty"`
	RetryAttempts         []semanticBodyDigest            `json:"retry_attempts,omitempty"`
	Archives              map[string]fileDigest           `json:"archives,omitempty"`
	LogEvents             []semanticLogEvent              `json:"log_events,omitempty"`
	HistorySecondRequest  *semanticHistoryRequestEvidence `json:"history_second_request,omitempty"`
}

type semanticTool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type semanticBodyDigest struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type fileDigest struct {
	Records int    `json:"records"`
	SHA256  string `json:"sha256"`
}

type semanticLogEvent struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
}

type semanticHistoryRequestEvidence struct {
	Method                      string `json:"method"`
	Endpoint                    string `json:"endpoint"`
	BodySHA256                  string `json:"body_sha256"`
	BodyBytes                   int64  `json:"body_bytes"`
	ContentLength               int64  `json:"content_length"`
	RequestContentType          string `json:"request_content_type"`
	DecodedImageSHA256          string `json:"decoded_image_sha256"`
	DecodedImageBytes           int64  `json:"decoded_image_bytes"`
	PromptCacheKey              string `json:"prompt_cache_key"`
	ContentMode                 string `json:"content_mode"`
	UpstreamResponseContentType string `json:"upstream_response_content_type"`
	UpstreamResponseMode        string `json:"upstream_response_mode"`
	RestoredToolName            string `json:"restored_tool_name"`
	RestoredToolResult          string `json:"restored_tool_result"`
	RestoredUserText            string `json:"restored_user_text"`
}
