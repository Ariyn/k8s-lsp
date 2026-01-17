package schema

import "testing"

func TestLoadGVKSchemasFromYAML_MinimalDoc(t *testing.T) {
	reg := NewRegistry()

	y := "" +
		"group: \"\"\n" +
		"version: v1\n" +
		"kind: Pod\n" +
		"openAPIV3Schema:\n" +
		"  type: object\n" +
		"  properties:\n" +
		"    spec:\n" +
		"      type: object\n" +
		"      properties:\n" +
		"        restartPolicy:\n" +
		"          type: string\n" +
		"          enum: [Always, OnFailure, Never]\n"

	loaded, err := LoadGVKSchemasFromYAML(reg, y)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("expected 1 loaded schema, got %d", loaded)
	}
	if reg.Get(GVK{Group: "", Version: "v1", Kind: "Pod"}) == nil {
		t.Fatalf("expected schema to be registered")
	}
}

func TestLoadGVKSchemasFromYAML_SpecWrapped(t *testing.T) {
	reg := NewRegistry()

	y := "" +
		"apiVersion: k8s-lsp.dev/v1\n" +
		"kind: Schema\n" +
		"spec:\n" +
		"  group: \"\"\n" +
		"  version: v1\n" +
		"  kind: Service\n" +
		"  schema:\n" +
		"    openAPIV3Schema:\n" +
		"      type: object\n" +
		"      properties:\n" +
		"        spec:\n" +
		"          type: object\n" +
		"          properties:\n" +
		"            type:\n" +
		"              type: string\n" +
		"              enum: [ClusterIP]\n"

	loaded, err := LoadGVKSchemasFromYAML(reg, y)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("expected 1 loaded schema, got %d", loaded)
	}
	if reg.Get(GVK{Group: "", Version: "v1", Kind: "Service"}) == nil {
		t.Fatalf("expected schema to be registered")
	}
}
