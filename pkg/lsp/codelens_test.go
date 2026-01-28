package lsp

import (
	"testing"

	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"
)

func TestBuildCodeLenses_ReferenceScalar(t *testing.T) {
	y := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    metadata:
      name: demo-pod
    spec:
      serviceAccountName: default
`
	stream, err := yamlstream.Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)

	lenses := buildCodeLenses(stream, reg, "file:///test.yaml")
	if len(lenses) == 0 {
		t.Fatalf("expected at least one codelens")
	}

	found := false
	for _, l := range lenses {
		if l.Command != nil && (l.Command.Command == cmdPeekDefinition || l.Command.Command == cmdGoToDefinition) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected peek/go codelens commands")
	}
}
