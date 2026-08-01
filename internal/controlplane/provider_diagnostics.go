package controlplane

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type GitHubCLIDiagnostic struct {
	Required      bool
	Installed     bool
	Authenticated bool
	Code          string
	Message       string
}

var githubCLILookPath = exec.LookPath
var githubCLIAuthStatus = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", "github.com").CombinedOutput()
}

func (s *Store) DiagnoseGitHubCLI(ctx context.Context) (GitHubCLIDiagnostic, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM automations
		WHERE trigger_type IN ('github_issue', 'github_pull_request')
	`).Scan(&count); err != nil {
		return GitHubCLIDiagnostic{}, unavailable(err)
	}
	if count == 0 {
		return GitHubCLIDiagnostic{Message: "No provider Automations require gh."}, nil
	}
	diagnostic := GitHubCLIDiagnostic{Required: true}
	if _, err := githubCLILookPath("gh"); err != nil {
		diagnostic.Code = "gh_missing"
		diagnostic.Message = "GitHub provider Automations require the GitHub CLI (gh), but gh was not found on PATH. Install gh, then run `gh auth login`."
		return diagnostic, nil
	}
	diagnostic.Installed = true
	commandContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := githubCLIAuthStatus(commandContext)
	if err != nil {
		diagnostic.Code = "gh_unauthenticated"
		message := strings.TrimSpace(string(output))
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			message = "gh auth status timed out"
		}
		if message != "" {
			message = ": " + truncateAutomationDiagnostic(message)
		}
		diagnostic.Message = "GitHub provider Automations cannot authenticate to github.com" + message + ". Run `gh auth login`, then verify with `gh auth status --hostname github.com`."
		return diagnostic, nil
	}
	diagnostic.Authenticated = true
	diagnostic.Message = "GitHub CLI is installed and authenticated for github.com; provider Automation evaluation is ready."
	return diagnostic, nil
}
