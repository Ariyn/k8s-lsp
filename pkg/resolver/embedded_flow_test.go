package resolver

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestUpdateEmbeddedContentFlowStyle(t *testing.T) {
	// Setup Resolver
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// Input uses Flow Style for data
	docContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: example
data: { key: "initial value" }
`
	key := "key"
	newContent := "line1\nline2\n"

	updated, err := r.UpdateEmbeddedContent(docContent, key, newContent)
	if err != nil {
		t.Fatalf("UpdateEmbeddedContent failed: %v", err)
	}

	// Check if it uses |- (block scalar)
	expectedSnippet := "key: |-"
	if !strings.Contains(updated, expectedSnippet) {
		t.Errorf("Expected output to contain %q, but got:\n%s", expectedSnippet, updated)
	}

	// Check if data is converted to block style (implicit check by checking for key: |-)
	// Also check that it's NOT flow style anymore (no braces around data value)
	if strings.Contains(updated, "data: {") {
		t.Errorf("Expected output to NOT contain flow style data, but got:\n%s", updated)
	}
}
