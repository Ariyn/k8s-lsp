package validator

import (
	"os"
	"testing"

	"k8s-lsp/pkg/indexer"
	"k8s-lsp/pkg/yamlstream"

	"gopkg.in/yaml.v3"
)

func TestFindNodes_SupportsBracketStarSegments(t *testing.T) {
	yamlText := `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ing
spec:
  rules:
    - http:
        paths:
          - backend:
              service:
                name: svc-a
          - backend:
              service:
                name: svc-b
`

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		t.Fatalf("unexpected yaml doc")
	}
	root := doc.Content[0]

	nodes := findNodes(root, "spec.rules[*].http.paths[*].backend.service.name")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	got := []string{nodes[0].Value, nodes[1].Value}
	if got[0] != "svc-a" || got[1] != "svc-b" {
		t.Fatalf("unexpected values: %#v", got)
	}
}

func TestValidate_ResourceMatchAccessModesSequence(t *testing.T) {
	// Create a PV on disk so getValueFromResource can load it.
	pvYAML := `apiVersion: v1
kind: PersistentVolume
metadata:
  name: pv1
spec:
  accessModes:
    - ReadWriteOnce
  capacity:
    storage: 1Gi
`
	pvFile, err := os.CreateTemp("", "pv-*.yaml")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer os.Remove(pvFile.Name())
	if _, err := pvFile.WriteString(pvYAML); err != nil {
		t.Fatalf("write pv: %v", err)
	}
	_ = pvFile.Close()

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "PersistentVolume", Namespace: "", Name: "pv1", FilePath: pvFile.Name()})

	v := &Validator{
		store: store,
		rules: []Rule{{
			Kind: "PersistentVolumeClaim",
			Checks: []Check{{
				Type:           "resource-match",
				Path:           "spec.volumeName",
				TargetKind:     "PersistentVolume",
				SourceProperty: "spec.accessModes",
				TargetProperty: "spec.accessModes",
				Message:        "Access modes mismatch",
			}},
		}},
	}

	pvcYAML := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc1
spec:
  volumeName: pv1
  accessModes:
    - ReadOnlyMany
`
	stream, err := yamlstream.Parse(pvcYAML)
	if err != nil {
		t.Fatalf("parse pvc: %v", err)
	}
	diags := v.ValidateStream("file:///pvc.yaml", stream)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if got := diags[0].Message; got == "" || got[:len("Access modes mismatch")] != "Access modes mismatch" {
		t.Fatalf("unexpected diag message: %q", got)
	}
	// Values should be comparable strings (comma-joined lists).
	if wantSub := "spec.accessModes (ReadOnlyMany) != spec.accessModes (ReadWriteOnce)"; diags[0].Message == "" || !contains(diags[0].Message, wantSub) {
		t.Fatalf("expected message to contain %q, got %q", wantSub, diags[0].Message)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
