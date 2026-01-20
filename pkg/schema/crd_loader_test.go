package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCRDSchemas_Composed_oneOf_MergesProperties(t *testing.T) {
	reg := NewRegistry()

	crd := "" +
		"apiVersion: apiextensions.k8s.io/v1\n" +
		"kind: CustomResourceDefinition\n" +
		"metadata:\n" +
		"  name: widgets.example.com\n" +
		"spec:\n" +
		"  group: example.com\n" +
		"  names:\n" +
		"    kind: Widget\n" +
		"    plural: widgets\n" +
		"  scope: Namespaced\n" +
		"  versions:\n" +
		"  - name: v1\n" +
		"    served: true\n" +
		"    storage: true\n" +
		"    schema:\n" +
		"      openAPIV3Schema:\n" +
		"        type: object\n" +
		"        properties:\n" +
		"          spec:\n" +
		"            oneOf:\n" +
		"            - type: object\n" +
		"              properties:\n" +
		"                foo:\n" +
		"                  type: string\n" +
		"            - type: object\n" +
		"              properties:\n" +
		"                bar:\n" +
		"                  type: string\n"

	loaded, err := LoadCRDSchemasFromYAML(reg, crd)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("expected 1 schema loaded, got %d", loaded)
	}

	root := reg.Get(GVK{Group: "example.com", Version: "v1", Kind: "Widget"})
	if root == nil {
		t.Fatalf("expected schema")
	}

	spec := ResolvePath(root, []string{"spec"})
	if spec == nil || spec.Properties == nil {
		t.Fatalf("expected spec schema with properties")
	}
	if spec.Properties["foo"] == nil {
		t.Fatalf("expected merged property foo")
	}
	if spec.Properties["bar"] == nil {
		t.Fatalf("expected merged property bar")
	}
}

func TestValidateDocument_ComposedSchema_UnknownFieldStillWorks(t *testing.T) {
	reg := NewRegistry()

	crd := "" +
		"apiVersion: apiextensions.k8s.io/v1\n" +
		"kind: CustomResourceDefinition\n" +
		"metadata:\n" +
		"  name: widgets.example.com\n" +
		"spec:\n" +
		"  group: example.com\n" +
		"  names:\n" +
		"    kind: Widget\n" +
		"    plural: widgets\n" +
		"  scope: Namespaced\n" +
		"  versions:\n" +
		"  - name: v1\n" +
		"    served: true\n" +
		"    storage: true\n" +
		"    schema:\n" +
		"      openAPIV3Schema:\n" +
		"        type: object\n" +
		"        properties:\n" +
		"          spec:\n" +
		"            oneOf:\n" +
		"            - type: object\n" +
		"              properties:\n" +
		"                foo:\n" +
		"                  type: string\n"

	_, err := LoadCRDSchemasFromYAML(reg, crd)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	root := reg.Get(GVK{Group: "example.com", Version: "v1", Kind: "Widget"})
	if root == nil {
		t.Fatalf("expected schema")
	}

	y := "" +
		"apiVersion: example.com/v1\n" +
		"kind: Widget\n" +
		"metadata:\n" +
		"  name: w\n" +
		"spec:\n" +
		"  baz: oops\n"

	doc := decodeSingleDoc(t, y)
	diags := ValidateDocument(doc, root)
	if len(diags) == 0 {
		t.Fatalf("expected unknown-field diagnostic")
	}
}

func TestLoadLocalSchemaDirectory_AllYAMLParse(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	schemasDir := filepath.Join("..", "..", "rules", "schemas")
	entries, err := os.ReadDir(schemasDir)
	if err != nil {
		// If the directory doesn't exist (e.g., in minimal test env), don't fail.
		// In this repo it should exist.
		t.Fatalf("failed to read %q: %v", schemasDir, err)
	}
	loaded := 0
	for _, ent := range entries {
		if ent == nil || ent.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(ent.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		p := filepath.Join(schemasDir, ent.Name())
		n, err := LoadCRDSchemasFromFile(reg, p)
		if err != nil {
			t.Fatalf("failed to load schema %q: %v", p, err)
		}
		loaded += n
	}
	if loaded == 0 {
		t.Fatalf("expected at least 1 schema loaded from %q", schemasDir)
	}
}
