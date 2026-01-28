package lsp

import (
	"sort"
	"strings"

	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

var semanticTokenTypes = []string{
	"property",
	"keyword",
	"type",
	"namespace",
	"variable",
	"enumMember",
	"string",
	"number",
	"boolean",
}

var semanticTokenModifiers = []string{
	"declaration",
	"readonly",
	"defaultLibrary",
}

var semanticTokenTypeIndex = func() map[string]int {
	m := make(map[string]int, len(semanticTokenTypes))
	for i, t := range semanticTokenTypes {
		m[t] = i
	}
	return m
}()

var semanticTokenModifierBit = func() map[string]uint32 {
	m := make(map[string]uint32, len(semanticTokenModifiers))
	for i, t := range semanticTokenModifiers {
		m[t] = 1 << uint32(i)
	}
	return m
}()

type semanticToken struct {
	line      int
	startChar int
	length    int
	typeIdx   int
	mods      uint32
}

func textDocumentSemanticTokensFull(context *glsp.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	if context == nil || params == nil || state == nil {
		return &protocol.SemanticTokens{Data: []protocol.UInteger{}}, nil
	}
	if !state.SemanticTokensEnabled {
		return &protocol.SemanticTokens{Data: []protocol.UInteger{}}, nil
	}
	uri := params.TextDocument.URI
	content, ok, _ := getOrLoadDocument(uri)
	if !ok || content == "" {
		return &protocol.SemanticTokens{Data: []protocol.UInteger{}}, nil
	}

	_, ver, _ := state.getDocument(uri)
	cacheKey := uri

	state.semanticMu.Lock()
	if snap, ok := state.semanticCache[cacheKey]; ok && snap.ver == ver && snap.schemaGen == state.schemaGen {
		data := snap.data
		state.semanticMu.Unlock()
		return &protocol.SemanticTokens{Data: data}, nil
	}
	state.semanticMu.Unlock()

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil || stream == nil {
		return &protocol.SemanticTokens{Data: []protocol.UInteger{}}, nil
	}

	data := buildSemanticTokensData(stream, state.Schemas)

	state.semanticMu.Lock()
	state.semanticCache[cacheKey] = semanticTokensSnapshot{ver: ver, schemaGen: state.schemaGen, data: data}
	state.semanticMu.Unlock()

	return &protocol.SemanticTokens{Data: data}, nil
}

func buildSemanticTokensData(stream *yamlstream.Stream, reg *schema.Registry) []protocol.UInteger {
	if stream == nil {
		return []protocol.UInteger{}
	}

	var tokens []semanticToken
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

		walkYAMLForSemanticTokens(root, sch, nil, &tokens)
	}

	if len(tokens) == 0 {
		return []protocol.UInteger{}
	}

	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].line != tokens[j].line {
			return tokens[i].line < tokens[j].line
		}
		if tokens[i].startChar != tokens[j].startChar {
			return tokens[i].startChar < tokens[j].startChar
		}
		// longer first to reduce overlap flicker
		return tokens[i].length > tokens[j].length
	})

	data := make([]protocol.UInteger, 0, len(tokens)*5)
	prevLine := 0
	prevStart := 0
	for idx, t := range tokens {
		if t.length <= 0 || t.line < 0 || t.startChar < 0 {
			continue
		}
		deltaLine := t.line
		deltaStart := t.startChar
		if idx > 0 {
			deltaLine = t.line - prevLine
			if deltaLine == 0 {
				deltaStart = t.startChar - prevStart
			}
		}
		data = append(data,
			protocol.UInteger(deltaLine),
			protocol.UInteger(deltaStart),
			protocol.UInteger(t.length),
			protocol.UInteger(t.typeIdx),
			protocol.UInteger(t.mods),
		)
		prevLine = t.line
		prevStart = t.startChar
	}

	return data
}

func walkYAMLForSemanticTokens(n *yaml.Node, rootSchema *schema.Node, path []string, out *[]semanticToken) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			walkYAMLForSemanticTokens(n.Content[0], rootSchema, path, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k == nil || v == nil {
				continue
			}

			keyPath := append(path, k.Value)
			addKeyToken(k, path, out)

			childSchema := schema.ResolvePath(rootSchema, keyPath)
			addValueToken(k, v, keyPath, childSchema, out)

			walkYAMLForSemanticTokens(v, rootSchema, keyPath, out)
		}
	case yaml.SequenceNode:
		for _, it := range n.Content {
			walkYAMLForSemanticTokens(it, rootSchema, path, out)
		}
	}
}

func addKeyToken(k *yaml.Node, parentPath []string, out *[]semanticToken) {
	if k == nil || k.Kind != yaml.ScalarNode {
		return
	}
	line := k.Line - 1
	col := k.Column - 1
	if line < 0 || col < 0 {
		return
	}
	typeName := "property"
	if len(parentPath) == 0 {
		// top-level keys
		switch k.Value {
		case "apiVersion", "kind", "metadata", "spec", "status":
			typeName = "keyword"
		}
	}
	addToken(out, line, col, utfLen(k.Value), typeName, 0)
}

func addValueToken(k *yaml.Node, v *yaml.Node, path []string, sch *schema.Node, out *[]semanticToken) {
	if v == nil {
		return
	}
	if v.Kind != yaml.ScalarNode {
		return
	}
	line := v.Line - 1
	col := v.Column - 1
	if line < 0 || col < 0 {
		return
	}

	mods := uint32(0)
	if sch != nil && sch.Ref != nil {
		if sch.Ref.IsDefinition() {
			mods |= semanticTokenModifierBit["declaration"]
			addToken(out, line, col, utfLen(v.Value), "variable", mods)
			return
		}
		if sch.Ref.IsReference() {
			mods |= semanticTokenModifierBit["readonly"]
			addToken(out, line, col, utfLen(v.Value), "variable", mods)
			return
		}
	}

	// Special well-known paths.
	if len(path) >= 2 && path[len(path)-2] == "metadata" && path[len(path)-1] == "name" {
		mods |= semanticTokenModifierBit["declaration"]
		addToken(out, line, col, utfLen(v.Value), "variable", mods)
		return
	}
	if len(path) >= 2 && path[len(path)-2] == "metadata" && path[len(path)-1] == "namespace" {
		addToken(out, line, col, utfLen(v.Value), "namespace", mods)
		return
	}
	if len(path) == 1 && path[0] == "kind" {
		addToken(out, line, col, utfLen(v.Value), "type", mods)
		return
	}

	// Schema-driven typing.
	if sch != nil {
		if len(sch.Enum) > 0 {
			addToken(out, line, col, utfLen(v.Value), "enumMember", mods)
			return
		}
		switch sch.Type {
		case schema.TypeBoolean:
			addToken(out, line, col, utfLen(v.Value), "boolean", mods)
			return
		case schema.TypeInteger, schema.TypeNumber:
			addToken(out, line, col, utfLen(v.Value), "number", mods)
			return
		case schema.TypeString:
			addToken(out, line, col, utfLen(v.Value), "string", mods)
			return
		}
	}

	// Fallback: treat as string-ish scalar.
	addToken(out, line, col, utfLen(v.Value), "string", mods)
}

func addToken(out *[]semanticToken, line, start, length int, typeName string, mods uint32) {
	if length <= 0 {
		return
	}
	idx, ok := semanticTokenTypeIndex[typeName]
	if !ok {
		idx = semanticTokenTypeIndex["string"]
	}
	*out = append(*out, semanticToken{line: line, startChar: start, length: length, typeIdx: idx, mods: mods})
}

func utfLen(s string) int {
	// Best-effort: LSP positions are UTF-16 code units, but most YAML keys/values here are ASCII.
	// Using rune length avoids obvious multi-byte issues.
	return len([]rune(s))
}

func isYAMLScalarMatch(n *yaml.Node, line0, char0 int) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	l := n.Line - 1
	c := n.Column - 1
	if l != line0 {
		return false
	}
	if char0 < c {
		return false
	}
	return char0 <= c+utfLen(n.Value)
}

func normalizeScalarValue(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(n.Value)
}
