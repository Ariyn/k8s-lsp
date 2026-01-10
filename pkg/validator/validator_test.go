package validator

import (
	"path/filepath"
	"testing"

	"k8s-lsp/pkg/indexer"
)

func TestValidate_MultiDoc_SecondDocument(t *testing.T) {
	store := indexer.NewStore()
	val, err := NewValidator(filepath.Join("..", "..", "rules", "validation.yaml"), store)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	content := `
apiVersion: v1
kind: Service
metadata:
  name: svc
spec:
  selector:
    app: my-app
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      serviceAccountName: missing-sa
`

	diags := val.Validate("file:///tmp/multi.yaml", content)
	if len(diags) == 0 {
		t.Fatalf("Expected diagnostics for missing ServiceAccount in doc #2")
	}

	found := false
	for _, d := range diags {
		if d.Message != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Expected at least one diagnostic message")
	}
}
