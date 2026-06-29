package workflows

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverListsBuiltInAndRepoWorkflows(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, "WORKFLOWS")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"issue-triage.md", "bug-fix.md"} {
		if err := os.WriteFile(filepath.Join(workflowDir, name), []byte("# Workflow\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(repo)
	if err != nil {
		t.Fatal(err)
	}

	names := workflowNames(got)
	want := []string{"hello", "bug-fix", "issue-triage"}
	if len(names) != len(want) {
		t.Fatalf("names = %#v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %#v", names)
		}
	}
	for _, workflow := range got {
		if !workflow.Runnable {
			t.Fatalf("workflow should be runnable: %#v", workflow)
		}
	}
}

func TestDiscoverReportsMissingWorkflowsDirectory(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("workflows = %#v", got)
	}
	if got[0].Name != "hello" || !got[0].Runnable {
		t.Fatalf("built-in workflow = %#v", got[0])
	}
	if got[1].Name != "repo-owned" || got[1].Runnable {
		t.Fatalf("missing workflow marker = %#v", got[1])
	}
}

func workflowNames(workflows []Workflow) []string {
	names := make([]string, 0, len(workflows))
	for _, workflow := range workflows {
		names = append(names, workflow.Name)
	}
	return names
}
