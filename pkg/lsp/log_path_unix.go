//go:build !windows

package lsp

func getLogFilePath() string {
	return "/tmp/k8s-lsp.log"
}
