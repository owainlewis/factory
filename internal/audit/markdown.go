package audit

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

func WriteMarkdown(w io.Writer, repoName string, report Report) error {
	passed, warnings, failed := report.Summary()
	if _, err := fmt.Fprintf(w, "# Factory Audit: %s\n\n", repoName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Repo path: `%s`\n\n", filepath.ToSlash(report.RepoPath)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "## Summary\n\n- Passed: %d\n- Warnings: %d\n- Failed: %d\n\n", passed, warnings, failed); err != nil {
		return err
	}

	if _, err := fmt.Fprint(w, "## Findings\n\n"); err != nil {
		return err
	}
	for _, bucket := range buckets(report.Findings) {
		if _, err := fmt.Fprintf(w, "### %s\n\n", titleCase(bucket)); err != nil {
			return err
		}
		for _, finding := range report.Findings {
			if finding.Bucket != bucket {
				continue
			}
			if err := writeFinding(w, finding); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprint(w, "## Candidate Objectives\n\n"); err != nil {
		return err
	}
	if len(report.CandidateObjectives) == 0 {
		_, err := fmt.Fprintln(w, "No candidate objectives. The deterministic audit did not find gaps.")
		return err
	}
	for _, objective := range report.CandidateObjectives {
		if _, err := fmt.Fprintf(w, "- Priority %d: `%s`\n", objective.Priority, objective.ID); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "   - Workflow: `%s`\n", objective.Workflow); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "   - Reason: %s\n", objective.Reason); err != nil {
			return err
		}
		if objective.Goal != "" {
			if _, err := fmt.Fprintf(w, "   - Goal: %s\n", objective.Goal); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(w, "   - Run note: write this goal to `.factory/OBJECTIVES/current-objective.md` before running the workflow."); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeFinding(w io.Writer, finding Finding) error {
	if _, err := fmt.Fprintf(w, "- **%s** `%s` %s\n", finding.Status, finding.Severity, finding.Title); err != nil {
		return err
	}
	for _, evidence := range finding.Evidence {
		if _, err := fmt.Fprintf(w, "  - Evidence: %s\n", evidence); err != nil {
			return err
		}
	}
	if finding.SuggestedObjective != "" {
		if _, err := fmt.Fprintf(w, "  - Suggested objective: `%s`\n", finding.SuggestedObjective); err != nil {
			return err
		}
	}
	if finding.Workflow != "" {
		if _, err := fmt.Fprintf(w, "  - Workflow: `%s`\n", finding.Workflow); err != nil {
			return err
		}
	}
	return nil
}

func buckets(findings []Finding) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, finding := range findings {
		if seen[finding.Bucket] {
			continue
		}
		seen[finding.Bucket] = true
		result = append(result, finding.Bucket)
	}
	sort.Strings(result)
	return result
}

func titleCase(value string) string {
	if strings.EqualFold(value, "ci") {
		return "CI"
	}
	words := strings.Fields(value)
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
