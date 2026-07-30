package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitConfigSelectionBypassesLegacyDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_WORKER_CONFIG", "")
	t.Setenv("FACTORY_V2_DATA_HOME", "")
	t.Setenv("FACTORY_V2_WORKER_CONFIG", "")
	legacyRoot := filepath.Join(home, ".factory-v2")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "worker.toml"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateDefaultConfigSelection(true); err != nil {
		t.Fatalf("explicit config selection was blocked: %v", err)
	}
	if err := validateDefaultConfigSelection(false); err == nil ||
		!strings.Contains(err.Error(), "preview worker state") {
		t.Fatalf("implicit config selection error = %v", err)
	}
}

func TestConfigArgumentDetection(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.String("config", "/default.toml", "")
	if err := flags.Parse([]string{"--config", "/explicit.toml"}); err != nil {
		t.Fatal(err)
	}
	if !flagExplicit(flags, "config") {
		t.Fatal("--config was not detected")
	}

	for _, arguments := range [][]string{
		{"attempt-id", "--config", "/explicit.toml"},
		{"attempt-id", "--config=/explicit.toml"},
	} {
		if !cleanupConfigExplicit(arguments) {
			t.Fatalf("cleanup config was not detected in %v", arguments)
		}
	}
	if cleanupConfigExplicit([]string{"attempt-id"}) {
		t.Fatal("cleanup config reported explicit without an argument")
	}
}
