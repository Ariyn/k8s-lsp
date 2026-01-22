//go:build windows

package lsp

import (
	"os"
	"path/filepath"
)

func getLogFilePath() string {
	return filepath.Join(os.TempDir(), "k8s-lsp.log")
}
