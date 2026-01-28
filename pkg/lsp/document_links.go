package lsp

import (
	"encoding/json"
	"net/url"

	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func textDocumentDocumentLink(context *glsp.Context, params *protocol.DocumentLinkParams) ([]protocol.DocumentLink, error) {
	if context == nil || params == nil || state == nil {
		return []protocol.DocumentLink{}, nil
	}
	if !state.ReferencesVisualizationEnabled || !state.DocumentLinksEnabled {
		return []protocol.DocumentLink{}, nil
	}

	uri := params.TextDocument.URI
	content, ok, _ := getOrLoadDocument(uri)
	if !ok || content == "" {
		return []protocol.DocumentLink{}, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil || stream == nil {
		return []protocol.DocumentLink{}, nil
	}

	return buildDocumentLinks(stream, state.Schemas, uri), nil
}

func buildDocumentLinks(stream *yamlstream.Stream, reg *schema.Registry, uri string) []protocol.DocumentLink {
	if stream == nil || uri == "" {
		return []protocol.DocumentLink{}
	}

	out := make([]protocol.DocumentLink, 0, 16)
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

		walkYAMLForDocumentLinks(root, sch, nil, uri, &out)
	}

	return out
}

func walkYAMLForDocumentLinks(n *yaml.Node, rootSchema *schema.Node, path []string, uri string, out *[]protocol.DocumentLink) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			walkYAMLForDocumentLinks(n.Content[0], rootSchema, path, uri, out)
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
			addDocumentLinkForScalarValue(v, childSchema, keyPath, uri, out)
			walkYAMLForDocumentLinks(v, rootSchema, keyPath, uri, out)
		}
	case yaml.SequenceNode:
		for _, it := range n.Content {
			walkYAMLForDocumentLinks(it, rootSchema, path, uri, out)
		}
	}
}

func addDocumentLinkForScalarValue(v *yaml.Node, sch *schema.Node, path []string, uri string, out *[]protocol.DocumentLink) {
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

	payload := map[string]any{
		"uri":      uri,
		"position": map[string]any{"line": line0, "character": col0},
		"path":     path,
		"value":    value,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}

	target := "command:" + cmdGoToDefinition + "?" + url.QueryEscape(string(b))
	tt := "Go to definition"
	*out = append(*out, protocol.DocumentLink{Range: rng, Target: &target, Tooltip: &tt})
}
