package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFindsMissingCIAndSuggestsObjective(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "README.md", "# Demo\n\n## Build\n\n## Test\n\n## Usage\n")
	writeFile(t, repo, "go.mod", "module example.com/demo\n")
	writeFile(t, repo, "LICENSE", "MIT\n")
	writeFile(t, repo, filepath.Join(".factory", "STANDARDS.md"), "# Standards\n")
	writeFile(t, repo, filepath.Join(".factory", "AGENTS.md"), "# Agents\n")
	writeFile(t, repo, filepath.Join(".factory", "JOURNAL.md"), "# Journal\n")
	writeFile(t, repo, filepath.Join(".factory", "WORKFLOWS", "standards-check.md"), "# Standards Check\n")

	report, err := Run(repo)
	if err != nil {
		t.Fatal(err)
	}

	finding := findFinding(report.Findings, "ci", StatusFail)
	if finding == nil {
		t.Fatalf("expected missing CI finding: %#v", report.Findings)
	}
	if finding.SuggestedObjective != "ci-readiness" {
		t.Fatalf("objective = %q", finding.SuggestedObjective)
	}
	if finding.Workflow != "standards-check" {
		t.Fatalf("workflow = %q", finding.Workflow)
	}
	if len(report.CandidateObjectives) == 0 || report.CandidateObjectives[0].ID != "ci-readiness" {
		t.Fatalf("objectives = %#v", report.CandidateObjectives)
	}
	if report.CandidateObjectives[0].Workflow != "standards-check" {
		t.Fatalf("objective workflow = %q", report.CandidateObjectives[0].Workflow)
	}
	if report.CandidateObjectives[0].Goal == "" {
		t.Fatal("objective goal is empty")
	}
}

func TestRunPassesFactoryReadinessFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "README.md", "# Demo\n\n## Install\n\n## Build\n\n## Test\n\n## Usage\n")
	writeFile(t, repo, "go.mod", "module example.com/demo\n")
	writeFile(t, repo, "LICENSE", "MIT\n")
	writeFile(t, repo, filepath.Join(".github", "workflows", "test.yml"), "name: test\n")
	writeFile(t, repo, "CHANGELOG.md", "# Changelog\n")
	writeFile(t, repo, filepath.Join("docs", "releasing.md"), "# Releasing\n")
	writeFile(t, repo, filepath.Join(".factory", "STANDARDS.md"), "# Standards\n")
	writeFile(t, repo, filepath.Join(".factory", "AGENTS.md"), "# Agents\n")
	writeFile(t, repo, filepath.Join(".factory", "JOURNAL.md"), "# Journal\n")
	writeFile(t, repo, filepath.Join(".factory", "WORKFLOWS", "standards-check.md"), "# Standards Check\n")

	report, err := Run(repo)
	if err != nil {
		t.Fatal(err)
	}

	if finding := findFinding(report.Findings, "agent readiness", StatusFail); finding != nil {
		t.Fatalf("unexpected agent readiness failure: %#v", finding)
	}
	if finding := findFinding(report.Findings, "ci", StatusPass); finding == nil {
		t.Fatalf("expected CI pass: %#v", report.Findings)
	}
}

func TestWriteMarkdown(t *testing.T) {
	report := Report{
		RepoPath: "/tmp/demo",
		Findings: []Finding{{
			Bucket:             "ci",
			Status:             StatusFail,
			Severity:           SeverityHigh,
			Title:              "Pull requests do not appear to run CI",
			Evidence:           []string{".github/workflows has no workflow files"},
			SuggestedObjective: "ci-readiness",
			Workflow:           "standards-check",
		}},
		CandidateObjectives: []CandidateObjective{{
			ID:       "ci-readiness",
			Priority: 1,
			Workflow: "standards-check",
			Reason:   "Pull requests do not appear to run CI",
			Goal:     "Make pull requests run the project build and tests in CI.",
		}},
	}

	var out bytes.Buffer
	if err := WriteMarkdown(&out, "demo", report); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		"# Factory Audit: demo",
		"## Summary",
		"### CI",
		"**fail** `high` Pull requests do not appear to run CI",
		"`ci-readiness`",
		"Make pull requests run the project build and tests in CI.",
		".factory/OBJECTIVES/current-objective.md",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("markdown missing %q:\n%s", want, body)
		}
	}
}

func writeFile(t *testing.T, repo string, path string, body string) {
	t.Helper()
	fullPath := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findFinding(findings []Finding, bucket string, status Status) *Finding {
	for i := range findings {
		if findings[i].Bucket == bucket && findings[i].Status == status {
			return &findings[i]
		}
	}
	return nil
}
