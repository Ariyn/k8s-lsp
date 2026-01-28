package schema

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadCRDSchemasFromFile loads CRD OpenAPIV3 schemas from a CRD YAML file.
// It supports multi-doc YAML; non-CRD documents are ignored.
func LoadCRDSchemasFromFile(reg *Registry, path string) (int, error) {
	if reg == nil {
		return 0, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return LoadCRDSchemasFromYAML(reg, string(b))
}

func LoadCRDSchemasFromYAML(reg *Registry, content string) (int, error) {
	if reg == nil {
		return 0, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(content))
	loaded := 0
	var firstErr error
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			firstErr = err
			break
		}
		if n := loadCRDSchemaFromDoc(reg, &doc); n > 0 {
			loaded += n
		}
	}
	return loaded, firstErr
}

func loadCRDSchemaFromDoc(reg *Registry, doc *yaml.Node) int {
	root := doc
	if root == nil {
		return 0
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return 0
	}
	kind := getMapScalar(root, "kind")
	if kind != "CustomResourceDefinition" {
		return 0
	}

	spec := getMap(root, "spec")
	if spec == nil {
		return 0
	}
	group := getMapScalar(spec, "group")
	names := getMap(spec, "names")
	crKind := ""
	if names != nil {
		crKind = getMapScalar(names, "kind")
	}
	// Note: real CRDs require a non-empty group, but we also support
	// CRD-like local schema documents for core-group kinds (group: "").
	if crKind == "" {
		return 0
	}

	versions := getMap(spec, "versions")
	if versions == nil {
		// Some CRDs still use spec.version.
		v := strings.TrimSpace(getMapScalar(spec, "version"))
		if v == "" {
			return 0
		}
		// Try spec.validation.openAPIV3Schema
		validation := getMap(spec, "validation")
		openapi := getMap(validation, "openAPIV3Schema")
		if openapi == nil {
			return 0
		}
		s := convertOpenAPIV3Schema(openapi)
		if s == nil {
			return 0
		}
		reg.Set(GVK{Group: group, Version: v, Kind: crKind}, s)
		return 1
	}

	loaded := 0
	for _, item := range asSequence(versions) {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}
		v := getMapScalar(item, "name")
		if v == "" {
			continue
		}
		schemaNode := getMap(item, "schema")
		openapi := getMap(schemaNode, "openAPIV3Schema")
		if openapi == nil {
			continue
		}
		s := convertOpenAPIV3Schema(openapi)
		if s == nil {
			continue
		}
		reg.Set(GVK{Group: group, Version: v, Kind: crKind}, s)
		loaded++
	}
	return loaded
}

func convertOpenAPIV3Schema(n *yaml.Node) *Node {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}

	// Composed schemas: best-effort merge.
	// We merge properties/enums recursively to increase completion/diagnostics coverage
	// while avoiding strict type requirements that may cause false positives.
	if merged := convertComposedSchema(n); merged != nil {
		return merged
	}

	s := &Node{Type: TypeAny}

	// k8s-lsp extensions (schema-driven reference/definition visualization)
	if rm := parseRefMeta(n); rm != nil {
		s.Ref = rm
	}

	// nullable
	if strings.ToLower(getMapScalar(n, "nullable")) == "true" {
		s.Nullable = true
	}

	// Kubernetes-specific hint: allow int-or-string without strict typing.
	if strings.ToLower(getMapScalar(n, "x-kubernetes-int-or-string")) == "true" {
		s.Type = TypeAny
	}

	// type
	if t := getMapScalar(n, "type"); t != "" {
		s.Type = Type(strings.ToLower(t))
	}

	// description/default
	s.Description = getMapScalar(n, "description")
	s.Default = getMapScalar(n, "default")

	// enum
	if enumNode := getMap(n, "enum"); enumNode != nil {
		var vals []string
		for _, it := range asSequence(enumNode) {
			if it == nil {
				continue
			}
			if it.Kind == yaml.ScalarNode {
				vals = append(vals, it.Value)
			}
		}
		s.Enum = vals
	}

	// preserve unknown
	if p := strings.ToLower(getMapScalar(n, "x-kubernetes-preserve-unknown-fields")); p == "true" {
		s.PreserveUnknownFields = true
	}

	// properties
	propsNode := getMap(n, "properties")
	if propsNode != nil && propsNode.Kind == yaml.MappingNode {
		if s.Type == TypeAny {
			s.Type = TypeObject
		}
		s.Properties = make(map[string]*Node)
		for i := 0; i < len(propsNode.Content); i += 2 {
			k := propsNode.Content[i]
			v := propsNode.Content[i+1]
			if k == nil || v == nil {
				continue
			}
			child := convertOpenAPIV3Schema(v)
			if child == nil {
				continue
			}
			s.Properties[k.Value] = child
		}
	}

	// items
	itemsNode := getMap(n, "items")
	if itemsNode != nil {
		if s.Type == TypeAny {
			s.Type = TypeArray
		}
		s.Items = convertOpenAPIV3Schema(itemsNode)
	}

	// additionalProperties
	ap := getMapRaw(n, "additionalProperties")
	if ap != nil {
		// Could be bool or schema object.
		if ap.Kind == yaml.ScalarNode {
			if strings.ToLower(strings.TrimSpace(ap.Value)) == "true" {
				s.AdditionalProperties = Any()
			}
		} else if ap.Kind == yaml.MappingNode {
			s.AdditionalProperties = convertOpenAPIV3Schema(ap)
		}
	}

	// Heuristic: if properties present but type unset, assume object.
	if s.Type == TypeAny && len(s.Properties) > 0 {
		s.Type = TypeObject
	}

	return s
}

func parseRefMeta(n *yaml.Node) *RefMeta {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(getMapScalar(n, "x-k8s-lsp-ref-role")))
	if role == "" {
		return nil
	}
	r := &RefMeta{Role: RefRole(role)}
	r.Kind = strings.TrimSpace(getMapScalar(n, "x-k8s-lsp-ref-kind"))
	r.Scope = strings.ToLower(strings.TrimSpace(getMapScalar(n, "x-k8s-lsp-ref-scope")))
	return r
}

func convertComposedSchema(n *yaml.Node) *Node {
	// allOf merges constraints; oneOf/anyOf pick one alternative.
	// We implement all three as a union-style merge of properties/enums/items.
	// This is intentionally permissive to avoid false positives.
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		seq := getMap(n, key)
		if seq == nil {
			continue
		}
		alts := asSequence(seq)
		if len(alts) == 0 {
			continue
		}
		var nodes []*Node
		for _, a := range alts {
			cn := convertOpenAPIV3Schema(a)
			if cn != nil {
				nodes = append(nodes, cn)
			}
		}
		if len(nodes) == 0 {
			continue
		}
		merged := mergeNodes(nodes)
		// Allow local overrides on the wrapper node as well.
		if strings.ToLower(getMapScalar(n, "nullable")) == "true" {
			merged.Nullable = true
		}
		if p := strings.ToLower(getMapScalar(n, "x-kubernetes-preserve-unknown-fields")); p == "true" {
			merged.PreserveUnknownFields = true
		}
		return merged
	}
	return nil
}

func mergeNodes(nodes []*Node) *Node {
	if len(nodes) == 0 {
		return nil
	}
	out := &Node{Type: TypeAny}

	// Type: keep if all equal and not Any, else Any.
	firstType := nodes[0].Type
	sameType := firstType != "" && firstType != TypeAny
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if n == nil {
			continue
		}
		if n.Nullable {
			out.Nullable = true
		}
		if n.PreserveUnknownFields {
			out.PreserveUnknownFields = true
		}
		if out.Description == "" && n.Description != "" {
			out.Description = n.Description
		}
		if out.Default == "" && n.Default != "" {
			out.Default = n.Default
		}
		if sameType {
			if n.Type != firstType {
				sameType = false
			}
		}
	}
	if sameType {
		out.Type = firstType
	}

	// Enum: union.
	enumSet := map[string]struct{}{}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		for _, ev := range n.Enum {
			if ev == "" {
				continue
			}
			enumSet[ev] = struct{}{}
		}
	}
	if len(enumSet) > 0 {
		out.Enum = make([]string, 0, len(enumSet))
		for ev := range enumSet {
			out.Enum = append(out.Enum, ev)
		}
	}

	// RefMeta: keep only if all non-nil refs agree on role.
	var ref *RefMeta
	conflict := false
	for _, n := range nodes {
		if n == nil || n.Ref == nil {
			continue
		}
		if ref == nil {
			ref = n.Ref
			continue
		}
		if ref.Role != n.Ref.Role {
			conflict = true
			break
		}
	}
	if !conflict {
		out.Ref = ref
	}

	// Properties: union; merge conflicts recursively.
	props := map[string]*Node{}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		for k, v := range n.Properties {
			if k == "" || v == nil {
				continue
			}
			if existing, ok := props[k]; ok && existing != nil {
				props[k] = mergeNodes([]*Node{existing, v})
			} else {
				props[k] = v
			}
		}
	}
	if len(props) > 0 {
		out.Properties = props
		if out.Type == TypeAny {
			out.Type = TypeObject
		}
	}

	// Items: merge if present.
	var itemNodes []*Node
	for _, n := range nodes {
		if n != nil && n.Items != nil {
			itemNodes = append(itemNodes, n.Items)
		}
	}
	if len(itemNodes) > 0 {
		out.Items = mergeNodes(itemNodes)
		if out.Type == TypeAny {
			out.Type = TypeArray
		}
	}

	// additionalProperties: if any, merge.
	var apNodes []*Node
	for _, n := range nodes {
		if n != nil && n.AdditionalProperties != nil {
			apNodes = append(apNodes, n.AdditionalProperties)
		}
	}
	if len(apNodes) > 0 {
		out.AdditionalProperties = mergeNodes(apNodes)
		if out.Type == TypeAny {
			out.Type = TypeObject
		}
	}

	return out
}

func getMap(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		v := n.Content[i+1]
		if k != nil && k.Value == key {
			return v
		}
	}
	return nil
}

func getMapRaw(n *yaml.Node, key string) *yaml.Node {
	return getMap(n, key)
}

func getMapScalar(n *yaml.Node, key string) string {
	v := getMap(n, key)
	if v == nil {
		return ""
	}
	if v.Kind == yaml.ScalarNode {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

func asSequence(n *yaml.Node) []*yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.SequenceNode {
		return n.Content
	}
	return nil
}

func (t Type) normalize() Type {
	s := strings.ToLower(string(t))
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "!!")
	return Type(s)
}

func validateOpenAPIType(t string) (Type, error) {
	t = strings.TrimSpace(strings.ToLower(t))
	switch t {
	case "object":
		return TypeObject, nil
	case "array":
		return TypeArray, nil
	case "string":
		return TypeString, nil
	case "integer":
		return TypeInteger, nil
	case "number":
		return TypeNumber, nil
	case "boolean":
		return TypeBoolean, nil
	case "null":
		return TypeNull, nil
	case "":
		return TypeAny, nil
	default:
		return TypeAny, fmt.Errorf("unknown openapi type %q", t)
	}
}
