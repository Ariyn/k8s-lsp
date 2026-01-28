package schema

import "testing"

func TestParseRefMeta_FromSchemaPack(t *testing.T) {
	y := `group: ""
version: "v1"
kind: "Pod"
openAPIV3Schema:
  type: object
  properties:
    apiVersion: { type: string }
    kind: { type: string }
    metadata:
      type: object
      properties:
        name:
          type: string
          x-k8s-lsp-ref-role: definition
    spec:
      type: object
      properties:
        serviceName:
          type: string
          x-k8s-lsp-ref-role: reference
          x-k8s-lsp-ref-kind: Service
`
	reg := NewRegistry()
	RegisterBuiltins(reg)
	if _, err := LoadGVKSchemasFromYAML(reg, y); err != nil {
		t.Fatalf("load: %v", err)
	}

	root := reg.Get(GVK{Group: "", Version: "v1", Kind: "Pod"})
	if root == nil {
		t.Fatalf("expected schema root")
	}
	nameNode := ResolvePath(root, []string{"metadata", "name"})
	if nameNode == nil || nameNode.Ref == nil || !nameNode.Ref.IsDefinition() {
		t.Fatalf("expected metadata.name to be a definition ref")
	}

	svcNode := ResolvePath(root, []string{"spec", "serviceName"})
	if svcNode == nil || svcNode.Ref == nil || !svcNode.Ref.IsReference() {
		t.Fatalf("expected spec.serviceName to be a reference ref")
	}
	if svcNode.Ref.Kind != "Service" {
		t.Fatalf("expected ref kind Service, got %q", svcNode.Ref.Kind)
	}
}
