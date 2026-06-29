package audit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Finding struct {
	Bucket             string
	Status             Status
	Severity           Severity
	Title              string
	Evidence           []string
	SuggestedObjective string
	Workflow           string
}

type CandidateObjective struct {
	ID       string
	Priority int
	Workflow string
	Reason   string
}

type Report struct {
	RepoPath            string
	Findings            []Finding
	CandidateObjectives []CandidateObjective
}

func Run(repoPath string) (Report, error) {
	if _, err := os.Stat(repoPath); err != nil {
		return Report{}, err
	}

	report := Report{RepoPath: repoPath}
	report.add(identityFindings(repoPath)...)
	report.add(usabilityFindings(repoPath)...)
	report.add(buildFindings(repoPath)...)
	report.add(testingFindings(repoPath)...)
	report.add(ciFindings(repoPath)...)
	report.add(releaseFindings(repoPath)...)
	report.add(governanceFindings(repoPath)...)
	report.add(agentReadinessFindings(repoPath)...)
	report.CandidateObjectives = candidateObjectives(report.Findings)
	return report, nil
}

func (r Report) Summary() (passed int, warnings int, failed int) {
	for _, finding := range r.Findings {
		switch finding.Status {
		case StatusPass:
			passed++
		case StatusWarn:
			warnings++
		case StatusFail:
			failed++
		}
	}
	return passed, warnings, failed
}

func (r *Report) add(findings ...Finding) {
	r.Findings = append(r.Findings, findings...)
}

func identityFindings(repoPath string) []Finding {
	if exists(repoPath, "README.md") {
		return []Finding{pass("identity", "README.md exists", "README.md")}
	}
	return []Finding{fail("identity", SeverityHigh, "README.md is missing", "README.md not found", "docs", "docs")}
}

func usabilityFindings(repoPath string) []Finding {
	readme, ok := readLower(repoPath, "README.md")
	if !ok {
		return []Finding{warn("usability", "Cannot check usage docs without README.md", "README.md not found", "docs", "docs")}
	}

	checks := []struct {
		name     string
		terms    []string
		severity Severity
	}{
		{name: "README documents how to install or set up the project", terms: []string{"install", "setup"}, severity: SeverityMedium},
		{name: "README documents how to build the project", terms: []string{"build"}, severity: SeverityMedium},
		{name: "README documents how to test the project", terms: []string{"test"}, severity: SeverityHigh},
		{name: "README documents how to run or use the project", terms: []string{"usage", "run"}, severity: SeverityMedium},
	}

	findings := []Finding{}
	for _, check := range checks {
		if containsAny(readme, check.terms) {
			findings = append(findings, pass("usability", check.name, "README.md"))
			continue
		}
		findings = append(findings, fail("usability", check.severity, check.name, "README.md does not contain any of: "+strings.Join(check.terms, ", "), "docs", "docs"))
	}
	return findings
}

func buildFindings(repoPath string) []Finding {
	switch {
	case exists(repoPath, "go.mod"):
		return []Finding{pass("build", "Go module detected", "go.mod")}
	case exists(repoPath, "dune-project"):
		return []Finding{pass("build", "OCaml Dune project detected", "dune-project")}
	case exists(repoPath, "package.json"):
		return []Finding{pass("build", "Node package detected", "package.json")}
	case exists(repoPath, "Cargo.toml"):
		return []Finding{pass("build", "Rust package detected", "Cargo.toml")}
	default:
		return []Finding{warn("build", "No common build metadata found", "go.mod, dune-project, package.json, and Cargo.toml not found", "docs", "docs")}
	}
}

func testingFindings(repoPath string) []Finding {
	findings := []Finding{}
	switch {
	case exists(repoPath, "go.mod"):
		findings = append(findings, pass("testing", "Go test command is inferable", "go test ./..."))
	case exists(repoPath, "dune-project"):
		findings = append(findings, pass("testing", "Dune test command is inferable", "dune runtest or make test"))
	case exists(repoPath, "package.json"):
		if fileContains(repoPath, "package.json", `"test"`) {
			findings = append(findings, pass("testing", "npm test script exists", "package.json contains test script"))
		} else {
			findings = append(findings, fail("testing", SeverityHigh, "npm test script is missing", "package.json does not contain a test script", "tests", "standards"))
		}
	default:
		if hasPathPrefix(repoPath, "test") || hasPathPrefix(repoPath, "tests") {
			findings = append(findings, pass("testing", "Test directory exists", "test or tests directory found"))
		} else {
			findings = append(findings, warn("testing", "No test signal found", "No known test metadata or test directory found", "tests", "standards"))
		}
	}
	return findings
}

func ciFindings(repoPath string) []Finding {
	if hasWorkflow(repoPath) {
		return []Finding{pass("ci", "GitHub Actions workflows exist", ".github/workflows contains workflow files")}
	}
	return []Finding{fail("ci", SeverityHigh, "Pull requests do not appear to run CI", ".github/workflows has no .yml or .yaml files", "ci", "ci")}
}

func releaseFindings(repoPath string) []Finding {
	findings := []Finding{}
	if exists(repoPath, "CHANGELOG.md") {
		findings = append(findings, pass("release", "CHANGELOG.md exists", "CHANGELOG.md"))
	} else {
		findings = append(findings, fail("release", SeverityMedium, "CHANGELOG.md is missing", "CHANGELOG.md not found", "release", "release"))
	}
	if exists(repoPath, filepath.Join("docs", "releasing.md")) {
		findings = append(findings, pass("release", "Release process is documented", "docs/releasing.md"))
	} else {
		findings = append(findings, fail("release", SeverityMedium, "Release process is not documented", "docs/releasing.md not found", "release", "release"))
	}
	return findings
}

func governanceFindings(repoPath string) []Finding {
	if exists(repoPath, "LICENSE") || exists(repoPath, "LICENSE.md") {
		return []Finding{pass("governance", "License file exists", "LICENSE or LICENSE.md")}
	}
	return []Finding{fail("governance", SeverityHigh, "License file is missing", "LICENSE and LICENSE.md not found", "github", "standards")}
}

func agentReadinessFindings(repoPath string) []Finding {
	checks := []struct {
		path      string
		passTitle string
		failTitle string
		severity  Severity
		objective string
		workflow  string
	}{
		{path: filepath.Join(".factory", "STANDARDS.md"), passTitle: ".factory/STANDARDS.md exists", failTitle: ".factory/STANDARDS.md is missing", severity: SeverityHigh, objective: "standards", workflow: "standards"},
		{path: filepath.Join(".factory", "AGENTS.md"), passTitle: ".factory/AGENTS.md exists", failTitle: ".factory/AGENTS.md is missing", severity: SeverityMedium, objective: "standards", workflow: "standards"},
		{path: filepath.Join(".factory", "JOURNAL.md"), passTitle: ".factory/JOURNAL.md exists", failTitle: ".factory/JOURNAL.md is missing", severity: SeverityLow, objective: "standards", workflow: "standards"},
		{path: filepath.Join(".factory", "WORKFLOWS", "standards.md"), passTitle: ".factory/WORKFLOWS/standards.md exists", failTitle: ".factory/WORKFLOWS/standards.md is missing", severity: SeverityHigh, objective: "standards", workflow: "standards"},
	}
	findings := []Finding{}
	for _, check := range checks {
		if exists(repoPath, check.path) {
			findings = append(findings, pass("agent readiness", check.passTitle, check.path))
			continue
		}
		findings = append(findings, fail("agent readiness", check.severity, check.failTitle, check.path+" not found", check.objective, check.workflow))
	}
	return findings
}

func candidateObjectives(findings []Finding) []CandidateObjective {
	seen := map[string]CandidateObjective{}
	for _, finding := range findings {
		if finding.Status == StatusPass || finding.SuggestedObjective == "" {
			continue
		}
		priority := priorityFor(finding)
		existing, ok := seen[finding.SuggestedObjective]
		if ok && existing.Priority <= priority {
			continue
		}
		seen[finding.SuggestedObjective] = CandidateObjective{
			ID:       finding.SuggestedObjective,
			Priority: priority,
			Workflow: finding.Workflow,
			Reason:   finding.Title,
		}
	}

	objectives := make([]CandidateObjective, 0, len(seen))
	for _, objective := range seen {
		objectives = append(objectives, objective)
	}
	sort.Slice(objectives, func(i, j int) bool {
		if objectives[i].Priority == objectives[j].Priority {
			return objectives[i].ID < objectives[j].ID
		}
		return objectives[i].Priority < objectives[j].Priority
	})
	return objectives
}

func priorityFor(finding Finding) int {
	switch finding.Severity {
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	default:
		return 3
	}
}

func pass(bucket string, title string, evidence string) Finding {
	return Finding{Bucket: bucket, Status: StatusPass, Severity: SeverityLow, Title: title, Evidence: []string{evidence}}
}

func warn(bucket string, title string, evidence string, objective string, workflow string) Finding {
	return Finding{Bucket: bucket, Status: StatusWarn, Severity: SeverityLow, Title: title, Evidence: []string{evidence}, SuggestedObjective: objective, Workflow: workflow}
}

func fail(bucket string, severity Severity, title string, evidence string, objective string, workflow string) Finding {
	return Finding{Bucket: bucket, Status: StatusFail, Severity: severity, Title: title, Evidence: []string{evidence}, SuggestedObjective: objective, Workflow: workflow}
}

func exists(repoPath string, path string) bool {
	_, err := os.Stat(filepath.Join(repoPath, path))
	return err == nil
}

func readLower(repoPath string, path string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(repoPath, path))
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(data)), true
}

func containsAny(value string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func fileContains(repoPath string, path string, term string) bool {
	data, err := os.ReadFile(filepath.Join(repoPath, path))
	return err == nil && strings.Contains(string(data), term)
}

func hasPathPrefix(repoPath string, prefix string) bool {
	_, err := os.Stat(filepath.Join(repoPath, prefix))
	return err == nil
}

func hasWorkflow(repoPath string) bool {
	workflowsDir := filepath.Join(repoPath, ".github", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yml" || ext == ".yaml" {
			return true
		}
	}
	return false
}
