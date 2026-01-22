package main

import (
	"os"
	"path/filepath"
	"strings"

	"k8s-lsp/pkg/schema"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func setupLogging() {
	logFile, err := os.OpenFile(getLogFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}
	if err != nil {
		log.Logger = log.Output(consoleWriter)
		log.Error().Err(err).Msg("Failed to open log file")
		return
	}

	multi := zerolog.MultiLevelWriter(consoleWriter, logFile)
	log.Logger = log.Output(multi)
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
}

func configPathFromExecutable() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get executable path, using current directory")
		return "."
	}
	return filepath.Dir(exePath)
}

func loadLocalSchemaPacks(reg *schema.Registry, configPath string) {
	if reg == nil {
		return
	}
	schemasDir := filepath.Join(configPath, "schemas")
	loaded := 0
	_ = filepath.Walk(schemasDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info == nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		n, err := schema.LoadGVKSchemasFromFile(reg, p)
		if err != nil {
			log.Warn().Err(err).Str("path", p).Msg("Failed to load local schema pack")
			return nil
		}
		loaded += n
		return nil
	})
	if loaded > 0 {
		log.Info().Int("schemas", loaded).Msg("Loaded local schema packs")
	}
}
