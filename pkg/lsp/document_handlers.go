package lsp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func textDocumentDidOpen(context *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	state.setNotifyContext(context)
	state.setDocument(params.TextDocument.URI, params.TextDocument.Text, int32(params.TextDocument.Version))
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
			state.setNotifyContext(context)
			state.setDocument(params.TextDocument.URI, change.Text, int32(params.TextDocument.Version))
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
				state.setNotifyContext(context)
				state.setDocument(params.TextDocument.URI, changeWhole.Text, int32(params.TextDocument.Version))
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
			state.setNotifyContext(context)
			content, _, ok := state.getDocument(uri)
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
					state.deleteDocument(uri)
					if context != nil {
						context.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
							URI:         uri,
							Diagnostics: []protocol.Diagnostic{},
						})
					}
					continue
				}
				content = string(b)
				state.setDocument(uri, content, 0)
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
			state.deleteDocument(uri)
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
