package indexer

import "testing"

func TestStoreRemoveByFilePath_RemovesAllResourcesFromThatFile(t *testing.T) {
	store := NewStore()

	store.Add(&K8sResource{Kind: "Service", Name: "svc-a", Namespace: "default", FilePath: "/tmp/a.yaml"})
	store.Add(&K8sResource{Kind: "ConfigMap", Name: "cm-a", Namespace: "default", FilePath: "/tmp/a.yaml"})
	store.Add(&K8sResource{Kind: "Service", Name: "svc-b", Namespace: "default", FilePath: "/tmp/b.yaml"})

	removed := store.RemoveByFilePath("/tmp/a.yaml")
	if removed != 2 {
		t.Fatalf("expected removed=2, got %d", removed)
	}

	if got := store.Get("Service", "default", "svc-a"); got != nil {
		t.Fatalf("expected svc-a removed, got %+v", got)
	}
	if got := store.Get("ConfigMap", "default", "cm-a"); got != nil {
		t.Fatalf("expected cm-a removed, got %+v", got)
	}
	if got := store.Get("Service", "default", "svc-b"); got == nil {
		t.Fatalf("expected svc-b still present")
	}
}

func TestStoreAdd_UpdatesReverseIndexWhenKeyMovesFiles(t *testing.T) {
	store := NewStore()

	// Same logical key (Kind/Namespace/Name) appears to move to a different file.
	store.Add(&K8sResource{Kind: "Service", Name: "svc", Namespace: "default", FilePath: "/tmp/old.yaml"})
	store.Add(&K8sResource{Kind: "Service", Name: "svc", Namespace: "default", FilePath: "/tmp/new.yaml"})

	removedOld := store.RemoveByFilePath("/tmp/old.yaml")
	if removedOld != 0 {
		t.Fatalf("expected removedOld=0 (key moved), got %d", removedOld)
	}

	removedNew := store.RemoveByFilePath("/tmp/new.yaml")
	if removedNew != 1 {
		t.Fatalf("expected removedNew=1, got %d", removedNew)
	}
	if got := store.Get("Service", "default", "svc"); got != nil {
		t.Fatalf("expected svc removed after removing new.yaml, got %+v", got)
	}
}
