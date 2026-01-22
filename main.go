package main

import (
	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/validator"
	"k8s-lsp/pkg/yamlstream"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"
)

func main() {
	setupLogging()

	configPath := configPathFromExecutable()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error().Err(err).Str("path", configPath).Msg("Failed to load config")
		if cfg == nil {
			cfg = &config.Config{}
		}
	}
	log.Info().Int("symbols", len(cfg.Symbols)).Int("references", len(cfg.References)).Msg("Loaded configuration")

	store := indexer.NewStore()
	idx := indexer.NewIndexer(store, cfg)

	schemas := schema.NewRegistry()
	schema.RegisterBuiltins(schemas)
	loadLocalSchemaPacks(schemas, configPath)

	res := resolver.NewResolver(store, cfg, schemas)

	val, err := validator.NewValidator(filepath.Join(configPath, "rules/validation.yaml"), store)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load validation rules")
	}

	state = &ServerState{
		Store:      store,
		Indexer:    idx,
		Resolver:   res,
		Validator:  val,
		Schemas:    schemas,
		Documents:  make(map[string]string),
		DocVersion: make(map[string]int32),
		YAMLCache:  yamlstream.NewCache(),
		ScanDone:   make(chan struct{}),
		// Defaults; may be overridden by client initializationOptions.
		DiagnosticsDebounce: 200 * time.Millisecond,
		IndexDebounce:       250 * time.Millisecond,
	}

	handler := protocol.Handler{
		Initialize:                     initialize,
		Initialized:                    initialized,
		Shutdown:                       shutdown,
		SetTrace:                       setTrace,
		TextDocumentDidOpen:            textDocumentDidOpen,
		TextDocumentDidChange:          textDocumentDidChange,
		TextDocumentDocumentSymbol:     textDocumentDocumentSymbol,
		TextDocumentDefinition:         textDocumentDefinition,
		TextDocumentReferences:         textDocumentReferences,
		TextDocumentCompletion:         textDocumentCompletion,
		TextDocumentHover:              textDocumentHover,
		TextDocumentCodeAction:         textDocumentCodeAction,
		TextDocumentRename:             textDocumentRename,
		TextDocumentDidSave:            textDocumentDidSave,
		WorkspaceDidChangeWatchedFiles: workspaceDidChangeWatchedFiles,
		WorkspaceSymbol:                workspaceSymbol,
		WorkspaceExecuteCommand:        workspaceExecuteCommand,
	}

	s := server.NewServer(&handler, lsName, false)

	log.Info().Msg("Starting Kubernetes LSP Server...")
	if err := s.RunStdio(); err != nil {
		log.Fatal().Err(err).Msg("Server failed")
	}
}
