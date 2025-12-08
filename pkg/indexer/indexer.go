package indexer

import (
	"os"
	"path/filepath"
	"strings"

	"k8s-lsp/pkg/config"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

type Indexer struct {
	Store  *Store
	Config *config.Config
}

func NewIndexer(store *Store, cfg *config.Config) *Indexer {
	return &Indexer{Store: store, Config: cfg}
}

func (i *Indexer) ScanWorkspace(rootPath string) error {
	log.Info().Str("root", rootPath).Msg("Scanning workspace...")
	count := 0
	filesFound := 0
	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir // Skip hidden dirs like .git, but not the root itself if it starts with .
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			filesFound++
			if i.IndexFile(path) {
				count++
			}
		}
		return nil
	})
	log.Info().Int("filesFound", filesFound).Int("indexedCount", count).Msg("Workspace scan completed")
	return err
}

func (i *Indexer) IndexFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("Failed to open file")
		return false
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	indexed := false
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Log warning but continue if possible (though usually decode error stops the stream)
			log.Warn().Err(err).Str("path", path).Msg("Failed to decode YAML")
			break
		}

		res := i.parseK8sResource(&node, path)
		if res != nil {
			i.Store.Add(res)
			log.Debug().Str("kind", res.Kind).Str("name", res.Name).Str("path", path).Msg("Indexed resource")
			indexed = true
		}
	}
	return indexed
}

func (i *Indexer) parseK8sResource(node *yaml.Node, path string) *K8sResource {
	// node.Kind should be yaml.DocumentNode. Content[0] is the MappingNode (usually)
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		root := node.Content[0]
		if root.Kind != yaml.MappingNode {
			return nil
		}

		var apiVersion, kind string
		// Extract apiVersion and kind first
		for j := 0; j < len(root.Content); j += 2 {
			keyNode := root.Content[j]
			valNode := root.Content[j+1]
			if keyNode.Value == "apiVersion" {
				apiVersion = valNode.Value
			} else if keyNode.Value == "kind" {
				kind = valNode.Value
			}
		}

		if kind == "" {
			return nil
		}

		res := &K8sResource{
			ApiVersion: apiVersion,
			Kind:       kind,
			FilePath:   path,
			Labels:     make(map[string]string),
		}

		i.traverse(node, []string{}, func(n *yaml.Node, p []string) {
			// Check definitions
			for _, sym := range i.Config.Symbols {
				for _, def := range sym.Definitions {
					if contains(def.Kinds, kind) && matchPath(p, def.Path) {
						if sym.Name == "k8s.resource.name" {
							res.Name = n.Value
							res.Line = n.Line - 1
							res.Col = n.Column - 1
							// Also try to find namespace if we are at metadata.name
							// But namespace is at metadata.namespace.
							// We can't easily look sideways in this traversal without parent pointer.
							// But we can capture namespace when we visit metadata.namespace.
						} else if sym.Name == "k8s.label" {
							// n is the map node for labels
							if n.Kind == yaml.MappingNode {
								for k := 0; k < len(n.Content); k += 2 {
									lKey := n.Content[k]
									lVal := n.Content[k+1]
									res.Labels[lKey.Value] = lVal.Value
								}
							}
						}
					}
				}
			}

			// Special case for Namespace: if we visit metadata.namespace, capture it
			if matchPath(p, "metadata.namespace") {
				res.Namespace = n.Value
			}

			// Check references
			for _, refRule := range i.Config.References {
				if matchesKind(refRule.Match.Kinds, kind) && matchPath(p, refRule.Match.Path) {
					// Special handling for label selectors (Map)
					if refRule.Symbol == "k8s.label" && n.Kind == yaml.MappingNode {
						for k := 0; k < len(n.Content); k += 2 {
							_ = n.Content[k] // lKey unused
							lVal := n.Content[k+1]
							res.References = append(res.References, Reference{
								Name:   lVal.Value,
								Symbol: refRule.Symbol,
								Line:   lVal.Line - 1,
								Col:    lVal.Column - 1,
								Kind:   refRule.TargetKind,
							})
						}
						continue
					}

					// Standard reference (Scalar)
					ref := Reference{
						Name:   n.Value,
						Symbol: refRule.Symbol,
						Line:   n.Line - 1,
						Col:    n.Column - 1,
						Kind:   refRule.TargetKind,
					}
					res.References = append(res.References, ref)
				}
			}
		})

		if res.Name != "" {
			return res
		}
	}
	return nil
}

func (i *Indexer) traverse(node *yaml.Node, path []string, visitor func(*yaml.Node, []string)) {
	visitor(node, path)

	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			i.traverse(child, path, visitor)
		}
	} else if node.Kind == yaml.MappingNode {
		for j := 0; j < len(node.Content); j += 2 {
			keyNode := node.Content[j]
			valNode := node.Content[j+1]
			// Copy path
			newPath := make([]string, len(path)+1)
			copy(newPath, path)
			newPath[len(path)] = keyNode.Value

			i.traverse(valNode, newPath, visitor)
		}
	} else if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			i.traverse(child, path, visitor)
		}
	}
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
