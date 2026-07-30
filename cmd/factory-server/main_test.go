package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDatabasePathUsesFactoryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_V2_DATA_HOME", "")

	database, root, err := defaultDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, ".factory")
	if root != wantRoot {
		t.Fatalf("root = %q, want %q", root, wantRoot)
	}
	if want := filepath.Join(wantRoot, "server", "factory.sqlite3"); database != want {
		t.Fatalf("database = %q, want %q", database, want)
	}
}

func TestDefaultDatabasePathHonorsOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FACTORY_V2_DATA_HOME", root)

	database, gotRoot, err := defaultDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if want := filepath.Join(root, "server", "factory.sqlite3"); database != want {
		t.Fatalf("database = %q, want %q", database, want)
	}
}

func TestValidateNoLegacyServerDefaultRefusesLegacyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_V2_DATA_HOME", "")
	legacyRoot := filepath.Join(home, ".factory-v2")
	legacyDatabase := filepath.Join(legacyRoot, "server", "factory.sqlite3")
	if err := os.MkdirAll(filepath.Dir(legacyDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyDatabase, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	newRoot := filepath.Join(home, ".factory")
	err := validateNoLegacyServerDefault(newRoot)
	if err == nil {
		t.Fatal("legacy V2 server state was accepted")
	}
	for _, want := range []string{legacyDatabase, "FACTORY_V2_DATA_HOME=" + legacyRoot} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if err := validateLegacyServerSelection("", true, newRoot); err != nil {
		t.Fatalf("explicit database selection was blocked: %v", err)
	}
	if err := validateLegacyServerSelection(legacyRoot, false, newRoot); err != nil {
		t.Fatalf("explicit data home was blocked: %v", err)
	}

	t.Setenv("FACTORY_V2_DATA_HOME", legacyRoot)
	database, root, err := defaultDatabasePath()
	if err != nil {
		t.Fatalf("explicit legacy root: %v", err)
	}
	if root != legacyRoot || database != legacyDatabase {
		t.Fatalf("explicit legacy paths = %q, %q", root, database)
	}
}

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

func TestValidateV2DataRootAllowsV1RepositorySibling(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".factory")
	v1RepositoryRoot := filepath.Join(root, "0123456789abcdef0123")
	if err := os.MkdirAll(v1RepositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1RepositoryRoot, "factory.sqlite3"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateV2DataRoot(root); err != nil {
		t.Fatalf("validate shared Factory home with isolated V1 sibling: %v", err)
	}
}
