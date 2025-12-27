package indexer

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"k8s-lsp/pkg/config"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

type Indexer struct {
	Store  *Store
	Config *config.Config
	mu     sync.RWMutex
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

	return i.indexReader(f, path)
}

func (i *Indexer) IndexContent(path, content string) bool {
	return i.indexReader(strings.NewReader(content), path)
}

func (i *Indexer) indexReader(r io.Reader, path string) bool {
	decoder := yaml.NewDecoder(r)
	indexed := false
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Log warning but continue if possible
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

		// Handle CRD registration
		if kind == "CustomResourceDefinition" {
			i.handleCRD(root)
		}

		res := &K8sResource{
			ApiVersion: apiVersion,
			Kind:       kind,
			FilePath:   path,
			Labels:     make(map[string]string),
		}

		i.mu.RLock()
		defer i.mu.RUnlock()

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

		// Special-case indexing for ConfigMap usages that require sibling context
		// (e.g. configMapKeyRef.name + configMapKeyRef.key).
		// This is intentionally not driven by rules because we need to correlate fields.
		res.References = append(res.References, extractConfigMapReferences(root, kind, normalizeNamespace(res.Namespace))...)
		res.References = dedupeReferences(res.References)

		if res.Name != "" {
			return res
		}
	}
	return nil
}

func normalizeNamespace(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func extractConfigMapReferences(root *yaml.Node, kind string, resourceNamespace string) []Reference {
	// Only pod-spec-bearing resources can reference ConfigMaps this way.
	if !(kind == "Pod" || kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "Job" || kind == "CronJob") {
		return nil
	}

	podSpec := findPodSpecNode(root, kind)
	if podSpec == nil {
		return nil
	}

	var refs []Reference

	// volumes[].configMap.name + volumes[].configMap.items[].key
	for _, vol := range findVolumes(podSpec) {
		cm := getMapValue(vol, "configMap")
		cmNameNode := getMapValue(cm, "name")
		if cmNameNode != nil && cmNameNode.Kind == yaml.ScalarNode {
			refs = append(refs, Reference{
				Kind:      "ConfigMap",
				Name:      cmNameNode.Value,
				Namespace: resourceNamespace,
				Line:      cmNameNode.Line - 1,
				Col:       cmNameNode.Column - 1,
			})

			items := getMapValue(cm, "items")
			for _, item := range asSequence(items) {
				keyNode := getMapValue(item, "key")
				if keyNode != nil && keyNode.Kind == yaml.ScalarNode {
					refs = append(refs, Reference{
						Kind:      "ConfigMap",
						Name:      cmNameNode.Value,
						Key:       keyNode.Value,
						Namespace: resourceNamespace,
						Line:      keyNode.Line - 1,
						Col:       keyNode.Column - 1,
					})
				}
			}
		}

		// volumes[].projected.sources[].configMap.{name,items[].key}
		projected := getMapValue(vol, "projected")
		sources := getMapValue(projected, "sources")
		for _, src := range asSequence(sources) {
			cm2 := getMapValue(src, "configMap")
			cm2NameNode := getMapValue(cm2, "name")
			if cm2NameNode != nil && cm2NameNode.Kind == yaml.ScalarNode {
				refs = append(refs, Reference{
					Kind:      "ConfigMap",
					Name:      cm2NameNode.Value,
					Namespace: resourceNamespace,
					Line:      cm2NameNode.Line - 1,
					Col:       cm2NameNode.Column - 1,
				})

				items := getMapValue(cm2, "items")
				for _, item := range asSequence(items) {
					keyNode := getMapValue(item, "key")
					if keyNode != nil && keyNode.Kind == yaml.ScalarNode {
						refs = append(refs, Reference{
							Kind:      "ConfigMap",
							Name:      cm2NameNode.Value,
							Key:       keyNode.Value,
							Namespace: resourceNamespace,
							Line:      keyNode.Line - 1,
							Col:       keyNode.Column - 1,
						})
					}
				}
			}
		}
	}

	// containers[].envFrom[].configMapRef.name (whole ConfigMap)
	for _, container := range findContainers(podSpec) {
		envFrom := getMapValue(container, "envFrom")
		for _, envFromItem := range asSequence(envFrom) {
			cmRef := getMapValue(envFromItem, "configMapRef")
			nameNode := getMapValue(cmRef, "name")
			if nameNode != nil && nameNode.Kind == yaml.ScalarNode {
				refs = append(refs, Reference{
					Kind:      "ConfigMap",
					Name:      nameNode.Value,
					Namespace: resourceNamespace,
					Line:      nameNode.Line - 1,
					Col:       nameNode.Column - 1,
				})
			}
		}

		// containers[].env[].valueFrom.configMapKeyRef.{name,key}
		env := getMapValue(container, "env")
		for _, envItem := range asSequence(env) {
			valueFrom := getMapValue(envItem, "valueFrom")
			cmKeyRef := getMapValue(valueFrom, "configMapKeyRef")
			nameNode := getMapValue(cmKeyRef, "name")
			keyNode := getMapValue(cmKeyRef, "key")
			if nameNode != nil && nameNode.Kind == yaml.ScalarNode {
				refs = append(refs, Reference{
					Kind:      "ConfigMap",
					Name:      nameNode.Value,
					Namespace: resourceNamespace,
					Line:      nameNode.Line - 1,
					Col:       nameNode.Column - 1,
				})
			}
			if nameNode != nil && nameNode.Kind == yaml.ScalarNode && keyNode != nil && keyNode.Kind == yaml.ScalarNode {
				refs = append(refs, Reference{
					Kind:      "ConfigMap",
					Name:      nameNode.Value,
					Key:       keyNode.Value,
					Namespace: resourceNamespace,
					Line:      keyNode.Line - 1,
					Col:       keyNode.Column - 1,
				})
			}
		}
	}

	return refs
}

func dedupeReferences(refs []Reference) []Reference {
	seen := make(map[string]struct{}, len(refs))
	out := make([]Reference, 0, len(refs))
	for _, r := range refs {
		k := r.Kind + "|" + r.Namespace + "|" + r.Name + "|" + r.Key + "|" + r.Symbol + "|" + fmtInt(r.Line) + "|" + fmtInt(r.Col)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, r)
	}
	return out
}

func fmtInt(v int) string {
	// avoid importing fmt just for Sprintf
	return strconv.Itoa(v)
}

func findPodSpecNode(root *yaml.Node, kind string) *yaml.Node {
	// root is the MappingNode of the document
	get := func(n *yaml.Node, key string) *yaml.Node {
		return getMapValue(n, key)
	}

	spec := get(root, "spec")
	if spec == nil {
		return nil
	}

	if kind == "Pod" {
		return spec
	}

	if kind == "Deployment" || kind == "DaemonSet" || kind == "StatefulSet" || kind == "Job" {
		tmpl := get(spec, "template")
		return get(tmpl, "spec")
	}

	if kind == "CronJob" {
		jt := get(spec, "jobTemplate")
		jtSpec := get(jt, "spec")
		tmpl := get(jtSpec, "template")
		return get(tmpl, "spec")
	}

	// Fallback: spec.template.spec
	tmpl := get(spec, "template")
	if tmpl != nil {
		if ps := get(tmpl, "spec"); ps != nil {
			return ps
		}
	}
	return nil
}

func getMapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func asSequence(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func findVolumes(podSpec *yaml.Node) []*yaml.Node {
	vols := getMapValue(podSpec, "volumes")
	return asSequence(vols)
}

func findContainers(podSpec *yaml.Node) []*yaml.Node {
	containers := getMapValue(podSpec, "containers")
	return asSequence(containers)
}

func (i *Indexer) handleCRD(root *yaml.Node) {
	// We need to find spec.names.kind
	// root is the MappingNode of the document
	var specNode *yaml.Node
	for j := 0; j < len(root.Content); j += 2 {
		if root.Content[j].Value == "spec" {
			specNode = root.Content[j+1]
			break
		}
	}

	if specNode == nil || specNode.Kind != yaml.MappingNode {
		return
	}

	var namesNode *yaml.Node
	for j := 0; j < len(specNode.Content); j += 2 {
		if specNode.Content[j].Value == "names" {
			namesNode = specNode.Content[j+1]
			break
		}
	}

	if namesNode == nil || namesNode.Kind != yaml.MappingNode {
		return
	}

	var kindName string
	for j := 0; j < len(namesNode.Content); j += 2 {
		if namesNode.Content[j].Value == "kind" {
			kindName = namesNode.Content[j+1].Value
			break
		}
	}

	if kindName != "" {
		i.registerKind(kindName)
	}
}

func (i *Indexer) registerKind(kind string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Find k8s.resource.name symbol
	for idx, sym := range i.Config.Symbols {
		if sym.Name == "k8s.resource.name" {
			// Check if already registered
			for _, def := range sym.Definitions {
				if contains(def.Kinds, kind) {
					return // Already registered
				}
			}

			// Add to the first definition that uses metadata.name
			found := false
			for dIdx, def := range sym.Definitions {
				if def.Path == "metadata.name" {
					i.Config.Symbols[idx].Definitions[dIdx].Kinds = append(i.Config.Symbols[idx].Definitions[dIdx].Kinds, kind)
					log.Info().Str("kind", kind).Msg("Registered new dynamic kind from CRD")
					found = true
					break
				}
			}

			if !found {
				// Create new definition
				i.Config.Symbols[idx].Definitions = append(i.Config.Symbols[idx].Definitions, config.SymbolDefinition{
					Kinds: []string{kind},
					Path:  "metadata.name",
				})
				log.Info().Str("kind", kind).Msg("Registered new dynamic kind from CRD (new definition)")
			}
			return
		}
	}
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
