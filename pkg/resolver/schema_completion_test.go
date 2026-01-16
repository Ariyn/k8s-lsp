package resolver

import (
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/schema"
)

func TestCompletion_Schema_KeyCompletion_DeploymentPodSpec(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)

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
