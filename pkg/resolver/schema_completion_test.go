package resolver

import (
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/schema"
)

func mustLoadTestSchemas(t *testing.T, reg *schema.Registry, y string) {
	t.Helper()
	loaded, err := schema.LoadGVKSchemasFromYAML(reg, y)
	if err != nil {
		t.Fatalf("failed to load test schemas: %v", err)
	}
	if loaded == 0 {
		t.Fatalf("expected to load at least 1 schema")
	}
}

func TestCompletion_Schema_KeyCompletion_DeploymentPodSpec(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	mustLoadTestSchemas(t, reg, ""+
		"group: apps\n"+
		"version: v1\n"+
		"kind: Deployment\n"+
		"openAPIV3Schema:\n"+
		"  type: object\n"+
		"  properties:\n"+
		"    spec:\n"+
		"      type: object\n"+
		"      properties:\n"+
		"        template:\n"+
		"          type: object\n"+
		"          properties:\n"+
		"            spec:\n"+
		"              type: object\n"+
		"              properties:\n"+
		"                containers:\n"+
		"                  type: array\n"+
		"                  items: { type: object }\n"+
		"              additionalProperties: true\n"+
		"          additionalProperties: true\n"+
		"      additionalProperties: true\n"+
		"  additionalProperties: true\n")

	r := NewResolver(store, cfg, reg)

	yamlContent := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metadata:\n" +
		"  name: dep\n" +
		"spec:\n" +
		"  template:\n" +
		"    spec:\n" +
		"      cont: x\n"

	// Cursor on the key "cont" (0-based line/col)
	line := 7
	col := 7

	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected completion items")
	}

	foundContainers := false
	for _, it := range items {
		if it.Label == "containers" {
			foundContainers = true
			break
		}
	}
	if !foundContainers {
		t.Fatalf("expected to find containers in completion: %#v", items)
	}
}

func TestCompletion_Schema_EnumValueCompletion_ServiceType(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	mustLoadTestSchemas(t, reg, ""+
		"group: \"\"\n"+
		"version: v1\n"+
		"kind: Service\n"+
		"openAPIV3Schema:\n"+
		"  type: object\n"+
		"  properties:\n"+
		"    spec:\n"+
		"      type: object\n"+
		"      properties:\n"+
		"        type:\n"+
		"          type: string\n"+
		"          enum: [ClusterIP, NodePort, LoadBalancer, ExternalName]\n"+
		"      additionalProperties: true\n"+
		"  additionalProperties: true\n")

	r := NewResolver(store, cfg, reg)

	yamlContent := "" +
		"apiVersion: v1\n" +
		"kind: Service\n" +
		"metadata:\n" +
		"  name: svc\n" +
		"spec:\n" +
		"  type: \n"

	// Cursor after "type: " on line 5.
	line := 5
	col := 8

	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected enum completion items")
	}

	expected := map[string]bool{"ClusterIP": false, "NodePort": false, "LoadBalancer": false, "ExternalName": false}
	for _, it := range items {
		if _, ok := expected[it.Label]; ok {
			expected[it.Label] = true
		}
	}
	for k, v := range expected {
		if !v {
			t.Fatalf("missing enum value %q in completion: %#v", k, items)
		}
	}
}

func TestCompletion_Schema_KeyCompletion_PVCSpec(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	mustLoadTestSchemas(t, reg, ""+
		"group: \"\"\n"+
		"version: v1\n"+
		"kind: PersistentVolumeClaim\n"+
		"openAPIV3Schema:\n"+
		"  type: object\n"+
		"  properties:\n"+
		"    spec:\n"+
		"      type: object\n"+
		"      properties:\n"+
		"        storageClassName: { type: string }\n"+
		"      additionalProperties: true\n"+
		"  additionalProperties: true\n")

	r := NewResolver(store, cfg, reg)

	yamlContent := "" +
		"apiVersion: v1\n" +
		"kind: PersistentVolumeClaim\n" +
		"metadata:\n" +
		"  name: pvc\n" +
		"spec:\n" +
		"  stor: x\n"

	// Cursor on the key "stor" (0-based line/col)
	line := 5
	col := 4

	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected completion items")
	}

	foundStorageClassName := false
	for _, it := range items {
		if it.Label == "storageClassName" {
			foundStorageClassName = true
			break
		}
	}
	if !foundStorageClassName {
		t.Fatalf("expected to find storageClassName in completion: %#v", items)
	}
}
