package schema

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeSingleDoc(t *testing.T, content string) *yaml.Node {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(content))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	return &doc
}

func TestValidateDocument_UnknownField_TopLevel(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	y := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metdata:\n" +
		"  name: dep\n" +
		"spec: {}\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "apps", Version: "v1", Kind: "Deployment"})
	if root == nil {
		t.Fatalf("expected built-in schema")
	}

	diags := ValidateDocument(doc, root)
	if len(diags) == 0 {
		t.Fatalf("expected at least 1 diagnostic")
	}

	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Unknown field") && strings.Contains(d.Message, "metdata") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unknown field diagnostic for metdata, got: %#v", diags)
	}
}

func TestValidateDocument_TypeMismatch_Integer(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	y := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metadata:\n" +
		"  name: dep\n" +
		"spec:\n" +
		"  replicas: \"three\"\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "apps", Version: "v1", Kind: "Deployment"})
	if root == nil {
		t.Fatalf("expected built-in schema")
	}

	diags := ValidateDocument(doc, root)
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Type mismatch") && strings.Contains(d.Message, "expected integer") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected type mismatch diagnostic, got: %#v", diags)
	}
}

func TestValidateDocument_EnumMismatch(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	y := "" +
		"apiVersion: v1\n" +
		"kind: Service\n" +
		"metadata:\n" +
		"  name: svc\n" +
		"spec:\n" +
		"  type: CluserIP\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "", Version: "v1", Kind: "Service"})
	if root == nil {
		t.Fatalf("expected built-in schema")
	}

	diags := ValidateDocument(doc, root)
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Invalid value") && strings.Contains(d.Message, "CluserIP") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected enum mismatch diagnostic, got: %#v", diags)
	}
}
