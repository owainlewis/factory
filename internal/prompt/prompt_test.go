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

func TestBuildRepoWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowDir := filepath.Join(dir, "WORKFLOWS")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "triage.md"), []byte("Open issues only."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "STANDARDS.md"), []byte("Tests must pass."), 0o644); err != nil {
		t.Fatal(err)
	}

	source, body, err := Build(dir, "triage")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(source, "WORKFLOWS/triage.md") {
		t.Fatalf("source = %q", source)
	}
	if !strings.Contains(body, "Open issues only.") {
		t.Fatal("workflow body missing")
	}
	if !strings.Contains(body, "Tests must pass.") {
		t.Fatal("compiled standards missing")
	}
}
