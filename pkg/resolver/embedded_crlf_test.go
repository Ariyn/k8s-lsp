package resolver

import (
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestUpdateEmbeddedContentCRLF(t *testing.T) {
	// Setup Resolver
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: example
data:
  key: initial value
`
	key := "key"
	// Simulate content with CRLF
	newContent := "line1\r\nline2\r\n"

	updated, err := r.UpdateEmbeddedContent(docContent, key, newContent)
	if err != nil {
		t.Fatalf("UpdateEmbeddedContent failed: %v", err)
	}

	// Check if it uses |- (block scalar with strip chomping)
	expectedSnippet := "key: |-"
	if !strings.Contains(updated, expectedSnippet) {
		t.Errorf("Expected output to contain %q, but got:\n%s", expectedSnippet, updated)
	}

	// Check if content is correct (normalized to \n)
	if strings.Contains(updated, "\r\n") {
		t.Errorf("Expected output to NOT contain CRLF, but got:\n%s", updated)
	}

	if !strings.Contains(updated, "line1") || !strings.Contains(updated, "line2") {
		t.Errorf("Expected output to contain content, but got:\n%s", updated)
	}
}
