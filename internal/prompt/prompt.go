package prompt

import (
	"fmt"
	"os"
	"path/filepath"
)

func Build(repoPath string, goal string) (string, string, error) {
	if goal == "" || goal == "hello" {
		return "built-in:hello", helloPrompt(), nil
	}

	goalPath := filepath.Join(repoPath, ".factory", "goals", goal+".md")
	data, err := os.ReadFile(goalPath)
	if err != nil {
		return "", "", fmt.Errorf("goal %q not found at %s", goal, goalPath)
	}

	return goalPath, wrapGoal(goal, string(data)), nil
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

func wrapGoal(name string, body string) string {
	return fmt.Sprintf(`You are running under Factory.

Goal: %s

Before doing work, read AGENTS.md if it exists.
Follow repo instructions.
Do not merge pull requests.
Do not push to the default branch.

Goal file:

%s
`, name, body)
}
