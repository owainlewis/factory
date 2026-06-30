package labels

import (
	"context"
	"errors"
	"testing"
)

func TestPlanCreatesMissingLabels(t *testing.T) {
	create, update := Plan(nil)
	if len(create) != len(Standard()) {
		t.Fatalf("expected all standard labels to be created, got %d", len(create))
	}
	if len(update) != 0 {
		t.Fatalf("expected no updates, got %d", len(update))
	}
}

func TestPlanLeavesMatchingLabels(t *testing.T) {
	create, update := Plan(Standard())
	if len(create) != 0 || len(update) != 0 {
		t.Fatalf("expected no changes, got create=%d update=%d", len(create), len(update))
	}
}

func TestPlanDoesNotClobberHumanDescription(t *testing.T) {
	existing := []Label{
		{Name: "factory-ready", Description: "custom human wording", Color: "ffffff"},
	}
	create, update := Plan(existing)
	if len(update) != 0 {
		t.Fatalf("non-empty human description/color must not be overwritten, got %#v", update)
	}
	// The other three are still missing.
	if len(create) != len(Standard())-1 {
		t.Fatalf("expected %d creates, got %d", len(Standard())-1, len(create))
	}
}

func TestPlanFillsEmptyFields(t *testing.T) {
	existing := []Label{
		{Name: "factory-ready", Description: "", Color: ""},
		{Name: "factory-triage", Description: "Needs clarification, acceptance criteria, or scope shaping", Color: "fbca04"},
		{Name: "factory-needs-human", Description: "Needs a human decision before implementation", Color: "b60205"},
		{Name: "factory-blocked", Description: "Blocked until a named dependency is resolved", Color: "d93f0b"},
	}
	create, update := Plan(existing)
	if len(create) != 0 {
		t.Fatalf("expected no creates, got %#v", create)
	}
	if len(update) != 1 || update[0].Name != "factory-ready" {
		t.Fatalf("expected only factory-ready filled, got %#v", update)
	}
	if update[0].Description == "" || update[0].Color == "" {
		t.Fatalf("expected empty fields to be filled, got %#v", update[0])
	}
}

func TestPlanIgnoresUserLabels(t *testing.T) {
	existing := append(Standard(), Label{Name: "bug", Description: "user label", Color: "d73a4a"})
	create, update := Plan(existing)
	if len(create) != 0 || len(update) != 0 {
		t.Fatalf("user labels must be left alone, got create=%#v update=%#v", create, update)
	}
}

type fakeClient struct {
	existing []Label
	created  []string
	updated  []string
	listErr  error
}

func (f *fakeClient) List(context.Context) ([]Label, error) { return f.existing, f.listErr }
func (f *fakeClient) Create(_ context.Context, l Label) error {
	f.created = append(f.created, l.Name)
	return nil
}
func (f *fakeClient) Update(_ context.Context, l Label) error {
	f.updated = append(f.updated, l.Name)
	return nil
}

func TestSyncCreatesAndReports(t *testing.T) {
	f := &fakeClient{existing: []Label{{Name: "factory-ready", Description: "An agent may work this issue now", Color: "0e8a16"}}}
	report, err := Sync(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Created) != 3 {
		t.Fatalf("expected 3 created, got %v", report.Created)
	}
	if len(report.Unchanged) != 1 || report.Unchanged[0] != "factory-ready" {
		t.Fatalf("expected factory-ready unchanged, got %v", report.Unchanged)
	}
	if len(f.created) != 3 {
		t.Fatalf("client should have created 3 labels, got %v", f.created)
	}
}

func TestSyncPropagatesListError(t *testing.T) {
	f := &fakeClient{listErr: errors.New("boom")}
	if _, err := Sync(context.Background(), f); err == nil {
		t.Fatal("expected error from List to propagate")
	}
}

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owainlewis/factory.git":     "owainlewis/factory",
		"https://github.com/owainlewis/factory":     "owainlewis/factory",
		"https://github.com/owainlewis/factory.git": "owainlewis/factory",
		"git@github.com:owner/repo":                 "owner/repo",
	}
	for url, want := range cases {
		got, err := RepoSlug(url)
		if err != nil {
			t.Errorf("RepoSlug(%q): %v", url, err)
			continue
		}
		if got != want {
			t.Errorf("RepoSlug(%q) = %q, want %q", url, got, want)
		}
	}

	bad := []string{
		"https://gitlab.com/owner/repo.git",
		"https://github.com/owner",
		"https://github.com/owner/repo/extra",
		"https://example.com/github.com/owner/repo",
		"",
	}
	for _, url := range bad {
		if _, err := RepoSlug(url); err == nil {
			t.Errorf("expected error for %q", url)
		}
	}
}

func TestPlanCaseInsensitiveNames(t *testing.T) {
	existing := []Label{
		{Name: "Factory-Ready", Description: "An agent may work this issue now", Color: "0e8a16"},
		{Name: "FACTORY-TRIAGE", Description: "Needs clarification, acceptance criteria, or scope shaping", Color: "fbca04"},
		{Name: "factory-needs-human", Description: "Needs a human decision before implementation", Color: "b60205"},
		{Name: "factory-blocked", Description: "Blocked until a named dependency is resolved", Color: "d93f0b"},
	}
	create, update := Plan(existing)
	if len(create) != 0 {
		t.Fatalf("case-different labels must not be recreated, got %#v", create)
	}
	if len(update) != 0 {
		t.Fatalf("matching labels must not be updated, got %#v", update)
	}
}

func TestIsBlocked(t *testing.T) {
	blocked := errBlocked{err: errors.New("HTTP 403")}
	if !IsBlocked(blocked) {
		t.Error("expected blocked error to be detected")
	}
	if IsBlocked(errors.New("plain error")) {
		t.Error("plain error should not be blocked")
	}
	wrapped := errors.Join(errors.New("ctx"), blocked)
	if !IsBlocked(wrapped) {
		t.Error("expected wrapped blocked error to be detected")
	}
}

func TestIsAuthOrPermission(t *testing.T) {
	if !isAuthOrPermission("gh: Resource not accessible by integration (HTTP 403)") {
		t.Error("expected permission error to be detected")
	}
	if isAuthOrPermission("label already exists") {
		t.Error("benign error should not be auth/permission")
	}
}
