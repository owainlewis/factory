package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsRepo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.yaml")
	err := os.WriteFile(path, []byte(`
repos:
  cortex:
    url: git@github.com:owainlewis/cortex.git
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	repo := cfg.Repos["cortex"]
	if cfg.Factory.DataDir != ".factory-state" {
		t.Fatalf("data dir = %q", cfg.Factory.DataDir)
	}
	if repo.Branch != "main" {
		t.Fatalf("branch = %q", repo.Branch)
	}
	if repo.Agent != "claude" {
		t.Fatalf("agent = %q", repo.Agent)
	}
}

func TestLoadRejectsRepoWithoutLocation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.yaml")
	err := os.WriteFile(path, []byte(`
repos:
  cortex: {}
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected error")
	}
}
