package crd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadAll_UsesCacheOn304(t *testing.T) {
	etag := "\"v1\""
	body := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\nspec:\n  group: example.com\n  names:\n    kind: Widget\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := DefaultOptions()
	opts.CacheDir = dir
	opts.Timeout = 2 * time.Second

	paths1, err := DownloadAll([]string{srv.URL}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths1) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths1))
	}
	if _, err := os.Stat(paths1[0]); err != nil {
		t.Fatalf("expected cached file to exist: %v", err)
	}

	paths2, err := DownloadAll([]string{srv.URL}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths2) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths2))
	}
	if filepath.Clean(paths1[0]) != filepath.Clean(paths2[0]) {
		t.Fatalf("expected same cache path")
	}
}
