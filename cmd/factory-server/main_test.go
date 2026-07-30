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
	t.Setenv("FACTORY_DATA_HOME", "")
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
	t.Setenv("FACTORY_DATA_HOME", root)

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

func TestDefaultDatabasePathHonorsPreviewAlias(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FACTORY_DATA_HOME", "")
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
	t.Setenv("FACTORY_DATA_HOME", "")
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
		t.Fatal("preview server state was accepted")
	}
	for _, want := range []string{legacyDatabase, "FACTORY_DATA_HOME=" + legacyRoot} {
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

	t.Setenv("FACTORY_DATA_HOME", legacyRoot)
	database, root, err := defaultDatabasePath()
	if err != nil {
		t.Fatalf("explicit legacy root: %v", err)
	}
	if root != legacyRoot || database != legacyDatabase {
		t.Fatalf("explicit legacy paths = %q, %q", root, database)
	}

	currentDatabase := filepath.Join(newRoot, "server", "factory.sqlite3")
	if err := os.MkdirAll(filepath.Dir(currentDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentDatabase, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNoLegacyServerDefault(newRoot); err != nil {
		t.Fatalf("preview state overrode current default: %v", err)
	}
}

func TestValidateDataRootRefusesRetiredStateAncestor(t *testing.T) {
	retiredRoot := t.TempDir()
	marker := filepath.Join(retiredRoot, "factory.sqlite3")
	if err := os.WriteFile(marker, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(retiredRoot, "nested", "control-plane")

	err := validateDataRoot(dataRoot)
	if err == nil || !strings.Contains(err.Error(), "below retired local state") {
		t.Fatalf("validate data root error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(retiredRoot, "nested")); !os.IsNotExist(err) {
		t.Fatalf("validation mutated retired root: %v", err)
	}
}

func TestValidateDataRootRefusesSymlinkedRetiredStateAncestor(t *testing.T) {
	retiredRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(retiredRoot, "factory.sqlite3"), []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-retired")
	if err := os.Symlink(retiredRoot, link); err != nil {
		t.Fatal(err)
	}

	err := validateDataRoot(filepath.Join(link, "nested", "control-plane"))
	if err == nil || !strings.Contains(err.Error(), "below retired local state") {
		t.Fatalf("validate symlinked data root error = %v", err)
	}
}

func TestValidateDataRootAllowsSeparatePath(t *testing.T) {
	if err := validateDataRoot(filepath.Join(t.TempDir(), "nested", "control-plane")); err != nil {
		t.Fatalf("validate separate data root: %v", err)
	}
}

func TestValidateDataRootAllowsRetiredRepositorySibling(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".factory")
	retiredRepositoryRoot := filepath.Join(root, "0123456789abcdef0123")
	if err := os.MkdirAll(retiredRepositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retiredRepositoryRoot, "factory.sqlite3"), []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateDataRoot(root); err != nil {
		t.Fatalf("validate shared Factory home with isolated retired sibling: %v", err)
	}
}
