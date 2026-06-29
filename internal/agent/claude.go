package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type Claude struct{}

func (Claude) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	logFile, err := os.Create(spec.LogPath)
	if err != nil {
		return RunResult{}, err
	}
	defer logFile.Close()

	var output bytes.Buffer
	permissionMode := claudePermissionMode(spec.Mode)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--permission-mode", permissionMode, spec.Prompt)
	cmd.Dir = spec.RepoPath
	cmd.Stdout = io.MultiWriter(logFile, &output)
	cmd.Stderr = io.MultiWriter(logFile, &output)

	if err := cmd.Run(); err != nil {
		if claudeBlocked(output.String()) {
			return RunResult{
				Status:  "blocked",
				Output:  output.String(),
				Blocker: "Claude Code credit balance is too low",
			}, nil
		}
		return RunResult{Status: "failed", Output: output.String()}, fmt.Errorf("claude failed: %w", err)
	}
	return RunResult{Status: "success", Output: output.String()}, nil
}

func claudePermissionMode(mode string) string {
	switch mode {
	case "execute":
		return "auto"
	default:
		return "plan"
	}
}

func claudeBlocked(output string) bool {
	return bytes.Contains([]byte(output), []byte("Credit balance is too low"))
}
