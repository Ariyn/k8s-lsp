package lsp

import (
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func textDocumentDocumentHighlight(context *glsp.Context, params *protocol.DocumentHighlightParams) ([]protocol.DocumentHighlight, error) {
	if context == nil || params == nil || state == nil {
		return nil, nil
	}
	if !state.ReferencesVisualizationEnabled {
		return nil, nil
	}
	uri := params.TextDocument.URI
	content, ok, _ := getOrLoadDocument(uri)
	if !ok || content == "" {
		return nil, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil || stream == nil {
		return nil, nil
	}

	line0 := int(params.Position.Line)
	char0 := int(params.Position.Character)

	// Find the scalar under cursor and its path.
	path, value, isDecl := findScalarPathAt(stream, state.Schemas, line0, char0)
	if len(path) == 0 || value == "" {
		return nil, nil
	}

	// Highlight all same path + same value within the document.
	hls := findHighlightsForPathValue(stream, path, value, isDecl)
	return hls, nil
}

type scalarAt struct {
	path  []string
	value string
	decl  bool
}

func findScalarPathAt(stream *yamlstream.Stream, reg *schema.Registry, line0, char0 int) ([]string, string, bool) {
	if stream == nil {
		return nil, "", false
	}

	for _, doc := range stream.Docs {
		if doc.Node == nil {
			continue
		}
		root := doc.Node
		if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
			root = root.Content[0]
		}
		if root == nil {
			continue
		}
		if a := findScalarAtNode(root, nil, line0, char0); a != nil {
			decl := false
			// Schema-driven role detection (fallback to metadata.name convention).
			if reg != nil {
				apiVersion := extractTopLevelScalar(doc.Node, "apiVersion")
				kind := extractTopLevelScalar(doc.Node, "kind")
				if apiVersion != "" && kind != "" {
					group, version := schema.ParseAPIVersion(apiVersion)
					gvk := schema.GVK{Group: group, Version: version, Kind: kind}
					sRoot := reg.Get(gvk)
					if sRoot == nil {
						sRoot = schema.KubernetesObjectFallback()
					}
					if sNode := schema.ResolvePath(sRoot, a.path); sNode != nil && sNode.Ref != nil {
						decl = sNode.Ref.IsDefinition()
					}
				}
			}
			if !decl {
				decl = len(a.path) >= 2 && a.path[len(a.path)-2] == "metadata" && a.path[len(a.path)-1] == "name"
			}
			return a.path, a.value, decl
		}
	}
	return nil, "", false
}

func findScalarAtNode(n *yaml.Node, path []string, line0, char0 int) *scalarAt {
	if n == nil {
		return nil
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			return findScalarAtNode(n.Content[0], path, line0, char0)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k == nil || v == nil {
				continue
			}
			nextPath := append(path, k.Value)
			if isYAMLScalarMatch(v, line0, char0) {
				return &scalarAt{path: nextPath, value: normalizeScalarValue(v)}
			}
			if found := findScalarAtNode(v, nextPath, line0, char0); found != nil {
				return found
			}
		}
	case yaml.SequenceNode:
		for _, it := range n.Content {
			if found := findScalarAtNode(it, path, line0, char0); found != nil {
				return found
			}
		}
	}
	return nil
}

func findHighlightsForPathValue(stream *yamlstream.Stream, targetPath []string, targetValue string, decl bool) []protocol.DocumentHighlight {
	if stream == nil {
		return nil
	}

	var out []protocol.DocumentHighlight
	for _, doc := range stream.Docs {
		if doc.Node == nil {
			continue
		}
		root := doc.Node
		if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
			root = root.Content[0]
		}
		collectHighlights(root, nil, targetPath, targetValue, decl, &out)
	}
	return out
}

func collectHighlights(n *yaml.Node, path []string, targetPath []string, targetValue string, decl bool, out *[]protocol.DocumentHighlight) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			collectHighlights(n.Content[0], path, targetPath, targetValue, decl, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k == nil || v == nil {
				continue
			}
			nextPath := append(path, k.Value)
			if v.Kind == yaml.ScalarNode {
				if samePath(nextPath, targetPath) && normalizeScalarValue(v) == targetValue {
					line := v.Line - 1
					col := v.Column - 1
					if line >= 0 && col >= 0 {
						kind := protocol.DocumentHighlightKindRead
						if decl {
							kind = protocol.DocumentHighlightKindWrite
						}
						*out = append(*out, protocol.DocumentHighlight{
							Range: protocol.Range{
								Start: protocol.Position{Line: uint32(line), Character: uint32(col)},
								End:   protocol.Position{Line: uint32(line), Character: uint32(col + utfLen(v.Value))},
							},
							Kind: &kind,
						})
					}
				}
			}
			collectHighlights(v, nextPath, targetPath, targetValue, decl, out)
		}
	case yaml.SequenceNode:
		for _, it := range n.Content {
			collectHighlights(it, path, targetPath, targetValue, decl, out)
		}
	}
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
