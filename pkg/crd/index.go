package crd

import (
	"fmt"
	"os"

	"k8s-lsp/pkg/indexer"

	"github.com/rs/zerolog/log"
)

// DownloadAndIndex downloads CRD YAMLs from URLs into a persistent cache directory and
// indexes them so dynamic kinds become available before scanning the workspace.
func DownloadAndIndex(idx *indexer.Indexer, sources []string) {
	if idx == nil || len(sources) == 0 {
		return
	}

	opts := DefaultOptions()
	paths, err := DownloadAll(sources, opts)
	if err != nil {
		log.Warn().Err(err).Msg("One or more CRD downloads failed")
	}

	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Warn().Err(err).Str("path", p).Msg("Failed to read cached CRD")
			continue
		}

		if ok := idx.IndexContent(p, string(b)); !ok {
			log.Debug().Str("path", p).Msg("CRD file produced no indexed resources")
		} else {
			log.Info().Str("path", p).Msg("Indexed CRD")
		}
	}

	if len(paths) > 0 {
		log.Info().Msg(fmt.Sprintf("CRD preload complete (%d source(s))", len(paths)))
	}
}
