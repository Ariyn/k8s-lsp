package schema

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadGVKSchemasFromFile loads OpenAPIV3 schemas for built-in (non-CRD) resources
// from a YAML file.
//
// Supported document shapes (multi-doc YAML supported; non-matching docs ignored):
//
//  1. Minimal pack doc:
//     group: ""
//     version: "v1"
//     kind: "Pod"
//     openAPIV3Schema: { ... }
//
//  2. Spec-wrapped doc:
//     apiVersion: k8s-lsp.dev/v1
//     kind: Schema
//     spec:
//     group: ""
//     version: "v1"
//     kind: "Pod"
//     schema:
//     openAPIV3Schema: { ... }
func LoadGVKSchemasFromFile(reg *Registry, path string) (int, error) {
	if reg == nil {
		return 0, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return LoadGVKSchemasFromYAML(reg, string(b))
}

func LoadGVKSchemasFromYAML(reg *Registry, content string) (int, error) {
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
		if n := loadGVKSchemaFromDoc(reg, &doc); n > 0 {
			loaded += n
		}
	}
	return loaded, firstErr
}

func loadGVKSchemaFromDoc(reg *Registry, doc *yaml.Node) int {
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

	// Shape 1: top-level group/version/kind + openAPIV3Schema.
	// Note: some schema-pack formats use top-level kind as a document type (e.g. kind: Schema),
	// so spec-wrapped schemas will override these values.
	group := strings.TrimSpace(getMapScalar(root, "group"))
	version := strings.TrimSpace(getMapScalar(root, "version"))
	kind := strings.TrimSpace(getMapScalar(root, "kind"))
	openapi := getMap(root, "openAPIV3Schema")

	// Shape 2: spec-wrapped.
	if (version == "" || kind == "" || openapi == nil) && getMap(root, "spec") != nil {
		spec := getMap(root, "spec")
		if spec != nil {
			if g := strings.TrimSpace(getMapScalar(spec, "group")); g != "" {
				group = g
			}
			if v := strings.TrimSpace(getMapScalar(spec, "version")); v != "" {
				version = v
			}
			if k := strings.TrimSpace(getMapScalar(spec, "kind")); k != "" {
				kind = k
			}

			// spec.schema.openAPIV3Schema or spec.openAPIV3Schema
			if openapi == nil {
				if schemaNode := getMap(spec, "schema"); schemaNode != nil {
					openapi = getMap(schemaNode, "openAPIV3Schema")
				}
			}
			if openapi == nil {
				openapi = getMap(spec, "openAPIV3Schema")
			}
			if openapi == nil {
				// Some users may mirror CRD style under spec.validation.openAPIV3Schema.
				if validation := getMap(spec, "validation"); validation != nil {
					openapi = getMap(validation, "openAPIV3Schema")
				}
			}
		}
	}

	if version == "" || kind == "" || openapi == nil {
		return 0
	}

	s := convertOpenAPIV3Schema(openapi)
	if s == nil {
		return 0
	}
	reg.Set(GVK{Group: group, Version: version, Kind: kind}, s)
	return 1
}
