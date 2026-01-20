package resolver

import (
	"path/filepath"
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"gopkg.in/yaml.v3"
)

func posAfter(t *testing.T, content, needle string) (line, col int) {
	t.Helper()
	idx := strings.Index(content, needle)
	if idx < 0 {
		t.Fatalf("needle not found: %q", needle)
	}
	pos := idx + len(needle)
	line = strings.Count(content[:pos], "\n")
	lastNL := strings.LastIndex(content[:pos], "\n")
	if lastNL < 0 {
		col = pos
		return
	}
	col = pos - (lastNL + 1)
	return
}

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

func TestCompletion_MultiDoc_SecondDocument(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "cm-ref",
				Symbol:     "k8s.resource.name",
				TargetKind: "ConfigMap",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.template.spec.containers.envFrom.configMapRef.name",
				},
			},
		},
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Name: "cm-a", Namespace: "default", FilePath: "/tmp/cm-a.yaml"})
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Name: "cm-b", Namespace: "default", FilePath: "/tmp/cm-b.yaml"})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: svc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: c
        envFrom:
        - configMapRef:
            name: 
`

	stream, err := yamlstream.Parse(yamlContent)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Cursor inside doc #2 on the blank value after "name: ".
	// (0-based line/col). The leading newline makes lines 1-based in comments.
	line := 17
	col := 18

	items, err := r.CompletionStream(stream, line, col)
	if err != nil {
		t.Fatalf("CompletionStream failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Expected 2 completion items, got %d", len(items))
	}
}

func TestCompletion_SchemaValueEnum_ServiceType(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	_, _ = schema.LoadCRDSchemasFromFile(reg, filepath.Join("..", "..", "rules", "schemas", "service.core.v1.schema.yaml"))
	r := NewResolver(store, cfg, reg)

	yamlContent := "\napiVersion: v1\nkind: Service\nmetadata:\n  name: svc\nspec:\n  type: "
	line, col := posAfter(t, yamlContent, "type: ")
	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("Expected completion items, got 0")
	}
	found := false
	for _, it := range items {
		if it.Label == "ClusterIP" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Expected to find enum value ClusterIP in completion items")
	}
}

func TestCompletion_SchemaValueBoolean_DeploymentPaused(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	r := NewResolver(store, cfg, reg)

	yamlContent := "\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: dep\nspec:\n  paused: "
	line, col := posAfter(t, yamlContent, "paused: ")
	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("Expected at least 2 completion items, got %d", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Label] = true
	}
	if !seen["true"] || !seen["false"] {
		t.Fatalf("Expected boolean completions true/false, got: %+v", seen)
	}
}

func TestCompletion_LabelSelector_MatchLabels_Value(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service.selector.label",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Service"},
					Path:  "spec.selector",
				},
			},
		},
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "a", Namespace: "default", FilePath: "/tmp/a.yaml", Labels: map[string]string{"app": "web"}})
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "b", Namespace: "default", FilePath: "/tmp/b.yaml", Labels: map[string]string{"app": "api"}})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: svc
spec:
  selector:
    app: 
`

	// Cursor after "app: "
	line := 7
	col := 9
	items, err := r.Completion(yamlContent, line, col)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Expected 2 completion items, got %d", len(items))
	}
}

func TestCompletion_LabelSelector_MatchLabels_Key(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "workload.selector.matchLabels",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.selector.matchLabels",
				},
			},
		},
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "a", Namespace: "default", FilePath: "/tmp/a.yaml", Labels: map[string]string{"app": "web", "tier": "backend"}})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  selector:
    matchLabels:
      ap: x
`

	// Cursor on the key "ap" (should suggest keys like "app", "tier").
	items, err := r.Completion(yamlContent, 8, 7)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) < 1 {
		t.Fatalf("Expected at least 1 completion item, got %d", len(items))
	}
}

func TestCompletion_LabelSelector_Value_FallbackAcrossNamespaces(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service.selector.label",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Service"},
					Path:  "spec.selector",
				},
			},
		},
	}

	store := indexer.NewStore()
	// Only in other namespace; completion should still suggest it via fallback.
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "a", Namespace: "other", FilePath: "/tmp/a.yaml", Labels: map[string]string{"app": "web"}})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: svc
  namespace: default
spec:
  selector:
    app: 
`

	items, err := r.Completion(yamlContent, 8, 9)
	if err != nil {
		t.Fatalf("Completion failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Expected 1 completion item, got %d", len(items))
	}
}

func TestCompletion_LabelSelector_MatchExpressions_Key_And_Values(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "workload.selector.matchExpressions",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.selector.matchExpressions",
				},
			},
		},
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "a", Namespace: "default", FilePath: "/tmp/a.yaml", Labels: map[string]string{"app": "web", "tier": "backend"}})
	store.Add(&indexer.K8sResource{Kind: "Deployment", Name: "b", Namespace: "default", FilePath: "/tmp/b.yaml", Labels: map[string]string{"app": "api"}})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  selector:
    matchExpressions:
    - key: 
      operator: In
      values:
      - 
`

	// 1) Complete the matchExpressions key field (should suggest label keys)
	itemsKey, err := r.Completion(yamlContent, 8, 11)
	if err != nil {
		t.Fatalf("Completion (key) failed: %v", err)
	}
	if len(itemsKey) < 1 {
		t.Fatalf("Expected at least 1 completion item for key, got %d", len(itemsKey))
	}

	// 2) Complete the values entry. Set key to 'app' and then locate the values[] scalar node
	// from the parsed YAML AST to get stable line/col.
	yamlContent2 := strings.Replace(yamlContent, "key: ", "key: app", 1)
	stream, err := yamlstream.Parse(yamlContent2)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if stream == nil || len(stream.Docs) == 0 || stream.Docs[0].Node == nil {
		t.Fatalf("Expected parsed stream with at least one document")
	}

	// Navigate to spec.selector.matchExpressions[0].values[0]
	root := stream.Docs[0].Node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			t.Fatalf("Expected document content")
		}
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		t.Fatalf("Expected root mapping")
	}
	spec := getMappingValue(root, "spec")
	selector := getMappingValue(spec, "selector")
	matchExprs := getMappingValue(selector, "matchExpressions")
	if matchExprs == nil || matchExprs.Kind != yaml.SequenceNode || len(matchExprs.Content) == 0 {
		t.Fatalf("Expected matchExpressions sequence")
	}
	expr0 := matchExprs.Content[0]
	values := getMappingValue(expr0, "values")
	if values == nil || values.Kind != yaml.SequenceNode || len(values.Content) == 0 {
		t.Fatalf("Expected values sequence")
	}
	item0 := values.Content[0]
	if item0 == nil {
		t.Fatalf("Expected first values item")
	}

	line := item0.Line - 1
	col := item0.Column - 1
	if line < 0 {
		line = 0
	}
	if col < 0 {
		col = 0
	}

	itemsVal, err := r.CompletionStream(stream, line, col)
	if err != nil {
		t.Fatalf("Completion (values) failed: %v", err)
	}
	if len(itemsVal) != 2 {
		t.Fatalf("Expected 2 completion items for values, got %d", len(itemsVal))
	}
}
