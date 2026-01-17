package schema

import (
	"fmt"

	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

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
				diags = append(diags, unknownFieldDiagnostic(keyNode, key))
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
				return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, yType)}
			}
		} else if sch.Type == TypeInteger {
			if yType != TypeInteger {
				return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, yType)}
			}
		} else if sch.Type == TypeBoolean {
			if yType != TypeBoolean {
				return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, yType)}
			}
		} else if sch.Type == TypeString {
			if yType != TypeString {
				return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, yType)}
			}
		} else if sch.Type == TypeNull {
			if yType != TypeNull {
				return []protocol.Diagnostic{typeMismatchDiagnostic(node, sch.Type, yType)}
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
			return []protocol.Diagnostic{enumMismatchDiagnostic(node, sch.Enum, v)}
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
	msg := fmt.Sprintf("Unknown field %q", key)
	return protocol.Diagnostic{
		Range:    mkRangeForNode(keyNode, len(key)),
		Severity: &severity,
		Source:   &source,
		Message:  msg,
	}
}

func typeMismatchDiagnostic(node *yaml.Node, expected Type, got Type) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityWarning
	source := "k8s-lsp"
	msg := fmt.Sprintf("Type mismatch: expected %s, got %s", expected, got)
	return protocol.Diagnostic{
		Range:    mkRangeForNode(node, len(node.Value)),
		Severity: &severity,
		Source:   &source,
		Message:  msg,
	}
}

func enumMismatchDiagnostic(node *yaml.Node, enum []string, got string) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityWarning
	source := "k8s-lsp"
	msg := fmt.Sprintf("Invalid value %q (allowed: %s)", got, joinEnum(enum))
	return protocol.Diagnostic{
		Range:    mkRangeForNode(node, len(node.Value)),
		Severity: &severity,
		Source:   &source,
		Message:  msg,
	}
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
