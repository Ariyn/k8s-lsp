package indexer

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"k8s-lsp/pkg/config"

	"gopkg.in/yaml.v3"
)

func TestDynamicCRDRegistration(t *testing.T) {
	// Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.resource.name",
				Definitions: []config.SymbolDefinition{
					{
						Kinds: []string{"Service"},
						Path:  "metadata.name",
					},
				},
			},
		},
	}

	store := NewStore()
	idx := NewIndexer(store, cfg)

	// 1. Parse CRD
	crdYaml := `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: myresources.example.com
spec:
  group: example.com
  names:
    kind: MyResource
`
	var crdNode yaml.Node
	if err := yaml.Unmarshal([]byte(crdYaml), &crdNode); err != nil {
		t.Fatalf("Failed to unmarshal CRD: %v", err)
	}

	// We call parseK8sResource directly to simulate indexing
	// Note: parseK8sResource expects a DocumentNode
	// yaml.Unmarshal returns a DocumentNode if the input is a document.
	// Let's verify node kind.
	if crdNode.Kind != yaml.DocumentNode {
		t.Fatalf("Expected DocumentNode, got %v", crdNode.Kind)
	}

	idx.parseK8sResource(&crdNode, "crd.yaml")

	// 2. Verify Config updated
	found := false
	for _, sym := range cfg.Symbols {
		if sym.Name == "k8s.resource.name" {
			for _, def := range sym.Definitions {
				for _, k := range def.Kinds {
					if k == "MyResource" {
						found = true
					}
				}
			}
		}
	}

	if !found {
		t.Fatal("MyResource was not registered in Config after parsing CRD")
	}

	// 3. Parse MyResource instance
	crYaml := `
apiVersion: example.com/v1
kind: MyResource
metadata:
  name: my-instance
`
	var crNode yaml.Node
	if err := yaml.Unmarshal([]byte(crYaml), &crNode); err != nil {
		t.Fatalf("Failed to unmarshal CR: %v", err)
	}

	res := idx.parseK8sResource(&crNode, "cr.yaml")
	if res == nil {
		t.Fatal("Failed to index MyResource instance")
	}

	if res.Name != "my-instance" {
		t.Errorf("Expected name 'my-instance', got '%s'", res.Name)
	}
}

func TestIndexContent(t *testing.T) {
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.resource.name",
				Definitions: []config.SymbolDefinition{
					{Kinds: []string{"Pod"}, Path: "metadata.name"},
				},
			},
		},
	}
	store := NewStore()
	idx := NewIndexer(store, cfg)

	content := `
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
`
	idx.IndexContent("test.yaml", content)

	res := store.Get("Pod", "default", "test-pod")
	if res == nil {
		t.Fatal("Pod was not indexed from content")
	}
	if res.Name != "test-pod" {
		t.Errorf("Expected name 'test-pod', got '%s'", res.Name)
	}
}

func TestConcurrentAccess(t *testing.T) {
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.resource.name",
				Definitions: []config.SymbolDefinition{
					{Kinds: []string{"Service"}, Path: "metadata.name"},
				},
			},
		},
	}
	store := NewStore()
	idx := NewIndexer(store, cfg)

	// Simulate concurrent CRD registration and indexing
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			crdYaml := fmt.Sprintf(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: myresources%d.example.com
spec:
  group: example.com
  names:
    kind: MyResource%d
`, i, i)
			idx.IndexContent(fmt.Sprintf("crd%d.yaml", i), crdYaml)
		}(i)
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Read config concurrently
			idx.mu.RLock()
			_ = len(cfg.Symbols)
			idx.mu.RUnlock()
		}(i)
	}

	wg.Wait()

	// Verify all kinds were registered
	sym := cfg.Symbols[0] // k8s.resource.name
	count := 0
	for _, def := range sym.Definitions {
		for _, k := range def.Kinds {
			if strings.HasPrefix(k, "MyResource") {
				count++
			}
		}
	}

	if count != 10 {
		t.Errorf("Expected 10 dynamic kinds, got %d", count)
	}
}
