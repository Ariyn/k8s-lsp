package lsp

import "k8s-lsp/pkg/yamlstream"

func withYAMLStream[T any](uri string, content string, fn func(stream *yamlstream.Stream) (T, error)) (T, error) {
	var zero T
	stream, err := getYAMLStreamForContent(uri, content)
	if err != nil {
		return zero, err
	}
	return fn(stream)
}
