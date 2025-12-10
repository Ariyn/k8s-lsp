//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func getLogFilePath() string {
	return filepath.Join(os.TempDir(), "k8s-lsp.log")
}
