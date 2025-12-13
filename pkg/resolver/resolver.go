package resolver

import (
	"fmt"
	"io"
	"strings"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

type Resolver struct {
	Store  *indexer.Store
	Config *config.Config
}

func NewResolver(store *indexer.Store, cfg *config.Config) *Resolver {
	return &Resolver{Store: store, Config: cfg}
}

func (r *Resolver) ResolveHover(docContent string, uri string, line, col int) (*protocol.Hover, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		targetNode, parentNode, path := findNodeAt(&node, line+1, col+1)
		if targetNode != nil {
			kind := findKind(&node)
			currentNamespace := findNamespace(&node)

			for _, refRule := range r.Config.References {
				if matchesKind(refRule.Match.Kinds, kind) && matchPath(path, refRule.Match.Path) {
					if refRule.Symbol == "k8s.resource.name" {
						targetKind := refRule.TargetKind
						ns := currentNamespace
						// Check for sibling namespace
						if parentNode != nil && parentNode.Kind == yaml.MappingNode {
							for k := 0; k < len(parentNode.Content); k += 2 {
								if parentNode.Content[k].Value == "namespace" {
									ns = parentNode.Content[k+1].Value
									break
								}
							}
						}
						if targetKind == "Namespace" {
							ns = ""
						}

						res := r.Store.Get(targetKind, ns, targetNode.Value)
						if res != nil {
							contents := fmt.Sprintf("**%s**\n\nKind: %s\nNamespace: %s\nFile: %s",
								res.Name, res.Kind, res.Namespace, res.FilePath)

							return &protocol.Hover{
								Contents: protocol.MarkupContent{
									Kind:  protocol.MarkupKindMarkdown,
									Value: contents,
								},
							}, nil
						}
					}
				}
			}
		}
	}
	return nil, nil
}

func (r *Resolver) ResolveDefinition(docContent string, uri string, line, col int) ([]protocol.LocationLink, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			log.Error().Err(err).Msg("Failed to parse YAML for definition")
			return nil, err
		}

		// LSP is 0-based, yaml.v3 is 1-based
		targetNode, parentNode, path := findNodeAt(&node, line+1, col+1)
		if targetNode != nil {
			log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor")

			originRange := calculateOriginRange(targetNode)
			kind := findKind(&node)
			currentNamespace := findNamespace(&node)

			// Check if we are at a definition site (Symbol)
			for _, sym := range r.Config.Symbols {
				for _, def := range sym.Definitions {
					if contains(def.Kinds, kind) && matchPath(path, def.Path) {
						log.Debug().Str("symbol", sym.Name).Msg("Found definition site at cursor")
						// We are at the definition. Return self.
						// We need to construct a LocationLink where TargetURI is the current file.

						// TargetRange should be the range of the definition.
						// Since we are at the definition, targetNode is the value node (e.g. "registry").
						targetRange := protocol.Range{
							Start: protocol.Position{Line: uint32(targetNode.Line - 1), Character: uint32(targetNode.Column - 1)},
							End:   protocol.Position{Line: uint32(targetNode.Line - 1), Character: uint32(targetNode.Column - 1 + len(targetNode.Value))},
						}

						return []protocol.LocationLink{{
							OriginSelectionRange: &originRange,
							TargetURI:            uri,
							TargetRange:          targetRange,
							TargetSelectionRange: targetRange,
						}}, nil
					}
				}
			}

			for _, refRule := range r.Config.References {
				if matchesKind(refRule.Match.Kinds, kind) && matchPath(path, refRule.Match.Path) {
					if refRule.Symbol == "k8s.label" {
						labelKey := path[len(path)-1]
						labelValue := targetNode.Value
						return r.findWorkloadsByLabel(labelKey, labelValue, originRange), nil
					} else if refRule.Symbol == "k8s.resource.name" {
						targetKind := refRule.TargetKind

						if targetKind != "" {
							// Namespace resource has no namespace
							ns := currentNamespace
							// Check for sibling namespace
							if parentNode != nil && parentNode.Kind == yaml.MappingNode {
								for k := 0; k < len(parentNode.Content); k += 2 {
									if parentNode.Content[k].Value == "namespace" {
										ns = parentNode.Content[k+1].Value
										break
									}
								}
							}

							if targetKind == "Namespace" {
								ns = "" // or "default" depending on store
							}

							log.Debug().Str("kind", targetKind).Str("ns", ns).Str("name", targetNode.Value).Msg("Looking up definition")
							res := r.Store.Get(targetKind, ns, targetNode.Value)
							if res != nil {
								targetRange := protocol.Range{
									Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
									End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
								}
								return []protocol.LocationLink{{
									OriginSelectionRange: &originRange,
									TargetURI:            "file://" + res.FilePath,
									TargetRange:          targetRange,
									TargetSelectionRange: targetRange,
								}}, nil
							} else {
								log.Debug().Msg("Definition not found in store")
							}
						}
					}
				}
			}

			return nil, nil
		}
	}

	return nil, nil
}

func (r *Resolver) ResolveReferences(docContent string, line, col int) ([]protocol.Location, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			log.Error().Err(err).Msg("Failed to parse YAML for references")
			return nil, err
		}

		targetNode, _, path := findNodeAt(&node, line+1, col+1)
		if targetNode != nil {
			log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor (References)")

			// Check if we are on metadata.name
			// Path: ["metadata", "name"]
			if len(path) == 2 && path[0] == "metadata" && path[1] == "name" {
				// We are on the definition of a resource.
				// We need to find out what resource this is.
				// Since we don't have the full resource struct here easily without re-parsing,
				// we can try to extract Kind from the node tree or just use the value as Name.
				// But we need Kind.

				// Let's parse the node into a K8sResource structure partially to get Kind.
				// Or just traverse up to find Kind.
				kind := findKind(&node)
				name := findName(&node)
				namespace := findNamespace(&node)

				if kind != "" && name != "" {
					log.Debug().Str("kind", kind).Str("name", name).Str("namespace", namespace).Msg("Finding references for resource")
					return r.findReferences(kind, name, namespace), nil
				}
			}

			// Check if we are on metadata.namespace
			// Path: ["metadata", "namespace"]
			if len(path) == 2 && path[0] == "metadata" && path[1] == "namespace" {
				namespaceName := targetNode.Value
				log.Debug().Str("namespace", namespaceName).Msg("Finding references for namespace")
				// Namespace resources are cluster-scoped, so namespace arg is empty
				return r.findReferences("Namespace", namespaceName, ""), nil
			}

			// Check configured references
			kind := findKind(&node)

			// Check if we are at a definition site (Symbol)
			for _, sym := range r.Config.Symbols {
				for _, def := range sym.Definitions {
					match := matchPath(path, def.Path)
					if !match && sym.Name == "k8s.label" {
						match = matchPathPrefix(path, def.Path)
					}

					if contains(def.Kinds, kind) && match {
						if sym.Name == "k8s.label" {
							// Assuming we are on the value
							labelKey := path[len(path)-1]
							labelValue := targetNode.Value
							log.Debug().Str("key", labelKey).Str("value", labelValue).Msg("Finding references for label definition")
							return r.findLabelReferences(labelKey, labelValue), nil
						}
					}
				}
			}

			for _, refRule := range r.Config.References {
				match := matchPath(path, refRule.Match.Path)
				if !match && refRule.Symbol == "k8s.label" {
					match = matchPathPrefix(path, refRule.Match.Path)
				}

				if matchesKind(refRule.Match.Kinds, kind) && match {
					if refRule.Symbol == "k8s.resource.name" {
						targetKind := refRule.TargetKind
						targetName := targetNode.Value
						// For namespace reference, target namespace is empty
						targetNamespace := ""
						if targetKind != "Namespace" {
							targetNamespace = findNamespace(&node)
						}

						log.Debug().Str("targetKind", targetKind).Str("targetName", targetName).Msg("Finding references for configured rule")
						return r.findReferences(targetKind, targetName, targetNamespace), nil
					} else if refRule.Symbol == "k8s.label" {
						labelKey := path[len(path)-1]
						labelValue := targetNode.Value
						log.Debug().Str("key", labelKey).Str("value", labelValue).Msg("Finding references for label usage")
						return r.findLabelReferences(labelKey, labelValue), nil
					}
				}
			}
		}
	}
	return nil, nil
}

func findName(root *yaml.Node) string {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "metadata" {
				metaNode := root.Content[i+1]
				if metaNode.Kind == yaml.MappingNode {
					for j := 0; j < len(metaNode.Content); j += 2 {
						if metaNode.Content[j].Value == "name" {
							return metaNode.Content[j+1].Value
						}
					}
				}
			}
		}
	}
	return ""
}

func findKind(root *yaml.Node) string {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "kind" {
				return root.Content[i+1].Value
			}
		}
	}
	return ""
}

func (r *Resolver) findReferences(kind, name, namespace string) []protocol.Location {
	var locations []protocol.Location

	// 1. Add the definition itself if found
	def := r.Store.Get(kind, namespace, name)
	if def != nil {
		locations = append(locations, protocol.Location{
			URI: "file://" + def.FilePath,
			Range: protocol.Range{
				Start: protocol.Position{Line: uint32(def.Line), Character: uint32(def.Col)},
				End:   protocol.Position{Line: uint32(def.Line), Character: uint32(def.Col + len(def.Name))},
			},
		})
	}

	// 2. Find references in other files
	resources := r.Store.FindReferences(kind, name)

	for _, res := range resources {

		// Find the exact location of the reference in the file
		for _, ref := range res.References {
			if ref.Kind == kind && ref.Name == name {
				locations = append(locations, protocol.Location{
					URI: "file://" + res.FilePath,
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col)},
						End:   protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col + len(ref.Name))},
					},
				})
			}
		}
	}
	return locations
}

func calculateOriginRange(node *yaml.Node) protocol.Range {
	startCol := node.Column - 1
	length := len(node.Value)

	// Rough estimation for quoted strings to include quotes in highlighting
	if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
		length += 2
	}

	return protocol.Range{
		Start: protocol.Position{Line: uint32(node.Line - 1), Character: uint32(startCol)},
		End:   protocol.Position{Line: uint32(node.Line - 1), Character: uint32(startCol + length)},
	}
}

func (r *Resolver) findWorkloadsByLabel(key, value string, originRange protocol.Range) []protocol.LocationLink {
	var links []protocol.LocationLink
	resources := r.Store.FindByLabel(key, value)
	for _, res := range resources {
		targetRange := protocol.Range{
			Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
			End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
		}
		links = append(links, protocol.LocationLink{
			OriginSelectionRange: &originRange,
			TargetURI:            "file://" + res.FilePath,
			TargetRange:          targetRange,
			TargetSelectionRange: targetRange,
		})
	}
	return links
}

func (r *Resolver) findServiceByName(name string, originRange protocol.Range) []protocol.LocationLink {
	// Assuming current namespace (we need context of the current file's namespace, but let's search all for now or default)
	// Ideally we pass the current document's namespace to ResolveDefinition.

	// Simple lookup by name (ignoring namespace for a moment or checking all namespaces)
	// Store.Get requires (kind, namespace, name).
	// We'll implement a FindByName in Store to search across namespaces or just use "default" for now.

	res := r.Store.Get("Service", "default", name) // TODO: Handle namespace correctly
	if res != nil {
		targetRange := protocol.Range{
			Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
			End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
		}
		return []protocol.LocationLink{{
			OriginSelectionRange: &originRange,
			TargetURI:            "file://" + res.FilePath,
			TargetRange:          targetRange,
			TargetSelectionRange: targetRange,
		}}
	}
	return nil
}

func (r *Resolver) findNamespaceByName(name string, originRange protocol.Range) []protocol.LocationLink {
	// Namespace resources are cluster-scoped, so they don't have a namespace.
	// Our store defaults empty namespace to "default".
	res := r.Store.Get("Namespace", "", name)
	if res != nil {
		targetRange := protocol.Range{
			Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
			End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
		}
		return []protocol.LocationLink{{
			OriginSelectionRange: &originRange,
			TargetURI:            "file://" + res.FilePath,
			TargetRange:          targetRange,
			TargetSelectionRange: targetRange,
		}}
	}
	return nil
} // Helper functions for path checking

func isServiceSelector(path []string) bool {
	// Check if path contains "spec" and "selector"
	// Example: spec -> selector -> app
	if len(path) < 2 {
		return false
	}
	// Simple check: last parent is selector, and somewhere before is spec
	// This is loose matching.
	if path[len(path)-2] == "selector" {
		return true
	}
	return false
}

func isIngressServiceRef(path []string) bool {
	// spec.rules[].http.paths[].backend.service.name
	if len(path) < 3 {
		return false
	}
	if path[len(path)-1] == "name" && path[len(path)-2] == "service" && path[len(path)-3] == "backend" {
		return true
	}
	return false
}

func isNamespaceRef(path []string) bool {
	// metadata.namespace
	if len(path) == 2 && path[0] == "metadata" && path[1] == "namespace" {
		return true
	}
	return false
}

// findNodeAt traverses the YAML AST to find the node at the given line/col.
// It returns the node and the path of keys leading to it.
func findNodeAt(node *yaml.Node, line, col int) (*yaml.Node, *yaml.Node, []string) {
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) > 0 {
			return findNodeAt(node.Content[0], line, col)
		}
		return nil, nil, nil
	}

	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]

			// Check if cursor is on the key
			// Key is usually strict
			if isKeyMatch(keyNode, line, col) {
				return keyNode, node, []string{keyNode.Value}
			}

			// Check if cursor is on the value
			// Value can be loose (rest of the line) or inside complex structure
			if isValueMatch(valNode, line, col) {
				if valNode.Kind == yaml.ScalarNode {
					return valNode, node, []string{keyNode.Value}
				}
				// Recurse
				found, parent, subPath := findNodeAt(valNode, line, col)
				if found != nil {
					return found, parent, append([]string{keyNode.Value}, subPath...)
				}
			} else {
				// Fallback: if key is on the same line, and cursor is after key, and valNode is null/empty scalar on same line
				// This handles completion for empty values like "key: "
				if keyNode.Line == line && valNode.Kind == yaml.ScalarNode && valNode.Line == line && valNode.Value == "" {
					// Check if cursor is after the key
					keyEndCol := keyNode.Column + len(keyNode.Value)
					if col > keyEndCol {
						return valNode, node, []string{keyNode.Value}
					}
				}
			}
		}
	} else if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if isValueMatch(item, line, col) {
				found, parent, subPath := findNodeAt(item, line, col)
				if found != nil {
					return found, parent, subPath
				}
			}
		}
	} else if node.Kind == yaml.ScalarNode {
		if isValueMatch(node, line, col) {
			return node, nil, nil
		}
	}

	return nil, nil, nil
}

func isKeyMatch(node *yaml.Node, line, col int) bool {
	if node.Line != line {
		return false
	}
	// Strict check for key to avoid overlapping with value
	endCol := node.Column + len(node.Value)
	if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
		endCol += 2
	}
	// Allow cursor to be at the end of the word
	match := col >= node.Column && col <= endCol
	if match {
		// log.Debug().Str("key", node.Value).Msg("Key matched")
	}
	return match
}

func isValueMatch(node *yaml.Node, line, col int) bool {
	// If node is Scalar, it usually ends on the same line (unless multiline string).
	// Enforce same line check for ScalarNode to prevent matching all subsequent lines.
	if node.Kind == yaml.ScalarNode {
		// TODO: Handle multiline strings (Style & yaml.TaggedStyle etc) if needed
		endCol := node.Column + len(node.Value)
		if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
			endCol += 2
		}
		// Allow cursor to be at the end of the word
		match := line == node.Line && col >= node.Column && col <= endCol
		if !match && line == node.Line {
			// log.Debug().Str("val", node.Value).Int("nodeCol", node.Column).Int("endCol", endCol).Int("cursorCol", col).Msg("Scalar mismatch")
		}
		return match
	}

	// If node is multiline (like a block), check if line is within range
	// Since we don't have end line, we assume it starts at node.Line
	if line < node.Line {
		return false
	}
	// If on the same line, check column
	if line == node.Line && col < node.Column {
		return false
	}
	return true
}

func isInside(node *yaml.Node, line, col int) bool {
	return isValueMatch(node, line, col)
}

func findNamespace(root *yaml.Node) string {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "metadata" {
				metaNode := root.Content[i+1]
				if metaNode.Kind == yaml.MappingNode {
					for j := 0; j < len(metaNode.Content); j += 2 {
						if metaNode.Content[j].Value == "namespace" {
							return metaNode.Content[j+1].Value
						}
					}
				}
			}
		}
	}
	return ""
}

func matchPath(current []string, pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) != len(current) {
		return false
	}
	for i, part := range parts {
		cleanPart := strings.TrimSuffix(part, "[]")
		if cleanPart != current[i] {
			return false
		}
	}
	return true
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func matchesKind(ruleKinds []string, currentKind string) bool {
	for _, k := range ruleKinds {
		if k == "*" || k == currentKind {
			return true
		}
	}
	return false
}

func matchPathPrefix(current []string, pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) > len(current) {
		return false
	}
	for i, part := range parts {
		cleanPart := strings.TrimSuffix(part, "[]")
		if cleanPart != current[i] {
			return false
		}
	}
	return true
}

func (r *Resolver) findLabelReferences(key, value string) []protocol.Location {
	var locations []protocol.Location

	// 1. Find definitions (resources having this label)
	resources := r.Store.FindByLabel(key, value)
	for _, res := range resources {
		locations = append(locations, protocol.Location{
			URI: "file://" + res.FilePath,
			Range: protocol.Range{
				Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
				End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
			},
		})
	}

	// 2. Find usages (resources referencing this label)
	refs := r.Store.FindLabelReferences(value)
	for _, res := range refs {
		for _, ref := range res.References {
			if ref.Symbol == "k8s.label" && ref.Name == value {
				locations = append(locations, protocol.Location{
					URI: "file://" + res.FilePath,
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col)},
						End:   protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col + len(ref.Name))},
					},
				})
			}
		}
	}

	return locations
}
