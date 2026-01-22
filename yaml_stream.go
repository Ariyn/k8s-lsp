package main

import "k8s-lsp/pkg/yamlstream"

func getYAMLStreamForContent(uri string, content string) (*yamlstream.Stream, error) {
	if state == nil {
		return yamlstream.Parse(content)
	}
	_, ver, _ := state.getDocument(uri)
	return state.YAMLCache.Get(uri, ver, content)
}
