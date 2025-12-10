package main

import (
	"net/url"
	"os"
	"path/filepath"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"
)

const lsName = "k8s-lsp"

var version = "0.0.1"

type ServerState struct {
	Store     *indexer.Store
	Indexer   *indexer.Indexer
	Resolver  *resolver.Resolver
	Documents map[string]string
	RootPath  string
}

var state *ServerState

func main() {
	// Configure logging to file and stderr
	logFile, err := os.OpenFile(getLogFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Fallback to stderr if file fails
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
		log.Error().Err(err).Msg("Failed to open log file")
	} else {
		multi := zerolog.MultiLevelWriter(zerolog.ConsoleWriter{Out: os.Stderr}, logFile)
		log.Logger = log.Output(multi)
	}

	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	// Determine executable path to find rules directory
	exePath, err := os.Executable()
	configPath := "."
	if err != nil {
		log.Error().Err(err).Msg("Failed to get executable path, using current directory")
	} else {
		configPath = filepath.Dir(exePath)
	}

	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error().Err(err).Str("path", configPath).Msg("Failed to load config")
		// Continue with empty config or default?
		// config.Load returns partial config even on error if it read something, or we can just use empty.
		if cfg == nil {
			cfg = &config.Config{}
		}
	}
	log.Info().Int("symbols", len(cfg.Symbols)).Int("references", len(cfg.References)).Msg("Loaded configuration")

	// Initialize state
	store := indexer.NewStore()
	idx := indexer.NewIndexer(store, cfg)
	res := resolver.NewResolver(store, cfg)
	state = &ServerState{
		Store:     store,
		Indexer:   idx,
		Resolver:  res,
		Documents: make(map[string]string),
	}

	handler := protocol.Handler{
		Initialize:             initialize,
		Initialized:            initialized,
		Shutdown:               shutdown,
		SetTrace:               setTrace,
		TextDocumentDidOpen:    textDocumentDidOpen,
		TextDocumentDidChange:  textDocumentDidChange,
		TextDocumentDefinition: textDocumentDefinition,
		TextDocumentReferences: textDocumentReferences,
		TextDocumentCompletion: textDocumentCompletion,
	}

	s := server.NewServer(&handler, lsName, false)

	log.Info().Msg("Starting Kubernetes LSP Server...")

	if err := s.RunStdio(); err != nil {
		log.Fatal().Err(err).Msg("Server failed")
	}
}

func initialize(context *glsp.Context, params *protocol.InitializeParams) (any, error) {
	capabilities := protocol.ServerCapabilities{
		TextDocumentSync:   protocol.TextDocumentSyncKindFull,
		DefinitionProvider: true,
		ReferencesProvider: true,
		CompletionProvider: &protocol.CompletionOptions{
			TriggerCharacters: []string{":", " "},
		},
	}

	// Determine root path
	if params.RootURI != nil {
		parsed, err := url.Parse(*params.RootURI)
		if err == nil && parsed.Scheme == "file" {
			state.RootPath = parsed.Path
		}
	} else if params.RootPath != nil {
		state.RootPath = *params.RootPath
	}

	log.Info().Str("root", state.RootPath).Msg("Initializing...")

	return protocol.InitializeResult{
		Capabilities: capabilities,
		ServerInfo: &protocol.InitializeResultServerInfo{
			Name:    lsName,
			Version: &version,
		},
	}, nil
}

func initialized(context *glsp.Context, params *protocol.InitializedParams) error {
	log.Info().Msg("Client initialized")

	if state.RootPath != "" {
		go func() {
			log.Info().Msg("Starting workspace scan...")
			if err := state.Indexer.ScanWorkspace(state.RootPath); err != nil {
				log.Error().Err(err).Msg("Failed to scan workspace")
			} else {
				log.Info().Msg("Workspace scan completed")
			}
		}()
	}

	return nil
}

func shutdown(context *glsp.Context) error {
	protocol.SetTraceValue(protocol.TraceValueOff)
	return nil
}

func setTrace(context *glsp.Context, params *protocol.SetTraceParams) error {
	protocol.SetTraceValue(params.Value)
	return nil
}

func textDocumentDidOpen(context *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	state.Documents[params.TextDocument.URI] = params.TextDocument.Text
	return nil
}

func textDocumentDidChange(context *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	// Since we use Full sync, ContentChanges has one element with the full text
	if len(params.ContentChanges) > 0 {
		change, ok := params.ContentChanges[0].(protocol.TextDocumentContentChangeEvent)
		if ok {
			state.Documents[params.TextDocument.URI] = change.Text
		} else {
			// Fallback or log error if type assertion fails
			// In some versions it might be TextDocumentContentChangeEventWhole
			if changeWhole, ok := params.ContentChanges[0].(protocol.TextDocumentContentChangeEventWhole); ok {
				state.Documents[params.TextDocument.URI] = changeWhole.Text
			}
		}
	}
	return nil
}

func textDocumentDefinition(context *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received definition request")

	uri := params.TextDocument.URI
	log.Debug().Str("uri", uri).Msg("Looking up document content")
	content, ok := state.Documents[uri]
	log.Debug().Bool("foundInMemory", ok).Msg("Document content lookup result")
	if !ok {
		// Try to read from file if not in memory (e.g. not opened yet but requested?)
		// Usually client opens before requesting definition.
		// But let's try to read from file path if possible.
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
			}
		}
	}
	log.Debug().Bool("contentAvailable", content != "").Msg("Document content availability")

	if content == "" {
		return nil, nil
	}

	log.Debug().Str("uri", uri).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Resolving definition")
	log.Debug().Str("content", content).Msg("Document content for definition")

	locs, err := state.Resolver.ResolveDefinition(content, uri, int(params.Position.Line), int(params.Position.Character))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve definition")
		return nil, nil
	}
	log.Debug().Int("locationsFound", len(locs)).Msg("Definition resolution completed")

	return locs, nil
}

func textDocumentReferences(context *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received references request")

	uri := params.TextDocument.URI
	content, ok := state.Documents[uri]
	if !ok {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
			}
		}
	}

	if content == "" {
		return nil, nil
	}

	locs, err := state.Resolver.ResolveReferences(content, int(params.Position.Line), int(params.Position.Character))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve references")
		return nil, nil
	}

	return locs, nil
}

func textDocumentCompletion(context *glsp.Context, params *protocol.CompletionParams) (any, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received completion request")

	uri := params.TextDocument.URI
	content, ok := state.Documents[uri]
	if !ok {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
			}
		}
	}

	if content == "" {
		return nil, nil
	}

	items, err := state.Resolver.Completion(content, int(params.Position.Line), int(params.Position.Character))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve completion")
		return nil, nil
	}

	return items, nil
}
