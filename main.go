package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"
	"k8s-lsp/pkg/validator"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"
)

const lsName = "k8s-lsp"

var version = "0.0.1"

type ServerState struct {
	Store      *indexer.Store
	Indexer    *indexer.Indexer
	Resolver   *resolver.Resolver
	Validator  *validator.Validator
	Documents  map[string]string
	DocVersion map[string]int32
	YAMLCache  *yamlstream.Cache
	RootPath   string
	CRDSources []string

	scanMu      sync.Mutex
	scanStarted bool
	ScanDone    chan struct{}
}

var state *ServerState

func main() {
	// Configure logging to file and stderr
	logFile, err := os.OpenFile(getLogFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}
	if err != nil {
		// Fallback to stderr if file fails
		log.Logger = log.Output(consoleWriter)
		log.Error().Err(err).Msg("Failed to open log file")
	} else {
		multi := zerolog.MultiLevelWriter(consoleWriter, logFile)
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

	val, err := validator.NewValidator(filepath.Join(configPath, "rules/validation.yaml"), store)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load validation rules")
	}

	state = &ServerState{
		Store:     store,
		Indexer:   idx,
		Resolver:  res,
		Validator: val,
		Documents: make(map[string]string),
		DocVersion: make(map[string]int32),
		YAMLCache:  yamlstream.NewCache(),
		ScanDone:  make(chan struct{}),
	}

	handler := protocol.Handler{
		Initialize:                     initialize,
		Initialized:                    initialized,
		Shutdown:                       shutdown,
		SetTrace:                       setTrace,
		TextDocumentDidOpen:            textDocumentDidOpen,
		TextDocumentDidChange:          textDocumentDidChange,
		TextDocumentDefinition:         textDocumentDefinition,
		TextDocumentReferences:         textDocumentReferences,
		TextDocumentCompletion:         textDocumentCompletion,
		TextDocumentHover:              textDocumentHover,
		TextDocumentDidSave:            textDocumentDidSave,
		WorkspaceDidChangeWatchedFiles: workspaceDidChangeWatchedFiles,
		WorkspaceExecuteCommand:        workspaceExecuteCommand,
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
		ExecuteCommandProvider: &protocol.ExecuteCommandOptions{
			Commands: []string{"k8s.embeddedContent", "k8s.saveEmbeddedContent"},
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

	// Read initializationOptions from the client (VS Code extension).
	// Expected shape: { crdSources: string[] }
	if params != nil {
		if raw := params.InitializationOptions; raw != nil {
			if m, ok := raw.(map[string]any); ok {
				if v, ok := m["crdSources"]; ok {
					state.CRDSources = toStringSlice(v)
				}
			}
		}
	}
	if len(state.CRDSources) > 0 {
		log.Info().Int("count", len(state.CRDSources)).Msg("Configured CRD sources")
	}

	return protocol.InitializeResult{
		Capabilities: capabilities,
		ServerInfo: &protocol.InitializeResultServerInfo{
			Name:    lsName,
			Version: &version,
		},
	}, nil
}

func toStringSlice(v any) []string {
	switch vv := v.(type) {
	case []string:
		out := make([]string, 0, len(vv))
		for _, s := range vv {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(vv))
		for _, it := range vv {
			s, ok := it.(string)
			if ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func initialized(context *glsp.Context, params *protocol.InitializedParams) error {
	log.Info().Msg("Client initialized")

	state.startWorkspaceScan()

	return nil
}

func (s *ServerState) startWorkspaceScan() {
	s.scanMu.Lock()
	if s.scanStarted || s.RootPath == "" {
		s.scanMu.Unlock()
		return
	}
	s.scanStarted = true
	s.scanMu.Unlock()

	go func(root string, done chan struct{}) {
		defer close(done)
		log.Info().Msg("Starting workspace scan...")
		if err := s.Indexer.ScanWorkspace(root); err != nil {
			log.Error().Err(err).Msg("Failed to scan workspace")
			return
		}
		log.Info().Msg("Workspace scan completed")
	}(s.RootPath, s.ScanDone)
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
	state.DocVersion[params.TextDocument.URI] = int32(params.TextDocument.Version)
	if state.YAMLCache != nil {
		state.YAMLCache.InvalidateURI(params.TextDocument.URI)
	}

	// Index the content to support dynamic updates (e.g. new CRDs)
	path := uriToPath(params.TextDocument.URI)
	state.Indexer.IndexContent(path, params.TextDocument.Text)

	go publishDiagnostics(context, params.TextDocument.URI, params.TextDocument.Text)
	return nil
}

func textDocumentDidChange(context *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	// Since we use Full sync, ContentChanges has one element with the full text
	if len(params.ContentChanges) > 0 {
		change, ok := params.ContentChanges[0].(protocol.TextDocumentContentChangeEvent)
		if ok {
			state.Documents[params.TextDocument.URI] = change.Text
			state.DocVersion[params.TextDocument.URI] = int32(params.TextDocument.Version)
			if state.YAMLCache != nil {
				state.YAMLCache.InvalidateURI(params.TextDocument.URI)
			}

			// Index the content
			path := uriToPath(params.TextDocument.URI)
			state.Indexer.IndexContent(path, change.Text)

			go publishDiagnostics(context, params.TextDocument.URI, change.Text)
		} else {
			// Fallback or log error if type assertion fails
			// In some versions it might be TextDocumentContentChangeEventWhole
			if changeWhole, ok := params.ContentChanges[0].(protocol.TextDocumentContentChangeEventWhole); ok {
				state.Documents[params.TextDocument.URI] = changeWhole.Text
				state.DocVersion[params.TextDocument.URI] = int32(params.TextDocument.Version)
				if state.YAMLCache != nil {
					state.YAMLCache.InvalidateURI(params.TextDocument.URI)
				}

				// Index the content
				path := uriToPath(params.TextDocument.URI)
				state.Indexer.IndexContent(path, changeWhole.Text)

				go publishDiagnostics(context, params.TextDocument.URI, changeWhole.Text)
			}
		}
	}
	return nil
}

func textDocumentDidSave(context *glsp.Context, params *protocol.DidSaveTextDocumentParams) error {
	log.Debug().Str("uri", params.TextDocument.URI).Msg("Document saved")
	return nil
}

func workspaceDidChangeWatchedFiles(context *glsp.Context, params *protocol.DidChangeWatchedFilesParams) error {
	for _, change := range params.Changes {
		log.Debug().Str("uri", change.URI).Int("type", int(change.Type)).Msg("Watched file changed")
		// TODO: Handle file events (Created, Changed, Deleted)
		// For now, we just log.
		// If we wanted to be correct, we should:
		// 1. If Created/Changed: IndexFile(uriToPath(change.URI))
		// 2. If Deleted: Remove resources from store (requires Store update to track by file)
	}
	return nil
}

func uriToPath(uri string) string {
	parsed, err := url.Parse(uri)
	if err == nil && parsed.Scheme == "file" {
		return parsed.Path
	}
	return uri
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
				state.DocVersion[uri] = 0
			}
		}
	}
	log.Debug().Bool("contentAvailable", content != "").Msg("Document content availability")

	if content == "" {
		return nil, nil
	}

	log.Debug().Str("uri", uri).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Resolving definition")
	log.Debug().Str("content", content).Msg("Document content for definition")

	state.startWorkspaceScan()

	resolve := func() ([]protocol.LocationLink, error) {
		ver := state.DocVersion[uri]
		stream, err := state.YAMLCache.Get(uri, ver, content)
		if err != nil {
			return nil, err
		}
		return state.Resolver.ResolveDefinitionStream(stream, uri, int(params.Position.Line), int(params.Position.Character))
	}

	locs, err := resolve()
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve definition")
		return nil, nil
	}

	// If the workspace scan is still running, the store may not yet contain the target definition.
	// Wait briefly for scan completion and retry once.
	if len(locs) == 0 {
		state.scanMu.Lock()
		scanStarted := state.scanStarted
		scanDone := state.ScanDone
		state.scanMu.Unlock()

		if scanStarted && scanDone != nil {
			select {
			case <-scanDone:
			case <-time.After(1500 * time.Millisecond):
			}
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
	content, ok := state.Documents[uri]
	if !ok {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
				state.DocVersion[uri] = 0
			}
		}
	}

	if content == "" {
		return nil, nil
	}

	state.startWorkspaceScan()

	resolve := func() ([]protocol.Location, error) {
		ver := state.DocVersion[uri]
		stream, err := state.YAMLCache.Get(uri, ver, content)
		if err != nil {
			return nil, err
		}
		return state.Resolver.ResolveReferencesStream(stream, uri, int(params.Position.Line), int(params.Position.Character))
	}

	locs, err := resolve()
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve references")
		return nil, nil
	}

	// If the workspace scan is still running, the store may not yet contain references.
	// Wait briefly for scan completion and retry once.
	if len(locs) == 0 {
		state.scanMu.Lock()
		scanStarted := state.scanStarted
		scanDone := state.ScanDone
		state.scanMu.Unlock()

		if scanStarted && scanDone != nil {
			select {
			case <-scanDone:
			case <-time.After(1500 * time.Millisecond):
			}
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
	content, ok := state.Documents[uri]
	if !ok {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
				state.DocVersion[uri] = 0
			}
		}
	}

	if content == "" {
		return nil, nil
	}

	ver := state.DocVersion[uri]
	stream, err := state.YAMLCache.Get(uri, ver, content)
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

func publishDiagnostics(context *glsp.Context, uri string, content string) {
	if state.Validator == nil {
		return
	}
	ver := state.DocVersion[uri]
	stream, err := state.YAMLCache.Get(uri, ver, content)
	if err != nil {
		return
	}
	diagnostics := state.Validator.ValidateStream(uri, stream)
	if diagnostics == nil {
		diagnostics = []protocol.Diagnostic{}
	}

	context.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diagnostics,
	})
}

func textDocumentHover(context *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	log.Debug().Str("uri", params.TextDocument.URI).Int("line", int(params.Position.Line)).Int("char", int(params.Position.Character)).Msg("Received hover request")

	uri := params.TextDocument.URI
	content, ok := state.Documents[uri]
	if !ok {
		parsed, err := url.Parse(uri)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[uri] = content
				state.DocVersion[uri] = 0
			}
		}
	}

	if content == "" {
		return nil, nil
	}

	ver := state.DocVersion[uri]
	stream, err := state.YAMLCache.Get(uri, ver, content)
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

func workspaceExecuteCommand(context *glsp.Context, params *protocol.ExecuteCommandParams) (any, error) {
	if params.Command == "k8s.embeddedContent" {
		if len(params.Arguments) > 0 {
			argBytes, err := json.Marshal(params.Arguments[0])
			if err != nil {
				return nil, err
			}

			var embeddedParams EmbeddedContentParams
			if err := json.Unmarshal(argBytes, &embeddedParams); err != nil {
				return nil, err
			}

			return handleEmbeddedContent(context, &embeddedParams)
		}
	} else if params.Command == "k8s.saveEmbeddedContent" {
		if len(params.Arguments) > 0 {
			argBytes, err := json.Marshal(params.Arguments[0])
			if err != nil {
				return nil, err
			}

			var saveParams SaveEmbeddedContentParams
			if err := json.Unmarshal(argBytes, &saveParams); err != nil {
				return nil, err
			}

			return handleSaveEmbeddedContent(context, &saveParams)
		}
	}
	return nil, nil
}

type EmbeddedContentParams struct {
	URI string `json:"uri"`
}

type SaveEmbeddedContentParams struct {
	URI     string `json:"uri"`
	Content string `json:"content"`
}

func handleSaveEmbeddedContent(context *glsp.Context, params *SaveEmbeddedContentParams) (any, error) {
	log.Debug().Str("uri", params.URI).Msg("Received save embedded content request")

	u, err := url.Parse(params.URI)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	sourceEncoded := q.Get("source")
	keyEncoded := q.Get("key")

	embeddedNamespace := u.Host
	configMapName := ""
	if p := strings.Trim(u.Path, "/"); p != "" {
		parts := strings.Split(p, "/")
		if len(parts) >= 1 {
			configMapName = parts[0]
		}
	}

	if sourceEncoded == "" || keyEncoded == "" {
		return nil, fmt.Errorf("missing source or key in URI")
	}

	sourceBytes, err := base64.URLEncoding.DecodeString(sourceEncoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode source: %w", err)
	}
	sourceURI := string(sourceBytes)

	keyBytes, err := base64.URLEncoding.DecodeString(keyEncoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key: %w", err)
	}
	key := string(keyBytes)

	content, ok := state.Documents[sourceURI]
	if !ok {
		parsed, err := url.Parse(sourceURI)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[sourceURI] = content
			}
		}
	}

	if content == "" {
		return nil, fmt.Errorf("document not found: %s", sourceURI)
	}

	log.Info().Str("source", sourceURI).Str("key", key).Str("content", params.Content).Msg("Saving embedded content")

	textEdit, err := state.Resolver.BuildEmbeddedContentTextEdit(content, key, params.Content, configMapName, embeddedNamespace)
	if err != nil {
		return nil, err
	}

	edit := protocol.WorkspaceEdit{
		Changes: map[string][]protocol.TextEdit{
			sourceURI: {*textEdit},
		},
	}

	return edit, nil
}

func handleEmbeddedContent(context *glsp.Context, params *EmbeddedContentParams) (string, error) {
	log.Debug().Str("uri", params.URI).Msg("Received embedded content request")

	u, err := url.Parse(params.URI)
	if err != nil {
		return "", err
	}

	log.Debug().Str("rawQuery", u.RawQuery).Msg("Parsed URI query")

	q := u.Query()
	sourceEncoded := q.Get("source")
	keyEncoded := q.Get("key")

	embeddedNamespace := u.Host
	configMapName := ""
	if p := strings.Trim(u.Path, "/"); p != "" {
		parts := strings.Split(p, "/")
		if len(parts) >= 1 {
			configMapName = parts[0]
		}
	}

	log.Debug().Str("sourceEncoded", sourceEncoded).Str("keyEncoded", keyEncoded).Msg("Extracted params")

	if sourceEncoded == "" || keyEncoded == "" {
		return "", fmt.Errorf("missing source or key in URI")
	}

	sourceBytes, err := base64.URLEncoding.DecodeString(sourceEncoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode source: %w", err)
	}
	sourceURI := string(sourceBytes)

	keyBytes, err := base64.URLEncoding.DecodeString(keyEncoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode key: %w", err)
	}
	key := string(keyBytes)

	log.Debug().Str("source", sourceURI).Str("key", key).Msg("Decoded params")

	content, ok := state.Documents[sourceURI]
	if !ok {
		// Try to read from disk
		parsed, err := url.Parse(sourceURI)
		if err == nil && parsed.Scheme == "file" {
			bytes, err := os.ReadFile(parsed.Path)
			if err == nil {
				content = string(bytes)
				state.Documents[sourceURI] = content
			}
		}
	}

	if content == "" {
		return "", fmt.Errorf("document not found: %s", sourceURI)
	}

	return state.Resolver.ResolveEmbeddedContent(content, key, configMapName, embeddedNamespace)
}
