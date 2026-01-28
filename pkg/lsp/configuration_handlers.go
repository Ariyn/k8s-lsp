package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"k8s-lsp/pkg/crd"
	"k8s-lsp/pkg/schema"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func workspaceDidChangeConfiguration(context *glsp.Context, params *protocol.DidChangeConfigurationParams) error {
	if state == nil || params == nil {
		return nil
	}
	state.setNotifyContext(context)

	settings := params.Settings
	m, ok := settings.(map[string]any)
	if !ok {
		return nil
	}
	// Some clients nest settings under the section name.
	if nested, ok := m["k8sLsp"].(map[string]any); ok {
		m = nested
	}

	newCRDSources := toStringSlice(m["crdSources"])
	newSchemaSources := toStringSlice(m["schemaSources"])

	// Feature toggles (default ON)
	if sem, ok := m["semanticTokens"].(map[string]any); ok {
		if v, ok := sem["enabled"]; ok {
			if b, ok := v.(bool); ok {
				state.SemanticTokensEnabled = b
			}
		}
	}
	if rv, ok := m["referencesVisualization"].(map[string]any); ok {
		if v, ok := rv["enabled"]; ok {
			if b, ok := v.(bool); ok {
				state.ReferencesVisualizationEnabled = b
			}
		}
	}
	if cl, ok := m["codeLens"].(map[string]any); ok {
		if v, ok := cl["enabled"]; ok {
			if b, ok := v.(bool); ok {
				state.CodeLensEnabled = b
			}
		}
	}
	if dl, ok := m["documentLinks"].(map[string]any); ok {
		if v, ok := dl["enabled"]; ok {
			if b, ok := v.(bool); ok {
				state.DocumentLinksEnabled = b
			}
		}
	}

	sourcesChanged := !stringSliceEqual(state.CRDSources, newCRDSources) || !stringSliceEqual(state.SchemaSources, newSchemaSources)
	state.CRDSources = newCRDSources
	state.SchemaSources = newSchemaSources

	if v, ok := m["diagnosticsDebounceMs"]; ok {
		if ms, ok := toInt(v); ok {
			if ms < 0 {
				ms = 0
			}
			if ms > 5000 {
				ms = 5000
			}
			state.DiagnosticsDebounce = timeMs(ms)
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
			state.IndexDebounce = timeMs(ms)
		}
	}

	if sourcesChanged {
		log.Info().Msg("Configuration changed; reloading CRDs/schemas")
		reloadSchemasAndCRDs(context)
		state.refreshDiagnosticsForOpenDocuments()
	}
	return nil
}

func reloadSchemasAndCRDs(context *glsp.Context) {
	if state == nil {
		return
	}
	state.reloadMu.Lock()
	defer state.reloadMu.Unlock()

	// Remove previously indexed CRDs from the workspace store.
	if state.Store != nil {
		for _, p := range state.loadedCRDPaths {
			state.Store.RemoveByFilePath(p)
		}
	}
	state.loadedCRDPaths = nil
	state.loadedSchemaPackPaths = nil

	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)
	loadLocalSchemaPacks(reg, configPathFromExecutable())

	// CRDs: download (ETag/304 cached), index, and load their schemas.
	if len(state.CRDSources) > 0 {
		paths, err := crd.DownloadAndIndex(state.Indexer, state.CRDSources)
		if err != nil {
			log.Warn().Err(err).Msg("CRD reload had errors")
		}
		state.loadedCRDPaths = paths

		loaded := 0
		for _, p := range paths {
			n, err := schema.LoadCRDSchemasFromFile(reg, p)
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

	// Schema packs: download (ETag/304 cached) and load GVK schemas.
	if len(state.SchemaSources) > 0 {
		opts := crd.DefaultOptions()
		paths, err := crd.DownloadAll(state.SchemaSources, opts)
		if err != nil {
			log.Warn().Err(err).Msg("Schema source reload had errors")
		}
		state.loadedSchemaPackPaths = paths

		loaded := 0
		for _, p := range paths {
			n, err := schema.LoadGVKSchemasFromFile(reg, p)
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

	state.Schemas = reg
	if state.Resolver != nil {
		state.Resolver.Schemas = reg
	}

	state.schemaGen++
	state.semanticMu.Lock()
	for k := range state.semanticCache {
		delete(state.semanticCache, k)
	}
	state.semanticMu.Unlock()
	if ctx := state.getNotifyContext(); ctx != nil {
		ctx.Notify(protocol.MethodWorkspaceSemanticTokensRefresh, struct{}{})
	}

	// Avoid unused warning if context is not used; keep it for future extensions.
	_ = context
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func timeMs(ms int) (d time.Duration) {
	return time.Duration(ms) * time.Millisecond
}

func isConfiguredLocalSourceFile(path string) bool {
	if state == nil || path == "" {
		return false
	}
	for _, src := range state.CRDSources {
		if p, ok := sourceToLocalPath(src, state.RootPath); ok && p == path {
			return true
		}
	}
	for _, src := range state.SchemaSources {
		if p, ok := sourceToLocalPath(src, state.RootPath); ok && p == path {
			return true
		}
	}
	return false
}

func sourceToLocalPath(src string, root string) (string, bool) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", false
	}

	u, err := url.Parse(src)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "" && scheme != "file" {
		return "", false
	}

	p := strings.TrimSpace(u.Path)
	if p == "" {
		// url.Parse("relative") stores it in Path; if empty, fall back to raw string.
		p = src
	}
	if p == "" {
		return "", false
	}

	// Resolve relative paths against the workspace root if known.
	if !filepath.IsAbs(p) && strings.TrimSpace(root) != "" {
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p), true
}
