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
	"k8s-lsp/pkg/crd"
	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/validator"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"
	"gopkg.in/yaml.v3"
)

const lsName = "k8s-lsp"

var version = "0.0.1"

type ServerState struct {
	Store      *indexer.Store
	Indexer    *indexer.Indexer
	Resolver   *resolver.Resolver
	Validator  *validator.Validator
	Schemas    *schema.Registry
	Documents  map[string]string
	DocVersion map[string]int32
	YAMLCache  *yamlstream.Cache
	RootPath   string
	CRDSources []string
	SchemaSources []string

	scanMu      sync.Mutex
	scanStarted bool
	ScanDone    chan struct{}

	DiagnosticsDebounce time.Duration
	IndexDebounce       time.Duration

	diagMu     sync.Mutex
	diagTimers map[string]*time.Timer
	diagLatest map[string]diagRequest
	diagSeq    map[string]uint64

	indexMu     sync.Mutex
	indexTimers map[string]*time.Timer
	indexLatest map[string]indexRequest
	indexSeq    map[string]uint64
}

type diagRequest struct {
	seq     uint64
	uri     string
	content string
}

type indexRequest struct {
	seq     uint64
	uri     string
	path    string
	content string
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
	schemas := schema.NewRegistry()
	schema.RegisterBuiltins(schemas)
	// Load schema packs shipped alongside the server binary (configPath/schemas/*.yaml).
	// This is the default mechanism for providing schemas for core/built-in resources.
	if schemas != nil {
		schemasDir := filepath.Join(configPath, "schemas")
		loaded := 0
		_ = filepath.Walk(schemasDir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info == nil || info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}
			n, err := schema.LoadGVKSchemasFromFile(schemas, p)
			if err != nil {
				log.Warn().Err(err).Str("path", p).Msg("Failed to load local schema pack")
				return nil
			}
			loaded += n
			return nil
		})
		if loaded > 0 {
			log.Info().Int("schemas", loaded).Msg("Loaded local schema packs")
		}
	}
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

func initialize(context *glsp.Context, params *protocol.InitializeParams) (any, error) {
	capabilities := protocol.ServerCapabilities{
		TextDocumentSync:        protocol.TextDocumentSyncKindFull,
		DefinitionProvider:      true,
		ReferencesProvider:      true,
		DocumentSymbolProvider:  true,
		WorkspaceSymbolProvider: true,
		CompletionProvider: &protocol.CompletionOptions{
			TriggerCharacters: []string{":", " "},
		},
		CodeActionProvider: &protocol.CodeActionOptions{
			CodeActionKinds: []protocol.CodeActionKind{protocol.CodeActionKindQuickFix, protocol.CodeActionKindSource},
		},
		RenameProvider: &protocol.RenameOptions{},
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
	// Expected shape:
	//   { crdSources?: string[], schemaSources?: string[], diagnosticsDebounceMs?: number, indexDebounceMs?: number }
	if params != nil {
		if raw := params.InitializationOptions; raw != nil {
			if m, ok := raw.(map[string]any); ok {
				if v, ok := m["crdSources"]; ok {
					state.CRDSources = toStringSlice(v)
				}
				if v, ok := m["schemaSources"]; ok {
					state.SchemaSources = toStringSlice(v)
				}
				if v, ok := m["diagnosticsDebounceMs"]; ok {
					if ms, ok := toInt(v); ok {
						if ms < 0 {
							ms = 0
						}
						if ms > 5000 {
							ms = 5000
						}
						state.DiagnosticsDebounce = time.Duration(ms) * time.Millisecond
						log.Info().Int("ms", ms).Msg("Configured diagnostics debounce")
					}
				}
				if v, ok := m["indexDebounceMs"]; ok {
					if ms, ok := toInt(v); ok {
						if ms < 0 {
							ms = 0
						}
						if ms > 5000 {
							ms = 5000
						}
						state.IndexDebounce = time.Duration(ms) * time.Millisecond
						log.Info().Int("ms", ms).Msg("Configured index debounce")
					}
				}
			}
		}
	}
	if len(state.CRDSources) > 0 {
		log.Info().Int("count", len(state.CRDSources)).Msg("Configured CRD sources")
	}
	if len(state.SchemaSources) > 0 {
		log.Info().Int("count", len(state.SchemaSources)).Msg("Configured schema sources")
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

func toInt(v any) (int, bool) {
	switch vv := v.(type) {
	case int:
		return vv, true
	case int32:
		return int(vv), true
	case int64:
		return int(vv), true
	case float64:
		return int(vv), true
	case float32:
		return int(vv), true
	case json.Number:
		i, err := vv.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func initialized(context *glsp.Context, params *protocol.InitializedParams) error {
	log.Info().Msg("Client initialized")

	// Preload CRDs (download -> index -> load schemas) before scanning the workspace.
	// This ensures dynamic kinds and their schemas are available early.
	if state != nil && len(state.CRDSources) > 0 {
		paths, err := crd.DownloadAndIndex(state.Indexer, state.CRDSources)
		if err != nil {
			log.Warn().Err(err).Msg("CRD preload had errors")
		}
		if state.Schemas != nil {
			loaded := 0
			for _, p := range paths {
				n, err := schema.LoadCRDSchemasFromFile(state.Schemas, p)
				if err != nil {
					log.Warn().Err(err).Str("path", p).Msg("Failed to load CRD schema")
					continue
				}
				loaded += n
			}
			if loaded > 0 {
				log.Info().Int("schemas", loaded).Msg("Loaded CRD schemas")
			}
		}
	}

	// Preload additional YAML schema packs for built-in resources.
	// This lets users define core GVK schemas (OpenAPIV3) in plain YAML, similar to CRDs.
	if state != nil && len(state.SchemaSources) > 0 {
		opts := crd.DefaultOptions()
		paths, err := crd.DownloadAll(state.SchemaSources, opts)
		if err != nil {
			log.Warn().Err(err).Msg("Schema source preload had errors")
		}
		if state.Schemas != nil {
			loaded := 0
			for _, p := range paths {
				n, err := schema.LoadGVKSchemasFromFile(state.Schemas, p)
				if err != nil {
					log.Warn().Err(err).Str("path", p).Msg("Failed to load schema pack")
					continue
				}
				loaded += n
			}
			if loaded > 0 {
				log.Info().Int("schemas", loaded).Msg("Loaded schema pack schemas")
			}
		}
	}

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

	// Workspace index/store mutations only apply to file:// URIs.
	// Non-file URIs (e.g. k8s-embedded://, untitled:) are supported for document-local features,
	// but must not pollute the workspace store.
	if path, ok := fileURIPath(params.TextDocument.URI); ok {
		state.Store.RemoveByFilePath(path)
		state.Indexer.IndexContent(path, params.TextDocument.Text)
	}

	scheduleDiagnostics(context, params.TextDocument.URI)
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


			// Workspace indexing can be expensive on every keystroke.
			// Debounce index updates for file:// URIs; on-save events still reindex immediately.
			if path, ok := fileURIPath(params.TextDocument.URI); ok {
				scheduleIndexUpdate(params.TextDocument.URI, path)
			}

			if context != nil {
				scheduleDiagnostics(context, params.TextDocument.URI)
			}
		} else {
			// Fallback or log error if type assertion fails
			// In some versions it might be TextDocumentContentChangeEventWhole
			if changeWhole, ok := params.ContentChanges[0].(protocol.TextDocumentContentChangeEventWhole); ok {
				state.Documents[params.TextDocument.URI] = changeWhole.Text
				state.DocVersion[params.TextDocument.URI] = int32(params.TextDocument.Version)
				if state.YAMLCache != nil {
					state.YAMLCache.InvalidateURI(params.TextDocument.URI)
				}

				if path, ok := fileURIPath(params.TextDocument.URI); ok {
					scheduleIndexUpdate(params.TextDocument.URI, path)
				}

				if context != nil {
					scheduleDiagnostics(context, params.TextDocument.URI)
				}
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
		uri := string(change.URI)
		log.Debug().Str("uri", uri).Int("type", int(change.Type)).Msg("Watched file changed")
		path, ok := fileURIPath(uri)
		if !ok {
			continue
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		switch change.Type {
		case protocol.FileChangeTypeCreated, protocol.FileChangeTypeChanged:
			// Prefer in-memory content if the document is open/being edited.
			content, ok := state.Documents[uri]
			if !ok {
				b, err := os.ReadFile(path)
				if err != nil {
					log.Warn().Err(err).Str("path", path).Msg("Failed to read changed file; treating as delete")
					state.cancelIndex(uri)
					state.cancelDiagnostics(uri)
					state.Store.RemoveByFilePath(path)
					if state.YAMLCache != nil {
						state.YAMLCache.InvalidateURI(uri)
					}
					delete(state.Documents, uri)
					delete(state.DocVersion, uri)
					if context != nil {
						context.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
							URI:         uri,
							Diagnostics: []protocol.Diagnostic{},
						})
					}
					continue
				}
				content = string(b)
				state.Documents[uri] = content
				state.DocVersion[uri] = 0
			}

			if state.YAMLCache != nil {
				state.YAMLCache.InvalidateURI(uri)
			}
			state.cancelIndex(uri)
			state.Store.RemoveByFilePath(path)
			state.Indexer.IndexContent(path, content)
			scheduleDiagnostics(context, uri)

		case protocol.FileChangeTypeDeleted:
			state.cancelDiagnostics(uri)
			state.cancelIndex(uri)
			state.Store.RemoveByFilePath(path)
			if state.YAMLCache != nil {
				state.YAMLCache.InvalidateURI(uri)
			}
			delete(state.Documents, uri)
			delete(state.DocVersion, uri)
			if context != nil {
				context.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
					URI:         uri,
					Diagnostics: []protocol.Diagnostic{},
				})
			}
		}
	}
	return nil
}

func (s *ServerState) cancelDiagnostics(uri string) {
	if s == nil || uri == "" {
		return
	}
	s.diagMu.Lock()
	defer s.diagMu.Unlock()
	if s.diagTimers != nil {
		if t, ok := s.diagTimers[uri]; ok && t != nil {
			t.Stop()
			delete(s.diagTimers, uri)
		}
	}
	if s.diagLatest != nil {
		delete(s.diagLatest, uri)
	}
}

func (s *ServerState) cancelIndex(uri string) {
	if s == nil || uri == "" {
		return
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexTimers != nil {
		if t, ok := s.indexTimers[uri]; ok && t != nil {
			t.Stop()
			delete(s.indexTimers, uri)
		}
	}
	if s.indexLatest != nil {
		delete(s.indexLatest, uri)
	}
}

func fileURIPath(uri string) (string, bool) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	return parsed.Path, true
}

// uriToPath converts file:// URIs to filesystem paths, otherwise returns the input.
// Use this for non-indexing path computations only.
func uriToPath(uri string) string {
	if p, ok := fileURIPath(uri); ok {
		return p
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
	if context == nil {
		return
	}
	if state.Validator == nil {
		return
	}
	ver := state.DocVersion[uri]
	stream, err := state.YAMLCache.Get(uri, ver, content)
	if err != nil {
		return
	}
	diagnostics := state.Validator.ValidateStream(uri, stream)
	// Schema-based diagnostics (unknown fields, type/enum mismatches)
	if state.Schemas != nil {
		for _, doc := range stream.Docs {
			if doc.Node == nil {
				continue
			}
			apiVersion := extractTopLevelScalar(doc.Node, "apiVersion")
			kind := extractTopLevelScalar(doc.Node, "kind")
			if kind == "" || apiVersion == "" {
				continue
			}
			group, version := schema.ParseAPIVersion(apiVersion)
			gvk := schema.GVK{Group: group, Version: version, Kind: kind}
			sch := state.Schemas.Get(gvk)
			if sch == nil {
				sch = schema.KubernetesObjectFallback()
			}
			diagnostics = append(diagnostics, schema.ValidateDocument(doc.Node, sch)...)
		}
	}
	if diagnostics == nil {
		diagnostics = []protocol.Diagnostic{}
	}

	context.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diagnostics,
	})
}

func extractTopLevelScalar(docNode *yaml.Node, key string) string {
	if docNode == nil {
		return ""
	}
	if docNode.Kind == yaml.DocumentNode {
		if len(docNode.Content) == 0 {
			return ""
		}
		docNode = docNode.Content[0]
	}
	if docNode == nil || docNode.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i < len(docNode.Content); i += 2 {
		k := docNode.Content[i]
		v := docNode.Content[i+1]
		if k != nil && k.Value == key && v != nil && v.Kind == yaml.ScalarNode {
			return v.Value
		}
	}
	return ""
}

func scheduleDiagnostics(context *glsp.Context, uri string) {
	if context == nil || uri == "" {
		return
	}
	if state == nil || state.Validator == nil {
		return
	}
	content, ok := state.Documents[uri]
	if !ok {
		return
	}

	debounce := state.DiagnosticsDebounce
	if debounce < 0 {
		debounce = 0
	}

	state.diagMu.Lock()
	if state.diagTimers == nil {
		state.diagTimers = make(map[string]*time.Timer)
	}
	if state.diagLatest == nil {
		state.diagLatest = make(map[string]diagRequest)
	}
	if state.diagSeq == nil {
		state.diagSeq = make(map[string]uint64)
	}

	state.diagSeq[uri]++
	seq := state.diagSeq[uri]
	state.diagLatest[uri] = diagRequest{seq: seq, uri: uri, content: content}

	if t, ok := state.diagTimers[uri]; ok && t != nil {
		t.Stop()
	}
	if debounce == 0 {
		state.diagMu.Unlock()
		publishDiagnostics(context, uri, content)
		return
	}

	state.diagTimers[uri] = time.AfterFunc(debounce, func() {
		state.diagMu.Lock()
		req, ok := state.diagLatest[uri]
		if !ok || req.seq != seq {
			state.diagMu.Unlock()
			return
		}
		// Keep latest so subsequent changes can overwrite and reschedule.
		content := req.content
		state.diagMu.Unlock()

		publishDiagnostics(context, uri, content)
	})
	state.diagMu.Unlock()
}

func scheduleIndexUpdate(uri string, path string) {
	if uri == "" || path == "" {
		return
	}
	if state == nil || state.Indexer == nil || state.Store == nil {
		return
	}
	content, ok := state.Documents[uri]
	if !ok {
		return
	}

	debounce := state.IndexDebounce
	if debounce < 0 {
		debounce = 0
	}

	state.indexMu.Lock()
	if state.indexTimers == nil {
		state.indexTimers = make(map[string]*time.Timer)
	}
	if state.indexLatest == nil {
		state.indexLatest = make(map[string]indexRequest)
	}
	if state.indexSeq == nil {
		state.indexSeq = make(map[string]uint64)
	}

	state.indexSeq[uri]++
	seq := state.indexSeq[uri]
	state.indexLatest[uri] = indexRequest{seq: seq, uri: uri, path: path, content: content}

	if t, ok := state.indexTimers[uri]; ok && t != nil {
		t.Stop()
	}

	if debounce == 0 {
		state.indexMu.Unlock()
		state.Store.RemoveByFilePath(path)
		state.Indexer.IndexContent(path, content)
		return
	}

	state.indexTimers[uri] = time.AfterFunc(debounce, func() {
		state.indexMu.Lock()
		req, ok := state.indexLatest[uri]
		if !ok || req.seq != seq {
			state.indexMu.Unlock()
			return
		}
		p := req.path
		c := req.content
		state.indexMu.Unlock()

		state.Store.RemoveByFilePath(p)
		state.Indexer.IndexContent(p, c)
	})
	state.indexMu.Unlock()
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
