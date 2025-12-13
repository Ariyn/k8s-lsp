package resolver

import (
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestCrossNamespaceResolution(t *testing.T) {
	// Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.resource.name",
				Definitions: []config.SymbolDefinition{
					{Kinds: []string{"Secret"}, Path: "metadata.name"},
				},
			},
		},
		References: []config.Reference{
			{
				Name:       "secret.ref",
				Symbol:     "k8s.resource.name",
				TargetKind: "Secret",
				Match: config.ReferenceMatch{
					Kinds: []string{"ExternalSecret"},
					Path:  "spec.secretRef.name",
				},
			},
		},
	}

	store := indexer.NewStore()
	idx := indexer.NewIndexer(store, cfg)
	res := NewResolver(store, cfg)

	// 1. Index Secret in "other-ns"
	secretYaml := `
apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: other-ns
`
	idx.IndexContent("secret.yaml", secretYaml)

	// 2. Index ExternalSecret in "default" referencing "other-ns"
	esYaml := `
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-es
  namespace: default
spec:
  secretRef:
    name: my-secret
    namespace: other-ns
`
	// "name: my-secret" is on line 9 (0-indexed: 8), value starts at col 10
	// spec:
	//   secretRef:
	//     name: my-secret
	// 01234567890

	locs, err := res.ResolveDefinition(esYaml, "es.yaml", 8, 11)
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}

	if len(locs) == 0 {
		t.Fatal("Expected to find definition, found none")
	}

	found := false
	for _, loc := range locs {
		if loc.TargetURI == "file://secret.yaml" || loc.TargetURI == "secret.yaml" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected to find secret.yaml, got %v", locs)
	}
}
