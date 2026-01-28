package lsp

import (
	"sync"
	"time"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/resolver"
	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/validator"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

type ServerState struct {
	Store      *indexer.Store
	Indexer    *indexer.Indexer
	Resolver   *resolver.Resolver
	Validator  *validator.Validator
	Schemas    *schema.Registry
	Documents  map[string]string
	DocVersion map[string]int32
	docsMu     sync.RWMutex

	notifyMu  sync.RWMutex
	notifyCtx *glsp.Context
	YAMLCache *yamlstream.Cache
	RootPath  string

	CRDSources    []string
	SchemaSources []string

	schemaGen uint64

	semanticMu    sync.Mutex
	semanticCache map[string]semanticTokensSnapshot

	reloadMu              sync.Mutex
	loadedCRDPaths        []string
	loadedSchemaPackPaths []string

	scanMu      sync.Mutex
	scanStarted bool
	ScanDone    chan struct{}

	DiagnosticsDebounce time.Duration
	IndexDebounce       time.Duration

	SemanticTokensEnabled          bool
	ReferencesVisualizationEnabled bool
	CodeLensEnabled                bool
	DocumentLinksEnabled           bool

	diagMu     sync.Mutex
	diagTimers map[string]*time.Timer
	diagLatest map[string]diagRequest
	diagSeq    map[string]uint64

	indexMu     sync.Mutex
	indexTimers map[string]*time.Timer
	indexLatest map[string]indexRequest
	indexSeq    map[string]uint64
}

type semanticTokensSnapshot struct {
	ver       int32
	schemaGen uint64
	data      []protocol.UInteger
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

type docSnapshot struct {
	uri     string
	content string
	ver     int32
}

func (s *ServerState) setNotifyContext(ctx *glsp.Context) {
	if s == nil || ctx == nil {
		return
	}
	s.notifyMu.Lock()
	s.notifyCtx = ctx
	s.notifyMu.Unlock()
}

func (s *ServerState) getNotifyContext() *glsp.Context {
	if s == nil {
		return nil
	}
	s.notifyMu.RLock()
	ctx := s.notifyCtx
	s.notifyMu.RUnlock()
	return ctx
}

func (s *ServerState) getDocument(uri string) (content string, ver int32, ok bool) {
	if s == nil || uri == "" {
		return "", 0, false
	}
	s.docsMu.RLock()
	content, ok = s.Documents[uri]
	ver = s.DocVersion[uri]
	s.docsMu.RUnlock()
	return content, ver, ok
}

func (s *ServerState) setDocument(uri string, content string, ver int32) {
	if s == nil || uri == "" {
		return
	}
	s.docsMu.Lock()
	s.Documents[uri] = content
	s.DocVersion[uri] = ver
	s.docsMu.Unlock()
}

func (s *ServerState) deleteDocument(uri string) {
	if s == nil || uri == "" {
		return
	}
	s.docsMu.Lock()
	delete(s.Documents, uri)
	delete(s.DocVersion, uri)
	s.docsMu.Unlock()
}

func (s *ServerState) snapshotDocuments() []docSnapshot {
	if s == nil {
		return nil
	}
	s.docsMu.RLock()
	out := make([]docSnapshot, 0, len(s.Documents))
	for uri, content := range s.Documents {
		out = append(out, docSnapshot{uri: uri, content: content, ver: s.DocVersion[uri]})
	}
	s.docsMu.RUnlock()
	return out
}

func (s *ServerState) refreshDiagnosticsForOpenDocuments() {
	ctx := s.getNotifyContext()
	if ctx == nil {
		return
	}
	for _, doc := range s.snapshotDocuments() {
		if doc.uri == "" || doc.content == "" {
			continue
		}
		publishDiagnostics(ctx, doc.uri, doc.content)
	}
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
		s.refreshDiagnosticsForOpenDocuments()
	}(s.RootPath, s.ScanDone)
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
