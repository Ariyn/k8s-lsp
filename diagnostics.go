package main

import (
	"time"

	"k8s-lsp/pkg/schema"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

func publishDiagnostics(context *glsp.Context, uri string, content string) {
	if context == nil {
		return
	}
	if state.Validator == nil {
		return
	}
	state.setNotifyContext(context)

	// Reference-based diagnostics depend on the workspace store.
	// If the initial workspace scan is still running, wait briefly so we don't
	// publish stale "not found" warnings that would disappear once indexing finishes.
	_ = waitForWorkspaceScanOnce(defaultScanWaitTimeout)

	stream, err := getYAMLStreamForContent(uri, content)
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
	state.setNotifyContext(context)
	content, _, ok := state.getDocument(uri)
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
