package schema

import (
	"fmt"
	"sort"
	"strings"

	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func levenshtein(a, b string) int {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// DP with two rows.
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		ai := a[i-1]
		for j := 1; j <= len(b); j++ {
			cost := 0
			if ai != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func unknownFieldSuggestions(sch *Node, key string) []string {
	if sch == nil || sch.Type != TypeObject || len(sch.Properties) == 0 {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}

	type cand struct {
		k string
		d int
	}
	var cands []cand
	for k := range sch.Properties {
		if k == "" || k == key {
			continue
		}
		d := levenshtein(key, k)
		cands = append(cands, cand{k: k, d: d})
	}
	if len(cands) == 0 {
		return nil
	}

	// Threshold: keep it conservative to avoid noisy/wrong suggestions.
	maxDist := 2
	if len(key) >= 12 {
		maxDist = 3
	}

	filtered := cands[:0]
	for _, c := range cands {
		if c.d <= maxDist {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].d != filtered[j].d {
			return filtered[i].d < filtered[j].d
		}
		return filtered[i].k < filtered[j].k
	})

	limit := 2
	if len(filtered) < limit {
		limit = len(filtered)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, filtered[i].k)
	}
	return out
}

func ValidateDocument(docNode *yaml.Node, schemaRoot *Node) []protocol.Diagnostic {
	if docNode == nil || schemaRoot == nil {
		return nil
	}
	if docNode.Kind == yaml.DocumentNode {
		if len(docNode.Content) == 0 {
			return nil
		}
		docNode = docNode.Content[0]
	}
	return validateNode(docNode, schemaRoot)
}

func validateNode(node *yaml.Node, sch *Node) []protocol.Diagnostic {
	if node == nil || sch == nil {
		return nil
	}

	// TypeAny accepts anything and does not produce schema-based diagnostics.
	// This is especially important for fallback schemas where we intentionally
	// don't know the structure (e.g. spec/status for unknown kinds).
	if sch.Type == TypeAny {
		return nil
	}

	// If schema says array but YAML is not a sequence, still type-check scalars/maps.
	switch node.Kind {
	case yaml.MappingNode:
		// If schema is array, treat mapping as mismatch.
		if sch.Type != TypeAny && sch.Type != "" && sch.Type != TypeObject {
			return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, TypeObject)}
		}
		return validateMapping(node, sch)
	case yaml.SequenceNode:
		if sch.Type != TypeAny && sch.Type != "" && sch.Type != TypeArray {
			return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, TypeArray)}
		}
		return validateSequence(node, sch)
	case yaml.ScalarNode:
		return validateScalar(node, sch)
	default:
		return nil
	}
}

func validateMapping(node *yaml.Node, sch *Node) []protocol.Diagnostic {
	if node == nil {
		return nil
	}

	// Allow unknown keys under preserve-unknown-fields.
	allowUnknown := sch.PreserveUnknownFields
	if sch.AdditionalProperties != nil {
		// Treat additionalProperties as “map” and allow unknown keys.
		allowUnknown = true
	}

	var diags []protocol.Diagnostic

	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		key := ""
		if keyNode != nil {
			key = keyNode.Value
		}

		childSchema := (*Node)(nil)
		if sch.Type == TypeObject && sch.Properties != nil {
			childSchema = sch.Properties[key]
		}
		if childSchema == nil && sch.Type == TypeObject && sch.AdditionalProperties != nil {
			childSchema = sch.AdditionalProperties
		}

		if childSchema == nil {
			if !allowUnknown && key != "" {
				d := unknownFieldDiagnostic(keyNode, key)
				if sugg := unknownFieldSuggestions(sch, key); len(sugg) > 0 {
					d.Data = map[string]any{"suggestions": sugg}
				}
				diags = append(diags, d)
			}
			continue
		}

		diags = append(diags, validateNode(valNode, childSchema)...)
	}

	return diags
}

func validateSequence(node *yaml.Node, sch *Node) []protocol.Diagnostic {
	if node == nil {
		return nil
	}
	items := sch.Items
	if sch.Type != TypeArray {
		// If schema omitted type but provided items, still treat as array.
		items = sch.Items
	}
	if items == nil {
		return nil
	}

	var diags []protocol.Diagnostic
	for _, item := range node.Content {
		diags = append(diags, validateNode(item, items)...)
	}
	return diags
}

func validateScalar(node *yaml.Node, sch *Node) []protocol.Diagnostic {
	if node == nil {
		return nil
	}

	yType := yamlScalarType(node)
	if yType == TypeNull && sch.Nullable {
		return nil
	}

	// TypeAny accepts anything.
	if sch.Type != "" && sch.Type != TypeAny {
		// number accepts int or float
		if sch.Type == TypeNumber {
			if yType != TypeNumber && yType != TypeInteger {
				return []protocol.Diagnostic{typeMismatchDiagnosticWithData(node, sch, sch.Type, yType)}
			}
		} else if sch.Type == TypeInteger {
			if yType != TypeInteger {
				return []protocol.Diagnostic{typeMismatchDiagnosticWithData(node, sch, sch.Type, yType)}
			}
		} else if sch.Type == TypeBoolean {
			if yType != TypeBoolean {
				return []protocol.Diagnostic{typeMismatchDiagnosticWithData(node, sch, sch.Type, yType)}
			}
		} else if sch.Type == TypeString {
			if yType != TypeString {
				return []protocol.Diagnostic{typeMismatchDiagnosticWithData(node, sch, sch.Type, yType)}
			}
		} else if sch.Type == TypeNull {
			if yType != TypeNull {
				return []protocol.Diagnostic{typeMismatchDiagnosticWithData(node, sch, sch.Type, yType)}
			}
		}
	}

	if len(sch.Enum) > 0 {
		v := node.Value
		ok := false
		for _, ev := range sch.Enum {
			if v == ev {
				ok = true
				break
			}
		}
		if !ok {
			return []protocol.Diagnostic{enumMismatchDiagnosticWithData(node, sch, sch.Enum, v)}
		}
	}

	return nil
}

func mkRangeForNode(n *yaml.Node, fallbackLen int) protocol.Range {
	if n == nil {
		return protocol.Range{}
	}
	startLine := n.Line - 1
	startChar := n.Column - 1
	endLine := startLine
	endChar := startChar
	valLen := fallbackLen
	if n.Kind == yaml.ScalarNode {
		valLen = len(n.Value)
	}
	endChar = startChar + valLen
	return protocol.Range{
		Start: protocol.Position{Line: uint32(startLine), Character: uint32(startChar)},
		End:   protocol.Position{Line: uint32(endLine), Character: uint32(endChar)},
	}
}

func unknownFieldDiagnostic(keyNode *yaml.Node, key string) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityWarning
	source := "k8s-lsp"
	code := protocol.IntegerOrString{Value: "k8s.schema.unknownField"}
	msg := fmt.Sprintf("Unknown field %q", key)
	return protocol.Diagnostic{
		Range:    mkRangeForNode(keyNode, len(key)),
		Severity: &severity,
		Code:     &code,
		Source:   &source,
		Message:  msg,
	}
}

func typeMismatchDiagnostic(node *yaml.Node, expected Type, got Type) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityWarning
	source := "k8s-lsp"
	code := protocol.IntegerOrString{Value: "k8s.schema.typeMismatch"}
	msg := fmt.Sprintf("Type mismatch: expected %s, got %s", expected, got)
	return protocol.Diagnostic{
		Range:    mkRangeForNode(node, len(node.Value)),
		Severity: &severity,
		Code:     &code,
		Source:   &source,
		Message:  msg,
	}
}

func typeMismatchSuggestions(sch *Node, expected Type) []string {
	if sch == nil {
		return nil
	}
	var out []string
	if strings.TrimSpace(sch.Default) != "" {
		out = append(out, sch.Default)
	}
	if len(sch.Enum) > 0 {
		// If enums exist, they are typically the best suggestion set.
		out = append(out, sch.Enum...)
	}
	switch expected {
	case TypeBoolean:
		out = append(out, "true", "false")
	case TypeInteger:
		out = append(out, "0", "1")
	case TypeNumber:
		out = append(out, "0", "1")
	case TypeNull:
		out = append(out, "null")
	}

	// De-dupe while preserving order.
	seen := map[string]struct{}{}
	uniq := out[:0]
	for _, v := range out {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		uniq = append(uniq, v)
	}
	return uniq
}

func typeMismatchDiagnosticWithData(node *yaml.Node, sch *Node, expected Type, got Type) protocol.Diagnostic {
	d := typeMismatchDiagnostic(node, expected, got)
	sugg := typeMismatchSuggestions(sch, expected)
	if len(sugg) > 0 {
		d.Data = map[string]any{
			"expectedType": string(expected),
			"gotType":      string(got),
			"suggestions":  sugg,
		}
	} else {
		d.Data = map[string]any{
			"expectedType": string(expected),
			"gotType":      string(got),
		}
	}
	return d
}

func enumMismatchDiagnostic(node *yaml.Node, enum []string, got string) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityWarning
	source := "k8s-lsp"
	code := protocol.IntegerOrString{Value: "k8s.schema.enumMismatch"}
	msg := fmt.Sprintf("Invalid value %q (allowed: %s)", got, joinEnum(enum))
	return protocol.Diagnostic{
		Range:    mkRangeForNode(node, len(node.Value)),
		Severity: &severity,
		Code:     &code,
		Source:   &source,
		Message:  msg,
	}
}

func enumMismatchDiagnosticWithData(node *yaml.Node, sch *Node, enum []string, got string) protocol.Diagnostic {
	d := enumMismatchDiagnostic(node, enum, got)
	// Keep raw enum values; code actions can format quoting depending on expected type.
	d.Data = map[string]any{
		"expectedType": string(sch.Type),
		"got":          got,
		"allowed":      enum,
		"suggestions":  enum,
	}
	return d
}

func joinEnum(enum []string) string {
	if len(enum) == 0 {
		return ""
	}
	out := enum[0]
	for i := 1; i < len(enum); i++ {
		out += ", " + enum[i]
	}
	return out
}
