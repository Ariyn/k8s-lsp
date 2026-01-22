package lsp

import (
	"net/url"
	"os"
)

// getOrLoadDocument returns document content from in-memory state if present,
// otherwise attempts to read from disk for file:// URIs.
// It caches disk reads back into the in-memory document store.
func getOrLoadDocument(uri string) (content string, ok bool, inMemory bool) {
	if state == nil || uri == "" {
		return "", false, false
	}

	content, _, inMemory = state.getDocument(uri)
	if inMemory && content != "" {
		return content, true, true
	}

	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false, inMemory
	}

	bytes, err := os.ReadFile(parsed.Path)
	if err != nil {
		return "", false, inMemory
	}

	content = string(bytes)
	if content != "" {
		state.setDocument(uri, content, 0)
		return content, true, inMemory
	}

	return "", false, inMemory
}
