package resolver

import (
	"fmt"
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func applyTextEdit(content string, edit protocol.TextEdit) (string, error) {
	lines := strings.Split(content, "\n")
	startLine := int(edit.Range.Start.Line)
	startChar := int(edit.Range.Start.Character)
	endLine := int(edit.Range.End.Line)
	endChar := int(edit.Range.End.Character)

	if startLine < 0 || startLine > len(lines) {
		return "", fmt.Errorf("invalid start line")
	}
	if endLine < 0 || endLine > len(lines) {
		return "", fmt.Errorf("invalid end line")
	}
	if startLine == len(lines) {
		return "", fmt.Errorf("start line at EOF")
	}
	if endLine < startLine {
		return "", fmt.Errorf("end before start")
	}

	// Build start prefix
	if startChar < 0 || startChar > len(lines[startLine]) {
		return "", fmt.Errorf("invalid start char")
	}
	prefix := lines[startLine][:startChar]

	// Build suffix
	if endLine == len(lines) {
		// End can be one past the last line when replacing up to EOF in some LSP clients.
		endLine = len(lines) - 1
		endChar = len(lines[endLine])
	}
	if endChar < 0 {
		endChar = 0
	}
	if endChar > len(lines[endLine]) {
		endChar = len(lines[endLine])
	}
	suffix := lines[endLine][endChar:]

	before := strings.Join(lines[:startLine], "\n")
	after := strings.Join(lines[endLine+1:], "\n")

	var b strings.Builder
	if startLine > 0 {
		b.WriteString(before)
		b.WriteString("\n")
	}
	b.WriteString(prefix)
	b.WriteString(edit.NewText)
	b.WriteString(suffix)
	if endLine+1 < len(lines) {
		b.WriteString("\n")
		b.WriteString(after)
	}
	return b.String(), nil
}

func extractYamlMappingEntry(docContent, key string) string {
	lines := strings.Split(docContent, "\n")
	needle := ":"

	start := -1
	for i, line := range lines {
		trim := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trim, key+needle) {
			start = i
			break
		}
	}
	if start == -1 {
		return ""
	}

	baseIndent := countLeadingSpaces(lines[start])
	stop := start + 1
	for stop < len(lines) {
		line := lines[stop]
		if strings.TrimSpace(line) == "" {
			stop++
			continue
		}
		if countLeadingSpaces(line) <= baseIndent {
			break
		}
		stop++
	}

	return strings.Join(lines[start:stop], "\n")
}

func TestBuildEmbeddedContentTextEdit_PreservesOtherKeysExactly(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// Two data entries are block scalars; editing one should not change the other.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: example\n" +
		"data:\n" +
		"  keep: |-\n" +
		"    line1\n" +
		"    line2\n" +
		"  edit: |-\n" +
		"    old1\n" +
		"    old2\n"

	originalKeep := extractYamlMappingEntry(docContent, "keep")
	if originalKeep == "" {
		t.Fatalf("failed to extract original keep entry")
	}

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "edit", "newA\nnewB\n", "", "")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	updatedKeep := extractYamlMappingEntry(updated, "keep")
	if updatedKeep != originalKeep {
		t.Fatalf("expected other key to remain identical\n--- original ---\n%s\n--- updated ---\n%s", originalKeep, updatedKeep)
	}

	updatedEdit := extractYamlMappingEntry(updated, "edit")
	if !strings.Contains(updatedEdit, "edit: |-") {
		t.Fatalf("expected edited key to keep block indicator, got:\n%s", updatedEdit)
	}
	if !strings.Contains(updatedEdit, "newA") || !strings.Contains(updatedEdit, "newB") {
		t.Fatalf("expected edited content to be updated, got:\n%s", updatedEdit)
	}
}

func TestBuildEmbeddedContentTextEdit_PreservesFoldedIndicator(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: example\n" +
		"data:\n" +
		"  folded: >-\n" +
		"    a b\n" +
		"    c d\n" +
		"  keep: |-\n" +
		"    untouched\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "folded", "x y\nz\n", "", "")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	updatedFolded := extractYamlMappingEntry(updated, "folded")
	if !strings.Contains(updatedFolded, "folded: >-") {
		t.Fatalf("expected folded indicator to be preserved, got:\n%s", updatedFolded)
	}

	updatedKeep := extractYamlMappingEntry(updated, "keep")
	if !strings.Contains(updatedKeep, "keep: |-") || !strings.Contains(updatedKeep, "untouched") {
		t.Fatalf("expected keep entry unchanged, got:\n%s", updatedKeep)
	}
}

func TestBuildEmbeddedContentTextEdit_MultiDocumentYAML(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: Service\n" +
		"metadata:\n" +
		"  name: svc\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"data:\n" +
		"  keep: |-\n" +
		"    stay\n" +
		"  edit: |-\n" +
		"    old\n"

	originalKeep := extractYamlMappingEntry(docContent, "keep")
	if originalKeep == "" {
		t.Fatalf("failed to extract original keep entry")
	}

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "edit", "new1\nnew2\n", "", "")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	updatedKeep := extractYamlMappingEntry(updated, "keep")
	if updatedKeep != originalKeep {
		t.Fatalf("expected keep entry to remain identical across multi-doc edit\n--- original ---\n%s\n--- updated ---\n%s", originalKeep, updatedKeep)
	}

	updatedEdit := extractYamlMappingEntry(updated, "edit")
	if !strings.Contains(updatedEdit, "edit: |-") {
		t.Fatalf("expected edited key to keep block indicator, got:\n%s", updatedEdit)
	}
	if !strings.Contains(updatedEdit, "new1") || !strings.Contains(updatedEdit, "new2") {
		t.Fatalf("expected edited content to be updated, got:\n%s", updatedEdit)
	}
}

func TestBuildEmbeddedContentTextEdit_MultipleConfigMaps_FirstMatchWins(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"data:\n" +
		"  edit: |-\n" +
		"    old1\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"data:\n" +
		"  edit: |-\n" +
		"    old2\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "edit", "new\n", "", "")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	// Only first ConfigMap's value should change; second should remain old2.
	if strings.Count(updated, "    new") != 1 {
		t.Fatalf("expected exactly one updated occurrence of new content, got:\n%s", updated)
	}
	if !strings.Contains(updated, "    old2") {
		t.Fatalf("expected second document to remain unchanged (old2), got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_DuplicateKeyInSameMappingIsError(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// Duplicate keys inside the same YAML mapping are ambiguous and should be rejected.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"data:\n" +
		"  edit: |-\n" +
		"    first\n" +
		"  edit: |-\n" +
		"    second\n"

	_, err := r.BuildEmbeddedContentTextEdit(docContent, "edit", "new\n", "", "")
	if err == nil {
		t.Fatalf("expected error for duplicate key in same mapping, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected duplicate key error, got: %v", err)
	}
}

func TestBuildEmbeddedContentTextEdit_MultiConfigMaps_SameKeyAcrossDocsIsOK(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// Same key (representing the same embedded "file") appears in multiple ConfigMaps.
	// This is valid because they are different Kubernetes objects.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old1\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old2\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "", "")
	if err != nil {
		t.Fatalf("expected no error when same key exists across documents, got: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	if strings.Count(updated, "    new") != 1 {
		t.Fatalf("expected exactly one updated occurrence, got:\n%s", updated)
	}
	if !strings.Contains(updated, "    old2") {
		t.Fatalf("expected second ConfigMap to remain unchanged, got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_MultiConfigMaps_DuplicateKeyInLaterDocDoesNotBlockEdit(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// Second document contains a duplicate key in the same mapping (invalid YAML),
	// but editing the first document's key should still succeed because we stop after the first match.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old1\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"data:\n" +
		"  other: |-\n" +
		"    x\n" +
		"  other: |-\n" +
		"    y\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "", "")
	if err != nil {
		t.Fatalf("expected no error editing first match even if later doc has duplicates, got: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	if !strings.Contains(updated, "    new") {
		t.Fatalf("expected edited content to be applied, got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_MultiConfigMaps_DuplicateKeyInMatchingDocIsError(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// First document does not contain the key; second document contains the key duplicated
	// inside the same mapping, which should error.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"data:\n" +
		"  unrelated: |-\n" +
		"    a\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    first\n" +
		"  app.conf: |-\n" +
		"    second\n"

	_, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "", "")
	if err == nil {
		t.Fatalf("expected error when matching document has duplicate key, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected duplicate key error, got: %v", err)
	}
}

func TestBuildEmbeddedContentTextEdit_SelectSecondConfigMapByNameNamespace(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old1\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old2\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "cm2", "default")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	if !strings.Contains(updated, "    old1") {
		t.Fatalf("expected cm1 to remain unchanged, got:\n%s", updated)
	}
	if strings.Count(updated, "    new") != 1 {
		t.Fatalf("expected exactly one updated occurrence, got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_DefaultNamespaceWhenMissingInDoc(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	// metadata.namespace is omitted; Kubernetes treats it as "default" for namespaced resources.
	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "cm", "default")
	if err != nil {
		t.Fatalf("expected default namespace to match when metadata.namespace missing, got: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}
	if !strings.Contains(updated, "    new") {
		t.Fatalf("expected content to be updated, got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_TargetMismatchDoesNotEditWrongDoc(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"  namespace: ns1\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old\n"

	// Wrong namespace should not match; must error.
	_, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "cm", "ns2")
	if err == nil {
		t.Fatalf("expected error for namespace mismatch, got nil")
	}

	// Wrong name should not match; must error.
	_, err = r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "other", "ns1")
	if err == nil {
		t.Fatalf("expected error for name mismatch, got nil")
	}
}

func TestBuildEmbeddedContentTextEdit_BinaryDataOnlyEditsTargetAndPreservesOthers(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"  namespace: default\n" +
		"binaryData:\n" +
		"  cert.pem: |-\n" +
		"    AAA\n" +
		"    BBB\n" +
		"  keep.pem: |-\n" +
		"    KEEP\n"

	originalKeep := extractYamlMappingEntry(docContent, "keep.pem")
	if originalKeep == "" {
		t.Fatalf("failed to extract keep.pem entry")
	}

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "cert.pem", "CCC\nDDD\n", "cm", "default")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	updatedKeep := extractYamlMappingEntry(updated, "keep.pem")
	if updatedKeep != originalKeep {
		t.Fatalf("expected keep.pem to remain identical\n--- original ---\n%s\n--- updated ---\n%s", originalKeep, updatedKeep)
	}

	updatedCert := extractYamlMappingEntry(updated, "cert.pem")
	if !strings.Contains(updatedCert, "cert.pem: |-") {
		t.Fatalf("expected cert.pem to remain a block scalar, got:\n%s", updatedCert)
	}
	if !strings.Contains(updatedCert, "CCC") || !strings.Contains(updatedCert, "DDD") {
		t.Fatalf("expected cert.pem content to be updated, got:\n%s", updatedCert)
	}
}

func TestBuildEmbeddedContentTextEdit_SelectFirstConfigMapByNameNamespace(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm1\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old1\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm2\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  app.conf: |-\n" +
		"    old2\n"

	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", "new\n", "cm1", "default")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	// Ensure only cm1 changed.
	if strings.Count(updated, "    new") != 1 {
		t.Fatalf("expected exactly one updated occurrence, got:\n%s", updated)
	}
	if !strings.Contains(updated, "    old2") {
		t.Fatalf("expected cm2 to remain unchanged, got:\n%s", updated)
	}
}

func TestBuildEmbeddedContentTextEdit_KeyNotFoundInMatchedDocIsError(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  present: |-\n" +
		"    ok\n"

	_, err := r.BuildEmbeddedContentTextEdit(docContent, "missing", "new\n", "cm", "default")
	if err == nil {
		t.Fatalf("expected error when key missing in matched document, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestBuildEmbeddedContentTextEdit_CRLFNormalizationAndNoCarriageReturns(t *testing.T) {
	cfg := &config.Config{}
	store := indexer.NewStore()
	r := NewResolver(store, cfg)

	docContent := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm\n" +
		"  namespace: default\n" +
		"data:\n" +
		"  keep: |-\n" +
		"    stay\n" +
		"  app.conf: |-\n" +
		"    old\n"

	newContent := "line1\r\nline2\r\n" // typical Windows newline input
	edit, err := r.BuildEmbeddedContentTextEdit(docContent, "app.conf", newContent, "cm", "default")
	if err != nil {
		t.Fatalf("BuildEmbeddedContentTextEdit failed: %v", err)
	}

	updated, err := applyTextEdit(docContent, *edit)
	if err != nil {
		t.Fatalf("applyTextEdit failed: %v", err)
	}

	if strings.Contains(updated, "\r") {
		t.Fatalf("expected no carriage returns in updated YAML, got:\n%s", updated)
	}

	updatedApp := extractYamlMappingEntry(updated, "app.conf")
	if !strings.Contains(updatedApp, "app.conf: |-") {
		t.Fatalf("expected block scalar for app.conf, got:\n%s", updatedApp)
	}
	if !strings.Contains(updatedApp, "line1") || !strings.Contains(updatedApp, "line2") {
		t.Fatalf("expected CRLF-normalized content, got:\n%s", updatedApp)
	}
}
