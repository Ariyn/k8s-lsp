package lsp

import (
	"os"
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"
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
	state.setDocument(uriA, mustReadFile(t, tmpA.path), 0)

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
	state.setDocument(uri, mustReadFile(t, pvNoClaim.path), 0)

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
	state.setDocument(pvURI, mustReadFile(t, pv.path), 1)

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

func TestPrepareRename_HyphenatedName_ReturnsFullScalarRange(t *testing.T) {
	// Ensure prepareRename returns full scalar range for values like "sec-old",
	// so the client doesn't fall back to word-splitting on '-'.
	tmp := mustTempFile(t, "s-*.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n  name: sec-old\n  namespace: default\n")
	defer tmp.cleanup()
	uri := "file://" + tmp.path

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmp.path, Line: 3, Col: 8})
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
	state.setDocument(uri, mustReadFile(t, tmp.path), 1)

	params := &protocol.PrepareRenameParams{}
	params.TextDocument.URI = uri
	// Cursor inside "sec-old" (line: "  name: sec-old")
	params.Position.Line = 3
	params.Position.Character = 12

	res, err := textDocumentPrepareRename(nil, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected prepareRename result")
	}
	withPH, ok := res.(protocol.RangeWithPlaceholder)
	if !ok {
		t.Fatalf("expected RangeWithPlaceholder, got %T", res)
	}
	if withPH.Placeholder != "sec-old" {
		t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
	}
	if withPH.Range.Start.Line != 3 || withPH.Range.End.Line != 3 {
		t.Fatalf("expected same-line range, got %v", withPH.Range)
	}
	if withPH.Range.Start.Character != 8 {
		t.Fatalf("expected start char 8, got %d", withPH.Range.Start.Character)
	}
	if withPH.Range.End.Character != 8+uint32(len("sec-old")) {
		t.Fatalf("expected end char %d, got %d", 8+uint32(len("sec-old")), withPH.Range.End.Character)
	}
}

func TestPrepareRename_EdgeCases(t *testing.T) {
	findNeedle := func(tb testing.TB, content, needle string) (line0 int, char0 int, lineText string) {
		tb.Helper()
		lines := strings.Split(content, "\n")
		for i, ln := range lines {
			if j := strings.Index(ln, needle); j >= 0 {
				return i, j, ln
			}
		}
		tb.Fatalf("needle %q not found", needle)
		return 0, 0, ""
	}

	t.Run("cursor at end of scalar allowed", func(t *testing.T) {
		content := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: sec-old\n  namespace: default\n"
		tmp := mustTempFile(t, "s-*.yaml", content)
		defer tmp.cleanup()
		uri := "file://" + tmp.path

		store := indexer.NewStore()
		store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmp.path, Line: 3, Col: 8})
		state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
		state.setDocument(uri, content, 1)

		line0, startChar, _ := findNeedle(t, content, "sec-old")
		params := &protocol.PrepareRenameParams{}
		params.TextDocument.URI = uri
		params.Position.Line = uint32(line0)
		params.Position.Character = uint32(startChar + len("sec-old"))

		res, err := textDocumentPrepareRename(nil, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		withPH, ok := res.(protocol.RangeWithPlaceholder)
		if !ok {
			t.Fatalf("expected RangeWithPlaceholder, got %T", res)
		}
		if withPH.Placeholder != "sec-old" {
			t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
		}
		if withPH.Range.Start.Character != uint32(startChar) {
			t.Fatalf("expected start char %d, got %d", startChar, withPH.Range.Start.Character)
		}
		if withPH.Range.End.Character != uint32(startChar+len("sec-old")) {
			t.Fatalf("expected end char %d, got %d", startChar+len("sec-old"), withPH.Range.End.Character)
		}
	})

	t.Run("cursor on hyphen returns full scalar", func(t *testing.T) {
		content := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: sec-old\n  namespace: default\n"
		tmp := mustTempFile(t, "s-*.yaml", content)
		defer tmp.cleanup()
		uri := "file://" + tmp.path

		store := indexer.NewStore()
		store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmp.path, Line: 3, Col: 8})
		state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
		state.setDocument(uri, content, 1)

		line0, startChar, lineText := findNeedle(t, content, "sec-old")
		hy := strings.Index(lineText, "-")
		if hy < 0 {
			t.Fatalf("expected hyphen in line")
		}

		params := &protocol.PrepareRenameParams{}
		params.TextDocument.URI = uri
		params.Position.Line = uint32(line0)
		params.Position.Character = uint32(hy)

		res, err := textDocumentPrepareRename(nil, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		withPH, ok := res.(protocol.RangeWithPlaceholder)
		if !ok {
			t.Fatalf("expected RangeWithPlaceholder, got %T", res)
		}
		if withPH.Placeholder != "sec-old" {
			t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
		}
		if withPH.Range.Start.Character != uint32(startChar) {
			t.Fatalf("expected start char %d, got %d", startChar, withPH.Range.Start.Character)
		}
		if withPH.Range.End.Character != uint32(startChar+len("sec-old")) {
			t.Fatalf("expected end char %d, got %d", startChar+len("sec-old"), withPH.Range.End.Character)
		}
	})

	t.Run("quoted scalar returns range for inner text", func(t *testing.T) {
		content := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: \"sec-old\"\n  namespace: default\n"
		tmp := mustTempFile(t, "s-*.yaml", content)
		defer tmp.cleanup()
		uri := "file://" + tmp.path

		store := indexer.NewStore()
		store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmp.path, Line: 3, Col: 8})
		state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
		state.setDocument(uri, content, 1)

		line0, startChar, _ := findNeedle(t, content, "sec-old")
		params := &protocol.PrepareRenameParams{}
		params.TextDocument.URI = uri
		params.Position.Line = uint32(line0)
		params.Position.Character = uint32(startChar + 1)

		res, err := textDocumentPrepareRename(nil, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		withPH, ok := res.(protocol.RangeWithPlaceholder)
		if !ok {
			t.Fatalf("expected RangeWithPlaceholder, got %T", res)
		}
		if withPH.Placeholder != "sec-old" {
			t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
		}
		if withPH.Range.Start.Character != uint32(startChar) {
			t.Fatalf("expected start char %d, got %d", startChar, withPH.Range.Start.Character)
		}
		if withPH.Range.End.Character != uint32(startChar+len("sec-old")) {
			t.Fatalf("expected end char %d, got %d", startChar+len("sec-old"), withPH.Range.End.Character)
		}
	})

	t.Run("cursor on key still selects value", func(t *testing.T) {
		content := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: sec-old\n  namespace: default\n"
		tmp := mustTempFile(t, "s-*.yaml", content)
		defer tmp.cleanup()
		uri := "file://" + tmp.path

		store := indexer.NewStore()
		store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "default", Name: "sec-old", FilePath: tmp.path, Line: 3, Col: 8})
		state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
		state.setDocument(uri, content, 1)

		line0, _, lineText := findNeedle(t, content, "name:")
		keyChar := strings.Index(lineText, "name:")
		if keyChar < 0 {
			t.Fatalf("expected key in line")
		}
		_, valueStartChar, _ := findNeedle(t, content, "sec-old")

		params := &protocol.PrepareRenameParams{}
		params.TextDocument.URI = uri
		params.Position.Line = uint32(line0)
		params.Position.Character = uint32(keyChar + 1)

		res, err := textDocumentPrepareRename(nil, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		withPH, ok := res.(protocol.RangeWithPlaceholder)
		if !ok {
			t.Fatalf("expected RangeWithPlaceholder, got %T", res)
		}
		if withPH.Placeholder != "sec-old" {
			t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
		}
		if withPH.Range.Start.Character != uint32(valueStartChar) {
			t.Fatalf("expected start char %d, got %d", valueStartChar, withPH.Range.Start.Character)
		}
		if withPH.Range.End.Character != uint32(valueStartChar+len("sec-old")) {
			t.Fatalf("expected end char %d, got %d", valueStartChar+len("sec-old"), withPH.Range.End.Character)
		}
	})

	t.Run("reference path returns full scalar", func(t *testing.T) {
		content := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\n  namespace: default\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          envFrom:\n            - secretRef:\n                name: sec-old\n"
		tmp := mustTempFile(t, "d-*.yaml", content)
		defer tmp.cleanup()
		uri := "file://" + tmp.path

		store := indexer.NewStore()
		state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}
		state.setDocument(uri, content, 1)

		cfg := &config.Config{References: []config.Reference{
			{
				Name:       "test.secretref",
				Symbol:     "k8s.resource.name",
				TargetKind: "Secret",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.template.spec.containers[].envFrom[].secretRef.name",
				},
			},
		}}
		state.Resolver = resolver.NewResolver(store, cfg)

		line0, startChar, _ := findNeedle(t, content, "sec-old")
		params := &protocol.PrepareRenameParams{}
		params.TextDocument.URI = uri
		params.Position.Line = uint32(line0)
		params.Position.Character = uint32(startChar + 2)

		res, err := textDocumentPrepareRename(nil, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		withPH, ok := res.(protocol.RangeWithPlaceholder)
		if !ok {
			t.Fatalf("expected RangeWithPlaceholder, got %T", res)
		}
		if withPH.Placeholder != "sec-old" {
			t.Fatalf("expected placeholder 'sec-old', got %q", withPH.Placeholder)
		}
		if withPH.Range.Start.Character != uint32(startChar) {
			t.Fatalf("expected start char %d, got %d", startChar, withPH.Range.Start.Character)
		}
		if withPH.Range.End.Character != uint32(startChar+len("sec-old")) {
			t.Fatalf("expected end char %d, got %d", startChar+len("sec-old"), withPH.Range.End.Character)
		}
	})
}

func mustReadFile(tb testing.TB, path string) string {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read file: %v", err)
	}
	return string(b)
}
