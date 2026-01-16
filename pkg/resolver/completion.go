package resolver

import (
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func normalizeNamespace(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func isMappingKey(parent *yaml.Node, node *yaml.Node) bool {
	if parent == nil || node == nil {
		return false
	}
	if parent.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i] == node {
			return true
		}
	}
	return false
}

func (r *Resolver) listLabelKeys(namespace string) []string {
	ns := normalizeNamespace(namespace)
	collect := func(filterNS string) map[string]struct{} {
		seen := map[string]struct{}{}
		for _, res := range r.Store.ListAll() {
			if filterNS != "" {
				resNS := normalizeNamespace(res.Namespace)
				if resNS != filterNS {
					continue
				}
			}
			for k := range res.Labels {
				if k == "" {
					continue
				}
				seen[k] = struct{}{}
			}
		}
		return seen
	}

	seen := collect(ns)
	// Fallback: if nothing exists in this namespace yet, suggest from all namespaces.
	if len(seen) == 0 {
		seen = collect("")
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Resolver) listLabelValues(namespace, key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}

	ns := normalizeNamespace(namespace)
	collect := func(filterNS string) map[string]struct{} {
		seen := map[string]struct{}{}
		for _, res := range r.Store.ListAll() {
			if filterNS != "" {
				resNS := normalizeNamespace(res.Namespace)
				if resNS != filterNS {
					continue
				}
			}
			if v, ok := res.Labels[key]; ok {
				if v == "" {
					continue
				}
				seen[v] = struct{}{}
			}
		}
		return seen
	}

	seen := collect(ns)
	// Fallback: if nothing exists in this namespace yet, suggest from all namespaces.
	if len(seen) == 0 {
		seen = collect("")
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (r *Resolver) Completion(docContent string, line, col int) ([]protocol.CompletionItem, error) {
	stream, err := yamlstream.Parse(docContent)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for completion")
		return nil, err
	}
	return r.CompletionStream(stream, line, col)
}

func (r *Resolver) CompletionStream(stream *yamlstream.Stream, line, col int) ([]protocol.CompletionItem, error) {
	if stream == nil {
		return nil, nil
	}

	line1 := line + 1
	col1 := col + 1

	if doc := stream.DocForLine(line1); doc != nil {
		if items, err := r.completionInDoc(doc.Node, line1, col1); items != nil || err != nil {
			return items, err
		}
	}

	// Fallback to preserve old behavior when span selection misses (e.g. cursor on a separator line).
	for _, doc := range stream.Docs {
		items, err := r.completionInDoc(doc.Node, line1, col1)
		if items != nil || err != nil {
			return items, err
		}
	}

	return nil, nil
}

func (r *Resolver) completionInDoc(docNode *yaml.Node, line1, col1 int) ([]protocol.CompletionItem, error) {
	if docNode == nil {
		return nil, nil
	}

	// Find node at cursor
	targetNode, parentNode, path := findNodeAt(docNode, line1, col1)
	if targetNode == nil {
		return nil, nil
	}

	log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor (Completion)")

	kind := findKind(docNode)

	// Check configured references
	for _, refRule := range r.Config.References {
		isMatch := false
		if refRule.Symbol == "k8s.label" {
			isMatch = matchPathPrefix(path, refRule.Match.Path)
		} else {
			isMatch = matchPath(path, refRule.Match.Path)
		}

		if matchesKind(refRule.Match.Kinds, kind) && isMatch {
			if refRule.Symbol == "k8s.label" {
				ns := normalizeNamespace(findNamespace(docNode))

				// We support:
				// - matchLabels / selector map values: selector.<labelKey>: <value>
				// - matchExpressions[].key: <labelKey>
				// - matchExpressions[].values[]: <labelValue>
				last := ""
				if len(path) > 0 {
					last = path[len(path)-1]
				}

				// If cursor is on a key inside a selector map, suggest label keys.
				if isMappingKey(parentNode, targetNode) {
					// Avoid suggesting label keys when editing container keys.
					if last == "" || last == "matchLabels" || last == "selector" || last == "matchExpressions" {
						return nil, nil
					}
					keys := r.listLabelKeys(ns)
					if len(keys) == 0 {
						return nil, nil
					}
					items := make([]protocol.CompletionItem, 0, len(keys))
					for _, k := range keys {
						knd := protocol.CompletionItemKindReference
						detail := "Label key"
						items = append(items, protocol.CompletionItem{Label: k, Kind: &knd, Detail: &detail})
					}
					return items, nil
				}

				// 1) matchExpressions: complete key names
				if last == "key" && pathContains(path, "matchExpressions") {
					keys := r.listLabelKeys(ns)
					if len(keys) == 0 {
						return nil, nil
					}
					items := make([]protocol.CompletionItem, 0, len(keys))
					for _, k := range keys {
						knd := protocol.CompletionItemKindReference
						detail := "Label key"
						items = append(items, protocol.CompletionItem{Label: k, Kind: &knd, Detail: &detail})
					}
					return items, nil
				}

				// 2) matchExpressions: complete values based on the sibling key
				if last == "values" && pathContains(path, "matchExpressions") {
					labelKey := findMatchExpressionKeyForValue(docNode, targetNode)
					vals := r.listLabelValues(ns, labelKey)
					if len(vals) == 0 {
						return nil, nil
					}
					items := make([]protocol.CompletionItem, 0, len(vals))
					for _, v := range vals {
						knd := protocol.CompletionItemKindReference
						detail := "Label value"
						items = append(items, protocol.CompletionItem{Label: v, Kind: &knd, Detail: &detail})
					}
					return items, nil
				}

				// 3) selector/matchLabels style: complete values for the map key
				// Here, `path` typically ends with the label key (e.g. spec.selector.app).
				labelKey := last
				// Avoid completing on container keys like 'matchLabels' itself.
				if labelKey == "" || labelKey == "matchLabels" || labelKey == "selector" {
					return nil, nil
				}
				vals := r.listLabelValues(ns, labelKey)
				if len(vals) == 0 {
					return nil, nil
				}
				items := make([]protocol.CompletionItem, 0, len(vals))
				for _, v := range vals {
					knd := protocol.CompletionItemKindReference
					detail := "Label value"
					items = append(items, protocol.CompletionItem{Label: v, Kind: &knd, Detail: &detail})
				}
				return items, nil
			}

			if refRule.Symbol == "k8s.resource.name" {
				targetKind := refRule.TargetKind
				log.Debug().Str("targetKind", targetKind).Msg("Found completion rule")

				resources := r.Store.ListByKind(targetKind)
				var items []protocol.CompletionItem
				for _, res := range resources {
					label := res.Name
					kind := protocol.CompletionItemKindReference
					detail := indexer.FormatResourceID(targetKind, res.Namespace, res.Name)

					items = append(items, protocol.CompletionItem{
						Label:  label,
						Kind:   &kind,
						Detail: &detail,
					})
				}
				return items, nil
			}
		}
	}

	return nil, nil
}
