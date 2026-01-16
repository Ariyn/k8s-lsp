package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ImportsValidationReferenceChecks(t *testing.T) {
	root := t.TempDir()
	rulesDir := filepath.Join(root, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatalf("mkdir rules: %v", err)
	}

	// Includes one selector-style reference (should be skipped) and one scalar-name reference (should be included).
	validation := `rules:
  - kind: "Service"
    checks:
      - type: "reference"
        path: "spec.selector"
        targetKind: "Deployment"
        targetPath: "spec.template.metadata.labels"
        message: "No Deployment found matching this selector"

  - kind: "PersistentVolumeClaim"
    checks:
      - type: "reference"
        path: "spec.storageClassName"
        targetKind: "StorageClass"
        targetPath: "metadata.name"
        message: "StorageClass not found"
`

	if err := os.WriteFile(filepath.Join(rulesDir, "validation.yaml"), []byte(validation), 0o644); err != nil {
		t.Fatalf("write validation.yaml: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	var foundStorageClass bool
	var foundSelector bool
	for _, r := range cfg.References {
		if r.TargetKind == "StorageClass" && r.Match.Path == "spec.storageClassName" {
			foundStorageClass = true
		}
		if r.Match.Path == "spec.selector" {
			foundSelector = true
		}
	}

	if !foundStorageClass {
		t.Fatalf("expected validation reference for StorageClass to be imported")
	}
	if foundSelector {
		t.Fatalf("expected selector-style validation reference to be skipped")
	}
}
