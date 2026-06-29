package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Factory FactoryConfig         `yaml:"factory"`
	Repos   map[string]RepoConfig `yaml:"repos"`
}

type FactoryConfig struct {
	Name    string `yaml:"name"`
	Purpose string `yaml:"purpose"`
	DataDir string `yaml:"data_dir"`
}

type RepoConfig struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
	Agent  string `yaml:"agent"`
	Path   string `yaml:"path"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Factory.DataDir == "" {
		cfg.Factory.DataDir = ".factory-state"
	}
	for name, repo := range cfg.Repos {
		if repo.URL == "" && repo.Path == "" {
			return Config{}, fmt.Errorf("repo %q needs url or path", name)
		}
		if repo.Branch == "" {
			repo.Branch = "main"
		}
		if repo.Agent == "" {
			repo.Agent = "claude"
		}
		cfg.Repos[name] = repo
	}
	return cfg, nil
}

func RepoPath(dataDir string, name string, repo RepoConfig) string {
	if repo.Path != "" {
		return expandHome(repo.Path)
	}
	return filepath.Join(dataDir, "repos", name)
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if len(path) > 2 && path[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
