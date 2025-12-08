package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version    int         `yaml:"version"`
	Symbols    []Symbol    `yaml:"symbols"`
	References []Reference `yaml:"references"`
}

type Symbol struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	KeyTemplate string             `yaml:"keyTemplate"`
	Definitions []SymbolDefinition `yaml:"definitions"`
}

type SymbolDefinition struct {
	Kinds []string `yaml:"kinds"`
	Path  string   `yaml:"path"`
}

type Reference struct {
	Name       string         `yaml:"name"`
	Symbol     string         `yaml:"symbol"`
	TargetKind string         `yaml:"targetKind"`
	Match      ReferenceMatch `yaml:"match"`
}

type ReferenceMatch struct {
	Kinds []string `yaml:"kinds"`
	Path  string   `yaml:"path"`
}

func Load(rootPath string) (*Config, error) {
	cfg := &Config{}

	// Walk through rules directory
	rulesDir := filepath.Join(rootPath, "rules")
	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml") {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			var c Config
			if err := yaml.NewDecoder(f).Decode(&c); err != nil {
				return err
			}

			cfg.Symbols = append(cfg.Symbols, c.Symbols...)
			cfg.References = append(cfg.References, c.References...)
		}
		return nil
	})

	if err != nil {
		// If rules dir doesn't exist, return empty config or error?
		// For now, just return what we have or nil
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	return cfg, nil
}
