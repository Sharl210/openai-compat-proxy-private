package anthropic

import (
	"strings"
	"testing"
)

func TestDecodeRequestIgnoresThinkingBlocksInFollowUpMessages(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"thinking","thinking":"internal reasoning","signature":"sig_123"},
					{"type":"text","text":"我刚刚在发呆。"}
				]
			},
			{
				"role":"user",
				"content":"继续说"
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 canonical messages, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "我刚刚在发呆。" {
		t.Fatalf("expected assistant text part to survive thinking block, got %#v", canon.Messages[0].Parts)
	}
	if got := canon.Messages[1].Parts[0].Text; got != "继续说" {
		t.Fatalf("expected user follow-up text, got %#v", canon.Messages[1].Parts)
	}
}

func TestDecodeRequestIgnoresRedactedThinkingBlocksInFollowUpMessages(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"redacted_thinking","data":"enc_123"},
					{"type":"text","text":"先这样。"}
				]
			},
			{
				"role":"user",
				"content":"再说一句"
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 canonical messages, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "先这样。" {
		t.Fatalf("expected assistant text part to survive redacted thinking block, got %#v", canon.Messages[0].Parts)
	}
}

func TestDecodeRequestAcceptsAnthropicImageURLBlock(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image","source":{"type":"url","url":"https://example.com/cat.png"}},
					{"type":"text","text":"描述这张图"}
				]
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 2 {
		t.Fatalf("expected one multimodal message, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Type; got != "image_url" {
		t.Fatalf("expected first part image_url, got %#v", canon.Messages[0].Parts)
	}
	if got := canon.Messages[0].Parts[0].ImageURL; got != "https://example.com/cat.png" {
		t.Fatalf("expected image URL preserved, got %#v", canon.Messages[0].Parts[0])
	}
	if got := canon.Messages[0].Parts[1].Text; got != "描述这张图" {
		t.Fatalf("expected text part preserved, got %#v", canon.Messages[0].Parts[1])
	}
}

func TestDecodeRequestAcceptsAnthropicBase64ImageBlock(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}},
					{"type":"text","text":"看图"}
				]
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 2 {
		t.Fatalf("expected one multimodal message, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].ImageURL; got != "data:image/png;base64,YWJj" {
		t.Fatalf("expected base64 image to become data URL, got %#v", canon.Messages[0].Parts[0])
	}
	if got := canon.Messages[0].Parts[0].MimeType; got != "image/png" {
		t.Fatalf("expected mime type preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestAcceptsAnthropicFileImageBlock(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	imageRaw, _ := canon.Messages[0].Parts[0].Raw["image_url"].(map[string]any)
	if got := imageRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected file image source to preserve file_id, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestAcceptsAnthropicDocumentTextSource(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"文档正文"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "文档正文" {
		t.Fatalf("expected document text source to become text part, got %#v", canon.Messages[0].Parts)
	}
}

func TestDecodeRequestAcceptsAnthropicDocumentBase64PDFSource(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0x"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	fileRaw, _ := canon.Messages[0].Parts[0].Raw["input_file"].(map[string]any)
	if got := fileRaw["file_data"]; got != "data:application/pdf;base64,JVBERi0x" {
		t.Fatalf("expected document base64 PDF to preserve data URL, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestPreservesToolResultMultimodalContent(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[{
			"role":"user",
			"content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"url","url":"https://example.com/tool.png"}}]}]
		}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "tool" {
		t.Fatalf("expected tool result message, got %#v", canon.Messages)
	}
	if len(canon.Messages[0].Parts) != 2 {
		t.Fatalf("expected text+image tool result parts, got %#v", canon.Messages[0].Parts)
	}
	if canon.Messages[0].Parts[1].Type != "image_url" {
		t.Fatalf("expected image tool result part preserved, got %#v", canon.Messages[0].Parts)
	}
}
