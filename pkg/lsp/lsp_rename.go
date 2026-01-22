package lsp

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func textDocumentRename(context *glsp.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	uri := params.TextDocument.URI
	state.setNotifyContext(context)
	content, _, _ := getOrLoadDocument(uri)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	newName := strings.TrimSpace(params.NewName)
	if newName == "" {
		return nil, errors.New("newName must not be empty")
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for rename")
		return nil, nil
	}

	line0 := int(params.Position.Line)
	col0 := int(params.Position.Character)
	ctx, err := symbolContextAt(stream, line0, col0)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, nil
	}

	// Enforce namespace scope.
	if ctx.clusterScoped {
		if ctx.kind == "PersistentVolume" {
			if ctx.connectedNamespace == "" {
				return nil, errors.New("rename for PersistentVolume is only supported when spec.claimRef.namespace is set")
			}
		} else {
			return nil, fmt.Errorf("rename not supported for cluster-scoped kind %s", ctx.kind)
		}
	}

	scopeNamespace := ctx.scopeNamespace
	if scopeNamespace == "" {
		return nil, errors.New("unable to determine namespace scope for rename")
	}

	edit, err := buildRenameWorkspaceEdit(ctx, newName, scopeNamespace)
	if err != nil {
		return nil, err
	}
	if edit == nil {
		return nil, nil
	}
	return edit, nil
}

type renameContext struct {
	kind               string
	oldName            string
	scopeNamespace     string // always normalized ("default" for namespaced)
	clusterScoped      bool
	connectedNamespace string // for PV
}

func symbolContextAt(stream *yamlstream.Stream, line0, col0 int) (*renameContext, error) {
	if stream == nil {
		return nil, nil
	}
	line1 := line0 + 1
	col1 := col0 + 1
	doc := stream.DocForLine(line1)
	if doc == nil || doc.Node == nil {
		return nil, nil
	}

	node, parent, path := findNodeAtInDoc(doc.Node, line1, col1)
	if node == nil || node.Kind != yaml.ScalarNode {
		return nil, nil
	}

	docKind := findYAMLString(doc.Node, "kind")
	if docKind == "" {
		docKind = findKindFallback(doc.Node)
	}
	if docKind == "" {
		return nil, nil
	}

	docNamespace := findYAMLString(doc.Node, "metadata", "namespace")
	scopeNamespace := indexer.NormalizeNamespace(docNamespace)

	// Definition rename (metadata.name)
	if len(path) == 2 && path[0] == "metadata" && path[1] == "name" {
		ctx := &renameContext{
			kind:           docKind,
			oldName:        node.Value,
			scopeNamespace: scopeNamespace,
			clusterScoped:  indexer.IsClusterScopedKind(docKind),
		}
		if ctx.clusterScoped && ctx.kind == "PersistentVolume" {
			connected := strings.TrimSpace(findYAMLString(doc.Node, "spec", "claimRef", "namespace"))
			ctx.connectedNamespace = connected
			ctx.scopeNamespace = connected
		}
		return ctx, nil
	}

	// Reference rename: match configured references in rules/k8s.yaml.
	// We intentionally keep it simple: only scalar references where we can infer target kind.
	if parent != nil {
		// The path returned for a scalar is relative in findNodeAt; for nested maps it becomes full (e.g. metadata.name).
		// For references, we depend on the full path as returned.
	}

	// Infer ref target kind using config rules by re-walking the doc to build the cursor path.
	// We already have `path` from findNodeAt; use it directly.
	refTargetKind := ""
	for _, refRule := range state.Resolver.Config.References {
		if !matchesKind(refRule.Match.Kinds, docKind) {
			continue
		}
		if matchPath(path, refRule.Match.Path) {
			refTargetKind = refRule.TargetKind
			break
		}
	}
	if refTargetKind == "" {
		return nil, nil
	}

	ctx := &renameContext{
		kind:           refTargetKind,
		oldName:        node.Value,
		scopeNamespace: scopeNamespace,
		clusterScoped:  indexer.IsClusterScopedKind(refTargetKind),
	}
	if ctx.clusterScoped && ctx.kind == "PersistentVolume" {
		connected, err := persistentVolumeConnectedNamespace(ctx.oldName)
		if err != nil {
			return nil, err
		}
		ctx.connectedNamespace = connected
		if connected == "" {
			return nil, errors.New("rename for PersistentVolume is only supported when it is bound to a PVC (spec.claimRef.namespace)")
		}
		if indexer.NormalizeNamespace(scopeNamespace) != indexer.NormalizeNamespace(connected) {
			return nil, errors.New("rename for PersistentVolume is only allowed within the bound PVC namespace")
		}
		ctx.scopeNamespace = connected
	}
	return ctx, nil
}

func buildRenameWorkspaceEdit(ctx *renameContext, newName string, scopeNamespace string) (*protocol.WorkspaceEdit, error) {
	if ctx == nil {
		return nil, nil
	}
	oldName := strings.TrimSpace(ctx.oldName)
	if oldName == "" {
		return nil, nil
	}
	if newName == oldName {
		return nil, nil
	}

	// Resolve definition.
	lookupNS := scopeNamespace
	if ctx.clusterScoped {
		lookupNS = ""
	}
	def := state.Store.Get(ctx.kind, lookupNS, oldName)
	if def == nil {
		return nil, fmt.Errorf("definition not found for %s '%s' in namespace '%s'", ctx.kind, oldName, lookupNS)
	}

	changes := map[string][]protocol.TextEdit{}
	addEdit := func(uri string, e protocol.TextEdit) {
		changes[uri] = append(changes[uri], e)
	}

	// Definition edit.
	defURI := filePathToURI(def.FilePath)
	addEdit(defURI, protocol.TextEdit{
		Range: protocol.Range{
			Start: protocol.Position{Line: uint32(def.Line), Character: uint32(def.Col)},
			End:   protocol.Position{Line: uint32(def.Line), Character: uint32(def.Col + len(oldName))},
		},
		NewText: newName,
	})

	// References (filtered by scopeNamespace).
	refResources := state.Store.FindReferences(ctx.kind, oldName)
	for _, res := range refResources {
		resNS := indexer.NormalizeNamespace(res.Namespace)
		if !ctx.clusterScoped {
			if resNS != indexer.NormalizeNamespace(scopeNamespace) {
				continue
			}
		} else {
			// cluster-scoped PV: only apply to connected namespace
			if resNS != indexer.NormalizeNamespace(scopeNamespace) {
				continue
			}
		}

		uri := filePathToURI(res.FilePath)
		for _, ref := range res.References {
			if ref.Kind == ctx.kind && ref.Name == oldName {
				addEdit(uri, protocol.TextEdit{
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col)},
						End:   protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col + len(oldName))},
					},
					NewText: newName,
				})
			}
		}
	}

	// Sort edits per-document for deterministic application.
	for u := range changes {
		sort.SliceStable(changes[u], func(i, j int) bool {
			a := changes[u][i].Range.Start
			b := changes[u][j].Range.Start
			if a.Line != b.Line {
				return a.Line > b.Line
			}
			return a.Character > b.Character
		})
	}

	return &protocol.WorkspaceEdit{Changes: changes}, nil
}

func persistentVolumeConnectedNamespace(pvName string) (string, error) {
	pv := state.Store.Get("PersistentVolume", "", pvName)
	if pv == nil {
		// Store defaults empty namespace to "default".
		pv = state.Store.Get("PersistentVolume", "default", pvName)
	}
	if pv == nil || pv.FilePath == "" {
		return "", nil
	}

	uri := filePathToURI(pv.FilePath)
	content, _, _ := state.getDocument(uri)
	if content == "" {
		b, err := os.ReadFile(pv.FilePath)
		if err != nil {
			return "", err
		}
		content = string(b)
		state.setDocument(uri, content, 0)
	}

	decoder := yaml.NewDecoder(strings.NewReader(content))
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			break
		}
		if findKindFallback(&doc) != "PersistentVolume" {
			continue
		}
		name := findYAMLString(&doc, "metadata", "name")
		if name != pvName {
			continue
		}
		ns := strings.TrimSpace(findYAMLString(&doc, "spec", "claimRef", "namespace"))
		return ns, nil
	}

	return "", nil
}

func filePathToURI(path string) string {
	if path == "" {
		return ""
	}
	if strings.Contains(path, "://") {
		return path
	}

	normalized := filepath.ToSlash(path)

	// Windows drive letter paths need a leading slash: /C:/Users/...
	if len(normalized) >= 3 {
		drive := normalized[0]
		if ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) && normalized[1] == ':' && normalized[2] == '/' {
			normalized = "/" + normalized
		}
	}

	u := url.URL{Scheme: "file", Path: normalized}
	return u.String()
}

// --- Minimal YAML helpers for rename context ---

func findNodeAtInDoc(docNode *yaml.Node, line1, col1 int) (*yaml.Node, *yaml.Node, []string) {
	// Reuse resolver's algorithm by keeping a local copy here.
	// This intentionally matches the behavior used in resolver for hover/completion.
	if docNode == nil {
		return nil, nil, nil
	}
	if docNode.Kind == yaml.DocumentNode {
		if len(docNode.Content) > 0 {
			return findNodeAtInDoc(docNode.Content[0], line1, col1)
		}
		return nil, nil, nil
	}

	if docNode.Kind == yaml.MappingNode {
		for i := 0; i < len(docNode.Content); i += 2 {
			keyNode := docNode.Content[i]
			valNode := docNode.Content[i+1]

			if isKeyMatchLocal(keyNode, line1, col1) {
				return keyNode, docNode, []string{keyNode.Value}
			}
			if isValueMatchLocal(valNode, line1, col1) {
				if valNode.Kind == yaml.ScalarNode {
					return valNode, docNode, []string{keyNode.Value}
				}
				found, parent, subPath := findNodeAtInDoc(valNode, line1, col1)
				if found != nil {
					return found, parent, append([]string{keyNode.Value}, subPath...)
				}
			}
		}
	} else if docNode.Kind == yaml.SequenceNode {
		for _, item := range docNode.Content {
			if isValueMatchLocal(item, line1, col1) {
				found, parent, subPath := findNodeAtInDoc(item, line1, col1)
				if found != nil {
					return found, parent, subPath
				}
			}
		}
	} else if docNode.Kind == yaml.ScalarNode {
		if isValueMatchLocal(docNode, line1, col1) {
			return docNode, nil, nil
		}
	}

	return nil, nil, nil
}

func isKeyMatchLocal(node *yaml.Node, line1, col1 int) bool {
	if node == nil || node.Line != line1 {
		return false
	}
	endCol := node.Column + len(node.Value)
	if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
		endCol += 2
	}
	return col1 >= node.Column && col1 <= endCol
}

func isValueMatchLocal(node *yaml.Node, line1, col1 int) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.ScalarNode {
		endCol := node.Column + len(node.Value)
		if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
			endCol += 2
		}
		return line1 == node.Line && col1 >= node.Column && col1 <= endCol
	}
	if line1 < node.Line {
		return false
	}
	if line1 == node.Line && col1 < node.Column {
		return false
	}
	return true
}

func findKindFallback(docNode *yaml.Node) string {
	k := findYAMLString(docNode, "kind")
	return strings.TrimSpace(k)
}

func matchesKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == "*" || k == kind {
			return true
		}
	}
	return false
}

func matchPath(current []string, pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) != len(current) {
		return false
	}
	for i, part := range parts {
		cleanPart := strings.TrimSuffix(part, "[]")
		if cleanPart != current[i] {
			return false
		}
	}
	return true
}

func init() {
	// Keep log noise low for rename helpers.
	_ = log.Logger
	_ = filepath.Separator
}
