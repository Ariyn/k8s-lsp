package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	// Additional schema: validation rules file shape.
	// We intentionally keep this type private to avoid package cycles.
	type validationConfig struct {
		Rules []struct {
			Kind   string `yaml:"kind"`
			Checks []struct {
				Type       string `yaml:"type"`
				Path       string `yaml:"path"`
				TargetKind string `yaml:"targetKind"`
				TargetPath string `yaml:"targetPath"`
				Message    string `yaml:"message"`
			} `yaml:"checks"`
		} `yaml:"rules"`
	}

	// Walk through rules directory
	rulesDir := filepath.Join(rootPath, "rules")
	err := filepath.Walk(rulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			// 1) Standard config schema (symbols/references)
			var c Config
			if err := yaml.Unmarshal(b, &c); err != nil {
				return err
			}
			cfg.Symbols = append(cfg.Symbols, c.Symbols...)
			cfg.References = append(cfg.References, c.References...)

			// 2) validation.yaml schema -> convert reference checks to References
			var vc validationConfig
			if err := yaml.Unmarshal(b, &vc); err != nil {
				return err
			}
			for _, rule := range vc.Rules {
				kind := strings.TrimSpace(rule.Kind)
				if kind == "" {
					continue
				}
				for _, chk := range rule.Checks {
					if strings.TrimSpace(chk.Type) != "reference" {
						continue
					}
					refPath := strings.TrimSpace(chk.Path)
					targetKind := strings.TrimSpace(chk.TargetKind)
					targetPath := strings.TrimSpace(chk.TargetPath)
					if refPath == "" || targetKind == "" {
						continue
					}

					// Skip selector-style references (these are mapping nodes; handled separately by k8s.label refs).
					// Heuristic: targetPath pointing at labels is a selector match, not a scalar resource-name reference.
					if strings.Contains(targetPath, "labels") {
						continue
					}

					cfg.References = append(cfg.References, Reference{
						Name:       fmt.Sprintf("validation.%s.%s", strings.ToLower(kind), strings.ReplaceAll(refPath, " ", "")),
						Symbol:     "k8s.resource.name",
						TargetKind: targetKind,
						Match: ReferenceMatch{
							Kinds: []string{kind},
							Path:  refPath,
						},
					})
				}
			}
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
