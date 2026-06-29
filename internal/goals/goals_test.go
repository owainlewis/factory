package goals

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverListsBuiltInAndRepoGoals(t *testing.T) {
	repo := t.TempDir()
	goalDir := filepath.Join(repo, ".factory", "goals")
	if err := os.MkdirAll(goalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"triage.md", "standards-review.md"} {
		if err := os.WriteFile(filepath.Join(goalDir, name), []byte("# Goal\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goalDir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(repo)
	if err != nil {
		t.Fatal(err)
	}

	names := goalNames(got)
	want := []string{"hello", "standards-review", "triage"}
	if len(names) != len(want) {
		t.Fatalf("names = %#v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %#v", names)
		}
	}
	for _, goal := range got {
		if !goal.Runnable {
			t.Fatalf("goal should be runnable: %#v", goal)
		}
	}
}

func TestDiscoverReportsMissingGoalsDirectory(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("goals = %#v", got)
	}
	if got[0].Name != "hello" || !got[0].Runnable {
		t.Fatalf("built-in goal = %#v", got[0])
	}
	if got[1].Name != "repo-owned" || got[1].Runnable {
		t.Fatalf("missing goal marker = %#v", got[1])
	}
}

func goalNames(goals []Goal) []string {
	names := make([]string, 0, len(goals))
	for _, goal := range goals {
		names = append(names, goal.Name)
	}
	return names
}
