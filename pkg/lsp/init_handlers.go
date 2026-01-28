package lsp

import (
	"encoding/json"
	"net/url"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func initialize(context *glsp.Context, params *protocol.InitializeParams) (any, error) {
	if state != nil {
		state.setNotifyContext(context)
	}
	resolveProvider := false
	docLinkResolve := false
	capabilities := protocol.ServerCapabilities{
		TextDocumentSync:          protocol.TextDocumentSyncKindFull,
		DefinitionProvider:        true,
		ReferencesProvider:        true,
		DocumentFormattingProvider: true,
		DocumentHighlightProvider: true,
		DocumentLinkProvider: &protocol.DocumentLinkOptions{
			ResolveProvider: &docLinkResolve,
		},
		CodeLensProvider: &protocol.CodeLensOptions{
			ResolveProvider: &resolveProvider,
		},
		DocumentSymbolProvider:  true,
		WorkspaceSymbolProvider: true,
		CompletionProvider: &protocol.CompletionOptions{
			TriggerCharacters: []string{":", " "},
		},
		SemanticTokensProvider: &protocol.SemanticTokensOptions{
			Legend: protocol.SemanticTokensLegend{
				TokenTypes:     semanticTokenTypes,
				TokenModifiers: semanticTokenModifiers,
			},
			Full: true,
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
				if fmtCfg, ok := m["formatting"].(map[string]any); ok {
					if v, ok := fmtCfg["enabled"]; ok {
						if b, ok := v.(bool); ok {
							state.FormattingEnabled = b
						}
					}
					if v, ok := fmtCfg["indentSize"]; ok {
						if n, ok := toInt(v); ok {
							if n < 1 {
								n = 1
							}
							if n > 8 {
								n = 8
							}
							state.FormattingIndentSize = n
						}
					}
					if v, ok := fmtCfg["disableForTemplates"]; ok {
						if b, ok := v.(bool); ok {
							state.FormattingDisableForTemplates = b
						}
					}
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
	if state != nil {
		state.setNotifyContext(context)
	}

	// Build the effective schema registry (builtins + local packs + CRDs + schema packs)
	// before scanning the workspace so dynamic kinds and their schemas are available early.
	reloadSchemasAndCRDs(context)

	state.startWorkspaceScan()
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
