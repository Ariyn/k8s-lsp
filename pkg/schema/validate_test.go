package schema

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeSingleDoc(t *testing.T, content string) *yaml.Node {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(content))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	return &doc
}

func TestValidateDocument_UnknownField_TopLevel(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	y := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metdata:\n" +
		"  name: dep\n" +
		"spec: {}\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "apps", Version: "v1", Kind: "Deployment"})
	if root == nil {
		t.Fatalf("expected built-in schema")
	}

	diags := ValidateDocument(doc, root)
	if len(diags) == 0 {
		t.Fatalf("expected at least 1 diagnostic")
	}

	found := false
	foundCode := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Unknown field") && strings.Contains(d.Message, "metdata") {
			found = true
			if d.Code != nil {
				if s, ok := d.Code.Value.(string); ok && s == "k8s.schema.unknownField" {
					foundCode = true
				}
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected unknown field diagnostic for metdata, got: %#v", diags)
	}
	if !foundCode {
		t.Fatalf("expected diagnostic code k8s.schema.unknownField, got: %#v", diags)
	}
}

func TestValidateDocument_TypeMismatch_Integer(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	y := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metadata:\n" +
		"  name: dep\n" +
		"spec:\n" +
		"  replicas: \"three\"\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "apps", Version: "v1", Kind: "Deployment"})
	if root == nil {
		t.Fatalf("expected built-in schema")
	}

	diags := ValidateDocument(doc, root)
	found := false
	foundCode := false
	foundSuggestions := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Type mismatch") && strings.Contains(d.Message, "expected integer") {
			found = true
			if d.Code != nil {
				if s, ok := d.Code.Value.(string); ok && s == "k8s.schema.typeMismatch" {
					foundCode = true
				}
			}
			if d.Data != nil {
				if m, ok := d.Data.(map[string]any); ok {
					if sugg, ok := m["suggestions"]; ok && sugg != nil {
						foundSuggestions = true
					}
				}
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected type mismatch diagnostic, got: %#v", diags)
	}
	if !foundCode {
		t.Fatalf("expected diagnostic code k8s.schema.typeMismatch, got: %#v", diags)
	}
	if !foundSuggestions {
		t.Fatalf("expected suggestions in diagnostic data, got: %#v", diags)
	}
}

func TestValidateDocument_DeploymentPodSpec_CommonFields_NotUnknown(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)

	// Load the richer Deployment schema from the shipped CRD-like YAML.
	// The built-in schema stays intentionally minimal.
	path := filepath.Join("..", "..", "rules", "schemas", "deployment.apps.v1.schema.yaml")
	loaded, err := LoadCRDSchemasFromFile(reg, path)
	if err != nil {
		t.Fatalf("failed to load local schema %q: %v", path, err)
	}
	if loaded == 0 {
		t.Fatalf("expected at least 1 schema loaded from %q", path)
	}

	y := "" +
		"apiVersion: apps/v1\n" +
		"kind: Deployment\n" +
		"metadata:\n" +
		"  name: dep\n" +
		"spec:\n" +
		"  template:\n" +
		"    spec:\n" +
		"      serviceAccountName: sa\n" +
		"      enableServiceLinks: true\n" +
		"      hostAliases:\n" +
		"        - ip: \"127.0.0.1\"\n" +
		"          hostnames: [\"local\"]\n" +
		"      dnsConfig:\n" +
		"        nameservers: [\"8.8.8.8\"]\n" +
		"      initContainers:\n" +
		"        - name: init\n" +
		"          image: busybox\n" +
		"      volumes:\n" +
		"        - name: data\n" +
		"          persistentVolumeClaim:\n" +
		"            claimName: mypvc\n" +
		"      containers:\n" +
		"        - name: app\n" +
		"          image: nginx\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "apps", Version: "v1", Kind: "Deployment"})
	if root == nil {
		t.Fatalf("expected schema for apps/v1 Deployment")
	}

	diags := ValidateDocument(doc, root)
	for _, d := range diags {
		if strings.Contains(d.Message, "Unknown field") &&
			(strings.Contains(d.Message, "serviceAccountName") ||
				strings.Contains(d.Message, "initContainers") ||
				strings.Contains(d.Message, "volumes") ||
				strings.Contains(d.Message, "enableServiceLinks") ||
				strings.Contains(d.Message, "hostAliases") ||
				strings.Contains(d.Message, "dnsConfig")) {
			t.Fatalf("unexpected unknown field diagnostic: %#v", d)
		}
	}
}

func TestValidateDocument_EnumMismatch(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	_, _ = LoadCRDSchemasFromFile(reg, filepath.Join("..", "..", "rules", "schemas", "service.core.v1.schema.yaml"))

	y := "" +
		"apiVersion: v1\n" +
		"kind: Service\n" +
		"metadata:\n" +
		"  name: svc\n" +
		"spec:\n" +
		"  type: CluserIP\n"

	doc := decodeSingleDoc(t, y)
	root := reg.Get(GVK{Group: "", Version: "v1", Kind: "Service"})
	if root == nil {
		t.Fatalf("expected schema for core/v1 Service")
	}

	diags := ValidateDocument(doc, root)
	found := false
	foundCode := false
	foundAllowed := false
	for _, d := range diags {
		if strings.Contains(d.Message, "Invalid value") && strings.Contains(d.Message, "CluserIP") {
			found = true
			if d.Code != nil {
				if s, ok := d.Code.Value.(string); ok && s == "k8s.schema.enumMismatch" {
					foundCode = true
				}
			}
			if d.Data != nil {
				if m, ok := d.Data.(map[string]any); ok {
					if a, ok := m["allowed"]; ok && a != nil {
						foundAllowed = true
					}
				}
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected enum mismatch diagnostic, got: %#v", diags)
	}
	if !foundCode {
		t.Fatalf("expected diagnostic code k8s.schema.enumMismatch, got: %#v", diags)
	}
	if !foundAllowed {
		t.Fatalf("expected allowed values in diagnostic data, got: %#v", diags)
	}
}
