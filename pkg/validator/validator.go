package validator

import (
	"fmt"
	"io"
	"os"
	"strings"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

type Rule struct {
	Kind   string  `yaml:"kind"`
	Checks []Check `yaml:"checks"`
}

type Check struct {
	Type           string `yaml:"type"`       // "reference", "required", "resource-match"
	Path           string `yaml:"path"`       // JSONPath-like string (e.g. spec.selector)
	TargetKind     string `yaml:"targetKind"` // For reference checks
	TargetPath     string `yaml:"targetPath"` // For reference checks
	Message        string `yaml:"message"`
	SourceProperty string `yaml:"sourceProperty"` // For resource-match
	TargetProperty string `yaml:"targetProperty"` // For resource-match
}

type Config struct {
	Rules []Rule `yaml:"rules"`
}

type Validator struct {
	rules []Rule
	store *indexer.Store
}

func NewValidator(rulePath string, store *indexer.Store) (*Validator, error) {
	data, err := os.ReadFile(rulePath)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &Validator{
		rules: cfg.Rules,
		store: store,
	}, nil
}

func (v *Validator) Validate(uri string, content string) []protocol.Diagnostic {
	stream, err := yamlstream.Parse(content)
	if err != nil {
		return nil
	}
	return v.ValidateStream(uri, stream)
}

func (v *Validator) ValidateStream(uri string, stream *yamlstream.Stream) []protocol.Diagnostic {
	if v == nil || stream == nil {
		return nil
	}

	var diagnostics []protocol.Diagnostic
	for _, doc := range stream.Docs {
		diagnostics = append(diagnostics, v.validateDoc(uri, doc.Node)...)
	}
	return diagnostics
}

func (v *Validator) validateDoc(uri string, docNode *yaml.Node) []protocol.Diagnostic {
	if docNode == nil || docNode.Kind != yaml.DocumentNode || len(docNode.Content) == 0 {
		return nil
	}
	root := docNode.Content[0]
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}

	kind := ""
	kindNodes := findNodes(root, "kind")
	if len(kindNodes) > 0 {
		kind = kindNodes[0].Value
	}

	namespace := "default"
	nsNodes := findNodes(root, "metadata.namespace")
	if len(nsNodes) > 0 {
		namespace = nsNodes[0].Value
	}

	var diagnostics []protocol.Diagnostic
	for _, rule := range v.rules {
		if rule.Kind != kind {
			continue
		}
		for _, check := range rule.Checks {
			switch check.Type {
			case "reference":
				diagnostics = append(diagnostics, v.checkReference(uri, root, check, namespace)...)
			case "resource-match":
				diagnostics = append(diagnostics, v.checkResourceMatch(uri, root, check, namespace)...)
			}
		}
	}

	return diagnostics
}

func findNodes(root *yaml.Node, path string) []*yaml.Node {
	currentNodes := []*yaml.Node{root}
	parts := strings.Split(path, ".")

	for _, part := range parts {
		key, expandSeq := normalizePathPart(part)
		var nextNodes []*yaml.Node
		for _, node := range currentNodes {
			if node.Kind == yaml.MappingNode {
				for i := 0; i < len(node.Content); i += 2 {
					if node.Content[i].Value == key {
						val := node.Content[i+1]
						if expandSeq && val != nil && val.Kind == yaml.SequenceNode {
							nextNodes = append(nextNodes, val.Content...)
						} else {
							nextNodes = append(nextNodes, val)
						}
					}
				}
			} else if node.Kind == yaml.SequenceNode {
				// If we encounter a sequence, we check all elements
				// If the part is "*", we just collect all elements
				if key == "*" {
					nextNodes = append(nextNodes, node.Content...)
				} else {
					// Otherwise, we assume the elements are maps and we look for the key 'part'
					for _, child := range node.Content {
						if child.Kind == yaml.MappingNode {
							for i := 0; i < len(child.Content); i += 2 {
								if child.Content[i].Value == key {
									val := child.Content[i+1]
									if expandSeq && val != nil && val.Kind == yaml.SequenceNode {
										nextNodes = append(nextNodes, val.Content...)
									} else {
										nextNodes = append(nextNodes, val)
									}
								}
							}
						}
					}
				}
			}
		}
		currentNodes = nextNodes
		if len(currentNodes) == 0 {
			return nil
		}
	}
	return currentNodes
}

func normalizePathPart(part string) (key string, expandSeq bool) {
	key = part
	if strings.HasSuffix(key, "[*]") {
		key = strings.TrimSuffix(key, "[*]")
		return key, true
	}
	if strings.HasSuffix(key, "[]") {
		key = strings.TrimSuffix(key, "[]")
		return key, true
	}
	return key, false
}

func (v *Validator) checkReference(uri string, root *yaml.Node, check Check, namespace string) []protocol.Diagnostic {
	nodes := findNodes(root, check.Path)
	if len(nodes) == 0 {
		return nil
	}

	var diagnostics []protocol.Diagnostic

	for _, node := range nodes {
		if node.Kind == yaml.ScalarNode {
			// Single value reference (e.g. Service Name, ConfigMap Name)
			targetName := node.Value
			found := v.store.Get(check.TargetKind, namespace, targetName)
			if found == nil && isClusterScopedKind(check.TargetKind) {
				// Store defaults empty/cluster-scoped namespaces to "default".
				found = v.store.Get(check.TargetKind, "default", targetName)
			}

			if found == nil {
				startLine := node.Line - 1
				startChar := node.Column - 1
				endLine := startLine
				endChar := startChar + len(targetName)

				severity := protocol.DiagnosticSeverityWarning
				source := "k8s-lsp"

				diagnostics = append(diagnostics, protocol.Diagnostic{
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(startLine), Character: uint32(startChar)},
						End:   protocol.Position{Line: uint32(endLine), Character: uint32(endChar)},
					},
					Severity: &severity,
					Source:   &source,
					Message:  check.Message + fmt.Sprintf(" (Kind: %s, Name: %s)", check.TargetKind, targetName),
				})
			}
		} else if node.Kind == yaml.MappingNode {
			// For Service selector, node is a MappingNode (labels)
			selector := make(map[string]string)
			for i := 0; i < len(node.Content); i += 2 {
				selector[node.Content[i].Value] = node.Content[i+1].Value
			}

			if len(selector) == 0 {
				continue
			}

			// Check if any resource of TargetKind matches ALL labels
			candidates := v.store.ListByKind(check.TargetKind)
			found := false
			for _, res := range candidates {
				match := true
				for k, v := range selector {
					if res.Labels[k] != v {
						match = false
						break
					}
				}
				if match {
					// Enforce namespace when selector is namespaced.
					resNS := res.Namespace
					if resNS == "" {
						resNS = "default"
					}
					if namespace != "" {
						ns := namespace
						if ns == "" {
							ns = "default"
						}
						if resNS != ns {
							continue
						}
					}
					found = true
					break
				}
			}

			if !found {
				startLine := node.Line - 1
				startChar := node.Column - 1
				endLine := startLine
				endChar := startChar + 10

				severity := protocol.DiagnosticSeverityWarning
				source := "k8s-lsp"

				diagnostics = append(diagnostics, protocol.Diagnostic{
					Range: protocol.Range{
						Start: protocol.Position{Line: uint32(startLine), Character: uint32(startChar)},
						End:   protocol.Position{Line: uint32(endLine), Character: uint32(endChar)},
					},
					Severity: &severity,
					Source:   &source,
					Message:  check.Message + fmt.Sprintf(" (Kind: %s)", check.TargetKind),
				})
			}
		}
	}

	return diagnostics
}

func isClusterScopedKind(kind string) bool {
	switch kind {
	case "Namespace", "Node", "PersistentVolume", "StorageClass", "ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition":
		return true
	default:
		return false
	}
}

func (v *Validator) checkResourceMatch(uri string, root *yaml.Node, check Check, namespace string) []protocol.Diagnostic {
	nodes := findNodes(root, check.Path)
	if len(nodes) == 0 {
		return nil
	}

	var diagnostics []protocol.Diagnostic

	for _, node := range nodes {
		if node.Kind != yaml.ScalarNode {
			continue
		}
		targetName := node.Value

		// Find target resource
		// Try current namespace first, then default (for cluster-scoped like PV)
		targetRes := v.store.Get(check.TargetKind, namespace, targetName)
		if targetRes == nil {
			targetRes = v.store.Get(check.TargetKind, "default", targetName)
		}

		if targetRes == nil {
			continue // Reference not found, maybe handled by reference check
		}

		// Get Source Value
		sourceNodes := findNodes(root, check.SourceProperty)
		if len(sourceNodes) == 0 {
			continue
		}
		sourceVal := nodeToComparableString(sourceNodes[0])

		// Get Target Value
		targetVal := v.getValueFromResource(targetRes, check.TargetProperty)
		if targetVal == "" {
			continue
		}

		if sourceVal != targetVal {
			startLine := node.Line - 1
			startChar := node.Column - 1
			endLine := startLine
			endChar := startChar + len(targetName)

			severity := protocol.DiagnosticSeverityWarning
			source := "k8s-lsp"

			diagnostics = append(diagnostics, protocol.Diagnostic{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(startLine), Character: uint32(startChar)},
					End:   protocol.Position{Line: uint32(endLine), Character: uint32(endChar)},
				},
				Severity: &severity,
				Source:   &source,
				Message:  fmt.Sprintf("%s: %s (%s) != %s (%s)", check.Message, check.SourceProperty, sourceVal, check.TargetProperty, targetVal),
			})
		}
	}
	return diagnostics
}

func (v *Validator) getValueFromResource(res *indexer.K8sResource, path string) string {
	f, err := os.Open(res.FilePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			root := node.Content[0]
			if root.Kind == yaml.MappingNode {
				// Check if this is the right resource
				kindNodes := findNodes(root, "kind")
				nameNodes := findNodes(root, "metadata.name")

				if len(kindNodes) > 0 && len(nameNodes) > 0 {
					if kindNodes[0].Value == res.Kind && nameNodes[0].Value == res.Name {
						// Found it
						targetNodes := findNodes(root, path)
						if len(targetNodes) > 0 {
							return nodeToComparableString(targetNodes[0])
						}
						return ""
					}
				}
			}
		}
	}
	return ""
}

func nodeToComparableString(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value
	case yaml.SequenceNode:
		vals := make([]string, 0, len(node.Content))
		for _, c := range node.Content {
			if c != nil && c.Kind == yaml.ScalarNode {
				vals = append(vals, c.Value)
			}
		}
		return strings.Join(vals, ",")
	case yaml.MappingNode:
		// Not currently used for resource-match checks.
		return ""
	default:
		return ""
	}
}
