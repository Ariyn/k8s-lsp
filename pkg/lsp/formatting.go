package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func textDocumentFormatting(context *glsp.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	if params == nil || state == nil {
		return []protocol.TextEdit{}, nil
	}
	if !state.FormattingEnabled {
		return []protocol.TextEdit{}, nil
	}

	uri := params.TextDocument.URI
	content, ok, _ := getOrLoadDocument(uri)
	if !ok || content == "" {
		return []protocol.TextEdit{}, nil
	}

	if state.FormattingDisableForTemplates && looksLikeTemplate(content) {
		return []protocol.TextEdit{}, nil
	}

	indent := state.FormattingIndentSize
	if indent <= 0 {
		indent = 2
		// Best-effort fallback to client options.
		if params.Options != nil {
			if v, ok := params.Options["tabSize"]; ok {
				if n, ok := toInt(v); ok && n > 0 {
					indent = n
				}
			}
		}
	}
	if indent < 1 {
		indent = 1
	}
	if indent > 8 {
		indent = 8
	}

	formatted, changed, err := formatYAMLDocument(content, indent)
	if err != nil || !changed {
		return []protocol.TextEdit{}, nil
	}

	// Safety: if formatting changed YAML meaning, do nothing.
	if !yamlMeaningEqual(content, formatted) {
		return []protocol.TextEdit{}, nil
	}

	edit := protocol.TextEdit{
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   endPositionForText(content),
		},
		NewText: formatted,
	}

	_ = context
	return []protocol.TextEdit{edit}, nil
}

func looksLikeTemplate(s string) bool {
	// Heuristic only; if this triggers we prefer no-op over destructive edits.
	// Helm: {{ ... }}
	// Go templates: {{- -}}
	// Some tools: <no value>
	signals := []string{"{{", "}}", "{%", "%}", "<no value>"}
	for _, sig := range signals {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

func formatYAMLDocument(content string, indentSize int) (string, bool, error) {
	// Normalize line endings during formatting; the server emits \n.
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	keepTrailingNewline := strings.HasSuffix(normalized, "\n")

	docs, err := decodeYAMLDocuments(normalized)
	if err != nil {
		return "", false, err
	}
	if len(docs) == 0 {
		return "", false, nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indentSize)
	for i := range docs {
		enforceK8sLiteralBlockScalars(&docs[i])
		if err := enc.Encode(&docs[i]); err != nil {
			_ = enc.Close()
			return "", false, err
		}
	}
	if err := enc.Close(); err != nil {
		return "", false, err
	}

	formatted := buf.String()
	if !keepTrailingNewline {
		formatted = strings.TrimRight(formatted, "\n")
	}

	if formatted == normalized {
		return formatted, false, nil
	}
	return formatted, true, nil
}

func decodeYAMLDocuments(content string) ([]yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(content))
	out := make([]yaml.Node, 0, 2)
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Be conservative: duplicate keys are ambiguous in YAML; we won't format such docs.
		if hasDuplicateMappingKeys(&doc) {
			return nil, fmt.Errorf("duplicate mapping keys")
		}
		out = append(out, doc)
	}
	return out, nil
}

func endPositionForText(content string) protocol.Position {
	// LSP positions are UTF-16 code units, but this server uses a best-effort rune length.
	// Keep consistent with semantic token logic.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return protocol.Position{Line: 0, Character: 0}
	}
	lastLine := len(lines) - 1
	lastChar := utfLen(lines[lastLine])
	return protocol.Position{Line: protocol.UInteger(lastLine), Character: protocol.UInteger(lastChar)}
}

func yamlMeaningEqual(a, b string) bool {
	a = strings.ReplaceAll(a, "\r\n", "\n")
	b = strings.ReplaceAll(b, "\r\n", "\n")

	docsA, err := decodeYAMLDocuments(a)
	if err != nil {
		return false
	}
	docsB, err := decodeYAMLDocuments(b)
	if err != nil {
		return false
	}
	if len(docsA) != len(docsB) {
		return false
	}

	return canonicalDocsHash(docsA) == canonicalDocsHash(docsB)
}

func canonicalDocsHash(docs []yaml.Node) string {
	h := sha256.New()
	for i := range docs {
		b := canonicalNodeBytes(&docs[i])
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalNodeBytes(n *yaml.Node) []byte {
	if n == nil {
		sum := sha256.Sum256([]byte("null"))
		return sum[:]
	}

	// Skip Document wrapper to focus on actual content.
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			sum := sha256.Sum256([]byte("doc-empty"))
			return sum[:]
		}
		return canonicalNodeBytes(n.Content[0])
	}

	switch n.Kind {
	case yaml.ScalarNode:
		// Tag carries type semantics (e.g. !!str vs !!int).
		key := "S|" + n.Tag + "|" + n.Value
		sum := sha256.Sum256([]byte(key))
		return sum[:]
	case yaml.SequenceNode:
		childHashes := make([]string, 0, len(n.Content))
		for _, it := range n.Content {
			b := canonicalNodeBytes(it)
			childHashes = append(childHashes, hex.EncodeToString(b))
		}
		joined := "Q|" + strings.Join(childHashes, ",")
		sum := sha256.Sum256([]byte(joined))
		return sum[:]
	case yaml.MappingNode:
		pairs := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			key := ""
			if k != nil {
				key = k.Value
			}
			vb := canonicalNodeBytes(v)
			pairs = append(pairs, key+"="+hex.EncodeToString(vb))
		}
		sort.Strings(pairs)
		joined := "M|" + strings.Join(pairs, ",")
		sum := sha256.Sum256([]byte(joined))
		return sum[:]
	default:
		// Treat other YAML node kinds conservatively.
		sum := sha256.Sum256([]byte(fmt.Sprintf("K|%d|%s", n.Kind, n.Value)))
		return sum[:]
	}
}

func enforceK8sLiteralBlockScalars(doc *yaml.Node) {
	if doc == nil {
		return
	}

	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return
	}

	kind, ok := mappingStringValue(root, "kind")
	if !ok {
		return
	}

	switch kind {
	case "ConfigMap":
		if data, ok := mappingValue(root, "data"); ok {
			ensureLiteralStyleForMultilineMappingValues(data)
		}
	case "Secret":
		if data, ok := mappingValue(root, "stringData"); ok {
			ensureLiteralStyleForMultilineMappingValues(data)
		}
	}
}

func mappingValue(m *yaml.Node, key string) (*yaml.Node, bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		v := m.Content[i+1]
		if k != nil && k.Kind == yaml.ScalarNode && k.Value == key {
			return v, true
		}
	}
	return nil, false
}

func mappingStringValue(m *yaml.Node, key string) (string, bool) {
	v, ok := mappingValue(m, key)
	if !ok || v == nil || v.Kind != yaml.ScalarNode {
		return "", false
	}
	return v.Value, true
}

func ensureLiteralStyleForMultilineMappingValues(n *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		v := n.Content[i+1]
		if v == nil || v.Kind != yaml.ScalarNode {
			continue
		}
		if strings.Contains(v.Value, "\n") {
			v.Style = yaml.LiteralStyle
		}
	}
}

func hasDuplicateMappingKeys(n *yaml.Node) bool {
	if n == nil {
		return false
	}

	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if hasDuplicateMappingKeys(c) {
				return true
			}
		}
		return false
	}

	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]struct{}{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k != nil && k.Kind == yaml.ScalarNode {
				key := strings.TrimSpace(k.Value)
				if _, ok := seen[key]; ok {
					return true
				}
				seen[key] = struct{}{}
			}
			if hasDuplicateMappingKeys(v) {
				return true
			}
		}
		return false
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if hasDuplicateMappingKeys(c) {
				return true
			}
		}
		return false
	default:
		for _, c := range n.Content {
			if hasDuplicateMappingKeys(c) {
				return true
			}
		}
		return false
	}
}
