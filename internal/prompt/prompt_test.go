package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildHelloPrompt(t *testing.T) {
	source, body, err := Build(t.TempDir(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if source != "built-in:hello" {
		t.Fatalf("source = %q", source)
	}
	if !strings.Contains(body, "no-edit smoke test") {
		t.Fatal("hello prompt missing smoke test text")
	}
}

func TestBuildRepoGoal(t *testing.T) {
	dir := t.TempDir()
	goalDir := filepath.Join(dir, ".factory", "goals")
	if err := os.MkdirAll(goalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goalDir, "triage.md"), []byte("Open issues only."), 0o644); err != nil {
		t.Fatal(err)
	}

	source, body, err := Build(dir, "triage")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(source, ".factory/goals/triage.md") {
		t.Fatalf("source = %q", source)
	}
	if !strings.Contains(body, "Open issues only.") {
		t.Fatal("goal body missing")
	}
}
