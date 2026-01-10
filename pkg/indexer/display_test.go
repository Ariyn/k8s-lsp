package indexer

import "testing"

func TestFormatResourceID(t *testing.T) {
	if got := FormatResourceID("Service", "default", "my-svc"); got != "Service default/my-svc" {
		t.Fatalf("unexpected: %q", got)
	}
	if got := FormatResourceID("Service", "", "my-svc"); got != "Service default/my-svc" {
		t.Fatalf("unexpected: %q", got)
	}
	if got := FormatResourceID("Namespace", "default", "kube-system"); got != "Namespace kube-system" {
		t.Fatalf("unexpected: %q", got)
	}
}
