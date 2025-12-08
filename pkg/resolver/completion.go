package resolver

import (
	"io"
	"strings"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func (r *Resolver) Completion(docContent string, line, col int) ([]protocol.CompletionItem, error) {
	decoder := yaml.NewDecoder(strings.NewReader(docContent))

	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			log.Error().Err(err).Msg("Failed to parse YAML for completion")
			return nil, err
		}

		// Find node at cursor
		targetNode, path := findNodeAt(&node, line+1, col+1)
		if targetNode != nil {
			log.Debug().Str("value", targetNode.Value).Strs("path", path).Msg("Found node at cursor (Completion)")

			kind := findKind(&node)

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
		}
	}
	return nil, nil
}
