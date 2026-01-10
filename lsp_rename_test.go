package main

import (
	"os"
	"testing"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestRename_NamespacedResource_OnlyCurrentNamespaceReferences(t *testing.T) {
	// Two Deployments in different namespaces referencing the same Secret name.
	tmpA := mustTempFile(t, "a-*.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\n  namespace: default\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          envFrom:\n            - secretRef:\n                name: sec-old\n")
	defer tmpA.cleanup()
	uriA := "file://" + tmpA.path

	tmpB := mustTempFile(t, "b-*.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\n  namespace: other\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          envFrom:\n            - secretRef:\n                name: sec-old\n")
	defer tmpB.cleanup()

	// Secret definition in default.
	tmpS := mustTempFile(t, "s-*.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n  name: sec-old\n  namespace: default\n")
	defer tmpS.cleanup()

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmpS.path, Line: 3, Col: 8})
	store.Add(&indexer.K8sResource{Kind: "Deployment", Namespace: "default", Name: "d", FilePath: tmpA.path, References: []indexer.Reference{{Kind: "Secret", Name: "sec-old", Line: 13, Col: 22}}})
	store.Add(&indexer.K8sResource{Kind: "Deployment", Namespace: "other", Name: "d", FilePath: tmpB.path, References: []indexer.Reference{{Kind: "Secret", Name: "sec-old", Line: 13, Col: 22}}})

	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
	state.Documents[uriA] = mustReadFile(t, tmpA.path)

	// Rename must be invoked on Secret definition, not deployment name. We'll call buildRenameWorkspaceEdit directly.
	ctx := &renameContext{kind: "Secret", oldName: "sec-old", scopeNamespace: "default", clusterScoped: false}
	edit, err := buildRenameWorkspaceEdit(ctx, "sec-new", "default")
	if err != nil {
		t.Fatalf("build edit: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected edit")
	}

	// Should include edits for secret def and only default namespace deployment refs.
	if len(edit.Changes) != 2 {
		t.Fatalf("expected 2 documents edited, got %d", len(edit.Changes))
	}
	// Ensure other namespace file not touched.
	otherURI := "file://" + tmpB.path
	if _, ok := edit.Changes[otherURI]; ok {
		t.Fatalf("expected other namespace not edited")
	}
}

func TestRename_PersistentVolume_RequiresClaimRefNamespace(t *testing.T) {
	pvNoClaim := mustTempFile(t, "pv-*.yaml", "apiVersion: v1\nkind: PersistentVolume\nmetadata:\n  name: pv1\nspec:\n  capacity:\n    storage: 1Gi\n")
	defer pvNoClaim.cleanup()
	uri := "file://" + pvNoClaim.path

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "PersistentVolume", Namespace: "", Name: "pv1", FilePath: pvNoClaim.path, Line: 3, Col: 8})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
	state.Documents[uri] = mustReadFile(t, pvNoClaim.path)

	// Cursor on metadata.name value.
	params := &protocol.RenameParams{}
	params.TextDocument.URI = uri
	params.Position.Line = 3
	params.Position.Character = 10
	params.NewName = "pv2"

	_, err := textDocumentRename(nil, params)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRename_PersistentVolume_WithClaimRefNamespace_OnlyEditsThatNamespace(t *testing.T) {
	pv := mustTempFile(t, "pv-*.yaml", "apiVersion: v1\nkind: PersistentVolume\nmetadata:\n  name: pv1\nspec:\n  capacity:\n    storage: 1Gi\n  claimRef:\n    namespace: default\n")
	defer pv.cleanup()
	pvURI := "file://" + pv.path

	// PVC in the bound namespace (default): should be edited.
	pvcDefault := mustTempFile(t, "pvc-default-*.yaml", "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: c1\n  namespace: default\nspec:\n  volumeName: pv1\n")
	defer pvcDefault.cleanup()
	pvcDefaultURI := "file://" + pvcDefault.path

	// PVC in another namespace: must NOT be edited.
	pvcOther := mustTempFile(t, "pvc-other-*.yaml", "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: c2\n  namespace: other\nspec:\n  volumeName: pv1\n")
	defer pvcOther.cleanup()
	pvcOtherURI := "file://" + pvcOther.path

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "PersistentVolume", Namespace: "", Name: "pv1", FilePath: pv.path, Line: 3, Col: 8})
	store.Add(&indexer.K8sResource{Kind: "PersistentVolumeClaim", Namespace: "default", Name: "c1", FilePath: pvcDefault.path, References: []indexer.Reference{{Kind: "PersistentVolume", Name: "pv1", Line: 6, Col: 14}}})
	store.Add(&indexer.K8sResource{Kind: "PersistentVolumeClaim", Namespace: "other", Name: "c2", FilePath: pvcOther.path, References: []indexer.Reference{{Kind: "PersistentVolume", Name: "pv1", Line: 6, Col: 14}}})

	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
	state.Documents[pvURI] = mustReadFile(t, pv.path)
	state.DocVersion[pvURI] = 1

	params := &protocol.RenameParams{}
	params.TextDocument.URI = pvURI
	// Cursor on metadata.name value (pv1)
	params.Position.Line = 3
	params.Position.Character = 10
	params.NewName = "pv2"

	edit, err := textDocumentRename(nil, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected edit")
	}

	if _, ok := edit.Changes[pvURI]; !ok {
		t.Fatalf("expected PV definition to be edited")
	}
	if _, ok := edit.Changes[pvcDefaultURI]; !ok {
		t.Fatalf("expected default namespace PVC to be edited")
	}
	if _, ok := edit.Changes[pvcOtherURI]; ok {
		t.Fatalf("expected other namespace PVC to NOT be edited")
	}
}

func mustReadFile(tb testing.TB, path string) string {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read file: %v", err)
	}
	return string(b)
}
