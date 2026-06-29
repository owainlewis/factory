package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Build(repoPath string, workflow string) (string, string, error) {
	if workflow == "" || workflow == "hello" {
		return "built-in:hello", helloPrompt(), nil
	}

	workflowPath := filepath.Join(repoPath, "WORKFLOWS", workflow+".md")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		return "", "", fmt.Errorf("workflow %q not found at %s", workflow, workflowPath)
	}

	context, err := compileContext(repoPath)
	if err != nil {
		return "", "", err
	}

	return workflowPath, wrapWorkflow(workflow, context, string(data)), nil
}

func helloPrompt() string {
	return `You are running under Factory.

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
Do not make network calls.`
}

func wrapWorkflow(name string, context string, body string) string {
	return fmt.Sprintf(`You are running under Factory.

Workflow: %s

Factory has compiled repository context for this run.
Do not merge pull requests.
Do not push to the default branch.

Repository context:

%s

Workflow:

%s
`, name, context, body)
}

func compileContext(repoPath string) (string, error) {
	sections := []string{}
	for _, file := range []string{"AGENTS.md", "STANDARDS.md", "JOURNAL.md"} {
		path := filepath.Join(repoPath, file)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", file, strings.TrimSpace(string(data))))
	}
	if len(sections) == 0 {
		return "No AGENTS.md, STANDARDS.md, or JOURNAL.md found.", nil
	}
	return strings.Join(sections, "\n\n"), nil
}
