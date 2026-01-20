package resolver

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func filePathToURI(path string) string {
	if path == "" {
		return ""
	}
	// Already a URI (file://, k8s-embedded://, etc).
	if strings.Contains(path, "://") {
		return path
	}

	normalized := filepath.ToSlash(path)

	// Windows drive letter paths need a leading slash: /C:/Users/...
	if len(normalized) >= 3 {
		drive := normalized[0]
		if ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) && normalized[1] == ':' && normalized[2] == '/' {
			normalized = "/" + normalized
		}
	}

	u := url.URL{Scheme: "file", Path: normalized}
	return u.String()
}

type Resolver struct {
	Store   *indexer.Store
	Config  *config.Config
	Schemas *schema.Registry
}

func NewResolver(store *indexer.Store, cfg *config.Config, schemas ...*schema.Registry) *Resolver {
	var s *schema.Registry
	if len(schemas) > 0 {
		s = schemas[0]
	}
	return &Resolver{Store: store, Config: cfg, Schemas: s}
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
							display := indexer.FormatResourceID(res.Kind, res.Namespace, res.Name)
							contents := fmt.Sprintf("**%s**\n\nKind: %s\nNamespace: %s\nFile: %s",
								display, res.Kind, res.Namespace, res.FilePath)

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

func (r *Resolver) ResolveHoverStream(stream *yamlstream.Stream, uri string, line, col int) (*protocol.Hover, error) {
	if stream == nil {
		return nil, nil
	}

	line1 := line + 1
	col1 := col + 1

	// Fast path: choose doc by span.
	if doc := stream.DocForLine(line1); doc != nil {
		h, err := r.resolveHoverInDoc(doc.Node, uri, line, col, line1, col1)
		if h != nil || err != nil {
			return h, err
		}
	}

	// Fallback to preserve behavior when spans can't map separator lines cleanly.
	for _, doc := range stream.Docs {
		h, err := r.resolveHoverInDoc(doc.Node, uri, line, col, line1, col1)
		if h != nil || err != nil {
			return h, err
		}
	}

	return nil, nil
}

func (r *Resolver) resolveHoverInDoc(docNode *yaml.Node, uri string, line0, col0, line1, col1 int) (*protocol.Hover, error) {
	if docNode == nil {
		return nil, nil
	}

	targetNode, parentNode, path := findNodeAt(docNode, line1, col1)
	if targetNode == nil {
		return nil, nil
	}

	kind := findKind(docNode)

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
				currentNamespace := findNamespace(docNode)
				if currentNamespace == "" {
					currentNamespace = "default"
				}
				configMapName := findName(docNode)
				if configMapName == "" {
					configMapName = "configmap"
				}

				sourceEncoded := base64.URLEncoding.EncodeToString([]byte(uri))
				keyEncoded := base64.URLEncoding.EncodeToString([]byte(targetNode.Value))

				embeddedURI := fmt.Sprintf("k8s-embedded://%s/%s/%s?source=%s&key=%s",
					currentNamespace, configMapName, targetNode.Value, sourceEncoded, keyEncoded)

				openArgs := fmt.Sprintf(`{"uri":%q}`, embeddedURI)
				openLink := "command:k8sLsp.openEmbeddedFile?" + url.QueryEscape(openArgs)

				findArgs := fmt.Sprintf(`{"uri":%q,"position":{"line":%d,"character":%d}}`, uri, line0, col0)
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

	currentNamespace := findNamespace(docNode)

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
					res = r.Store.Get(targetKind, "default", targetNode.Value)
				}
				if res != nil {
					display := indexer.FormatResourceID(res.Kind, res.Namespace, res.Name)
					contents := fmt.Sprintf("**%s**\n\nKind: %s\nNamespace: %s\nFile: %s",
						display, res.Kind, res.Namespace, res.FilePath)

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

	// Fallback: schema-based hover (description/default/enum).
	if h := r.schemaHover(docNode, targetNode, path); h != nil {
		return h, nil
	}

	return nil, nil
}

func (r *Resolver) ResolveDefinitionStream(stream *yamlstream.Stream, uri string, line, col int) ([]protocol.LocationLink, error) {
	if stream == nil {
		return nil, nil
	}
	line1 := line + 1
	col1 := col + 1

	if doc := stream.DocForLine(line1); doc != nil {
		locs, err := r.resolveDefinitionInDoc(doc.Node, uri, line, col, line1, col1)
		if len(locs) > 0 || err != nil {
			return locs, err
		}
	}
	for _, doc := range stream.Docs {
		locs, err := r.resolveDefinitionInDoc(doc.Node, uri, line, col, line1, col1)
		if len(locs) > 0 || err != nil {
			return locs, err
		}
	}
	return nil, nil
}

func (r *Resolver) resolveDefinitionInDoc(docNode *yaml.Node, uri string, line0, col0, line1, col1 int) ([]protocol.LocationLink, error) {
	if docNode == nil {
		return nil, nil
	}

	targetNode, parentNode, path := findNodeAt(docNode, line1, col1)
	if targetNode == nil {
		return nil, nil
	}

	originRange := calculateOriginRange(targetNode)

	if isVolumeMountNamePath(path) {
		podSpec := findPodSpecNode(docNode)
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

	kind := findKind(docNode)
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
				currentNamespace := findNamespace(docNode)
				if currentNamespace == "" {
					currentNamespace = "default"
				}
				configMapName := findName(docNode)
				if configMapName == "" {
					configMapName = "configmap"
				}

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

	currentNamespace := findNamespace(docNode)

	// Check if we are at a definition site (Symbol)
	for _, sym := range r.Config.Symbols {
		for _, def := range sym.Definitions {
			if contains(def.Kinds, kind) && matchPath(path, def.Path) {
				// We are at the definition. Return self.
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
				ns := currentNamespace
				if ns == "" {
					ns = "default"
				}

				labelKey := path[len(path)-1]
				labelValue := targetNode.Value
				if labelKey == "values" && pathContains(path, "matchExpressions") {
					if k := findMatchExpressionKeyForValue(docNode, targetNode); k != "" {
						labelKey = k
					}
				}

				return r.findWorkloadsByLabel(ns, labelKey, labelValue, originRange), nil
			}
			if refRule.Symbol == "k8s.resource.name" {
				targetKind := refRule.TargetKind
				if targetKind == "" {
					continue
				}

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
					res = r.Store.Get(targetKind, "default", targetNode.Value)
				}
				if res != nil {
					targetRange := protocol.Range{
						Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
						End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
					}
					return []protocol.LocationLink{{
						OriginSelectionRange: &originRange,
						TargetURI:            filePathToURI(res.FilePath),
						TargetRange:          targetRange,
						TargetSelectionRange: targetRange,
					}}, nil
				}
			}
		}
	}

	return nil, nil
}

func (r *Resolver) ResolveReferencesStream(stream *yamlstream.Stream, uri string, line, col int) ([]protocol.Location, error) {
	if stream == nil {
		return nil, nil
	}

	line1 := line + 1

	if doc := stream.DocForLine(line1); doc != nil {
		locs, ok, err := r.resolveReferencesInDoc(doc.Node, uri, line, col)
		if ok || err != nil {
			return locs, err
		}
	}

	for _, doc := range stream.Docs {
		locs, ok, err := r.resolveReferencesInDoc(doc.Node, uri, line, col)
		if ok || err != nil {
			return locs, err
		}
	}

	return nil, nil
}

func (r *Resolver) resolveReferencesInDoc(docNode *yaml.Node, uri string, line, col int) ([]protocol.Location, bool, error) {
	if docNode == nil {
		return nil, false, nil
	}

	line1 := line + 1
	col1 := col + 1

	targetNode, parentNode, path := findNodeAt(docNode, line1, col1)
	if targetNode == nil {
		return nil, false, nil
	}

	// Special case: clicking volumeMounts[].subPath should open the References UI
	// with multiple targets so the user can choose.
	if isVolumeMountSubPathPath(path) {
		locs := r.findVolumeMountSubPathTargets(docNode, parentNode, targetNode.Value)
		if len(locs) > 0 {
			return locs, true, nil
		}
	}

	// Special case: ConfigMap embedded file usages.
	kind := findKind(docNode)
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
			ns := findNamespace(docNode)
			if ns == "" {
				ns = "default"
			}
			cmName := findName(docNode)
			if cmName == "" {
				cmName = "configmap"
			}

			locs := r.findConfigMapEmbeddedFileUsages(ns, cmName, targetNode.Value)
			return filterOutLocationAtPosition(locs, uri, line, col), true, nil
		}
	}

	// Special case: PVC claimName -> volumeMount name usages (document-local).
	if isWorkloadPVCClaimNamePath(path) {
		locs := findPVCClaimMountUsagesInDocument(docNode, uri, targetNode.Value)
		if len(locs) > 0 {
			return filterOutLocationAtPosition(locs, uri, line, col), true, nil
		}
	}

	// metadata.name definition site: find references for the resource.
	if len(path) == 2 && path[0] == "metadata" && path[1] == "name" {
		kind := findKind(docNode)
		name := findName(docNode)
		namespace := findNamespace(docNode)
		if kind != "" && name != "" {
			locs := r.findReferences(kind, name, namespace)
			return filterOutLocationAtPosition(locs, uri, line, col), true, nil
		}
	}

	// metadata.namespace: namespace references.
	if len(path) == 2 && path[0] == "metadata" && path[1] == "namespace" {
		namespaceName := targetNode.Value
		locs := r.findReferences("Namespace", namespaceName, "")
		return filterOutLocationAtPosition(locs, uri, line, col), true, nil
	}

	// Document-local references: volumes[].name <-> containers[].volumeMounts[].name
	kind = findKind(docNode)
	if volPatterns, ok := volumeNamePatternsForKind(kind); ok {
		if matchesAnyPath(path, volPatterns) {
			name := targetNode.Value
			if name != "" {
				return findDocumentLocalScalarRefs(docNode, uri, name, volPatterns), true, nil
			}
		}
	}

	ns := findNamespace(docNode)
	if ns == "" {
		ns = "default"
	}

	// Definition sites for labels.
	for _, sym := range r.Config.Symbols {
		for _, def := range sym.Definitions {
			match := matchPath(path, def.Path)
			if !match && sym.Name == "k8s.label" {
				match = matchPathPrefix(path, def.Path)
			}

			if contains(def.Kinds, kind) && match {
				if sym.Name == "k8s.label" {
					labelKey := path[len(path)-1]
					labelValue := targetNode.Value
					locs := r.findLabelReferences(ns, labelKey, labelValue)
					return filterOutLocationAtPosition(locs, uri, line, col), true, nil
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
				targetNamespace := ""
				if targetKind != "Namespace" {
					targetNamespace = findNamespace(docNode)
				}

				locs := r.findReferences(targetKind, targetName, targetNamespace)
				return filterOutLocationAtPosition(locs, uri, line, col), true, nil
			}
			if refRule.Symbol == "k8s.label" {
				labelKey := path[len(path)-1]
				labelValue := targetNode.Value
				locs := r.findLabelReferences(ns, labelKey, labelValue)
				return filterOutLocationAtPosition(locs, uri, line, col), true, nil
			}
		}
	}

	return nil, true, nil
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
						ns := currentNamespace
						if ns == "" {
							ns = "default"
						}

						labelKey := path[len(path)-1]
						labelValue := targetNode.Value
						// matchExpressions values[] are scalars under the "values" key; the label key is a sibling field "key".
						if labelKey == "values" && pathContains(path, "matchExpressions") {
							if k := findMatchExpressionKeyForValue(&node, targetNode); k != "" {
								labelKey = k
							}
						}

						return r.findWorkloadsByLabel(ns, labelKey, labelValue, originRange), nil
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
									TargetURI:            filePathToURI(res.FilePath),
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

func pathContains(path []string, key string) bool {
	for _, p := range path {
		if p == key {
			return true
		}
	}
	return false
}

// findMatchExpressionKeyForValue finds the matchExpressions[].key corresponding to the given values[] scalar node.
func findMatchExpressionKeyForValue(root *yaml.Node, valueNode *yaml.Node) string {
	if root == nil || valueNode == nil {
		return ""
	}

	var walk func(n *yaml.Node) string
	walk = func(n *yaml.Node) string {
		if n == nil {
			return ""
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				if k := walk(c); k != "" {
					return k
				}
			}
		case yaml.MappingNode:
			// If this mapping has a "values" sequence containing the node, extract the sibling "key".
			if values := getMappingValue(n, "values"); values != nil && values.Kind == yaml.SequenceNode {
				for _, item := range values.Content {
					if item == valueNode {
						if keyNode := getMappingScalarValue(n, "key"); keyNode != nil {
							return keyNode.Value
						}
					}
				}
			}
			for i := 1; i < len(n.Content); i += 2 {
				if k := walk(n.Content[i]); k != "" {
					return k
				}
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				if k := walk(c); k != "" {
					return k
				}
			}
		}
		return ""
	}

	return walk(root)
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

			// Document-local references: volumes[].name <-> containers[].volumeMounts[].name
			// This is an intra-resource symbol (not a K8s resource), so it isn't in the global store.
			if volPatterns, ok := volumeNamePatternsForKind(kind); ok {
				if matchesAnyPath(path, volPatterns) {
					name := targetNode.Value
					if name != "" {
						return findDocumentLocalScalarRefs(&node, uri, name, volPatterns), nil
					}
				}
			}

			ns := findNamespace(&node)
			if ns == "" {
				ns = "default"
			}

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
							locs := r.findLabelReferences(ns, labelKey, labelValue)
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
						locs := r.findLabelReferences(ns, labelKey, labelValue)
						return filterOutLocationAtPosition(locs, uri, line, col), nil
					}
				}
			}
		}
	}

	return nil, nil
}

func (r *Resolver) schemaHover(docNode *yaml.Node, targetNode *yaml.Node, path []string) *protocol.Hover {
	if r == nil || r.Schemas == nil || docNode == nil || targetNode == nil || len(path) == 0 {
		return nil
	}
	apiVersion := findAPIVersion(docNode)
	kind := findKind(docNode)
	if apiVersion == "" || kind == "" {
		return nil
	}
	group, version := schema.ParseAPIVersion(apiVersion)
	gvk := schema.GVK{Group: group, Version: version, Kind: kind}
	root := r.Schemas.Get(gvk)
	if root == nil {
		root = schema.KubernetesObjectFallback()
	}
	if root == nil {
		return nil
	}

	s := schema.ResolvePath(root, path)
	if s == nil {
		return nil
	}
	name := path[len(path)-1]

	md := "**" + name + "**"
	if s.Type != "" && s.Type != schema.TypeAny {
		md += "\n\nType: `" + string(s.Type) + "`"
	}
	if s.Default != "" {
		md += "\n\nDefault: `" + s.Default + "`"
	}
	if len(s.Enum) > 0 {
		md += "\n\nEnum: `" + joinInline(s.Enum) + "`"
	}
	if s.Description != "" {
		md += "\n\n" + s.Description
	}

	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: md}}
}

func joinInline(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += ", " + items[i]
	}
	return out
}

func findAPIVersion(root *yaml.Node) string {
	if root == nil {
		return ""
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "apiVersion" {
				return root.Content[i+1].Value
			}
		}
	}
	return ""
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
			URI:   "file://" + res.FilePath,
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

func scalarRange(node *yaml.Node) protocol.Range {
	startCol := node.Column - 1
	length := len(node.Value)
	if node.Style == yaml.DoubleQuotedStyle || node.Style == yaml.SingleQuotedStyle {
		length += 2
	}
	return protocol.Range{
		Start: protocol.Position{Line: uint32(node.Line - 1), Character: uint32(startCol)},
		End:   protocol.Position{Line: uint32(node.Line - 1), Character: uint32(startCol + length)},
	}
}

func matchesAnyPath(path []string, patterns []string) bool {
	for _, p := range patterns {
		if matchPath(path, p) {
			return true
		}
	}
	return false
}

func volumeNamePatternsForKind(kind string) ([]string, bool) {
	// Workload kinds with PodTemplate
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		return []string{
			"spec.template.spec.volumes[].name",
			"spec.template.spec.containers[].volumeMounts[].name",
			"spec.template.spec.initContainers[].volumeMounts[].name",
		}, true
	case "Pod":
		return []string{
			"spec.volumes[].name",
			"spec.containers[].volumeMounts[].name",
			"spec.initContainers[].volumeMounts[].name",
		}, true
	default:
		return nil, false
	}
}

func findDocumentLocalScalarRefs(root *yaml.Node, uri string, value string, patterns []string) []protocol.Location {
	var locs []protocol.Location

	var walk func(n *yaml.Node, path []string)
	walk = func(n *yaml.Node, path []string) {
		switch n.Kind {
		case yaml.DocumentNode:
			if len(n.Content) > 0 {
				walk(n.Content[0], path)
			}
		case yaml.MappingNode:
			for i := 0; i < len(n.Content); i += 2 {
				keyNode := n.Content[i]
				valNode := n.Content[i+1]
				nextPath := append(path, keyNode.Value)

				if valNode.Kind == yaml.ScalarNode {
					if valNode.Value == value && matchesAnyPath(nextPath, patterns) {
						locs = append(locs, protocol.Location{URI: uri, Range: scalarRange(valNode)})
					}
					continue
				}
				walk(valNode, nextPath)
			}
		case yaml.SequenceNode:
			for _, item := range n.Content {
				walk(item, path)
			}
		}
	}

	walk(root, []string{})
	return locs
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
			URI: filePathToURI(def.FilePath),
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
					URI: filePathToURI(res.FilePath),
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

func (r *Resolver) findWorkloadsByLabel(namespace, key, value string, originRange protocol.Range) []protocol.LocationLink {
	var links []protocol.LocationLink
	if namespace == "" {
		namespace = "default"
	}
	resources := r.Store.FindByLabel(key, value)
	for _, res := range resources {
		resNS := res.Namespace
		if resNS == "" {
			resNS = "default"
		}
		if resNS != namespace {
			continue
		}

		// Prefer pointing to the exact label value node in the target file.
		targetRange := protocol.Range{
			Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
			End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
		}
		for _, ld := range res.LabelDefs {
			if ld.Key == key && ld.Value == value {
				targetRange = protocol.Range{
					Start: protocol.Position{Line: uint32(ld.Line), Character: uint32(ld.Col)},
					End:   protocol.Position{Line: uint32(ld.Line), Character: uint32(ld.Col + len(ld.Value))},
				}
				break
			}
		}
		if labelNode, err := findResourceLabelValueInFile(res.FilePath, res.Kind, resNS, res.Name, key, value); err == nil && labelNode != nil {
			targetRange = scalarRange(labelNode)
		}
		links = append(links, protocol.LocationLink{
			OriginSelectionRange: &originRange,
			TargetURI:            filePathToURI(res.FilePath),
			TargetRange:          targetRange,
			TargetSelectionRange: targetRange,
		})
	}
	return links
}

func findResourceLabelValueInFile(filePath, kind, namespace, name, labelKey, labelValue string) (*yaml.Node, error) {
	if filePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}

		if findKind(root) != kind {
			continue
		}
		if findName(root) != name {
			continue
		}
		ns := findNamespace(root)
		if ns == "" {
			ns = "default"
		}
		if namespace != "" && ns != namespace {
			continue
		}

		// metadata.labels
		if meta := getMappingValue(root, "metadata"); meta != nil {
			if labels := getMappingValue(meta, "labels"); labels != nil && labels.Kind == yaml.MappingNode {
				for i := 0; i < len(labels.Content); i += 2 {
					k := labels.Content[i]
					v := labels.Content[i+1]
					if k != nil && v != nil && k.Value == labelKey && v.Kind == yaml.ScalarNode && v.Value == labelValue {
						return v, nil
					}
				}
			}
		}

		// spec.template.metadata.labels
		if spec := getMappingValue(root, "spec"); spec != nil {
			if tmpl := getMappingValue(spec, "template"); tmpl != nil {
				if meta := getMappingValue(tmpl, "metadata"); meta != nil {
					if labels := getMappingValue(meta, "labels"); labels != nil && labels.Kind == yaml.MappingNode {
						for i := 0; i < len(labels.Content); i += 2 {
							k := labels.Content[i]
							v := labels.Content[i+1]
							if k != nil && v != nil && k.Value == labelKey && v.Kind == yaml.ScalarNode && v.Value == labelValue {
								return v, nil
							}
						}
					}
				}
			}
		}
	}

	return nil, nil
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
			TargetURI:            filePathToURI(res.FilePath),
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
			TargetURI:            filePathToURI(res.FilePath),
			TargetRange:          targetRange,
			TargetSelectionRange: targetRange,
		}}
	}
	return nil
}

// Helper functions for path checking

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
		cleanPart := cleanPathPart(part)
		if cleanPart != current[i] {
			return false
		}
	}
	return true
}

func cleanPathPart(part string) string {
	// Support both config-style array notation ("[]") and validation.yaml notation ("[*]").
	part = strings.TrimSuffix(part, "[]")
	part = strings.TrimSuffix(part, "[*]")
	return part
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
		cleanPart := cleanPathPart(part)
		if cleanPart != current[i] {
			return false
		}
	}
	return true
}

func (r *Resolver) findLabelReferences(namespace, key, value string) []protocol.Location {
	var locations []protocol.Location
	if namespace == "" {
		namespace = "default"
	}

	// 1. Find definitions (resources having this label)
	resources := r.Store.FindByLabel(key, value)
	for _, res := range resources {
		resNS := res.Namespace
		if resNS == "" {
			resNS = "default"
		}
		if resNS != namespace {
			continue
		}
		locations = append(locations, protocol.Location{
			URI: filePathToURI(res.FilePath),
			Range: protocol.Range{
				Start: protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col)},
				End:   protocol.Position{Line: uint32(res.Line), Character: uint32(res.Col + len(res.Name))},
			},
		})
	}

	// 2. Find usages (resources referencing this label)
	refs := r.Store.FindLabelReferencesByKeyValue(key, value)
	for _, res := range refs {
		resNS := res.Namespace
		if resNS == "" {
			resNS = "default"
		}
		if resNS != namespace {
			continue
		}
		for _, ref := range res.References {
			if ref.Symbol == "k8s.label" && ref.Name == value && (ref.Key == "" || ref.Key == key) {
				locations = append(locations, protocol.Location{
					URI: filePathToURI(res.FilePath),
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

func (r *Resolver) ResolveEmbeddedContent(docContent string, key string, nameAndNamespace ...string) (string, error) {
	resourceName := ""
	namespace := ""
	if len(nameAndNamespace) >= 1 {
		resourceName = nameAndNamespace[0]
	}
	if len(nameAndNamespace) >= 2 {
		namespace = nameAndNamespace[1]
	}

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
		if resourceName != "" && findName(root) != resourceName {
			continue
		}
		if namespace != "" {
			ns := findNamespace(root)
			if ns == "" {
				ns = "default"
			}
			if ns != namespace {
				continue
			}
		}
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
	stream, err := yamlstream.Parse(docContent)
	if err != nil {
		return "", err
	}
	return r.UpdateEmbeddedContentStream(stream, key, newContent)
}

func (r *Resolver) UpdateEmbeddedContentStream(stream *yamlstream.Stream, key string, newContent string) (string, error) {
	if stream == nil || len(stream.Docs) == 0 {
		return "", fmt.Errorf("invalid yaml document")
	}

	// Normalize line endings to \n.
	normalized := strings.ReplaceAll(newContent, "\r\n", "\n")
	// Remove trailing spaces from each line to prevent yaml.v3 from forcing quotes.
	lines := strings.Split(normalized, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	normalized = strings.Join(lines, "\n")
	normalized = strings.TrimSuffix(normalized, "\n")

	updateInDoc := func(docNode *yaml.Node) bool {
		if docNode == nil || docNode.Kind != yaml.DocumentNode || len(docNode.Content) == 0 {
			return false
		}
		root := docNode.Content[0]
		if root == nil || root.Kind != yaml.MappingNode {
			return false
		}

		kind := findKind(root)
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
				return true
			}
			if updateInSection("binaryData", normalized, 0) {
				return true
			}
			return false
		}
		if kind == "Secret" {
			if updateInSection("stringData", normalized, yaml.LiteralStyle) {
				return true
			}
			encoded := base64.StdEncoding.EncodeToString([]byte(normalized))
			if updateInSection("data", encoded, 0) {
				return true
			}
			return false
		}
		return false
	}

	updated := false
	for i := range stream.Docs {
		if updateInDoc(stream.Docs[i].Node) {
			updated = true
			break
		}
	}
	if !updated {
		return "", fmt.Errorf("key %s not found", key)
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	defer encoder.Close()
	for _, doc := range stream.Docs {
		if doc.Node == nil {
			continue
		}
		if err := encoder.Encode(doc.Node); err != nil {
			return "", err
		}
	}

	return buf.String(), nil
}

func countLeadingSpaces(s string) int {
	count := 0
	for count < len(s) && s[count] == ' ' {
		count++
	}
	return count
}

// BuildEmbeddedContentTextEdit returns a minimal edit that replaces only the YAML scalar value
// for the given key under ConfigMap data/binaryData. This avoids re-serializing the whole YAML
// document (which can change the style of other block scalars).
func (r *Resolver) BuildEmbeddedContentTextEdit(docContent string, key string, newContent string, configMapName string, namespace string) (*protocol.TextEdit, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	var valueNode *yaml.Node
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if configMapName != "" || namespace != "" {
			if findKind(&node) != "ConfigMap" {
				continue
			}
			if namespace != "" {
				ns := findNamespace(&node)
				if ns == "" {
					ns = "default"
				}
				if ns != namespace {
					continue
				}
			}
			if configMapName != "" {
				name := findName(&node)
				if name != configMapName {
					continue
				}
			}
		}

		matchCount := 0

		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			root := node.Content[0]
			for i := 0; i < len(root.Content); i += 2 {
				if root.Content[i].Value == "data" || root.Content[i].Value == "binaryData" {
					dataNode := root.Content[i+1]
					if dataNode.Kind == yaml.MappingNode {
						for j := 0; j < len(dataNode.Content); j += 2 {
							if dataNode.Content[j].Value == key {
								matchCount++
								if valueNode == nil {
									valueNode = dataNode.Content[j+1]
								}
							}
						}
						if matchCount > 1 {
							return nil, fmt.Errorf("duplicate key %s found in ConfigMap data", key)
						}
					}
				}
				if valueNode != nil {
					break
				}
			}
		}
		if valueNode != nil {
			break
		}
	}

	if valueNode == nil {
		return nil, fmt.Errorf("key %s not found in ConfigMap data", key)
	}
	if valueNode.Line <= 0 || valueNode.Column <= 0 {
		return nil, fmt.Errorf("unable to locate YAML scalar position for key %s", key)
	}

	// Convert YAML (1-based) to LSP (0-based)
	valueLineIdx := valueNode.Line - 1
	valueCharIdx := valueNode.Column - 1

	lines := strings.Split(docContent, "\n")
	if valueLineIdx < 0 || valueLineIdx >= len(lines) {
		return nil, fmt.Errorf("invalid YAML position for key %s", key)
	}
	lineText := lines[valueLineIdx]
	if valueCharIdx < 0 || valueCharIdx > len(lineText) {
		return nil, fmt.Errorf("invalid YAML column for key %s", key)
	}

	baseIndent := countLeadingSpaces(lineText)

	// Determine how far the current scalar extends in the source.
	// For block scalars, consume indented content lines; for inline scalars, consume to EOL.
	suffix := strings.TrimLeft(lineText[valueCharIdx:], " ")
	isBlock := strings.HasPrefix(suffix, "|") || strings.HasPrefix(suffix, ">")

	stopLine := valueLineIdx + 1
	if isBlock {
		for stopLine < len(lines) {
			nextLine := lines[stopLine]
			trimmed := strings.TrimSpace(nextLine)
			if trimmed == "" {
				// Treat truly empty lines as terminators unless they are indented beyond baseIndent.
				if countLeadingSpaces(nextLine) > baseIndent {
					stopLine++
					continue
				}
				break
			}
			if countLeadingSpaces(nextLine) <= baseIndent {
				break
			}
			stopLine++
		}
	} else {
		// Inline scalar ends at line break.
		stopLine = valueLineIdx
	}

	// Preserve the existing block indicator token if present (e.g. |-, |+, >-).
	indicator := "|"
	if isBlock {
		indicatorLine := strings.TrimSpace(lineText[valueCharIdx:])
		if fields := strings.Fields(indicatorLine); len(fields) > 0 {
			if strings.HasPrefix(fields[0], "|") || strings.HasPrefix(fields[0], ">") {
				indicator = fields[0]
			}
		}
	} else {
		// For multiline content, prefer a literal block scalar (avoids quoted \n strings).
		if strings.Contains(newContent, "\n") {
			indicator = "|-"
		}
	}

	// Normalize input similarly to UpdateEmbeddedContent
	normalized := strings.ReplaceAll(newContent, "\r\n", "\n")
	contentLines := strings.Split(normalized, "\n")
	for i, line := range contentLines {
		contentLines[i] = strings.TrimRight(line, " \t")
	}
	// Drop a single trailing empty line produced by editor newline-at-EOF
	if len(contentLines) > 0 && contentLines[len(contentLines)-1] == "" {
		contentLines = contentLines[:len(contentLines)-1]
	}

	contentIndent := baseIndent + 2
	// If the existing block scalar uses a different content indent, keep it.
	if isBlock {
		for i := valueLineIdx + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			indent := countLeadingSpaces(lines[i])
			if indent > baseIndent {
				contentIndent = indent
			}
			break
		}
	}

	indentPrefix := strings.Repeat(" ", contentIndent)
	var replacement strings.Builder
	replacement.WriteString(indicator)
	replacement.WriteString("\n")
	for i, line := range contentLines {
		replacement.WriteString(indentPrefix)
		replacement.WriteString(line)
		if i < len(contentLines)-1 {
			replacement.WriteString("\n")
		}
	}
	// Ensure the replacement ends with a newline when we replace up to the next line boundary.
	if isBlock && (len(contentLines) > 0 || indicator != "") {
		replacement.WriteString("\n")
	}

	edit := &protocol.TextEdit{
		Range: protocol.Range{
			Start: protocol.Position{Line: uint32(valueLineIdx), Character: uint32(valueCharIdx)},
			End:   protocol.Position{Line: uint32(stopLine), Character: 0},
		},
		NewText: replacement.String(),
	}
	return edit, nil
}
