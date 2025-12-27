package crd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DownloadOptions struct {
	Timeout       time.Duration
	MaxBytes      int64
	CacheDir      string
	UserAgent     string
	AllowInsecure bool // allow http:// (default false)
}

type cachedMeta struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	FetchedAt    time.Time `json:"fetchedAt"`
}

func DefaultOptions() DownloadOptions {
	return DownloadOptions{
		Timeout:   10 * time.Second,
		MaxBytes:  10 * 1024 * 1024, // 10 MiB
		UserAgent: "k8s-lsp/0.1 (crd-downloader)",
	}
}

func EnsureCacheDir(cacheDir string) (string, error) {
	if strings.TrimSpace(cacheDir) != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return "", err
		}
		return cacheDir, nil
	}

	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}

	out := filepath.Join(base, "k8s-lsp", "crds")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

func DownloadAll(sources []string, opts DownloadOptions) ([]string, error) {
	cacheDir, err := EnsureCacheDir(opts.CacheDir)
	if err != nil {
		return nil, err
	}
	opts.CacheDir = cacheDir

	if opts.Timeout <= 0 {
		opts.Timeout = DefaultOptions().Timeout
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultOptions().MaxBytes
	}
	if strings.TrimSpace(opts.UserAgent) == "" {
		opts.UserAgent = DefaultOptions().UserAgent
	}

	client := &http.Client{Timeout: opts.Timeout}

	var paths []string
	var firstErr error
	for _, src := range sources {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}

		p, err := downloadOne(client, src, opts)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		paths = append(paths, p)
	}

	// Return partial success with aggregated error
	return paths, firstErr
}

func downloadOne(client *http.Client, rawURL string, opts DownloadOptions) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid CRD URL %q: %w", rawURL, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" {
		if !(opts.AllowInsecure && scheme == "http") {
			return "", fmt.Errorf("CRD URL must be https:// (got %q)", rawURL)
		}
	}

	fileBase := sha256Hex(rawURL)
	yamlPath := filepath.Join(opts.CacheDir, fileBase+".yaml")
	metaPath := filepath.Join(opts.CacheDir, fileBase+".json")

	meta := cachedMeta{}
	if b, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(b, &meta)
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	if strings.TrimSpace(meta.ETag) != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	if strings.TrimSpace(meta.LastModified) != "" {
		req.Header.Set("If-Modified-Since", meta.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download CRD %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		if _, err := os.Stat(yamlPath); err == nil {
			return yamlPath, nil
		}
		return "", fmt.Errorf("CRD %q returned 304 but cache file missing", rawURL)
	case http.StatusOK:
		// proceed
	default:
		return "", fmt.Errorf("failed to download CRD %q: HTTP %d", rawURL, resp.StatusCode)
	}

	content, err := readWithLimit(resp.Body, opts.MaxBytes)
	if err != nil {
		return "", fmt.Errorf("failed to read CRD %q: %w", rawURL, err)
	}
	if len(bytesTrimSpace(content)) == 0 {
		return "", fmt.Errorf("downloaded CRD %q is empty", rawURL)
	}

	// Write atomically
	tmp := yamlPath + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, yamlPath); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	meta.URL = rawURL
	meta.ETag = resp.Header.Get("ETag")
	meta.LastModified = resp.Header.Get("Last-Modified")
	meta.FetchedAt = time.Now().UTC()

	if mb, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, mb, 0o644)
	}

	return yamlPath, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func readWithLimit(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, errors.New("max must be positive")
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("response too large (>%d bytes)", max)
	}
	return b, nil
}

func bytesTrimSpace(b []byte) []byte {
	// Avoid bytes import for a tiny helper
	start := 0
	for start < len(b) {
		c := b[start]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			break
		}
		start++
	}
	end := len(b)
	for end > start {
		c := b[end-1]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			break
		}
		end--
	}
	return b[start:end]
}
