package resolver

import (
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

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
	targetNode, _, path := findNodeAt(docNode, line1, col1)
	if targetNode == nil {
		return nil, nil
	}

	log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor (Completion)")

	kind := findKind(docNode)

	// Check configured references
	for _, refRule := range r.Config.References {
		if matchesKind(refRule.Match.Kinds, kind) && matchPath(path, refRule.Match.Path) {
			if refRule.Symbol == "k8s.resource.name" {
				targetKind := refRule.TargetKind
				log.Debug().Str("targetKind", targetKind).Msg("Found completion rule")

				resources := r.Store.ListByKind(targetKind)
				var items []protocol.CompletionItem
				for _, res := range resources {
					label := res.Name
					kind := protocol.CompletionItemKindReference
					detail := "Namespace: " + res.Namespace

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
