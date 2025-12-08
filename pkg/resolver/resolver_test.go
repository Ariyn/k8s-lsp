package resolver

import (
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestResolveDefinition(t *testing.T) {
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
	// line 14 is "              name: my-service" (because of leading newline)
	// indentation is 14 spaces. "name: " is 6 chars.
	// So "my-service" starts at col 20 (0-based).
	line := 14
	col := 20

	// 5. Call ResolveDefinition
	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/deployment.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/service.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/service.yaml, got %s", locs[0].TargetURI)
	}
	if locs[0].TargetRange.Start.Line != 0 {
		t.Errorf("Expected TargetRange.Start.Line 0, got %d", locs[0].TargetRange.Start.Line)
	}
}

func TestResolveDefinition_Self(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.service",
				Definitions: []config.SymbolDefinition{
					{
						Kinds: []string{"Service"},
						Path:  "metadata.name",
					},
				},
			},
		},
	}

	// 2. Setup Store (empty is fine, or with self)
	store := indexer.NewStore()

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content
	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
`
	// Line 0: empty
	// Line 1: apiVersion
	// Line 2: kind
	// Line 3: metadata
	// Line 4:   name: my-service

	// "  name: my-service"
	// Indent 2. "name: " 6.
	// "my-service" starts at col 8.
	line := 4
	col := 8

	// 5. Call ResolveDefinition
	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/service.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/service.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/service.yaml, got %s", locs[0].TargetURI)
	}
	// TargetRange should be the range of "my-service"
	if locs[0].TargetRange.Start.Line != 4 {
		t.Errorf("Expected TargetRange.Start.Line 4, got %d", locs[0].TargetRange.Start.Line)
	}
}

func TestResolveReferences(t *testing.T) {
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

	// Add Service definition
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
		Line:      0,
		Col:       0,
	}
	store.Add(serviceRes)

	// Add Deployment referencing the Service
	// We need to simulate that the indexer has already found the reference.
	// The Store stores references in the resource that HAS the reference?
	// No, Store.FindReferences(kind, name) searches all resources for references.
	// So we need to add a Deployment resource to the store, and it must have the reference in its `References` list.

	deploymentRes := &indexer.K8sResource{
		Kind:      "Deployment",
		Name:      "my-deployment",
		Namespace: "default",
		FilePath:  "/tmp/deployment.yaml",
		Line:      0,
		Col:       0,
		References: []indexer.Reference{
			{
				Kind:   "Service",
				Name:   "my-service",
				Symbol: "k8s.resource.name",
				Line:   14, // Matches the line in TestResolveDefinition
				Col:    20,
			},
		},
	}
	store.Add(deploymentRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content (The Service file)
	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
`
	// Line 4:   name: my-service
	line := 4
	col := 8

	// 5. Call ResolveReferences
	locs, err := r.ResolveReferences(yamlContent, line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	// Should find:
	// 1. The definition itself (Service)
	// 2. The reference in Deployment
	if len(locs) != 2 {
		t.Fatalf("Expected 2 locations, got %d", len(locs))
	}

	// Check if we found the deployment reference
	foundDeployment := false
	for _, loc := range locs {
		if loc.URI == "file:///tmp/deployment.yaml" {
			foundDeployment = true
			if loc.Range.Start.Line != 14 {
				t.Errorf("Expected reference at line 14, got %d", loc.Range.Start.Line)
			}
		}
	}
	if !foundDeployment {
		t.Error("Did not find reference in deployment.yaml")
	}
}

func TestResolveLabelReferences(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.label",
				Definitions: []config.SymbolDefinition{
					{
						Kinds: []string{"Pod"},
						Path:  "metadata.labels",
					},
				},
			},
		},
		References: []config.Reference{
			{
				Name:       "service-selector",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Service"},
					Path:  "spec.selector",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()

	// Add Service referencing the label
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
		Line:      0,
		Col:       0,
		References: []indexer.Reference{
			{
				Kind:   "Pod",
				Name:   "my-app", // The value of the label
				Symbol: "k8s.label",
				Line:   10,
				Col:    10,
			},
		},
	}
	store.Add(serviceRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content (The Pod file)
	yamlContent := `
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  labels:
    app: my-app
spec:
  containers:
  - name: nginx
    image: nginx
`
	// Line 6:     app: my-app
	// Indent 4. "app: " 5.
	// "my-app" starts at col 9.
	line := 6
	col := 9

	// 5. Call ResolveReferences
	locs, err := r.ResolveReferences(yamlContent, line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}

	// Should find:
	// 1. The definition itself (Pod label) - Wait, findLabelReferences finds definitions too?
	// Yes, step 1 in findLabelReferences calls Store.FindByLabel.
	// But we haven't added the Pod to the store yet.
	// ResolveReferences doesn't add the current file to the store automatically (unless we do it).
	// But findLabelReferences searches the store.
	// So if we want the definition to be found, we should add the Pod to the store too, or expect it not to be found if not in store.
	// However, usually "Find References" includes the current location if it's a definition.
	// But findLabelReferences implementation:
	// 1. Find definitions (resources having this label) -> Store.FindByLabel
	// 2. Find usages -> Store.FindLabelReferences

	// Since we didn't add the Pod to the store, step 1 will return empty.
	// Step 2 should find the Service.

	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}

	if locs[0].URI != "file:///tmp/service.yaml" {
		t.Errorf("Expected URI file:///tmp/service.yaml, got %s", locs[0].URI)
	}
}
