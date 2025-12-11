package validator

import (
	"fmt"
	"os"
	"strings"

	"k8s-lsp/pkg/indexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

type Rule struct {
	Kind   string  `yaml:"kind"`
	Checks []Check `yaml:"checks"`
}

type Check struct {
	Type       string `yaml:"type"`       // "reference", "required", etc.
	Path       string `yaml:"path"`       // JSONPath-like string (e.g. spec.selector)
	TargetKind string `yaml:"targetKind"` // For reference checks
	TargetPath string `yaml:"targetPath"` // For reference checks
	Message    string `yaml:"message"`
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
	var diagnostics []protocol.Diagnostic

	var docNode yaml.Node
	if err := yaml.Unmarshal([]byte(content), &docNode); err != nil {
		return diagnostics
	}

	// Handle multiple documents in one file if necessary, but usually root is DocumentNode
	// yaml.Unmarshal returns the first document if not using Decoder.
	// But yaml.Node from Unmarshal is a DocumentNode.

	if docNode.Kind == yaml.DocumentNode && len(docNode.Content) > 0 {
		root := docNode.Content[0]
		if root.Kind == yaml.MappingNode {
			kind := ""
			kindNodes := findNodes(root, "kind")
			if len(kindNodes) > 0 {
				kind = kindNodes[0].Value
			}

			// Extract namespace
			namespace := "default"
			nsNodes := findNodes(root, "metadata.namespace")
			if len(nsNodes) > 0 {
				namespace = nsNodes[0].Value
			}

			for _, rule := range v.rules {
				if rule.Kind == kind {
					for _, check := range rule.Checks {
						if check.Type == "reference" {
							if diags := v.checkReference(uri, root, check, namespace); len(diags) > 0 {
								diagnostics = append(diagnostics, diags...)
							}
						}
					}
				}
			}
		}
	}

	return diagnostics
}

func findNodes(root *yaml.Node, path string) []*yaml.Node {
	currentNodes := []*yaml.Node{root}
	parts := strings.Split(path, ".")

	for _, part := range parts {
		var nextNodes []*yaml.Node
		for _, node := range currentNodes {
			if node.Kind == yaml.MappingNode {
				for i := 0; i < len(node.Content); i += 2 {
					if node.Content[i].Value == part {
						nextNodes = append(nextNodes, node.Content[i+1])
					}
				}
			} else if node.Kind == yaml.SequenceNode {
				// If we encounter a sequence, we check all elements
				// If the part is "*", we just collect all elements
				if part == "*" {
					nextNodes = append(nextNodes, node.Content...)
				} else {
					// Otherwise, we assume the elements are maps and we look for the key 'part'
					for _, child := range node.Content {
						if child.Kind == yaml.MappingNode {
							for i := 0; i < len(child.Content); i += 2 {
								if child.Content[i].Value == part {
									nextNodes = append(nextNodes, child.Content[i+1])
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
