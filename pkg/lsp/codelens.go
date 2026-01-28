package lsp

import (
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

const (
	cmdPeekDefinition = "k8sLsp.peekDefinition"
	cmdGoToDefinition = "k8sLsp.goToDefinition"
)

func textDocumentCodeLens(context *glsp.Context, params *protocol.CodeLensParams) ([]protocol.CodeLens, error) {
	if context == nil || params == nil || state == nil {
		return []protocol.CodeLens{}, nil
	}
	if !state.CodeLensEnabled {
		return []protocol.CodeLens{}, nil
	}

	uri := params.TextDocument.URI
	content, ok, _ := getOrLoadDocument(uri)
	if !ok || content == "" {
		return []protocol.CodeLens{}, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil || stream == nil {
		return []protocol.CodeLens{}, nil
	}

	return buildCodeLenses(stream, state.Schemas, uri), nil
}

func buildCodeLenses(stream *yamlstream.Stream, reg *schema.Registry, uri string) []protocol.CodeLens {
	if stream == nil {
		return []protocol.CodeLens{}
	}
	if uri == "" {
		return []protocol.CodeLens{}
	}

	out := make([]protocol.CodeLens, 0, 16)
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

		// Pick schema for this document.
		var sch *schema.Node
		if reg != nil {
			apiVersion := extractTopLevelScalar(doc.Node, "apiVersion")
			kind := extractTopLevelScalar(doc.Node, "kind")
			if apiVersion != "" && kind != "" {
				group, version := schema.ParseAPIVersion(apiVersion)
				gvk := schema.GVK{Group: group, Version: version, Kind: kind}
				sch = reg.Get(gvk)
			}
		}
		if sch == nil {
			sch = schema.KubernetesObjectFallback()
		}

		walkYAMLForCodeLenses(root, sch, nil, uri, &out)
	}

	return out
}

func walkYAMLForCodeLenses(n *yaml.Node, rootSchema *schema.Node, path []string, uri string, out *[]protocol.CodeLens) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			walkYAMLForCodeLenses(n.Content[0], rootSchema, path, uri, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k == nil || v == nil {
				continue
			}

			keyPath := append(path, k.Value)

			childSchema := schema.ResolvePath(rootSchema, keyPath)
			addCodeLensForScalarValue(v, childSchema, keyPath, uri, out)

			walkYAMLForCodeLenses(v, rootSchema, keyPath, uri, out)
		}
	case yaml.SequenceNode:
		for _, it := range n.Content {
			walkYAMLForCodeLenses(it, rootSchema, path, uri, out)
		}
	}
}

func addCodeLensForScalarValue(v *yaml.Node, sch *schema.Node, path []string, uri string, out *[]protocol.CodeLens) {
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	if sch == nil || sch.Ref == nil || !sch.Ref.IsReference() {
		return
	}

	value := normalizeScalarValue(v)
	if value == "" {
		return
	}
	// Avoid spam on huge scalars.
	if utfLen(value) > 200 {
		return
	}

	line0 := v.Line - 1
	col0 := v.Column - 1
	if line0 < 0 || col0 < 0 {
		return
	}

	rng := protocol.Range{
		Start: protocol.Position{Line: uint32(line0), Character: uint32(col0)},
		End:   protocol.Position{Line: uint32(line0), Character: uint32(col0 + utfLen(v.Value))},
	}

	args := []any{map[string]any{
		"uri":      uri,
		"position": map[string]any{"line": line0, "character": col0},
		"path":     path,
		"value":    value,
	}}

	*out = append(*out,
		protocol.CodeLens{
			Range: rng,
			Command: &protocol.Command{
				Title:     "Peek definition",
				Command:   cmdPeekDefinition,
				Arguments: args,
			},
		},
		protocol.CodeLens{
			Range: rng,
			Command: &protocol.Command{
				Title:     "Go to definition",
				Command:   cmdGoToDefinition,
				Arguments: args,
			},
		},
	)
}
