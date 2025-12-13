package resolver

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestResolveHover(t *testing.T) {
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
		Line:      0,
		Col:       0,
	}
	store.Add(serviceRes)

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
              name: my-service
              key: some-key
`
	// "name: my-service" is on line 14.
	// "              name: " is 14 spaces + "name: " (6) = 20 chars.
	// "my-service" starts at col 20.
	line := 14
	col := 20

	// 5. Call ResolveHover
	hover, err := r.ResolveHover(yamlContent, "file:///tmp/deployment.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveHover failed: %v", err)
	}
	if hover == nil {
		t.Fatal("Expected hover, got nil")
	}

	expectedContent := "**my-service**\n\nKind: Service\nNamespace: default\nFile: /tmp/service.yaml"

	contents, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("Expected MarkupContent, got %T", hover.Contents)
	}

	if !strings.Contains(contents.Value, expectedContent) {
		t.Errorf("Expected hover content to contain %q, got %q", expectedContent, contents.Value)
	}
}
