package resolver

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestResolveReferences_ConfigMapEmbeddedFile_FindsUsages(t *testing.T) {
	store := indexer.NewStore()
	cfg := &config.Config{}
	r := NewResolver(store, cfg)

	// Resource that uses the ConfigMap (mount-all + item key)
	store.Add(&indexer.K8sResource{
		Kind:      "Deployment",
		Name:      "app",
		Namespace: "default",
		FilePath:  "/tmp/deploy.yaml",
		References: []indexer.Reference{
			{Kind: "ConfigMap", Name: "my-cm", Namespace: "default", Line: 10, Col: 20},
			{Kind: "ConfigMap", Name: "my-cm", Key: "app.conf", Namespace: "default", Line: 12, Col: 22},
		},
	})

	yamlContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  app.conf: |-
    hello
`

	// Cursor on the key "app.conf" (line 7 in YAML, 0-based line=6; column starts after two spaces)
	locs, err := r.ResolveReferences(yamlContent, "file:///tmp/cm.yaml", 6, 2)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	if len(locs) != 2 {
		t.Fatalf("Expected 2 usage locations, got %d", len(locs))
	}

	// Ensure both the ConfigMap name usage and the key usage are present.
	var gotName, gotKey bool
	for _, loc := range locs {
		if loc.URI != "file:///tmp/deploy.yaml" {
			t.Errorf("Unexpected URI: %s", loc.URI)
			continue
		}
		span := int(loc.Range.End.Character - loc.Range.Start.Character)
		switch span {
		case len("my-cm"):
			gotName = true
		case len("app.conf"):
			gotKey = true
		default:
			// ignore
		}
	}
	if !gotName {
		t.Fatalf("Expected to find a ConfigMap name usage")
	}
	if !gotKey {
		t.Fatalf("Expected to find an embedded file key usage")
	}
}

func TestResolveHover_ConfigMapEmbeddedFile_HasOpenAndFindActions(t *testing.T) {
	store := indexer.NewStore()
	cfg := &config.Config{}
	r := NewResolver(store, cfg)

	yamlContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  app.conf: |-
    hello
`

	hover, err := r.ResolveHover(yamlContent, "file:///tmp/cm.yaml", 6, 2)
	if err != nil {
		t.Fatalf("ResolveHover failed: %v", err)
	}
	if hover == nil {
		t.Fatalf("Expected hover, got nil")
	}

	contents, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("Expected MarkupContent, got %T", hover.Contents)
	}

	if !strings.Contains(contents.Value, "Open File") {
		t.Fatalf("Expected hover to contain Open File action, got: %q", contents.Value)
	}
	if !strings.Contains(contents.Value, "Find Usages") {
		t.Fatalf("Expected hover to contain Find Usages action, got: %q", contents.Value)
	}
	if !strings.Contains(contents.Value, "command:k8sLsp.openEmbeddedFile") {
		t.Fatalf("Expected hover to contain open command link, got: %q", contents.Value)
	}
	if !strings.Contains(contents.Value, "command:k8sLsp.findEmbeddedFileUsages") {
		t.Fatalf("Expected hover to contain find usages command link, got: %q", contents.Value)
	}
}
