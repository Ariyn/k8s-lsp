package main

import (
	"fmt"
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
	for _, diag := range params.Context.Diagnostics {
		if diag.Source == nil || *diag.Source != lsName {
			continue
		}

		kind, missingName, ok := parseMissingRefDiagnostic(diag)
		if !ok {
			continue
		}

		actions = append(actions, buildReplaceWithExistingActions(uri, diag, kind, currentNamespace)...)
		if stub := buildCreateMissingResourceAction(uri, content, diag, kind, missingName, currentNamespace); stub != nil {
			actions = append(actions, *stub)
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
