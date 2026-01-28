package lsp

import (
	"testing"

	"k8s-lsp/pkg/schema"
	"k8s-lsp/pkg/yamlstream"
)

func TestBuildSemanticTokensData_Smoke(t *testing.T) {
	y := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  paused: false
`
	stream, err := yamlstream.Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg := schema.NewRegistry()
	schema.RegisterBuiltins(reg)

	data := buildSemanticTokensData(stream, reg)
	if len(data) == 0 {
		t.Fatalf("expected non-empty semantic tokens")
	}
	if len(data)%5 != 0 {
		t.Fatalf("expected data length multiple of 5, got %d", len(data))
	}

	// Ensure we emit at least one 'type' token (for kind: Deployment).
	typeIdx := semanticTokenTypeIndex["type"]
	data32 := make([]uint32, 0, len(data))
	for _, v := range data {
		data32 = append(data32, uint32(v))
	}
	tokens := decodeSemanticTokens(data32)
	found := false
	for _, tok := range tokens {
		if tok.typeIdx == typeIdx {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one 'type' token")
	}
}

type decodedToken struct {
	line      int
	startChar int
	length    int
	typeIdx   int
	mods      uint32
}

func decodeSemanticTokens(data []uint32) []decodedToken {
	out := make([]decodedToken, 0, len(data)/5)
	prevLine := 0
	prevStart := 0
	for i := 0; i+4 < len(data); i += 5 {
		deltaLine := int(data[i])
		deltaStart := int(data[i+1])
		length := int(data[i+2])
		typeIdx := int(data[i+3])
		mods := uint32(data[i+4])

		line := prevLine + deltaLine
		start := deltaStart
		if deltaLine == 0 {
			start = prevStart + deltaStart
		}
		out = append(out, decodedToken{line: line, startChar: start, length: length, typeIdx: typeIdx, mods: mods})
		prevLine = line
		prevStart = start
	}
	return out
}
