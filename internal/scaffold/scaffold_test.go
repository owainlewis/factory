package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesContract(t *testing.T) {
	dir := t.TempDir()

	results, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected files to be created")
	}

	want := []string{
		".factory/AGENTS.md",
		".factory/JOURNAL.md",
		".factory/OBJECTIVES/initial-readiness.md",
		".factory/STANDARDS.md",
		".factory/WORKFLOWS/standards-check.md",
	}
	got := make([]string, 0, len(results))
	for _, r := range results {
		if !r.Created {
			t.Errorf("expected %s to be created", r.Path)
		}
		got = append(got, r.Path)
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing expected file %s in %v", w, got)
		}
	}

	// Files must actually exist on disk with content.
	for _, w := range want {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(w)))
		if err != nil {
			t.Fatalf("read %s: %v", w, err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			t.Errorf("%s is empty", w)
		}
	}
}

func TestInitDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".factory", "STANDARDS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := []byte("my custom standards\n")
	if err := os.WriteFile(target, custom, 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, r := range results {
		if r.Path == ".factory/STANDARDS.md" && r.Created {
			t.Error("STANDARDS.md should not have been overwritten")
		}
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, custom) {
		t.Errorf("STANDARDS.md was modified: %q", data)
	}
}

func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".factory", "STANDARDS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Init(dir, true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, r := range results {
		if !r.Created {
			t.Errorf("with force, expected %s to be created", r.Path)
		}
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "old\n" {
		t.Error("STANDARDS.md was not overwritten under force")
	}
}

func TestReportListsCreatedAndNext(t *testing.T) {
	dir := t.TempDir()
	results, err := Init(dir, false)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	Report(&buf, dir, results)
	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("report missing created status: %q", out)
	}
	if !strings.Contains(out, "Next:") {
		t.Errorf("report missing next command: %q", out)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
