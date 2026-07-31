package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	sourceTimeout  = 30 * time.Second
	maxSourceBytes = 4 << 20
	maxIssues      = 100
)

type Issue struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	URL         string   `json:"url"`
}

type issueResult struct {
	Issues []Issue `json:"issues"`
}

type sourceRunner struct {
	run func(context.Context, string, ...string) ([]byte, []byte, error)
}

func newSourceRunner() sourceRunner {
	return sourceRunner{run: runSourceCommand}
}

func (runner sourceRunner) list(ctx context.Context, queue QueueConfig) ([]Issue, error) {
	if queue.Source == "github" {
		return runner.listGitHub(ctx, queue)
	}
	arguments := append([]string(nil), queue.Command[1:]...)
	arguments = append(arguments, "--project", queue.Project, "--status", queue.Status)
	for _, label := range queue.Labels {
		arguments = append(arguments, "--label", label)
	}
	stdout, stderr, err := runner.run(ctx, queue.Command[0], arguments...)
	if err != nil {
		return nil, commandError(queue.Command[0], stderr, err)
	}
	var result issueResult
	if err := decodeStrictJSON(stdout, &result); err != nil {
		return nil, fmt.Errorf("decode %s source output: %w", queue.Source, err)
	}
	return validateIssues(queue, result.Issues)
}

func (runner sourceRunner) listGitHub(ctx context.Context, queue QueueConfig) ([]Issue, error) {
	arguments := []string{
		"issue", "list",
		"--repo", queue.Project,
		"--state", queue.Status,
		"--limit", strconv.Itoa(maxIssues + 1),
		"--json", "number,title,body,url,labels,state",
	}
	for _, label := range queue.Labels {
		arguments = append(arguments, "--label", label)
	}
	stdout, stderr, err := runner.run(ctx, "gh", arguments...)
	if err != nil {
		return nil, commandError("gh", stderr, err)
	}
	var values []struct {
		Number int `json:"number"`
		Title  string
		Body   string
		URL    string
		State  string
		Labels []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Color       string `json:"color"`
		} `json:"labels"`
	}
	if err := decodeStrictJSON(stdout, &values); err != nil {
		return nil, fmt.Errorf("decode gh issue list output: %w", err)
	}
	if len(values) > maxIssues {
		return nil, errors.New("GitHub issue result exceeded 100 entries; narrow the configured status or labels")
	}
	issues := make([]Issue, 0, len(values))
	for _, value := range values {
		labels := make([]string, 0, len(value.Labels))
		for _, label := range value.Labels {
			labels = append(labels, label.Name)
		}
		issues = append(issues, Issue{
			Key: "#" + strconv.Itoa(value.Number), Title: value.Title,
			Description: value.Body, State: strings.ToLower(value.State),
			Labels: labels, URL: value.URL,
		})
	}
	return validateIssues(queue, issues)
}

func validateIssues(queue QueueConfig, issues []Issue) ([]Issue, error) {
	if len(issues) > maxIssues {
		return nil, fmt.Errorf("%s source returned more than %d issues", queue.Source, maxIssues)
	}
	seen := make(map[string]bool, len(issues))
	for index := range issues {
		issue := &issues[index]
		issue.Key = strings.TrimSpace(issue.Key)
		issue.Title = strings.TrimSpace(issue.Title)
		issue.State = strings.TrimSpace(issue.State)
		issue.URL = strings.TrimSpace(issue.URL)
		if !issueKeyPattern.MatchString(issue.Key) || issue.Title == "" ||
			utf8.RuneCountInString(issue.Title) > 500 || issue.URL == "" ||
			len(issue.URL) > 2048 {
			return nil, fmt.Errorf("%s source issue %d has invalid key, title, or URL", queue.Source, index+1)
		}
		if issue.State != queue.Status {
			return nil, fmt.Errorf("%s source issue %q has state %q, want %q",
				queue.Source, issue.Key, issue.State, queue.Status)
		}
		if seen[issue.Key] {
			return nil, fmt.Errorf("%s source returned duplicate issue key %q", queue.Source, issue.Key)
		}
		seen[issue.Key] = true
		for _, label := range issue.Labels {
			if label == "" || label != strings.TrimSpace(label) || len(label) > 200 {
				return nil, fmt.Errorf("%s source issue %q has an invalid label", queue.Source, issue.Key)
			}
		}
		for _, requiredLabel := range queue.Labels {
			if !hasLabel(queue.Source, issue.Labels, requiredLabel) {
				return nil, fmt.Errorf("%s source issue %q is missing configured label %q",
					queue.Source, issue.Key, requiredLabel)
			}
		}
	}
	return issues, nil
}

func hasLabel(source string, labels []string, required string) bool {
	for _, label := range labels {
		if label == required || (source == "github" && strings.EqualFold(label, required)) {
			return true
		}
	}
	return false
}

func runSourceCommand(ctx context.Context, executable string, arguments ...string) ([]byte, []byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, arguments...)
	stdout := &limitBuffer{limit: maxSourceBytes}
	stderr := &limitBuffer{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if commandContext.Err() != nil {
		err = commandContext.Err()
	}
	if stdout.truncated {
		return nil, stderr.Bytes(), errors.New("source command output exceeded 4 MiB")
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func commandError(executable string, stderr []byte, err error) error {
	message := strings.TrimSpace(string(stderr))
	if message == "" {
		return fmt.Errorf("%s source command failed: %w", executable, err)
	}
	return fmt.Errorf("%s source command failed: %w: %s", executable, err, message)
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

type limitBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.Buffer.Write(value)
	}
	if original > remaining {
		buffer.truncated = true
	}
	return original, nil
}
