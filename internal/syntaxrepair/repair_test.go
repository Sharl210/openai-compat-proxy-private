package syntaxrepair

import "testing"

func TestRepairJSONClosesMissingQuoteAndBrace(t *testing.T) {
	repaired, ok := RepairJSON(`{"query":"hello"`)
	if !ok {
		t.Fatalf("expected truncated JSON to be repairable")
	}
	if repaired != `{"query":"hello"}` {
		t.Fatalf("expected repaired JSON object, got %q", repaired)
	}
}

func TestRepairJSONRemovesTrailingComma(t *testing.T) {
	repaired, ok := RepairJSON(`{"query":"hello",}`)
	if !ok {
		t.Fatalf("expected trailing-comma JSON to be repairable")
	}
	if repaired != `{"query":"hello"}` {
		t.Fatalf("expected trailing comma removed, got %q", repaired)
	}
}

func TestRepairJSONNormalizesRepeatedConcatenatedDocument(t *testing.T) {
	repaired, ok := RepairJSON(`{"query":"hello"}{"query":"hello"}`)
	if !ok {
		t.Fatalf("expected repeated JSON document to be repairable")
	}
	if repaired != `{"query":"hello"}` {
		t.Fatalf("expected repeated JSON to collapse into one document, got %q", repaired)
	}
}

func TestRepairStructuredTextLeavesXMLUntouched(t *testing.T) {
	original := `<root><item>ok</item>`
	repaired, format, changed := RepairStructuredText(original)
	if format != "" || changed {
		t.Fatalf("expected XML-like text to stay untouched in v1, got format=%q changed=%v repaired=%q", format, changed, repaired)
	}
	if repaired != original {
		t.Fatalf("expected original XML-like text unchanged, got %q", repaired)
	}
}
