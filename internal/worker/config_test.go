package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestLoadConfigDefaultsMaxConcurrentToTen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"pool\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConcurrent != 10 {
		t.Fatalf("default max_concurrent = %d; want 10", config.MaxConcurrent)
	}
}

func TestWorkerCapacityUsesSharedRange(t *testing.T) {
	for _, capacity := range []int{protocol.MinWorkerCapacity, protocol.MaxWorkerCapacity} {
		config := Config{
			Server: "http://127.0.0.1:7337", Name: "pool", Runtime: protocol.RuntimeCodex,
			MaxConcurrent: capacity, DataDirectory: t.TempDir(),
		}
		if err := validateConfig(config); err != nil {
			t.Fatalf("max_concurrent %d rejected: %v", capacity, err)
		}
	}

	for _, capacity := range []int{protocol.MinWorkerCapacity - 1, protocol.MaxWorkerCapacity + 1} {
		config := Config{
			Server: "http://127.0.0.1:7337", Name: "pool", Runtime: protocol.RuntimeCodex,
			MaxConcurrent: capacity, DataDirectory: t.TempDir(),
		}
		err := validateConfig(config)
		if err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
			t.Fatalf("max_concurrent %d error = %v", capacity, err)
		}
	}
}

func TestLoadConfigRejectsExplicitZeroCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(path, []byte("name = \"pool\"\nmax_concurrent = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Fatalf("explicit zero max_concurrent error = %v", err)
	}
}
