package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateV2DataRootRefusesV1Ancestor(t *testing.T) {
	v1Root := t.TempDir()
	marker := filepath.Join(v1Root, "factory.sqlite3")
	if err := os.WriteFile(marker, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	v2Root := filepath.Join(v1Root, "nested", "v2")

	err := validateV2DataRoot(v2Root)
	if err == nil || !strings.Contains(err.Error(), "refusing a V2 data root below V1 state") {
		t.Fatalf("validate V2 root error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(v1Root, "nested")); !os.IsNotExist(err) {
		t.Fatalf("validation mutated V1 root: %v", err)
	}
}

func TestValidateV2DataRootRefusesSymlinkedV1Ancestor(t *testing.T) {
	v1Root := t.TempDir()
	if err := os.WriteFile(filepath.Join(v1Root, "factory.sqlite3"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-v1")
	if err := os.Symlink(v1Root, link); err != nil {
		t.Fatal(err)
	}

	err := validateV2DataRoot(filepath.Join(link, "nested", "v2"))
	if err == nil || !strings.Contains(err.Error(), "refusing a V2 data root below V1 state") {
		t.Fatalf("validate symlinked V2 root error = %v", err)
	}
}

func TestValidateV2DataRootAllowsSeparatePath(t *testing.T) {
	if err := validateV2DataRoot(filepath.Join(t.TempDir(), "nested", "v2")); err != nil {
		t.Fatalf("validate separate V2 root: %v", err)
	}
}
