package main

import (
	"fmt"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestFileURIIndexingGuard_DoesNotIndexNonFileURI(t *testing.T) {
	cfg := &config.Config{Symbols: []config.Symbol{{
		Name:        "k8s.resource.name",
		Definitions: []config.SymbolDefinition{{Kinds: []string{"Pod"}, Path: "metadata.name"}},
	}}}
	store := indexer.NewStore()
	idx := indexer.NewIndexer(store, cfg)
	state = &ServerState{
		Store:      store,
		Indexer:    idx,
		Documents:  map[string]string{},
		DocVersion: map[string]int32{},
		YAMLCache:  yamlstream.NewCache(),
	}

	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"
	uri := "k8s-embedded://default/cm/app.conf"
	_ = textDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, Text: content, Version: 1},
	})

	if got := store.Get("Pod", "default", "p"); got != nil {
		t.Fatalf("expected non-file URI not to be indexed")
	}
}

func TestFileURIIndexingGuard_IndexesFileURI(t *testing.T) {
	cfg := &config.Config{Symbols: []config.Symbol{{
		Name:        "k8s.resource.name",
		Definitions: []config.SymbolDefinition{{Kinds: []string{"Pod"}, Path: "metadata.name"}},
	}}}
	store := indexer.NewStore()
	idx := indexer.NewIndexer(store, cfg)
	state = &ServerState{
		Store:      store,
		Indexer:    idx,
		Documents:  map[string]string{},
		DocVersion: map[string]int32{},
		YAMLCache:  yamlstream.NewCache(),
	}

	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"
	uri := "file:///tmp/x.yaml"
	_ = textDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, Text: content, Version: 1},
	})

	if got := store.Get("Pod", "default", "p"); got == nil {
		t.Fatalf("expected file URI to be indexed")
	}
}

func TestDocumentSymbol_MultiDoc(t *testing.T) {
	state = &ServerState{
		Documents:  map[string]string{},
		DocVersion: map[string]int32{},
		YAMLCache:  yamlstream.NewCache(),
		Store:      indexer.NewStore(),
	}

	uri := "file:///x.yaml"
	content := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm1\n  namespace: default\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: ns1\n"
	state.Documents[uri] = content
	state.DocVersion[uri] = 1

	res, err := textDocumentDocumentSymbol(nil, &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	syms, ok := res.([]protocol.DocumentSymbol)
	if !ok {
		t.Fatalf("expected []DocumentSymbol, got %T", res)
	}
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(syms))
	}
	if syms[0].Name != "ConfigMap default/cm1" {
		t.Fatalf("unexpected name[0]: %q", syms[0].Name)
	}
	if syms[1].Name != "Namespace ns1" {
		t.Fatalf("unexpected name[1]: %q", syms[1].Name)
	}
	if syms[0].SelectionRange.Start.Line != 3 {
		t.Fatalf("expected selection line 3, got %d", syms[0].SelectionRange.Start.Line)
	}
}

func TestWorkspaceSymbol_QueryAndFileOnly(t *testing.T) {
	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: "cm1", FilePath: "/w/cm1.yaml", Line: 1, Col: 10})
	store.Add(&indexer.K8sResource{Kind: "Secret", Namespace: "other-ns", Name: "s1", FilePath: "/w/s1.yaml", Line: 2, Col: 3})
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: "embedded", FilePath: "k8s-embedded://default/cm/app.conf", Line: 0, Col: 0})

	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	items, err := workspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "configmap default/cm"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(items))
	}
	if items[0].Name != "ConfigMap default/cm1" {
		t.Fatalf("unexpected symbol name: %q", items[0].Name)
	}
	if items[0].Location.URI == "" || items[0].Location.URI[:7] != "file://" {
		t.Fatalf("expected file:// URI, got %q", items[0].Location.URI)
	}

	// ns/name-only query should match.
	items, err = workspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "default/cm1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 1 || items[0].Name != "ConfigMap default/cm1" {
		t.Fatalf("unexpected results for default/cm1: %#v", items)
	}

	// kind + ns/name query should match.
	items, err = workspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "secret other-ns/s1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Secret other-ns/s1" {
		t.Fatalf("unexpected results for secret other-ns/s1: %#v", items)
	}
}

func TestWorkspaceSymbol_Capped(t *testing.T) {
	store := indexer.NewStore()
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("cm-%03d", i)
		store.Add(&indexer.K8sResource{Kind: "ConfigMap", Namespace: "default", Name: name, FilePath: "/w/" + name + ".yaml", Line: 0, Col: 0})
	}
	state = &ServerState{Store: store, Documents: map[string]string{}, DocVersion: map[string]int32{}, YAMLCache: yamlstream.NewCache()}

	items, err := workspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: ""})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 200 {
		t.Fatalf("expected 200 capped symbols, got %d", len(items))
	}
}
