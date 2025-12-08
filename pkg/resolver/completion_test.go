package resolver

import (
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestCompletion(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service-ref",
				Symbol:     "k8s.resource.name",
				TargetKind: "Service",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.template.spec.containers.env.valueFrom.configMapKeyRef.name",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
	}
	store.Add(serviceRes)

	serviceRes2 := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "other-service",
		Namespace: "default",
		FilePath:  "/tmp/service2.yaml",
	}
	store.Add(serviceRes2)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content
	yamlContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
spec:
  template:
    spec:
      containers:
      - name: my-container
        env:
        - name: MY_CONFIG
          valueFrom:
            configMapKeyRef:
              name: 
              key: some-key
`
	// Line 14: "              name: "
	// Indent 14. "name: " 6.
	// Cursor at col 20 (after "name: ")
	line := 14
	col := 20

	// 5. Call Completion
	items, err := r.Completion(yamlContent, line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Expected 2 completion items, got %d", len(items))
	}

	foundMyService := false
	foundOtherService := false

	for _, item := range items {
		if item.Label == "my-service" {
			foundMyService = true
		}
		if item.Label == "other-service" {
			foundOtherService = true
		}
	}

	if !foundMyService {
		t.Error("Did not find my-service in completion items")
	}
	if !foundOtherService {
		t.Error("Did not find other-service in completion items")
	}
}
