package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const healthCheckTimeout = 10 * time.Second

type health struct {
	State          string
	GitVersion     string
	RuntimeVersion string
	SourceAccess   []protocol.SourceAccess
	WeeklyLimit    *protocol.WeeklyLimit
	Error          error
}

func checkHealth(
	ctx context.Context,
	gitExecutable string,
	runtime string,
	runtimeExecutable string,
	githubExecutable string,
	_ []string,
) health {
	result := health{State: "unhealthy"}
	gitContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, _, err := runCommand(gitContext, gitExecutable, "", 64<<10, "--version")
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

	runtimeContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	stdout, _, err = runCommand(runtimeContext, runtimeExecutable, "", 64<<10, "--version")
	cancel()
	if err != nil {
		result.Error = fmt.Errorf("%s version check failed; install it and make it available on PATH", runtimeDisplayName(runtime))
		return result
	}
	result.RuntimeVersion = strings.TrimSpace(string(stdout))
	if result.RuntimeVersion == "" {
		result.Error = fmt.Errorf("%s version check returned no version", runtimeDisplayName(runtime))
		return result
	}

	authContext, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	var authArguments []string
	if runtime == protocol.RuntimeClaudeCode {
		authArguments = []string{"auth", "status", "--json"}
	} else {
		authArguments = []string{"login", "status"}
	}
	stdout, stderr, err := runCommand(authContext, runtimeExecutable, "", 64<<10, authArguments...)
	cancel()
	if err != nil {
		result.Error = fmt.Errorf("%s authentication check failed; authenticate the configured runtime", runtimeDisplayName(runtime))
		return result
	}
	if runtime == protocol.RuntimeClaudeCode {
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(stdout, &status) != nil || !status.LoggedIn {
			result.Error = errors.New("Claude Code authentication check reports that the worker is not logged in")
			return result
		}
	} else if strings.TrimSpace(string(stdout))+strings.TrimSpace(string(stderr)) == "" {
		result.Error = errors.New("Codex authentication check returned no status")
		return result
	}
	githubContext, githubCancel := context.WithTimeout(ctx, healthCheckTimeout)
	_, _, githubErr := runCommand(
		githubContext, githubExecutable, "", 64<<10,
		"auth", "status", "--hostname", "github.com",
	)
	githubCancel()
	if githubErr == nil {
		result.SourceAccess = append(result.SourceAccess, protocol.SourceAccess{
			Provider: "github", Hostname: "github.com",
		})
	}
	if runtime == protocol.RuntimeCodex {
		limitContext, limitCancel := context.WithTimeout(ctx, healthCheckTimeout)
		result.WeeklyLimit, _ = readCodexWeeklyLimit(limitContext, runtimeExecutable)
		limitCancel()
	}
	result.State = "healthy"
	return result
}

type codexRateLimitWindow struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

type codexRateLimitSnapshot struct {
	Primary   *codexRateLimitWindow `json:"primary"`
	Secondary *codexRateLimitWindow `json:"secondary"`
}

func readCodexWeeklyLimit(ctx context.Context, executable string) (*protocol.WeeklyLimit, error) {
	command := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
	configureNewProcessGroup(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = newLimitBuffer(64 << 10)
	if err := command.Start(); err != nil {
		return nil, err
	}
	pid := command.Process.Pid
	defer func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		select {
		case <-done:
			return
		case <-time.After(time.Second):
		}
		if identity, err := processIdentity(pid); err == nil {
			_ = stopOwnedProcessGroup(pid, identity, time.Second)
		} else {
			_ = forceStopStartedProcessGroup(pid)
		}
		<-done
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), protocol.MaxBodyBytes)
	if err := encoder.Encode(map[string]any{
		"method": "initialize", "id": 1,
		"params": map[string]any{"clientInfo": map[string]string{
			"name": "factory", "title": "Factory", "version": "1",
		}},
	}); err != nil {
		return nil, err
	}
	if err := readCodexResponse(scanner, 1, nil); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := encoder.Encode(map[string]any{
		"method": "account/rateLimits/read", "id": 2, "params": map[string]any{},
	}); err != nil {
		return nil, err
	}
	var result struct {
		RateLimits          codexRateLimitSnapshot            `json:"rateLimits"`
		RateLimitsByLimitID map[string]codexRateLimitSnapshot `json:"rateLimitsByLimitId"`
	}
	if err := readCodexResponse(scanner, 2, &result); err != nil {
		return nil, err
	}
	snapshot, ok := result.RateLimitsByLimitID["codex"]
	if !ok {
		snapshot = result.RateLimits
	}
	for _, window := range []*codexRateLimitWindow{snapshot.Primary, snapshot.Secondary} {
		if window != nil && window.WindowDurationMins != nil &&
			*window.WindowDurationMins == int64((7*24*time.Hour)/time.Minute) &&
			window.ResetsAt != nil && *window.ResetsAt > 0 &&
			window.UsedPercent >= 0 && window.UsedPercent <= 100 {
			return &protocol.WeeklyLimit{
				UsedPercent: window.UsedPercent,
				ResetsAt:    time.Unix(*window.ResetsAt, 0).UTC(),
			}, nil
		}
	}
	return nil, errors.New("Codex did not return a weekly rate-limit window")
}

func readCodexResponse(scanner *bufio.Scanner, id int, target any) error {
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return err
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return errors.New(message.Error.Message)
		}
		if target == nil {
			return nil
		}
		return json.Unmarshal(message.Result, target)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("Codex app server closed before replying")
}

func runtimeDisplayName(runtime string) string {
	if runtime == protocol.RuntimeClaudeCode {
		return "Claude Code"
	}
	return "Codex"
}
