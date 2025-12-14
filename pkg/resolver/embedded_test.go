package resolver

import (
	"log"
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
)

func TestUpdateEmbeddedContent(t *testing.T) {
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
	// Simulate content from editor which usually has a trailing newline
	newContent := "line1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\nline1\nline2\n"

	updated, err := r.UpdateEmbeddedContent(docContent, key, newContent)
	if err != nil {
		t.Fatalf("UpdateEmbeddedContent failed: %v", err)
	}

	// Check if it uses |- (block scalar with strip chomping)
	// yaml.v3 uses |- when there is no trailing newline in the value.
	// Since we trim the suffix in UpdateEmbeddedContent, it should result in |-

	log.Println(updated)

	expectedSnippet := "key: |-"
	if !strings.Contains(updated, expectedSnippet) {
		t.Errorf("Expected output to contain %q, but got:\n%s", expectedSnippet, updated)
	}

	// Check if content is correct
	if !strings.Contains(updated, "line1") || !strings.Contains(updated, "line2") {
		t.Errorf("Expected output to contain content, but got:\n%s", updated)
	}
}
