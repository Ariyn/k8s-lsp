package main

import (
	"os"
	"testing"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

func TestParseMissingRefDiagnostic(t *testing.T) {
	src := lsName
	d := protocol.Diagnostic{Source: &src, Message: "ConfigMap not found (Kind: ConfigMap, Name: cm-1)"}
	k, n, ok := parseMissingRefDiagnostic(d)
	if !ok {
		t.Fatalf("expected ok")
	}
	if k != "ConfigMap" || n != "cm-1" {
		t.Fatalf("unexpected parse: %q %q", k, n)
	}
}

func TestParseResourceMismatchDiagnostic(t *testing.T) {
	src := lsName
	d := protocol.Diagnostic{Source: &src, Message: "Access modes mismatch: spec.accessModes (ReadOnlyMany) != spec.accessModes (ReadWriteOnce)"}
	mm := parseResourceMismatchDiagnostic(d)
	if mm == nil {
		t.Fatalf("expected mismatch parsed")
	}
	if mm.message != "Access modes mismatch" {
		t.Fatalf("unexpected message: %q", mm.message)
	}
	if mm.sourcePath != "spec.accessModes" || mm.targetPath != "spec.accessModes" {
		t.Fatalf("unexpected paths: %q %q", mm.sourcePath, mm.targetPath)
	}
	if mm.sourceVal != "ReadOnlyMany" || mm.targetVal != "ReadWriteOnce" {
		t.Fatalf("unexpected values: %q %q", mm.sourceVal, mm.targetVal)
	}
}

func TestBuildCursorContextActions_EmbeddedConfigMap(t *testing.T) {
	store := indexer.NewStore()
	state = &ServerState{
		Store:      store,
		Documents:  map[string]string{},
		DocVersion: map[string]int32{},
		YAMLCache:  yamlstream.NewCache(),
	}

	uri := "file:///test.yaml"
	content := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n  namespace: default\ndata:\n  app.conf: |\n    hello\n"
	stream, err := yamlstream.Parse(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Cursor on key "app.conf".
	line0 := 6
	col0 := 2
	actions := buildCursorContextActions(uri, content, stream, "default", line0, col0)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Command == nil || actions[0].Command.Command != "k8sLsp.openEmbeddedFile" {
		t.Fatalf("expected openEmbeddedFile command")
	}
	if actions[1].Command == nil || actions[1].Command.Command != "k8sLsp.findEmbeddedFileUsages" {
		t.Fatalf("expected findEmbeddedFileUsages command")
	}
}

func TestBuildSelectorFixActions(t *testing.T) {
	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Deployment", Namespace: "default", Name: "dep1", Labels: map[string]string{"app": "demo"}})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	uri := "file:///svc.yaml"
	content := "apiVersion: v1\nkind: Service\nmetadata:\n  name: s\n  namespace: default\nspec:\n  selector:\n    app: wrong\n"
	stream, err := yamlstream.Parse(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	src := lsName
	diag := protocol.Diagnostic{Source: &src, Message: "No Deployment found matching this selector (Kind: Deployment)", Range: protocol.Range{Start: protocol.Position{Line: 6, Character: 2}, End: protocol.Position{Line: 6, Character: 10}}}

	actions := buildSelectorFixActions(uri, content, stream, diag, "default")
	if len(actions) == 0 {
		t.Fatalf("expected actions")
	}
	if actions[0].Edit == nil || actions[0].Edit.Changes == nil {
		t.Fatalf("expected workspace edit")
	}
	edits := actions[0].Edit.Changes[uri]
	if len(edits) == 0 {
		t.Fatalf("expected edits")
	}
	if edits[0].NewText == "" {
		t.Fatalf("expected non-empty selector replacement")
	}
	if !(contains(edits[0].NewText, "app: demo")) {
		t.Fatalf("expected selector to include deployment label, got %q", edits[0].NewText)
	}
}

func TestBuildFixAllAction_UnambiguousOnly(t *testing.T) {
	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: "cm-real"})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	uri := "file:///x.yaml"
	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\nspec:\n  containers: []\n"
	stream, _ := yamlstream.Parse(content)

	src := lsName
	diags := []protocol.Diagnostic{{Source: &src, Message: "ConfigMap not found (Kind: ConfigMap, Name: cm-missing)", Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 1}}}}

	ca := buildFixAllAction(uri, content, stream, "default", diags)
	if ca == nil || ca.Edit == nil {
		t.Fatalf("expected fixAll action")
	}
	if ca.Kind == nil || *ca.Kind != "source.fixAll.k8s-lsp" {
		t.Fatalf("unexpected kind")
	}
	if len(ca.Edit.Changes[uri]) != 1 {
		t.Fatalf("expected 1 edit")
	}
}

func TestBuildFixAllAction_SkipsAmbiguous(t *testing.T) {
	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: "cm-a"})
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: "cm-b"})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	uri := "file:///x.yaml"
	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\nspec:\n  containers: []\n"
	stream, _ := yamlstream.Parse(content)

	src := lsName
	diags := []protocol.Diagnostic{{Source: &src, Message: "ConfigMap not found (Kind: ConfigMap, Name: cm-missing)", Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 1}}}}

	ca := buildFixAllAction(uri, content, stream, "default", diags)
	if ca != nil {
		t.Fatalf("expected no fixAll when ambiguous")
	}
}

func TestBuildCreateMissingResourceAction_AppendsStub(t *testing.T) {
	uri := "file:///x.yaml"
	content := "apiVersion: v1\nkind: Deployment\nmetadata:\n  name: d\n"
	src := lsName
	diag := protocol.Diagnostic{Source: &src, Message: "Secret not found (Kind: Secret, Name: s1)", Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 1}}}

	ca := buildCreateMissingResourceAction(uri, content, diag, "Secret", "s1", "default")
	if ca == nil || ca.Edit == nil {
		t.Fatalf("expected create action")
	}
	edits := ca.Edit.Changes[uri]
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit")
	}
	if !contains(edits[0].NewText, "kind: Secret") || !contains(edits[0].NewText, "name: s1") {
		t.Fatalf("unexpected stub: %q", edits[0].NewText)
	}
	if !contains(edits[0].NewText, "namespace: default") {
		t.Fatalf("expected namespace in stub")
	}
}

func TestBuildResourceMismatchFixActions_CreatesEditsForPVCAndPV(t *testing.T) {
	// PV on disk.
	pvFile := mustTempFile(t, "pv-*.yaml", "apiVersion: v1\nkind: PersistentVolume\nmetadata:\n  name: pv1\nspec:\n  capacity:\n    storage: 2Gi\n  accessModes:\n    - ReadWriteOnce\n  claimRef:\n    namespace: default\n")
	defer pvFile.cleanup()

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "PersistentVolume", Namespace: "", Name: "pv1", FilePath: pvFile.path})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	uri := "file:///pvc.yaml"
	content := "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: pvc1\n  namespace: default\nspec:\n  volumeName: pv1\n  resources:\n    requests:\n      storage: 1Gi\n"
	stream, err := yamlstream.Parse(content)
	if err != nil {
		t.Fatalf("parse pvc: %v", err)
	}

	src := lsName
	diag := protocol.Diagnostic{Source: &src, Message: "Storage capacity mismatch: spec.resources.requests.storage (1Gi) != spec.capacity.storage (2Gi)", Range: protocol.Range{Start: protocol.Position{Line: 6, Character: 14}, End: protocol.Position{Line: 6, Character: 17}}}
	mm := parseResourceMismatchDiagnostic(diag)
	if mm == nil {
		t.Fatalf("expected parsed mismatch")
	}

	actions := buildResourceMismatchFixActions(uri, content, stream, diag, "default", mm)
	if len(actions) < 1 {
		t.Fatalf("expected at least 1 action")
	}
	// Should include at least one edit (PVC or PV).
	seenEdit := false
	for _, a := range actions {
		if a.Edit != nil {
			seenEdit = true
		}
	}
	if !seenEdit {
		t.Fatalf("expected workspace edits")
	}
}

type tempFile struct {
	path string
}

func (t tempFile) cleanup() {
	_ = os.Remove(t.path)
}

func mustTempFile(tb testing.TB, pattern string, content string) tempFile {
	tb.Helper()
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		tb.Fatalf("temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		tb.Fatalf("write temp: %v", err)
	}
	_ = f.Close()
	return tempFile{path: f.Name()}
}
