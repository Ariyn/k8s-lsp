package resolver

import (
	"fmt"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"
	"regexp"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

var yamlPlainSafeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func yamlQuoteIfNeeded(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "''"
	}
	// Keep plain scalars when safe to avoid noisy quoting.
	// YAML has many edge cases; we use a conservative allowlist here.
	if yamlPlainSafeRe.MatchString(s) {
		return s
	}
	// Single-quote escape: '' inside single quotes.
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func schemaValueCompletionItems(fieldSchema *schema.Node) []protocol.CompletionItem {
	if fieldSchema == nil {
		return nil
	}

	doc := any(nil)
	if strings.TrimSpace(fieldSchema.Description) != "" {
		doc = fieldSchema.Description
	}

	add := func(label string, kind protocol.CompletionItemKind, detail string, insertText *string, sortText *string) protocol.CompletionItem {
		ci := protocol.CompletionItem{Label: label, Kind: &kind}
		if strings.TrimSpace(detail) != "" {
			ci.Detail = &detail
		}
		if insertText != nil {
			ci.InsertText = insertText
		}
		if sortText != nil {
			ci.SortText = sortText
		}
		if doc != nil {
			ci.Documentation = doc
		}
		return ci
	}

	var items []protocol.CompletionItem

	// Default: offer first when present.
	if strings.TrimSpace(fieldSchema.Default) != "" {
		knd := protocol.CompletionItemKindValue
		insert := fieldSchema.Default
		if fieldSchema.Type == schema.TypeString {
			insert = yamlQuoteIfNeeded(insert)
		}
		detail := "Default"
		sortText := "0"
		items = append(items, add(fieldSchema.Default, knd, detail, &insert, &sortText))
	}

	// Enums
	if len(fieldSchema.Enum) > 0 {
		vals := append([]string(nil), fieldSchema.Enum...)
		sort.Strings(vals)
		for i, v := range vals {
			knd := protocol.CompletionItemKindEnumMember
			detail := "Enum value"
			insert := v
			if fieldSchema.Type == schema.TypeString {
				insert = yamlQuoteIfNeeded(v)
			}
			sortText := fmt.Sprintf("1-%03d", i)
			items = append(items, add(v, knd, detail, &insert, &sortText))
		}
		return items
	}

	// Primitive types
	switch fieldSchema.Type {
	case schema.TypeBoolean:
		knd := protocol.CompletionItemKindKeyword
		detail := "Boolean"
		st1, st2 := "1", "2"
		tv, fv := "true", "false"
		items = append(items, add("true", knd, detail, &tv, &st1))
		items = append(items, add("false", knd, detail, &fv, &st2))
		return items
	case schema.TypeNull:
		knd := protocol.CompletionItemKindKeyword
		detail := "Null"
		st := "1"
		nv := "null"
		items = append(items, add("null", knd, detail, &nv, &st))
		return items
	case schema.TypeInteger:
		knd := protocol.CompletionItemKindValue
		detail := "Integer"
		st1, st2 := "1", "2"
		z, o := "0", "1"
		items = append(items, add("0", knd, detail, &z, &st1))
		items = append(items, add("1", knd, detail, &o, &st2))
		return items
	case schema.TypeNumber:
		knd := protocol.CompletionItemKindValue
		detail := "Number"
		st1, st2, st3 := "1", "2", "3"
		z, h, o := "0", "0.5", "1"
		items = append(items, add("0", knd, detail, &z, &st1))
		items = append(items, add("0.5", knd, detail, &h, &st2))
		items = append(items, add("1", knd, detail, &o, &st3))
		return items
	case schema.TypeString:
		// No generic suggestions for free-form strings.
		return nil
	default:
		return nil
	}
}

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

	// Fallback: schema-based completion.
	if r != nil && r.Schemas != nil {
		if items := r.schemaCompletion(docNode, targetNode, parentNode, path); items != nil {
			return items, nil
		}
	}

	return nil, nil
}

func (r *Resolver) schemaCompletion(docNode *yaml.Node, targetNode *yaml.Node, parentNode *yaml.Node, path []string) []protocol.CompletionItem {
	if r == nil || r.Schemas == nil || docNode == nil || targetNode == nil {
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

	// Key completion: suggest properties for the parent mapping.
	if isMappingKey(parentNode, targetNode) {
		parentSchema := schema.ResolveParentPath(root, path)
		if parentSchema == nil {
			return nil
		}
		if parentSchema.Type == schema.TypeArray && parentSchema.Items != nil {
			parentSchema = parentSchema.Items
		}
		if parentSchema.Type != schema.TypeObject || len(parentSchema.Properties) == 0 {
			return nil
		}

		present := map[string]struct{}{}
		if parentNode != nil && parentNode.Kind == yaml.MappingNode {
			for i := 0; i < len(parentNode.Content); i += 2 {
				k := parentNode.Content[i]
				if k != nil && k.Value != "" {
					present[k.Value] = struct{}{}
				}
			}
		}

		keys := make([]string, 0, len(parentSchema.Properties))
		for k := range parentSchema.Properties {
			if _, ok := present[k]; ok {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)

		items := make([]protocol.CompletionItem, 0, len(keys))
		for _, k := range keys {
			knd := protocol.CompletionItemKindProperty
			items = append(items, protocol.CompletionItem{Label: k, Kind: &knd})
		}
		if len(items) == 0 {
			return nil
		}
		return items
	}

	// Value completion: suggest enum values.
	fieldSchema := schema.ResolvePath(root, path)
	if fieldSchema == nil {
		return nil
	}
	// Only offer value completions for scalars.
	if targetNode.Kind != yaml.ScalarNode {
		return nil
	}
	return schemaValueCompletionItems(fieldSchema)
}
