package lsp

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/yamlstream"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestFormatting_SingleDoc_IndentFix(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n name: x\ndata:\n  a: 1\n"
	out, changed, err := formatYAMLDocument(in, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed")
	}
	expected := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: 1\n"
	if out != expected {
		t.Fatalf("unexpected output:\n--- expected ---\n%s\n--- got ---\n%s", expected, out)
	}
	if !yamlMeaningEqual(in, out) {
		t.Fatalf("expected meaning preserved")
	}
}

func TestFormatting_MultiDoc_PreservesSeparator(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n name: x\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n"
	out, changed, err := formatYAMLDocument(in, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed")
	}
	if !yamlMeaningEqual(in, out) {
		t.Fatalf("expected meaning preserved")
	}
	if !strings.Contains(out, "\n---\n") {
		t.Fatalf("expected document separator in output, got: %q", out)
	}
}

func TestFormatting_Template_NoOp(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.name }}\n"
	state = &ServerState{
		Documents:                    map[string]string{},
		DocVersion:                   map[string]int32{},
		YAMLCache:                    yamlstream.NewCache(),
		FormattingEnabled:            true,
		FormattingIndentSize:         2,
		FormattingDisableForTemplates: true,
	}
	uri := "file:///tmp/tpl.yaml"
	state.setDocument(uri, in, 1)

	params := &protocol.DocumentFormattingParams{}
	params.TextDocument.URI = uri
	params.Options = protocol.FormattingOptions{"tabSize": 2, "insertSpaces": true}

	edits, err := textDocumentFormatting(nil, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edits) != 0 {
		t.Fatalf("expected no edits for templates")
	}
}

func TestFormatting_Handler_ReturnsWholeDocumentEdit(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n name: x\n"
	state = &ServerState{
		Documents:                    map[string]string{},
		DocVersion:                   map[string]int32{},
		YAMLCache:                    yamlstream.NewCache(),
		FormattingEnabled:            true,
		FormattingIndentSize:         2,
		FormattingDisableForTemplates: true,
	}
	uri := "file:///tmp/a.yaml"
	state.setDocument(uri, in, 1)

	params := &protocol.DocumentFormattingParams{}
	params.TextDocument.URI = uri
	params.Options = protocol.FormattingOptions{"tabSize": 2, "insertSpaces": true}

	edits, err := textDocumentFormatting(nil, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].NewText == "" {
		t.Fatalf("expected new text")
	}
	if edits[0].Range.Start.Line != 0 || edits[0].Range.Start.Character != 0 {
		t.Fatalf("expected start at 0,0")
	}
}
