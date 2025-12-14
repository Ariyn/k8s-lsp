package resolver

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestUpdateEmbeddedContentTrailingSpaces(t *testing.T) {
	// Setup Resolver
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: example
data:
  key: initial value
`
	key := "key"
	// Input with trailing spaces on an empty line
	newContent := "line1\n  \nline2\n"

	updated, err := r.UpdateEmbeddedContent(docContent, key, newContent)
	if err != nil {
		t.Fatalf("UpdateEmbeddedContent failed: %v", err)
	}

	// Check if it uses |- (block scalar)
	expectedSnippet := "key: |-"
	if !strings.Contains(updated, expectedSnippet) {
		t.Errorf("Expected output to contain %q, but got:\n%s", expectedSnippet, updated)
	}

	// Check if trailing spaces are removed
	if strings.Contains(updated, "line1\n  \nline2") {
		t.Errorf("Expected output to NOT contain trailing spaces on empty line, but got:\n%s", updated)
	}
}
