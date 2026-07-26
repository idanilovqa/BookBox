package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

type Root struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	ContainerPath string `yaml:"container_path"`
}

type Config struct {
	Listen string `yaml:"listen"`
	Roots  []Root `yaml:"roots"`
}

var ErrDuplicateRootID = errors.New("duplicate root id")

var safeRootID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.Listen == "" || len(cfg.Roots) == 0 {
		return Config{}, errors.New("listen and roots are required")
	}

	seen := make(map[string]struct{}, len(cfg.Roots))
	for _, root := range cfg.Roots {
		if !safeRootID.MatchString(root.ID) {
			return Config{}, fmt.Errorf("unsafe root id %q", root.ID)
		}
		if _, exists := seen[root.ID]; exists {
			return Config{}, ErrDuplicateRootID
		}
		seen[root.ID] = struct{}{}
		if !filepath.IsAbs(root.ContainerPath) {
			return Config{}, fmt.Errorf("root %q path must be absolute", root.ID)
		}
	}

	return cfg, nil
}
