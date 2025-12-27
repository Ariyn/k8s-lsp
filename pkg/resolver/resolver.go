package resolver

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
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

			// Check for ConfigMap embedded file
			if kind == "ConfigMap" && len(path) >= 2 && (path[len(path)-2] == "data" || path[len(path)-2] == "binaryData") {
				var valNode *yaml.Node
				if parentNode != nil && parentNode.Kind == yaml.MappingNode {
					for i := 0; i < len(parentNode.Content); i += 2 {
						if parentNode.Content[i] == targetNode {
							if i+1 < len(parentNode.Content) {
								valNode = parentNode.Content[i+1]
							}
							break
						}
					}
				}

				if valNode != nil && (valNode.Style == yaml.LiteralStyle || valNode.Style == yaml.FoldedStyle) {
					if strings.Contains(targetNode.Value, ".") {
						currentNamespace := findNamespace(&node)
						if currentNamespace == "" {
							currentNamespace = "default"
						}
						configMapName := findName(&node)
						if configMapName == "" {
							configMapName = "configmap"
						}

						// Use Base64 to avoid URL encoding issues with the source URI and key
						sourceEncoded := base64.URLEncoding.EncodeToString([]byte(uri))
						keyEncoded := base64.URLEncoding.EncodeToString([]byte(targetNode.Value))

						embeddedURI := fmt.Sprintf("k8s-embedded://%s/%s/%s?source=%s&key=%s",
							currentNamespace, configMapName, targetNode.Value, sourceEncoded, keyEncoded)

						openArgs := fmt.Sprintf(`{"uri":%q}`, embeddedURI)
						openLink := "command:k8sLsp.openEmbeddedFile?" + url.QueryEscape(openArgs)

						findArgs := fmt.Sprintf(`{"uri":%q,"position":{"line":%d,"character":%d}}`, uri, line, col)
						findLink := "command:k8sLsp.findEmbeddedFileUsages?" + url.QueryEscape(findArgs)

						contents := fmt.Sprintf(
							"Embedded File: **%s**\n\n[Open File](%s) · [Find Usages](%s)",
							targetNode.Value,
							openLink,
							findLink,
						)

						return &protocol.Hover{
							Contents: protocol.MarkupContent{
								Kind:  protocol.MarkupKindMarkdown,
								Value: contents,
							},
						}, nil
					}
				}
			}

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
						if res == nil && targetKind != "Namespace" && ns != "default" {
							// Store treats empty/cluster-scoped namespaces as "default".
							res = r.Store.Get(targetKind, "default", targetNode.Value)
						}
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

			// Special case: within a workload, go-to-definition for
			// containers[].volumeMounts[].name -> spec.template.spec.volumes[].name
			// (and initContainers[].volumeMounts[].name).
			if isVolumeMountNamePath(path) {
				podSpec := findPodSpecNode(&node)
				if podSpec != nil {
					if volNameNode := findVolumeNameNodeByName(podSpec, targetNode.Value); volNameNode != nil {
						targetRange := protocol.Range{
							Start: protocol.Position{Line: uint32(volNameNode.Line - 1), Character: uint32(volNameNode.Column - 1)},
							End:   protocol.Position{Line: uint32(volNameNode.Line - 1), Character: uint32(volNameNode.Column - 1 + len(volNameNode.Value))},
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

			// Check for ConfigMap embedded file
			kind := findKind(&node)
			if kind == "ConfigMap" && len(path) >= 2 && (path[len(path)-2] == "data" || path[len(path)-2] == "binaryData") {
				// Check if targetNode is a key
				var valNode *yaml.Node
				if parentNode != nil && parentNode.Kind == yaml.MappingNode {
					for i := 0; i < len(parentNode.Content); i += 2 {
						if parentNode.Content[i] == targetNode {
							if i+1 < len(parentNode.Content) {
								valNode = parentNode.Content[i+1]
							}
							break
						}
					}
				}

				if valNode != nil && (valNode.Style == yaml.LiteralStyle || valNode.Style == yaml.FoldedStyle) {
					// Check if key looks like a filename
					if strings.Contains(targetNode.Value, ".") {
						currentNamespace := findNamespace(&node)
						if currentNamespace == "" {
							currentNamespace = "default"
						}
						configMapName := findName(&node)
						if configMapName == "" {
							configMapName = "configmap"
						}

						// Use Base64 to avoid URL encoding issues with the source URI and key
						sourceEncoded := base64.URLEncoding.EncodeToString([]byte(uri))
						keyEncoded := base64.URLEncoding.EncodeToString([]byte(targetNode.Value))

						embeddedURI := fmt.Sprintf("k8s-embedded://%s/%s/%s?source=%s&key=%s",
							currentNamespace, configMapName, targetNode.Value, sourceEncoded, keyEncoded)

						targetRange := protocol.Range{
							Start: protocol.Position{Line: 0, Character: 0},
							End:   protocol.Position{Line: 0, Character: 0},
						}

						return []protocol.LocationLink{{
							OriginSelectionRange: &originRange,
							TargetURI:            embeddedURI,
							TargetRange:          targetRange,
							TargetSelectionRange: targetRange,
						}}, nil
					}
				}
			}

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
				isMatch := false
				if refRule.Symbol == "k8s.label" {
					isMatch = matchPathPrefix(path, refRule.Match.Path)
				} else {
					isMatch = matchPath(path, refRule.Match.Path)
				}

				if matchesKind(refRule.Match.Kinds, kind) && isMatch {
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
							if res == nil && targetKind != "Namespace" && ns != "default" {
								// Store treats empty/cluster-scoped namespaces as "default".
								res = r.Store.Get(targetKind, "default", targetNode.Value)
							}
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

func (r *Resolver) ResolveReferences(docContent string, uri string, line, col int) ([]protocol.Location, error) {
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

		targetNode, parentNode, path := findNodeAt(&node, line+1, col+1)
		if targetNode != nil {
			log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor (References)")

			// Special case: clicking volumeMounts[].subPath should open the References UI
			// with multiple targets so the user can choose:
			// - the ConfigMap key definition (in the ConfigMap YAML)
			// - the virtual embedded file (k8s-embedded://)
			if isVolumeMountSubPathPath(path) {
				locs := r.findVolumeMountSubPathTargets(&node, parentNode, targetNode.Value)
				if len(locs) > 0 {
					return locs, nil
				}
			}

			// Special case: ConfigMap embedded file (data/binaryData key)
			// Shift+F12 should return all usages (mounts/refs), not the virtual file.
			kind := findKind(&node)
			if kind == "ConfigMap" && len(path) >= 2 && (path[len(path)-2] == "data" || path[len(path)-2] == "binaryData") {
				var valNode *yaml.Node
				if parentNode != nil && parentNode.Kind == yaml.MappingNode {
					for i := 0; i < len(parentNode.Content); i += 2 {
						if parentNode.Content[i] == targetNode {
							if i+1 < len(parentNode.Content) {
								valNode = parentNode.Content[i+1]
							}
							break
						}
					}
				}

				if valNode != nil && (valNode.Style == yaml.LiteralStyle || valNode.Style == yaml.FoldedStyle) && strings.Contains(targetNode.Value, ".") {
					ns := findNamespace(&node)
					if ns == "" {
						ns = "default"
					}
					cmName := findName(&node)
					if cmName == "" {
						cmName = "configmap"
					}

					locs := r.findConfigMapEmbeddedFileUsages(ns, cmName, targetNode.Value)
					return filterOutLocationAtPosition(locs, uri, line, col), nil
				}
			}

			// Special case: within a workload (Deployment/DaemonSet/etc), map
			// spec.template.spec.volumes[].persistentVolumeClaim.claimName ->
			// containers[].volumeMounts[].name locations for the matching volume.
			// This helps "find references" show where a PVC claim is mounted.
			if isWorkloadPVCClaimNamePath(path) {
				locs := findPVCClaimMountUsagesInDocument(&node, uri, targetNode.Value)
				if len(locs) > 0 {
					return filterOutLocationAtPosition(locs, uri, line, col), nil
				}
			}

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
					locs := r.findReferences(kind, name, namespace)
					return filterOutLocationAtPosition(locs, uri, line, col), nil
				}
			}

			// Check if we are on metadata.namespace
			// Path: ["metadata", "namespace"]
			if len(path) == 2 && path[0] == "metadata" && path[1] == "namespace" {
				namespaceName := targetNode.Value
				log.Debug().Str("namespace", namespaceName).Msg("Finding references for namespace")
				// Namespace resources are cluster-scoped, so namespace arg is empty
				locs := r.findReferences("Namespace", namespaceName, "")
				return filterOutLocationAtPosition(locs, uri, line, col), nil
			}

			// Check configured references
			kind = findKind(&node)

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
							locs := r.findLabelReferences(labelKey, labelValue)
							return filterOutLocationAtPosition(locs, uri, line, col), nil
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
						locs := r.findReferences(targetKind, targetName, targetNamespace)
						return filterOutLocationAtPosition(locs, uri, line, col), nil
					} else if refRule.Symbol == "k8s.label" {
						labelKey := path[len(path)-1]
						labelValue := targetNode.Value
						log.Debug().Str("key", labelKey).Str("value", labelValue).Msg("Finding references for label usage")
						locs := r.findLabelReferences(labelKey, labelValue)
						return filterOutLocationAtPosition(locs, uri, line, col), nil
					}
				}
			}
		}
	}
	return nil, nil
}

func filterOutLocationAtPosition(locs []protocol.Location, uri string, line, col int) []protocol.Location {
	if len(locs) == 0 {
		return locs
	}

	pos := protocol.Position{Line: uint32(line), Character: uint32(col)}
	out := locs[:0]
	for _, loc := range locs {
		if loc.URI == uri && rangeContainsPosition(loc.Range, pos) {
			continue
		}
		out = append(out, loc)
	}
	return out
}

func rangeContainsPosition(r protocol.Range, p protocol.Position) bool {
	// LSP ranges are half-open: [start, end)
	return comparePosition(r.Start, p) <= 0 && comparePosition(p, r.End) < 0
}

func comparePosition(a, b protocol.Position) int {
	if a.Line < b.Line {
		return -1
	}
	if a.Line > b.Line {
		return 1
	}
	if a.Character < b.Character {
		return -1
	}
	if a.Character > b.Character {
		return 1
	}
	return 0
}

func (r *Resolver) findConfigMapEmbeddedFileUsages(namespace, configMapName, key string) []protocol.Location {
	var locations []protocol.Location
	if namespace == "" {
		namespace = "default"
	}

	resources := r.Store.FindReferences("ConfigMap", configMapName)
	for _, res := range resources {
		resNS := res.Namespace
		if resNS == "" {
			resNS = "default"
		}
		if resNS != namespace {
			continue
		}

		for _, ref := range res.References {
			if ref.Kind != "ConfigMap" || ref.Name != configMapName {
				continue
			}
			if ref.Key != "" && ref.Key != key {
				continue
			}

			display := ref.Name
			if ref.Key != "" {
				display = ref.Key
			}
			locations = append(locations, protocol.Location{
				URI: "file://" + res.FilePath,
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col)},
					End:   protocol.Position{Line: uint32(ref.Line), Character: uint32(ref.Col + len(display))},
				},
			})
		}
	}
	return locations
}

func isWorkloadPVCClaimNamePath(path []string) bool {
	// ...volumes[].persistentVolumeClaim.claimName
	if len(path) < 3 {
		return false
	}
	return path[len(path)-3] == "volumes" && path[len(path)-2] == "persistentVolumeClaim" && path[len(path)-1] == "claimName"
}

func findPVCClaimMountUsagesInDocument(root *yaml.Node, uri string, claimName string) []protocol.Location {
	var locations []protocol.Location

	podSpec := findPodSpecNode(root)
	if podSpec == nil {
		return nil
	}

	volumeNameNodes := findVolumeNameNodesForPVCClaim(podSpec, claimName)
	if len(volumeNameNodes) == 0 {
		return nil
	}

	// Index volume names by string for quick match.
	volumeNames := make(map[string]struct{}, len(volumeNameNodes))
	for _, n := range volumeNameNodes {
		if n != nil && n.Kind == yaml.ScalarNode {
			volumeNames[n.Value] = struct{}{}
			// Include the volume definition name itself as a helpful reference.
			locations = append(locations, protocol.Location{
				URI: uri,
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(n.Line - 1), Character: uint32(n.Column - 1)},
					End:   protocol.Position{Line: uint32(n.Line - 1), Character: uint32(n.Column - 1 + len(n.Value))},
				},
			})
		}
	}

	// Find matching volumeMounts by volume name.
	for _, mountNameNode := range findAllVolumeMountNameNodes(podSpec) {
		if mountNameNode == nil || mountNameNode.Kind != yaml.ScalarNode {
			continue
		}
		if _, ok := volumeNames[mountNameNode.Value]; ok {
			locations = append(locations, protocol.Location{
				URI: uri,
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(mountNameNode.Line - 1), Character: uint32(mountNameNode.Column - 1)},
					End:   protocol.Position{Line: uint32(mountNameNode.Line - 1), Character: uint32(mountNameNode.Column - 1 + len(mountNameNode.Value))},
				},
			})
		}
	}

	return locations
}

func isVolumeMountNamePath(path []string) bool {
	// ...containers[].volumeMounts[].name OR ...initContainers[].volumeMounts[].name
	if len(path) < 2 {
		return false
	}
	return path[len(path)-2] == "volumeMounts" && path[len(path)-1] == "name"
}

func isVolumeMountSubPathPath(path []string) bool {
	// ...containers[].volumeMounts[].subPath OR ...initContainers[].volumeMounts[].subPath
	if len(path) < 2 {
		return false
	}
	return path[len(path)-2] == "volumeMounts" && path[len(path)-1] == "subPath"
}

func findVolumeNameNodeByName(podSpec *yaml.Node, volumeName string) *yaml.Node {
	vol := findVolumeNodeByName(podSpec, volumeName)
	if vol == nil {
		return nil
	}
	for i := 0; i < len(vol.Content); i += 2 {
		if vol.Content[i].Value == "name" {
			n := vol.Content[i+1]
			if n != nil && n.Kind == yaml.ScalarNode {
				return n
			}
			break
		}
	}
	return nil
}

func findVolumeNodeByName(podSpec *yaml.Node, volumeName string) *yaml.Node {
	if podSpec == nil || podSpec.Kind != yaml.MappingNode {
		return nil
	}

	var volumes *yaml.Node
	for i := 0; i < len(podSpec.Content); i += 2 {
		if podSpec.Content[i].Value == "volumes" {
			volumes = podSpec.Content[i+1]
			break
		}
	}
	if volumes == nil || volumes.Kind != yaml.SequenceNode {
		return nil
	}

	for _, item := range volumes.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j < len(item.Content); j += 2 {
			if item.Content[j].Value == "name" {
				nameNode := item.Content[j+1]
				if nameNode != nil && nameNode.Kind == yaml.ScalarNode && nameNode.Value == volumeName {
					return item
				}
				break
			}
		}
	}
	return nil
}

func getMappingScalarValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			if v != nil && v.Kind == yaml.ScalarNode {
				return v
			}
			return nil
		}
	}
	return nil
}

func getMappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func (r *Resolver) findVolumeMountSubPathTargets(root *yaml.Node, volumeMountNode *yaml.Node, subPath string) []protocol.Location {
	if root == nil || volumeMountNode == nil || volumeMountNode.Kind != yaml.MappingNode {
		return nil
	}

	mountNameNode := getMappingScalarValue(volumeMountNode, "name")
	if mountNameNode == nil {
		return nil
	}

	podSpec := findPodSpecNode(root)
	if podSpec == nil {
		return nil
	}

	vol := findVolumeNodeByName(podSpec, mountNameNode.Value)
	if vol == nil {
		return nil
	}

	ns := findNamespace(root)
	if ns == "" {
		ns = "default"
	}

	var targets []protocol.Location

	addResourceTarget := func(kind, resName, key string) {
		if resName == "" || key == "" {
			return
		}
		res := r.Store.Get(kind, ns, resName)
		if res == nil && ns != "default" {
			res = r.Store.Get(kind, "default", resName)
		}
		if res == nil {
			return
		}

		keyNode, _, err := findResourceDataEntryInFile(res.FilePath, kind, ns, resName, key)
		if err != nil || keyNode == nil {
			return
		}

		keyRange := calculateOriginRange(keyNode)
		targets = append(targets, protocol.Location{
			URI: "file://" + res.FilePath,
			Range: protocol.Range{Start: keyRange.Start, End: keyRange.End},
		})

		// Offer the virtual embedded file as an alternative target.
		sourceURI := "file://" + res.FilePath
		sourceEncoded := base64.URLEncoding.EncodeToString([]byte(sourceURI))
		keyEncoded := base64.URLEncoding.EncodeToString([]byte(key))
		embeddedURI := fmt.Sprintf("k8s-embedded://%s/%s/%s?source=%s&key=%s", ns, resName, key, sourceEncoded, keyEncoded)
		targets = append(targets, protocol.Location{
			URI: embeddedURI,
			Range: protocol.Range{
				Start: protocol.Position{Line: 0, Character: 0},
				End:   protocol.Position{Line: 0, Character: 0},
			},
		})
	}

	// configMap volume
	if cm := getMappingValue(vol, "configMap"); cm != nil && cm.Kind == yaml.MappingNode {
		cmName := ""
		if cmNameNode := getMappingScalarValue(cm, "name"); cmNameNode != nil {
			cmName = cmNameNode.Value
		}
		if cmName != "" {
			key, ok := resolveKeyFromItems(getMappingValue(cm, "items"), subPath)
			if ok {
				addResourceTarget("ConfigMap", cmName, key)
			}
		}
	}

	// secret volume
	if sec := getMappingValue(vol, "secret"); sec != nil && sec.Kind == yaml.MappingNode {
		secName := ""
		if secNameNode := getMappingScalarValue(sec, "secretName"); secNameNode != nil {
			secName = secNameNode.Value
		}
		if secName != "" {
			key, ok := resolveKeyFromItems(getMappingValue(sec, "items"), subPath)
			if ok {
				addResourceTarget("Secret", secName, key)
			}
		}
	}

	// projected volume sources[].{configMap,secret}
	if projected := getMappingValue(vol, "projected"); projected != nil && projected.Kind == yaml.MappingNode {
		sources := getMappingValue(projected, "sources")
		if sources != nil && sources.Kind == yaml.SequenceNode {
			for _, src := range sources.Content {
				if src == nil || src.Kind != yaml.MappingNode {
					continue
				}
				if cm2 := getMappingValue(src, "configMap"); cm2 != nil && cm2.Kind == yaml.MappingNode {
					cmName := ""
					if cmNameNode := getMappingScalarValue(cm2, "name"); cmNameNode != nil {
						cmName = cmNameNode.Value
					}
					if cmName != "" {
						key, ok := resolveKeyFromItems(getMappingValue(cm2, "items"), subPath)
						if ok {
							addResourceTarget("ConfigMap", cmName, key)
						}
					}
				}

				if sec2 := getMappingValue(src, "secret"); sec2 != nil && sec2.Kind == yaml.MappingNode {
					secName := ""
					// projected secret uses "name" (not secretName)
					if secNameNode := getMappingScalarValue(sec2, "name"); secNameNode != nil {
						secName = secNameNode.Value
					}
					if secName != "" {
						key, ok := resolveKeyFromItems(getMappingValue(sec2, "items"), subPath)
						if ok {
							addResourceTarget("Secret", secName, key)
						}
					}
				}
			}
		}
	}

	if len(targets) == 0 {
		return nil
	}
	return targets
}

func resolveKeyFromItems(items *yaml.Node, subPath string) (string, bool) {
	// If items is not specified, filename defaults to key.
	if items == nil {
		return subPath, true
	}
	if items.Kind != yaml.SequenceNode {
		return subPath, true
	}
	for _, item := range items.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}
		keyNode := getMappingScalarValue(item, "key")
		pathNode := getMappingScalarValue(item, "path")
		if keyNode == nil {
			continue
		}
		fileName := keyNode.Value
		if pathNode != nil && pathNode.Value != "" {
			fileName = pathNode.Value
		}
		if fileName == subPath {
			return keyNode.Value, true
		}
	}
	// items specified but no match => not from this source
	return "", false
}

func findResourceDataEntryInFile(filePath, expectedKind, namespace, resName, key string) (*yaml.Node, *yaml.Node, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, err
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(bytes)))
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, err
		}

		root := &doc
		if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
			root = root.Content[0]
		}
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}

		if findKind(root) != expectedKind {
			continue
		}
		if findName(root) != resName {
			continue
		}
		resNS := findNamespace(root)
		if resNS == "" {
			resNS = "default"
		}
		if namespace == "" {
			namespace = "default"
		}
		if resNS != namespace {
			continue
		}

		searchSections := func(sectionKeys ...string) (*yaml.Node, *yaml.Node) {
			for i := 0; i < len(root.Content); i += 2 {
				for _, secKey := range sectionKeys {
					if root.Content[i].Value != secKey {
						continue
					}
					dataNode := root.Content[i+1]
					if dataNode == nil || dataNode.Kind != yaml.MappingNode {
						continue
					}
					for j := 0; j < len(dataNode.Content); j += 2 {
						k := dataNode.Content[j]
						v := dataNode.Content[j+1]
						if k != nil && k.Kind == yaml.ScalarNode && k.Value == key {
							return k, v
						}
					}
				}
			}
			return nil, nil
		}

		if expectedKind == "ConfigMap" {
			k, v := searchSections("data", "binaryData")
			if k != nil {
				return k, v, nil
			}
		}
		if expectedKind == "Secret" {
			// Prefer stringData if present.
			k, v := searchSections("stringData")
			if k != nil {
				return k, v, nil
			}
			k, v = searchSections("data")
			if k != nil {
				return k, v, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("%s %s/%s key %s not found", expectedKind, namespace, resName, key)
}


func findPodSpecNode(root *yaml.Node) *yaml.Node {
	// Supports the common workload shapes:
	// - Pod: spec
	// - Deployment/DaemonSet/StatefulSet/Job: spec.template.spec
	// - CronJob: spec.jobTemplate.spec.template.spec
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}

	kind := findKind(root)

	// Helper to follow a mapping path.
	get := func(n *yaml.Node, key string) *yaml.Node {
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

	spec := get(root, "spec")
	if spec == nil {
		return nil
	}

	if kind == "Pod" {
		return spec
	}

	// Workloads with template
	if kind == "Deployment" || kind == "DaemonSet" || kind == "StatefulSet" || kind == "Job" {
		tmpl := get(spec, "template")
		return get(tmpl, "spec")
	}

	// CronJob path
	if kind == "CronJob" {
		jt := get(spec, "jobTemplate")
		jtSpec := get(jt, "spec")
		tmpl := get(jtSpec, "template")
		return get(tmpl, "spec")
	}

	// Fallback: try spec.template.spec if present.
	tmpl := get(spec, "template")
	if tmpl != nil {
		if ps := get(tmpl, "spec"); ps != nil {
			return ps
		}
	}
	return nil
}

func findVolumeNameNodesForPVCClaim(podSpec *yaml.Node, claimName string) []*yaml.Node {
	// Find volumes[] entries where persistentVolumeClaim.claimName == claimName
	// and return the corresponding volumes[].name scalar nodes.
	if podSpec == nil || podSpec.Kind != yaml.MappingNode {
		return nil
	}

	var volumes *yaml.Node
	for i := 0; i < len(podSpec.Content); i += 2 {
		if podSpec.Content[i].Value == "volumes" {
			volumes = podSpec.Content[i+1]
			break
		}
	}
	if volumes == nil || volumes.Kind != yaml.SequenceNode {
		return nil
	}

	var results []*yaml.Node
	for _, item := range volumes.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}

		var nameNode *yaml.Node
		var pvcNode *yaml.Node
		for j := 0; j < len(item.Content); j += 2 {
			switch item.Content[j].Value {
			case "name":
				nameNode = item.Content[j+1]
			case "persistentVolumeClaim":
				pvcNode = item.Content[j+1]
			}
		}
		if pvcNode == nil || pvcNode.Kind != yaml.MappingNode {
			continue
		}
		var claimNode *yaml.Node
		for k := 0; k < len(pvcNode.Content); k += 2 {
			if pvcNode.Content[k].Value == "claimName" {
				claimNode = pvcNode.Content[k+1]
				break
			}
		}
		if claimNode != nil && claimNode.Kind == yaml.ScalarNode && claimNode.Value == claimName {
			if nameNode != nil && nameNode.Kind == yaml.ScalarNode {
				results = append(results, nameNode)
			}
		}
	}

	return results
}

func findAllVolumeMountNameNodes(podSpec *yaml.Node) []*yaml.Node {
	// Returns all containers[].volumeMounts[].name and initContainers[].volumeMounts[].name nodes.
	if podSpec == nil || podSpec.Kind != yaml.MappingNode {
		return nil
	}

	collectFromContainers := func(containers *yaml.Node) []*yaml.Node {
		if containers == nil || containers.Kind != yaml.SequenceNode {
			return nil
		}
		var out []*yaml.Node
		for _, c := range containers.Content {
			if c == nil || c.Kind != yaml.MappingNode {
				continue
			}
			var vms *yaml.Node
			for i := 0; i < len(c.Content); i += 2 {
				if c.Content[i].Value == "volumeMounts" {
					vms = c.Content[i+1]
					break
				}
			}
			if vms == nil || vms.Kind != yaml.SequenceNode {
				continue
			}
			for _, vm := range vms.Content {
				if vm == nil || vm.Kind != yaml.MappingNode {
					continue
				}
				for j := 0; j < len(vm.Content); j += 2 {
					if vm.Content[j].Value == "name" {
						out = append(out, vm.Content[j+1])
						break
					}
				}
			}
		}
		return out
	}

	var containers *yaml.Node
	var initContainers *yaml.Node
	for i := 0; i < len(podSpec.Content); i += 2 {
		switch podSpec.Content[i].Value {
		case "containers":
			containers = podSpec.Content[i+1]
		case "initContainers":
			initContainers = podSpec.Content[i+1]
		}
	}

	var results []*yaml.Node
	results = append(results, collectFromContainers(containers)...)
	results = append(results, collectFromContainers(initContainers)...)
	return results
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

func (r *Resolver) ResolveEmbeddedContent(docContent string, key string) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}

		if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
			continue
		}
		root := node.Content[0]
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}

		kind := findKind(root)
		searchMap := func(section string) (string, bool) {
			for i := 0; i < len(root.Content); i += 2 {
				if root.Content[i].Value != section {
					continue
				}
				m := root.Content[i+1]
				if m == nil || m.Kind != yaml.MappingNode {
					return "", false
				}
				for j := 0; j < len(m.Content); j += 2 {
					if m.Content[j].Value == key {
						val := m.Content[j+1]
						if val != nil {
							return val.Value, true
						}
						return "", true
					}
				}
			}
			return "", false
		}

		if kind == "ConfigMap" {
			if v, ok := searchMap("data"); ok {
				return v, nil
			}
			if v, ok := searchMap("binaryData"); ok {
				return v, nil
			}
		}
		if kind == "Secret" {
			// Prefer stringData (plain-text).
			if v, ok := searchMap("stringData"); ok {
				return v, nil
			}
			if v, ok := searchMap("data"); ok {
				decoded, err := base64.StdEncoding.DecodeString(v)
				if err != nil {
					return "", fmt.Errorf("failed to decode Secret.data[%s]: %w", key, err)
				}
				return string(decoded), nil
			}
		}
	}
	return "", fmt.Errorf("key %s not found", key)
}

func (r *Resolver) UpdateEmbeddedContent(docContent string, key string, newContent string) (string, error) {
	var node yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(docContent))
	if err := decoder.Decode(&node); err != nil {
		return "", err
	}

	found := false
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return "", fmt.Errorf("invalid yaml document")
	}
	root := node.Content[0]
	if root == nil || root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("invalid yaml root")
	}

	kind := findKind(root)

	// Normalize line endings to \n.
	normalized := strings.ReplaceAll(newContent, "\r\n", "\n")
	// Remove trailing spaces from each line to prevent yaml.v3 from forcing quotes.
	lines := strings.Split(normalized, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	normalized = strings.Join(lines, "\n")
	normalized = strings.TrimSuffix(normalized, "\n")

	updateInSection := func(section string, newVal string, style yaml.Style) bool {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value != section {
				continue
			}
			m := root.Content[i+1]
			if m == nil || m.Kind != yaml.MappingNode {
				return false
			}
			m.Style = 0
			for j := 0; j < len(m.Content); j += 2 {
				if m.Content[j].Value == key {
					valNode := m.Content[j+1]
					if valNode == nil {
						return false
					}
					valNode.Value = newVal
					valNode.Style = style
					return true
				}
			}
		}
		return false
	}

	if kind == "ConfigMap" {
		if updateInSection("data", normalized, yaml.LiteralStyle) {
			found = true
		} else if updateInSection("binaryData", normalized, 0) {
			found = true
		}
	} else if kind == "Secret" {
		// Prefer stringData when present.
		if updateInSection("stringData", normalized, yaml.LiteralStyle) {
			found = true
		} else {
			encoded := base64.StdEncoding.EncodeToString([]byte(normalized))
			if updateInSection("data", encoded, 0) {
				found = true
			}
		}
	}

	if !found {
		return "", fmt.Errorf("key %s not found", key)
	}

	log.Info().Str("key", key).Str("buf", fmt.Sprintf("%v", node)).Msg("Updated embedded content in ConfigMap")

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	defer encoder.Close()
	if err := encoder.Encode(&node); err != nil {
		return "", err
	}

	log.Info().Str("buf", buf.String()).Msg("Serialized updated ConfigMap content")

	return buf.String(), nil
}
