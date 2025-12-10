//go:build !windows

package main

func getLogFilePath() string {
	return "/tmp/k8s-lsp.log"
}
