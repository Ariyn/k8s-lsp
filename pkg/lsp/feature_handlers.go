package lsp

import (
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func textDocumentDefinition(context *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received definition request")

	uri := params.TextDocument.URI
	log.Debug().Str("uri", uri).Msg("Looking up document content")
	state.setNotifyContext(context)
	content, ok, inMemory := getOrLoadDocument(uri)
	log.Debug().Bool("foundInMemory", inMemory).Msg("Document content lookup result")
	log.Debug().Bool("contentAvailable", content != "").Msg("Document content availability")

	if !ok || content == "" {
		return nil, nil
	}

	log.Debug().Str("uri", uri).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Resolving definition")
	log.Debug().Str("content", content).Msg("Document content for definition")

	state.startWorkspaceScan()

	resolve := func() ([]protocol.LocationLink, error) {
		return withYAMLStream(uri, content, func(stream *yamlstream.Stream) ([]protocol.LocationLink, error) {
			return state.Resolver.ResolveDefinitionStream(stream, uri, int(params.Position.Line), int(params.Position.Character))
		})
	}

	locs, err := resolve()
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve definition")
		return nil, nil
	}

	// If the workspace scan is still running, the store may not yet contain the target definition.
	// Wait briefly for scan completion and retry once.
	if len(locs) == 0 {
		if waitForWorkspaceScanOnce(defaultScanWaitTimeout) {
			if retryLocs, retryErr := resolve(); retryErr == nil && len(retryLocs) > 0 {
				locs = retryLocs
			}
		}
	}
	log.Debug().Int("locationsFound", len(locs)).Msg("Definition resolution completed")

	return locs, nil
}

func textDocumentReferences(context *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received references request")

	uri := params.TextDocument.URI
	state.setNotifyContext(context)
	content, ok, _ := getOrLoadDocument(uri)

	if !ok || content == "" {
		return nil, nil
	}

	state.startWorkspaceScan()

	resolve := func() ([]protocol.Location, error) {
		return withYAMLStream(uri, content, func(stream *yamlstream.Stream) ([]protocol.Location, error) {
			return state.Resolver.ResolveReferencesStream(stream, uri, int(params.Position.Line), int(params.Position.Character))
		})
	}

	locs, err := resolve()
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve references")
		return nil, nil
	}

	// If the workspace scan is still running, the store may not yet contain references.
	// Wait briefly for scan completion and retry once.
	if len(locs) == 0 {
		if waitForWorkspaceScanOnce(defaultScanWaitTimeout) {
			if retryLocs, retryErr := resolve(); retryErr == nil && len(retryLocs) > 0 {
				locs = retryLocs
			}
		}
	}

	return locs, nil
}

func textDocumentCompletion(context *glsp.Context, params *protocol.CompletionParams) (any, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received completion request")

	uri := params.TextDocument.URI
	state.setNotifyContext(context)
	content, ok, _ := getOrLoadDocument(uri)

	if !ok || content == "" {
		return nil, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for completion")
		return nil, nil
	}
	items, err := state.Resolver.CompletionStream(stream, int(params.Position.Line), int(params.Position.Character))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve completion")
		return nil, nil
	}

	return items, nil
}

func textDocumentHover(context *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received hover request")

	uri := params.TextDocument.URI
	state.setNotifyContext(context)
	content, ok, _ := getOrLoadDocument(uri)

	if !ok || content == "" {
		return nil, nil
	}

	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for hover")
		return nil, nil
	}
	hover, err := state.Resolver.ResolveHoverStream(stream, uri, int(params.Position.Line), int(params.Position.Character))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve hover")
		return nil, nil
	}

	return hover, nil
}
