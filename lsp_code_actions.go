package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	"github.com/rs/zerolog/log"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"gopkg.in/yaml.v3"
)

var (
	reRefNotFound = regexp.MustCompile(`\(Kind:\s*([^,\)]+),\s*Name:\s*([^\)]+)\)\s*$`)
	reResourceMismatch = regexp.MustCompile(`^([^:]+):\s*([^\s]+)\s*\(([^\)]*)\)\s*!=\s*([^\s]+)\s*\(([^\)]*)\)\s*$`)
)

func textDocumentCodeAction(context *glsp.Context, params *protocol.CodeActionParams) (any, error) {
	uri := params.TextDocument.URI
	content, ok := state.Documents[uri]
	if !ok || content == "" {
		return nil, nil
	}

	ver := state.DocVersion[uri]
	stream, err := state.YAMLCache.Get(uri, ver, content)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse YAML for codeAction")
		return nil, nil
	}

	currentNamespace := namespaceForLine(stream, int(params.Range.Start.Line))
	if currentNamespace == "" {
		currentNamespace = "default"
	}

	var actions []protocol.CodeAction
	// Cursor-context actions (e.g., embedded ConfigMap file).
	actions = append(actions, buildCursorContextActions(uri, content, stream, currentNamespace, int(params.Range.Start.Line), int(params.Range.Start.Character))...)

	// A single, safe source action to apply all unambiguous fixes.
	if fixAll := buildFixAllAction(uri, content, stream, currentNamespace, params.Context.Diagnostics); fixAll != nil {
		actions = append(actions, *fixAll)
	}

	for _, diag := range params.Context.Diagnostics {
		if diag.Source == nil || *diag.Source != lsName {
			continue
		}

		// 1) Missing reference quick fixes.
		if kind, missingName, ok := parseMissingRefDiagnostic(diag); ok {
			actions = append(actions, buildReplaceWithExistingActions(uri, diag, kind, currentNamespace)...)
			if stub := buildCreateMissingResourceAction(uri, content, diag, kind, missingName, currentNamespace); stub != nil {
				actions = append(actions, *stub)
			}
			continue
		}

		// 2) Selector mismatch (Service/NetworkPolicy) -> offer to copy selector from an existing Deployment.
		if strings.HasPrefix(diag.Message, "No Deployment found matching this selector") {
			actions = append(actions, buildSelectorFixActions(uri, content, stream, diag, currentNamespace)...)
			continue
		}

		// 3) PVC/PV mismatch fixups from resource-match checks.
		if mm := parseResourceMismatchDiagnostic(diag); mm != nil {
			actions = append(actions, buildResourceMismatchFixActions(uri, content, stream, diag, currentNamespace, mm)...)
			continue
		}
	}

	if len(actions) == 0 {
		return nil, nil
	}

	// Keep ordering stable for UX.
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].Title < actions[j].Title
	})

	return actions, nil
}

type resourceMismatch struct {
	message    string
	sourcePath string
	sourceVal  string
	targetPath string
	targetVal  string
}

func parseResourceMismatchDiagnostic(diag protocol.Diagnostic) *resourceMismatch {
	m := reResourceMismatch.FindStringSubmatch(diag.Message)
	if len(m) != 6 {
		return nil
	}
	return &resourceMismatch{
		message:    strings.TrimSpace(m[1]),
		sourcePath: strings.TrimSpace(m[2]),
		sourceVal:  m[3],
		targetPath: strings.TrimSpace(m[4]),
		targetVal:  m[5],
	}
}

func parseMissingRefDiagnostic(diag protocol.Diagnostic) (kind string, name string, ok bool) {
	m := reRefNotFound.FindStringSubmatch(diag.Message)
	if len(m) != 3 {
		return "", "", false
	}
	kind = strings.TrimSpace(m[1])
	name = strings.TrimSpace(m[2])
	if kind == "" || name == "" {
		return "", "", false
	}
	return kind, name, true
}

func buildReplaceWithExistingActions(uri string, diag protocol.Diagnostic, targetKind string, currentNamespace string) []protocol.CodeAction {
	candidates := state.Store.ListByKind(targetKind)
	if len(candidates) == 0 {
		return nil
	}

	clusterScoped := isClusterScopedKind(targetKind)

	var filtered []*indexer.K8sResource
	for _, res := range candidates {
		if res == nil || res.Name == "" {
			continue
		}
		if clusterScoped {
			filtered = append(filtered, res)
			continue
		}
		if normalizeNamespace(res.Namespace) == normalizeNamespace(currentNamespace) {
			filtered = append(filtered, res)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	// Sort by name for deterministic output.
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})

	kindQuickFix := protocol.CodeActionKindQuickFix
	preferred := true

	var actions []protocol.CodeAction
	for i, res := range filtered {
		repl := protocol.TextEdit{Range: diag.Range, NewText: res.Name}
		ed := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{uri: {repl}}}

		a := protocol.CodeAction{
			Title:       fmt.Sprintf("Replace with %s '%s'", targetKind, res.Name),
			Kind:        &kindQuickFix,
			Diagnostics: []protocol.Diagnostic{diag},
			Edit:        &ed,
		}
		if i == 0 {
			a.IsPreferred = &preferred
		}
		actions = append(actions, a)
	}
	return actions
}

func buildCreateMissingResourceAction(uri string, docContent string, diag protocol.Diagnostic, missingKind string, missingName string, currentNamespace string) *protocol.CodeAction {
	stub, ok := resourceStubYAML(missingKind, missingName, currentNamespace)
	if !ok {
		return nil
	}

	insertText := "\n---\n" + stub
	if strings.HasSuffix(docContent, "\n") {
		insertText = "---\n" + stub
		// ensure it’s on a new doc boundary
		if !strings.HasSuffix(docContent, "\n---\n") && !strings.HasSuffix(docContent, "\n---\r\n") {
			insertText = "\n---\n" + stub
		}
	}

	pos := endPosition(docContent)
	edit := protocol.TextEdit{Range: protocol.Range{Start: pos, End: pos}, NewText: insertText}

	kindQuickFix := protocol.CodeActionKindQuickFix
	preferred := true
	ws := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{uri: {edit}}}

	ca := protocol.CodeAction{
		Title:       fmt.Sprintf("Create %s '%s'", missingKind, missingName),
		Kind:        &kindQuickFix,
		Diagnostics: []protocol.Diagnostic{diag},
		IsPreferred: &preferred,
		Edit:        &ws,
	}
	return &ca
}

func buildFixAllAction(uri string, docContent string, stream *yamlstream.Stream, currentNamespace string, diags []protocol.Diagnostic) *protocol.CodeAction {
	// Safe-by-default: only apply unambiguous replacements (exactly one candidate in-scope).
	var edits []protocol.TextEdit
	for _, diag := range diags {
		if diag.Source == nil || *diag.Source != lsName {
			continue
		}
		kind, _, ok := parseMissingRefDiagnostic(diag)
		if !ok {
			continue
		}
		candidates := state.Store.ListByKind(kind)
		clusterScoped := isClusterScopedKind(kind)
		var filtered []string
		for _, res := range candidates {
			if res == nil || res.Name == "" {
				continue
			}
			if clusterScoped || normalizeNamespace(res.Namespace) == normalizeNamespace(currentNamespace) {
				filtered = append(filtered, res.Name)
			}
		}
		sort.Strings(filtered)
		if len(filtered) != 1 {
			continue
		}
		edits = append(edits, protocol.TextEdit{Range: diag.Range, NewText: filtered[0]})
	}
	if len(edits) == 0 {
		return nil
	}

	// Apply from bottom to top to avoid shifting ranges.
	sort.SliceStable(edits, func(i, j int) bool {
		a := edits[i].Range.Start
		b := edits[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})

	ws := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{uri: edits}}
	k := protocol.CodeActionKind("source.fixAll.k8s-lsp")
	preferred := true
	return &protocol.CodeAction{
		Title:       "Fix all (unambiguous)",
		Kind:        &k,
		IsPreferred: &preferred,
		Edit:        &ws,
	}
}

func buildCursorContextActions(uri string, content string, stream *yamlstream.Stream, currentNamespace string, line0, col0 int) []protocol.CodeAction {
	// Today: embedded ConfigMap file actions.
	if stream == nil {
		return nil
	}
	line1 := line0 + 1
	col1 := col0 + 1
	doc := stream.DocForLine(line1)
	if doc == nil || doc.Node == nil {
		return nil
	}

	// Detect configmap embedded key at cursor.
	kind := findYAMLString(doc.Node, "kind")
	if kind != "ConfigMap" {
		return nil
	}
	cmName := findYAMLString(doc.Node, "metadata", "name")
	if cmName == "" {
		cmName = "configmap"
	}
	ns := findYAMLString(doc.Node, "metadata", "namespace")
	if ns == "" {
		ns = currentNamespace
	}

	node, parent, path := findNodeAtInDoc(doc.Node, line1, col1)
	if node == nil || parent == nil || node.Kind != yaml.ScalarNode {
		return nil
	}
	if len(path) < 2 {
		return nil
	}
	if path[len(path)-2] != "data" && path[len(path)-2] != "binaryData" {
		return nil
	}
	if !strings.Contains(node.Value, ".") {
		return nil
	}

	// Check the value node style is literal/folded (same as hover behavior).
	var valNode *yaml.Node
	if parent.Kind == yaml.MappingNode {
		for i := 0; i < len(parent.Content); i += 2 {
			if parent.Content[i] == node {
				if i+1 < len(parent.Content) {
					valNode = parent.Content[i+1]
				}
				break
			}
		}
	}
	if valNode == nil || (valNode.Style != yaml.LiteralStyle && valNode.Style != yaml.FoldedStyle) {
		return nil
	}

	sourceEncoded := base64.URLEncoding.EncodeToString([]byte(uri))
	keyEncoded := base64.URLEncoding.EncodeToString([]byte(node.Value))
	embeddedURI := fmt.Sprintf("k8s-embedded://%s/%s/%s?source=%s&key=%s", ns, cmName, node.Value, sourceEncoded, keyEncoded)

	kindQuickFix := protocol.CodeActionKindQuickFix

	open := protocol.CodeAction{
		Title: "Open embedded file",
		Kind:  &kindQuickFix,
		Command: &protocol.Command{
			Title:     "Open embedded file",
			Command:   "k8sLsp.openEmbeddedFile",
			Arguments: []any{map[string]any{"uri": embeddedURI}},
		},
	}
	find := protocol.CodeAction{
		Title: "Find embedded file usages",
		Kind:  &kindQuickFix,
		Command: &protocol.Command{
			Title:   "Find embedded file usages",
			Command: "k8sLsp.findEmbeddedFileUsages",
			Arguments: []any{map[string]any{
				"uri":      uri,
				"position": map[string]any{"line": line0, "character": col0},
			}},
		},
	}

	return []protocol.CodeAction{open, find}
}

func buildSelectorFixActions(uri string, content string, stream *yamlstream.Stream, diag protocol.Diagnostic, currentNamespace string) []protocol.CodeAction {
	if stream == nil {
		return nil
	}
	line0 := int(diag.Range.Start.Line)
	col0 := int(diag.Range.Start.Character)
	line1 := line0 + 1
	col1 := col0 + 1
	doc := stream.DocForLine(line1)
	if doc == nil || doc.Node == nil {
		return nil
	}

	node, parent, _ := findNodeAtInDoc(doc.Node, line1, col1)
	if node == nil {
		return nil
	}
	// Prefer the mapping node itself.
	selectorNode := node
	if selectorNode.Kind != yaml.MappingNode && parent != nil && parent.Kind == yaml.MappingNode {
		selectorNode = parent
	}
	if selectorNode.Kind != yaml.MappingNode {
		return nil
	}

	deploys := state.Store.ListByKind("Deployment")
	var candidates []*indexer.K8sResource
	for _, d := range deploys {
		if d == nil || d.Name == "" {
			continue
		}
		if normalizeNamespace(d.Namespace) != normalizeNamespace(currentNamespace) {
			continue
		}
		if len(d.Labels) == 0 {
			continue
		}
		candidates = append(candidates, d)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	if len(candidates) > 10 {
		candidates = candidates[:10]
	}

	start, end := nodeSpanPositions(content, selectorNode)
	if start == nil || end == nil {
		return nil
	}

	kindQuickFix := protocol.CodeActionKindQuickFix
	preferred := true
	var actions []protocol.CodeAction
	for i, d := range candidates {
		repl := protocol.TextEdit{Range: protocol.Range{Start: *start, End: *end}, NewText: renderMapping(d.Labels, selectorNode.Column-1)}
		ws := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{uri: {repl}}}
		a := protocol.CodeAction{
			Title:       fmt.Sprintf("Set selector to match Deployment '%s'", d.Name),
			Kind:        &kindQuickFix,
			Diagnostics: []protocol.Diagnostic{diag},
			Edit:        &ws,
		}
		if i == 0 {
			a.IsPreferred = &preferred
		}
		actions = append(actions, a)
	}
	return actions
}

func buildResourceMismatchFixActions(uri string, content string, stream *yamlstream.Stream, diag protocol.Diagnostic, currentNamespace string, mm *resourceMismatch) []protocol.CodeAction {
	// Currently only supports PVC -> PV fixes.
	if stream == nil || mm == nil {
		return nil
	}
	// Diagnostic range for resource-match is on the reference scalar (e.g. spec.volumeName).
	line0 := int(diag.Range.Start.Line)
	col0 := int(diag.Range.Start.Character)
	line1 := line0 + 1
	col1 := col0 + 1
	doc := stream.DocForLine(line1)
	if doc == nil || doc.Node == nil {
		return nil
	}

	refNode, _, _ := findNodeAtInDoc(doc.Node, line1, col1)
	if refNode == nil || refNode.Kind != yaml.ScalarNode {
		return nil
	}
	pvName := strings.TrimSpace(refNode.Value)
	if pvName == "" {
		return nil
	}

	pvRes := state.Store.Get("PersistentVolume", "", pvName)
	if pvRes == nil {
		pvRes = state.Store.Get("PersistentVolume", "default", pvName)
	}
	if pvRes == nil || pvRes.FilePath == "" {
		return nil
	}

	// Load PV content.
	pvURI := filePathToURI(pvRes.FilePath)
	pvContent := state.Documents[pvURI]
	if pvContent == "" {
		b, err := os.ReadFile(pvRes.FilePath)
		if err != nil {
			return nil
		}
		pvContent = string(b)
	}
	pvStream, err := yamlstream.Parse(pvContent)
	if err != nil {
		return nil
	}

	// Find PVC source node.
	pvcSource := findNodeByPath(doc.Node, mm.sourcePath)
	if pvcSource == nil {
		return nil
	}

	// Find PV target node in the correct document.
	pvDoc := findResourceDoc(pvStream, "PersistentVolume", pvName, "")
	if pvDoc == nil {
		return nil
	}
	pvTarget := findNodeByPath(pvDoc, mm.targetPath)
	if pvTarget == nil {
		return nil
	}

	kindQuickFix := protocol.CodeActionKindQuickFix
	preferred := true
	var actions []protocol.CodeAction

	// A) Set PVC to PV.
	if edit := buildCopyValueEdit(content, uri, pvcSource, pvTarget); edit != nil {
		ws := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{uri: {*edit}}}
		actions = append(actions, protocol.CodeAction{
			Title:       fmt.Sprintf("Set %s to match PV '%s'", mm.sourcePath, pvName),
			Kind:        &kindQuickFix,
			Diagnostics: []protocol.Diagnostic{diag},
			IsPreferred: &preferred,
			Edit:        &ws,
		})
	}

	// B) Set PV to PVC (only if editing PV node is feasible).
	if edit := buildCopyValueEdit(pvContent, pvURI, pvTarget, pvcSource); edit != nil {
		ws := protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{pvURI: {*edit}}}
		actions = append(actions, protocol.CodeAction{
			Title:       fmt.Sprintf("Set %s on PV '%s' to match %s", mm.targetPath, pvName, mm.sourcePath),
			Kind:        &kindQuickFix,
			Diagnostics: []protocol.Diagnostic{diag},
			Edit:        &ws,
		})
	}

	return actions
}

func buildCopyValueEdit(docContent string, docURI string, dst *yaml.Node, src *yaml.Node) *protocol.TextEdit {
	if dst == nil || src == nil {
		return nil
	}
	start, end := nodeSpanPositions(docContent, dst)
	if start == nil || end == nil {
		return nil
	}
	newText := renderNodeValue(src, dst.Column-1)
	if newText == "" {
		return nil
	}
	return &protocol.TextEdit{Range: protocol.Range{Start: *start, End: *end}, NewText: newText}
}

func findResourceDoc(stream *yamlstream.Stream, kind string, name string, namespace string) *yaml.Node {
	if stream == nil {
		return nil
	}
	for _, d := range stream.Docs {
		docNode := d.Node
		if docNode == nil {
			continue
		}
		k := findYAMLString(docNode, "kind")
		if k != kind {
			continue
		}
		n := findYAMLString(docNode, "metadata", "name")
		if n != name {
			continue
		}
		if !isClusterScopedKind(kind) {
			ns := findYAMLString(docNode, "metadata", "namespace")
			if normalizeNamespace(ns) != normalizeNamespace(namespace) {
				continue
			}
		}
		return docNode
	}
	return nil
}

func findNodeByPath(docNode *yaml.Node, path string) *yaml.Node {
	parts := strings.Split(path, ".")
	cur := docNode
	if cur == nil {
		return nil
	}
	if cur.Kind == yaml.DocumentNode && len(cur.Content) > 0 {
		cur = cur.Content[0]
	}

	for _, raw := range parts {
		key, expandSeq := normalizePathPart(raw)
		if cur == nil {
			return nil
		}
		switch cur.Kind {
		case yaml.MappingNode:
			cur = yamlMapValue(cur, key)
			if expandSeq && cur != nil && cur.Kind == yaml.SequenceNode {
				if len(cur.Content) == 0 {
					return nil
				}
				cur = cur.Content[0]
			}
		case yaml.SequenceNode:
			if key == "*" {
				if len(cur.Content) == 0 {
					return nil
				}
				cur = cur.Content[0]
				continue
			}
			// Search first element that has the mapping key.
			var next *yaml.Node
			for _, item := range cur.Content {
				if item != nil && item.Kind == yaml.MappingNode {
					if v := yamlMapValue(item, key); v != nil {
						next = v
						break
					}
				}
			}
			cur = next
			if expandSeq && cur != nil && cur.Kind == yaml.SequenceNode {
				if len(cur.Content) == 0 {
					return nil
				}
				cur = cur.Content[0]
			}
		default:
			return nil
		}
	}
	return cur
}

func normalizePathPart(part string) (key string, expandSeq bool) {
	key = part
	if strings.HasSuffix(key, "[*]") {
		key = strings.TrimSuffix(key, "[*]")
		return key, true
	}
	if strings.HasSuffix(key, "[]") {
		key = strings.TrimSuffix(key, "[]")
		return key, true
	}
	return key, false
}

func renderNodeValue(node *yaml.Node, indent int) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value
	case yaml.SequenceNode:
		// Render YAML list with current indentation.
		pad := strings.Repeat(" ", max(0, indent))
		var b strings.Builder
		for i, c := range node.Content {
			if c == nil || c.Kind != yaml.ScalarNode {
				continue
			}
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(pad)
			b.WriteString("- ")
			b.WriteString(c.Value)
		}
		return b.String()
	case yaml.MappingNode:
		// Not used currently.
		return ""
	default:
		return ""
	}
}

func renderMapping(m map[string]string, indent int) string {
	pad := strings.Repeat(" ", max(0, indent))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(pad)
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(m[k])
	}
	return b.String()
}

func nodeSpanPositions(docContent string, node *yaml.Node) (*protocol.Position, *protocol.Position) {
	if node == nil {
		return nil, nil
	}
	start := protocol.Position{Line: uint32(node.Line - 1), Character: uint32(max(0, node.Column-1))}
	end := nodeEndPosition(docContent, node)
	return &start, &end
}

func nodeEndPosition(docContent string, node *yaml.Node) protocol.Position {
	// Conservative-but-usable end position: walk subtree and pick the furthest (line, col).
	maxLine := node.Line
	maxColEnd := node.Column

	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Line > 0 {
			line := n.Line
			colEnd := n.Column
			if n.Kind == yaml.ScalarNode {
				colEnd = n.Column + len(n.Value)
				if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
					colEnd += 2
				}
			}
			if line > maxLine || (line == maxLine && colEnd > maxColEnd) {
				maxLine = line
				maxColEnd = colEnd
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(node)

	lines := strings.Split(docContent, "\n")
	line0 := max(0, maxLine-1)
	if line0 >= len(lines) {
		return endPosition(docContent)
	}
	// If we ended on a non-scalar container node, we might only have its start column.
	// In that case, fall back to end-of-line.
	col0 := max(0, maxColEnd-1)
	if col0 > len(lines[line0]) {
		col0 = len(lines[line0])
	}
	return protocol.Position{Line: uint32(line0), Character: uint32(col0)}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func resourceStubYAML(kind, name, namespace string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}

	ns := normalizeNamespace(namespace)
	useNamespace := !isClusterScopedKind(kind)

	switch kind {
	case "ConfigMap":
		b := &strings.Builder{}
		b.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ")
		b.WriteString(name)
		if useNamespace {
			b.WriteString("\n  namespace: ")
			b.WriteString(ns)
		}
		b.WriteString("\ndata: {}\n")
		return b.String(), true
	case "Secret":
		b := &strings.Builder{}
		b.WriteString("apiVersion: v1\nkind: Secret\nmetadata:\n  name: ")
		b.WriteString(name)
		if useNamespace {
			b.WriteString("\n  namespace: ")
			b.WriteString(ns)
		}
		b.WriteString("\ntype: Opaque\ndata: {}\n")
		return b.String(), true
	case "ServiceAccount":
		b := &strings.Builder{}
		b.WriteString("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: ")
		b.WriteString(name)
		if useNamespace {
			b.WriteString("\n  namespace: ")
			b.WriteString(ns)
		}
		b.WriteString("\n")
		return b.String(), true
	case "Service":
		b := &strings.Builder{}
		b.WriteString("apiVersion: v1\nkind: Service\nmetadata:\n  name: ")
		b.WriteString(name)
		if useNamespace {
			b.WriteString("\n  namespace: ")
			b.WriteString(ns)
		}
		b.WriteString("\nspec:\n  selector: {}\n  ports:\n    - port: 80\n      targetPort: 80\n")
		return b.String(), true
	case "PersistentVolumeClaim":
		b := &strings.Builder{}
		b.WriteString("apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: ")
		b.WriteString(name)
		if useNamespace {
			b.WriteString("\n  namespace: ")
			b.WriteString(ns)
		}
		b.WriteString("\nspec:\n  accessModes:\n    - ReadWriteOnce\n  resources:\n    requests:\n      storage: 1Gi\n")
		return b.String(), true
	case "StorageClass":
		b := &strings.Builder{}
		b.WriteString("apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata:\n  name: ")
		b.WriteString(name)
		b.WriteString("\nprovisioner: kubernetes.io/no-provisioner\n")
		return b.String(), true
	default:
		return "", false
	}
}

func endPosition(content string) protocol.Position {
	// LSP positions are 0-based.
	if content == "" {
		return protocol.Position{Line: 0, Character: 0}
	}

	lines := strings.Split(content, "\n")
	line := uint32(len(lines) - 1)
	char := uint32(len(lines[len(lines)-1]))
	return protocol.Position{Line: line, Character: char}
}

func namespaceForLine(stream *yamlstream.Stream, line0 int) string {
	if stream == nil {
		return ""
	}
	line1 := line0 + 1
	doc := stream.DocForLine(line1)
	if doc == nil || doc.Node == nil {
		return ""
	}
	return findYAMLString(doc.Node, "metadata", "namespace")
}

func findYAMLString(docNode *yaml.Node, keys ...string) string {
	if docNode == nil {
		return ""
	}
	root := docNode
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	cur := root
	for _, k := range keys {
		cur = yamlMapValue(cur, k)
		if cur == nil {
			return ""
		}
	}
	if cur.Kind == yaml.ScalarNode {
		return cur.Value
	}
	return ""
}

func yamlMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		v := node.Content[i+1]
		if k != nil && k.Value == key {
			return v
		}
	}
	return nil
}

func normalizeNamespace(ns string) string {
	if strings.TrimSpace(ns) == "" {
		return "default"
	}
	return ns
}

func isClusterScopedKind(kind string) bool {
	switch kind {
	case "Namespace", "Node", "PersistentVolume", "StorageClass", "ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition":
		return true
	default:
		return false
	}
}

func uriDir(uri string) string {
	p := uriToPath(uri)
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
}
