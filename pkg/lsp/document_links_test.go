package lsp

import (
	"testing"

	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"
)

func TestBuildDocumentLinks_ReferenceScalar(t *testing.T) {
	y := `apiVersion: v1
kind: Pod
metadata:
  name: demo
  namespace: default
spec:
  serviceAccountName: default
`
	stream, err := yamlstream.Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	reg := schema.NewRegistry()
	// Builtins now tag metadata.namespace and common refs.
	schema.RegisterBuiltins(reg)

	links := buildDocumentLinks(stream, reg, "file:///test.yaml")
	if len(links) == 0 {
		t.Fatalf("expected at least one document link")
	}

	found := false
	for _, l := range links {
		if l.Target != nil && *l.Target != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one document link target")
	}
}
