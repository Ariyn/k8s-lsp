package main

import (
	"sort"
	"strings"

	"k8s-lsp/pkg/indexer"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func textDocumentDocumentSymbol(context *glsp.Context, params *protocol.DocumentSymbolParams) (any, error) {
	uri := params.TextDocument.URI
	state.setNotifyContext(context)
	content, _, _ := getOrLoadDocument(uri)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for documentSymbol")
		return nil, nil
	}

	kindObj := protocol.SymbolKindObject
	var out []protocol.DocumentSymbol
	for _, d := range stream.Docs {
		docNode := d.Node
		if docNode == nil || docNode.Kind != yaml.DocumentNode || len(docNode.Content) == 0 {
			continue
		}
		root := docNode.Content[0]
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}

		kind := findYAMLString(docNode, "kind")
		nameNode := findYAMLScalarNode(docNode, "metadata", "name")
		if kind == "" || nameNode == nil || strings.TrimSpace(nameNode.Value) == "" {
			continue
		}
		ns := findYAMLString(docNode, "metadata", "namespace")
		name := strings.TrimSpace(nameNode.Value)

		start, end := nodeSpanPositions(content, root)
		if start == nil || end == nil {
			continue
		}
		sel := scalarSelectionRange(nameNode)

		out = append(out, protocol.DocumentSymbol{
			Name:           indexer.FormatResourceID(kind, ns, name),
			Kind:           kindObj,
			Range:          protocol.Range{Start: *start, End: *end},
			SelectionRange: sel,
		})
	}

	return out, nil
}

func workspaceSymbol(context *glsp.Context, params *protocol.WorkspaceSymbolParams) ([]protocol.SymbolInformation, error) {
	state.startWorkspaceScan()

	q := strings.ToLower(strings.TrimSpace(params.Query))
	resources := state.Store.ListAll()
	out := make([]protocol.SymbolInformation, 0, len(resources))

	for _, res := range resources {
		if res == nil || res.Kind == "" || res.Name == "" || res.FilePath == "" {
			continue
		}
		// Workspace symbol must only include on-disk workspace resources.
		// Embedded/virtual docs are excluded.
		if strings.Contains(res.FilePath, "://") {
			continue
		}

		display := indexer.FormatResourceID(res.Kind, res.Namespace, res.Name)
		if q != "" && !workspaceSymbolMatchesQuery(q, res.Kind, res.Namespace, res.Name, display) {
			continue
		}

		uri := filePathToURI(res.FilePath)
		start := protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)}
		end := protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))}

		out = append(out, protocol.SymbolInformation{
			Name: display,
			Kind: protocol.SymbolKindObject,
			Location: protocol.Location{
				URI:   uri,
				Range: protocol.Range{Start: start, End: end},
			},
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	const maxWorkspaceSymbols = 200
	if len(out) > maxWorkspaceSymbols {
		out = out[:maxWorkspaceSymbols]
	}
	return out, nil
}

func workspaceSymbolMatchesQuery(query string, kind string, namespace string, name string, display string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}

	k := strings.ToLower(strings.TrimSpace(kind))
	n := strings.ToLower(strings.TrimSpace(name))
	ns := strings.ToLower(indexer.NormalizeNamespace(namespace))
	nsName := ns + "/" + n
	id := strings.ToLower(strings.TrimSpace(display))

	// Support both single-string contains and token-based AND matching.
	// Tokens are separated by whitespace; every token must match at least one field.
	fields := []string{k, n, ns, nsName, id}
	tokens := strings.Fields(query)
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		matched := false
		for _, f := range fields {
			if strings.Contains(f, tok) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func findYAMLScalarNode(docNode *yaml.Node, keys ...string) *yaml.Node {
	if docNode == nil {
		return nil
	}
	root := docNode
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	cur := root
	for _, k := range keys {
		cur = yamlMapValue(cur, k)
		if cur == nil {
			return nil
		}
	}
	if cur.Kind == yaml.ScalarNode {
		return cur
	}
	return nil
}

func scalarSelectionRange(node *yaml.Node) protocol.Range {
	startLine := max(0, node.Line-1)
	startChar := max(0, node.Column-1)
	endChar := startChar + len(node.Value)
	if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
		endChar += 2
	}
	return protocol.Range{
		Start: protocol.Position{Line: uint32(startLine), Character: uint32(startChar)},
		End:   protocol.Position{Line: uint32(startLine), Character: uint32(max(startChar, endChar))},
	}
}
