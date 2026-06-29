package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Build(repoPath string, workflow string, mode string) (string, string, error) {
	if workflow == "" || workflow == "hello" {
		return "built-in:hello", helloPrompt(mode), nil
	}

	workflowPath := preferredRepoFile(repoPath, filepath.Join(".factory", "WORKFLOWS", workflow+".md"), filepath.Join("WORKFLOWS", workflow+".md"))
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		return "", "", fmt.Errorf("workflow %q not found at %s", workflow, workflowPath)
	}

	context, err := compileContext(repoPath)
	if err != nil {
		return "", "", err
	}

	return workflowPath, wrapWorkflow(workflow, mode, context, string(data)), nil
}

func helloPrompt(mode string) string {
	return fmt.Sprintf(`You are running under Factory.

Runtime mode: %s
This is a no-edit smoke test.
Read README.md if it exists.
Print exactly three short lines:
1. Repo: <the repo name or unknown>
2. Purpose: <one plain sentence>
3. Factory: hello run complete

Do not edit files.
Do not create branches.
Do not run tests.
Do not open issues or pull requests.
Do not make network calls.`, mode)
}

func wrapWorkflow(name string, mode string, context string, body string) string {
	return fmt.Sprintf(`You are running under Factory.

Workflow: %s
Runtime mode: %s

Factory has compiled repository context for this run.
Do not merge pull requests.
Do not push to the default branch.
In plan mode, inspect the repo and report the exact next steps without editing files.
In execute mode, make only the smallest workflow-scoped change, create a non-default branch, commit it, push it, and open a draft pull request when the workflow asks for code changes.

Repository context:

%s

Workflow:

%s
`, name, mode, context, body)
}

func compileContext(repoPath string) (string, error) {
	sections := []string{}
	for _, file := range []struct {
		title     string
		preferred string
		fallback  string
	}{
		{title: "AGENTS.md", preferred: filepath.Join(".factory", "AGENTS.md"), fallback: "AGENTS.md"},
		{title: "STANDARDS.md", preferred: filepath.Join(".factory", "STANDARDS.md"), fallback: "STANDARDS.md"},
		{title: "JOURNAL.md", preferred: filepath.Join(".factory", "JOURNAL.md"), fallback: "JOURNAL.md"},
	} {
		path := preferredRepoFile(repoPath, file.preferred, file.fallback)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", file.title, err)
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", file.title, strings.TrimSpace(string(data))))
	}
	if path, ok := currentObjectivePath(repoPath); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read current objective: %w", err)
		}
		sections = append(sections, fmt.Sprintf("## Current Objective\n\n%s", strings.TrimSpace(string(data))))
	}
	if len(sections) == 0 {
		return "No AGENTS.md, STANDARDS.md, JOURNAL.md, or current objective found.", nil
	}
	return strings.Join(sections, "\n\n"), nil
}

func currentObjectivePath(repoPath string) (string, bool) {
	for _, path := range []string{
		filepath.Join(".factory", "OBJECTIVES", "current-objective.md"),
		filepath.Join(".factory", "OBJECTIVES", "current.md"),
		filepath.Join("OBJECTIVES", "current-objective.md"),
		filepath.Join("OBJECTIVES", "current.md"),
	} {
		fullPath := filepath.Join(repoPath, path)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath, true
		}
	}
	return "", false
}

func preferredRepoFile(repoPath string, preferred string, fallback string) string {
	preferredPath := filepath.Join(repoPath, preferred)
	if _, err := os.Stat(preferredPath); err == nil {
		return preferredPath
	}
	return filepath.Join(repoPath, fallback)
}
