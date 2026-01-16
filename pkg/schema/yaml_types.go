package schema

import "gopkg.in/yaml.v3"

func yamlScalarType(n *yaml.Node) Type {
	if n == nil {
		return TypeAny
	}
	if n.Kind != yaml.ScalarNode {
		return TypeAny
	}
	switch n.Tag {
	case "!!str":
		return TypeString
	case "!!int":
		return TypeInteger
	case "!!float":
		return TypeNumber
	case "!!bool":
		return TypeBoolean
	case "!!null":
		return TypeNull
	default:
		// Best-effort fallback.
		return TypeString
	}
}
