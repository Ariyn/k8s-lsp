package lsp

import "net/url"

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
