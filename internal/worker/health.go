package worker

import (
	"context"
	"errors"
	"strings"
	"time"
)

const healthCheckTimeout = 10 * time.Second

type health struct {
	State        string
	GitVersion   string
	CodexVersion string
	Error        error
}

func checkHealth(ctx context.Context, gitExecutable, codexExecutable string) health {
	result := health{State: "unhealthy"}
	gitContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, stderr, err := runCommand(gitContext, gitExecutable, "", 64<<10, "--version")
	cancel()
	if err != nil {
		result.Error = errors.New("Git health check failed; install Git and make it available on PATH")
		return result
	}
	result.GitVersion = strings.TrimSpace(string(stdout))
	if result.GitVersion == "" {
		result.Error = errors.New("Git health check returned no version")
		return result
	}

	codexContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, stderr, err = runCommand(codexContext, codexExecutable, "", 64<<10, "--version")
	cancel()
	if err != nil {
		result.Error = errors.New("Codex version check failed; install Codex and make it available on PATH")
		return result
	}
	result.CodexVersion = strings.TrimSpace(string(stdout))
	if result.CodexVersion == "" {
		result.Error = errors.New("Codex version check returned no version")
		return result
	}

	authContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, stderr, err = runCommand(authContext, codexExecutable, "", 64<<10, "login", "status")
	cancel()
	if err != nil {
		result.Error = errors.New("Codex authentication check failed; run codex login and verify codex login status")
		return result
	}
	if strings.TrimSpace(string(stdout))+strings.TrimSpace(string(stderr)) == "" {
		result.Error = errors.New("Codex authentication check returned no status")
		return result
	}
	result.State = "healthy"
	return result
}
