package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	automationCommandTimeout       = 30 * time.Second
	automationStdoutLimit          = 4 << 20
	automationStderrLimit          = 64 << 10
	automationDiagnosticLimit      = 4 << 10
	automationShutdownDrainTimeout = 5 * time.Second
	maxObservationBytes            = 16 << 10
	maxConcurrentAutomationChecks  = 4
)

type automationCheckError struct {
	code    string
	message string
}

func (e *automationCheckError) Error() string { return e.message }

type githubIssueLister interface {
	ListIssues(context.Context, string, protocol.GitHubIssueTrigger) ([]protocol.GitHubIssueMatch, error)
}

type githubPullRequestLister interface {
	ListPullRequests(context.Context, string, protocol.GitHubPullRequestTrigger) ([]protocol.GitHubPullRequestMatch, error)
}

type githubIssueRunner struct {
	lookPath func(string) (string, error)
	run      func(context.Context, string, ...string) ([]byte, []byte, bool, bool, error)
}

func newGitHubIssueRunner() githubIssueRunner {
	return githubIssueRunner{lookPath: exec.LookPath, run: runAutomationCommand}
}

func (runner githubIssueRunner) ListIssues(
	ctx context.Context,
	repository string,
	trigger protocol.GitHubIssueTrigger,
) ([]protocol.GitHubIssueMatch, error) {
	if _, err := runner.lookPath("gh"); err != nil {
		return nil, &automationCheckError{
			code:    "gh_not_found",
			message: "GitHub CLI (gh) was not found on PATH. Install gh, then run `gh auth login`.",
		}
	}
	project := strings.TrimPrefix(repository, "github.com/")
	arguments := []string{
		"issue", "list", "--repo", project, "--state", trigger.State,
		"--limit", strconv.Itoa(protocol.MaxAutomationMatches + 1),
		"--json", "number,title,url,labels,state",
	}
	for _, label := range trigger.RequiredLabels {
		arguments = append(arguments, "--label", label)
	}
	stdout, stderr, stdoutTooLarge, stderrTooLarge, err := runner.run(ctx, "gh", arguments...)
	if stdoutTooLarge {
		return nil, &automationCheckError{code: "gh_output_too_large", message: "gh output exceeded 4 MiB. Narrow the issue state or required labels."}
	}
	if stderrTooLarge {
		return nil, &automationCheckError{code: "gh_error_output_too_large", message: "gh error output exceeded 64 KiB. Run `gh auth status` and retry the check."}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &automationCheckError{code: "gh_timed_out", message: "gh did not finish within 30 seconds. Check GitHub connectivity and narrow the match."}
		}
		if errors.Is(err, context.Canceled) {
			return nil, &automationCheckError{code: "gh_cancelled", message: "The GitHub check was cancelled before completion."}
		}
		message := strings.TrimSpace(string(stderr))
		lower := strings.ToLower(message)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "not logged") || strings.Contains(lower, "authentication") {
			return nil, &automationCheckError{code: "gh_unauthenticated", message: "gh is not authenticated for github.com. Run `gh auth login` and verify with `gh auth status`."}
		}
		if message == "" {
			message = err.Error()
		}
		return nil, &automationCheckError{code: "gh_failed", message: truncateAutomationDiagnostic("gh issue list failed: " + message + ". Run `gh auth status` and verify repository access.")}
	}
	var values []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Labels []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Color       string `json:"color"`
		} `json:"labels"`
	}
	if err := decodeAutomationJSON(stdout, &values); err != nil {
		return nil, &automationCheckError{code: "gh_malformed_output", message: "gh returned malformed or unexpected JSON: " + truncateAutomationDiagnostic(err.Error())}
	}
	if len(values) > protocol.MaxAutomationMatches {
		return nil, &automationCheckError{code: "gh_match_limit", message: "gh returned more than 100 issues. Add required labels or narrow the issue state."}
	}
	matches := make([]protocol.GitHubIssueMatch, 0, len(values))
	seen := make(map[int]protocol.GitHubIssueMatch, len(values))
	for index, value := range values {
		labels := make([]string, 0, len(value.Labels))
		for _, label := range value.Labels {
			labels = append(labels, label.Name)
		}
		match := protocol.GitHubIssueMatch{
			Number: value.Number, Title: strings.TrimSpace(value.Title),
			URL: strings.TrimSpace(value.URL), State: strings.ToLower(strings.TrimSpace(value.State)),
			Labels: labels,
		}
		if err := validateGitHubIssueMatch(repository, trigger, match); err != nil {
			return nil, &automationCheckError{code: "gh_invalid_output", message: fmt.Sprintf("gh result %d is invalid: %s", index+1, err)}
		}
		if previous, exists := seen[match.Number]; exists {
			if !equalGitHubIssueMatch(previous, match) {
				return nil, &automationCheckError{code: "gh_conflicting_duplicate", message: fmt.Sprintf("gh returned conflicting entries for issue #%d", match.Number)}
			}
			continue
		}
		seen[match.Number] = match
		matches = append(matches, match)
	}
	return matches, nil
}

func (runner githubIssueRunner) ListPullRequests(
	ctx context.Context,
	repository string,
	trigger protocol.GitHubPullRequestTrigger,
) ([]protocol.GitHubPullRequestMatch, error) {
	if _, err := runner.lookPath("gh"); err != nil {
		return nil, &automationCheckError{
			code:    "gh_not_found",
			message: "GitHub CLI (gh) was not found on PATH. Install gh, then run `gh auth login`.",
		}
	}
	checkContext, cancel := context.WithTimeout(ctx, automationCommandTimeout)
	defer cancel()
	branches := trigger.BaseBranches
	if len(branches) == 0 {
		branches = []string{""}
	}
	matches := make([]protocol.GitHubPullRequestMatch, 0)
	seen := make(map[int]protocol.GitHubPullRequestMatch)
	for _, branch := range branches {
		values, err := runner.listPullRequestsForBase(checkContext, repository, trigger, branch)
		if err != nil {
			return nil, err
		}
		for _, match := range values {
			if previous, exists := seen[match.Number]; exists {
				if !equalGitHubPullRequestMatch(previous, match) {
					return nil, &automationCheckError{code: "gh_conflicting_duplicate", message: fmt.Sprintf("gh returned conflicting entries for pull request #%d", match.Number)}
				}
				continue
			}
			seen[match.Number] = match
			matches = append(matches, match)
			if len(matches) > protocol.MaxAutomationMatches {
				return nil, &automationCheckError{code: "gh_match_limit", message: "gh returned more than 100 pull requests. Add required labels, base branches, or narrow the pull-request state."}
			}
		}
	}
	return matches, nil
}

func (runner githubIssueRunner) listPullRequestsForBase(
	ctx context.Context,
	repository string,
	trigger protocol.GitHubPullRequestTrigger,
	baseBranch string,
) ([]protocol.GitHubPullRequestMatch, error) {
	project := strings.TrimPrefix(repository, "github.com/")
	arguments := []string{
		"pr", "list", "--repo", project, "--state", trigger.State,
		"--limit", strconv.Itoa(protocol.MaxAutomationMatches + 1),
		"--json", "number,title,url,labels,state,isDraft,baseRefName,headRefOid",
	}
	if !trigger.IncludeDrafts {
		arguments = append(arguments, "--search", "draft:false")
	}
	if baseBranch != "" {
		arguments = append(arguments, "--base", baseBranch)
	}
	for _, label := range trigger.RequiredLabels {
		arguments = append(arguments, "--label", label)
	}
	stdout, stderr, stdoutTooLarge, stderrTooLarge, err := runner.run(ctx, "gh", arguments...)
	if stdoutTooLarge {
		return nil, &automationCheckError{code: "gh_output_too_large", message: "gh output exceeded 4 MiB. Narrow the pull-request state, labels, or base branches."}
	}
	if stderrTooLarge {
		return nil, &automationCheckError{code: "gh_error_output_too_large", message: "gh error output exceeded 64 KiB. Run `gh auth status` and retry the check."}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &automationCheckError{code: "gh_timed_out", message: "gh did not finish the pull-request check within 30 seconds. Check GitHub connectivity and narrow the match."}
		}
		if errors.Is(err, context.Canceled) {
			return nil, &automationCheckError{code: "gh_cancelled", message: "The GitHub pull-request check was cancelled before completion."}
		}
		message := strings.TrimSpace(string(stderr))
		lower := strings.ToLower(message)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "not logged") || strings.Contains(lower, "authentication") {
			return nil, &automationCheckError{code: "gh_unauthenticated", message: "gh is not authenticated for github.com. Run `gh auth login` and verify with `gh auth status`."}
		}
		if strings.Contains(lower, "permission") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "resource not accessible") || strings.Contains(lower, "could not resolve to a repository") || strings.Contains(lower, "not found") {
			return nil, &automationCheckError{code: "gh_permission_denied", message: "gh cannot access this repository or its pull requests. Verify private-repository access and token permissions with `gh auth status`."}
		}
		if message == "" {
			message = err.Error()
		}
		return nil, &automationCheckError{code: "gh_failed", message: truncateAutomationDiagnostic("gh pr list failed: " + message + ". Run `gh auth status` and verify repository access.")}
	}
	var values []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		State       string `json:"state"`
		IsDraft     *bool  `json:"isDraft"`
		BaseRefName string `json:"baseRefName"`
		HeadRefOID  string `json:"headRefOid"`
		Labels      []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Color       string `json:"color"`
		} `json:"labels"`
	}
	if err := decodeAutomationJSON(stdout, &values); err != nil {
		return nil, &automationCheckError{code: "gh_malformed_output", message: "gh returned malformed or unexpected pull-request JSON: " + truncateAutomationDiagnostic(err.Error())}
	}
	if values == nil {
		return nil, &automationCheckError{code: "gh_malformed_output", message: "gh returned null instead of a pull-request JSON array"}
	}
	if len(values) > protocol.MaxAutomationMatches {
		return nil, &automationCheckError{code: "gh_match_limit", message: "gh returned more than 100 pull requests for one base branch. Add required labels or narrow the pull-request state."}
	}
	matches := make([]protocol.GitHubPullRequestMatch, 0, len(values))
	for index, value := range values {
		if value.IsDraft == nil {
			return nil, &automationCheckError{code: "gh_malformed_output", message: fmt.Sprintf("gh pull-request result %d is missing isDraft", index+1)}
		}
		labels := make([]string, 0, len(value.Labels))
		for _, label := range value.Labels {
			labels = append(labels, label.Name)
		}
		match := protocol.GitHubPullRequestMatch{
			Number: value.Number, Title: strings.TrimSpace(value.Title), URL: strings.TrimSpace(value.URL),
			State: strings.ToLower(strings.TrimSpace(value.State)), IsDraft: *value.IsDraft,
			BaseBranch: strings.TrimSpace(value.BaseRefName), HeadCommit: strings.TrimSpace(value.HeadRefOID),
			Labels: labels,
		}
		if err := validateGitHubPullRequestMatch(repository, trigger, match); err != nil {
			return nil, &automationCheckError{code: "gh_invalid_output", message: fmt.Sprintf("gh pull-request result %d is invalid: %s", index+1, err)}
		}
		matches = append(matches, match)
	}
	return matches, nil
}

func runAutomationCommand(
	ctx context.Context,
	executable string,
	arguments ...string,
) ([]byte, []byte, bool, bool, error) {
	return runAutomationCommandWithLimits(
		ctx, automationCommandTimeout, automationStdoutLimit, automationStderrLimit,
		executable, arguments...,
	)
}

func runAutomationCommandWithLimits(
	ctx context.Context,
	timeout time.Duration,
	stdoutLimit, stderrLimit int,
	executable string,
	arguments ...string,
) ([]byte, []byte, bool, bool, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, arguments...)
	stdout := &automationLimitBuffer{limit: stdoutLimit}
	stderr := &automationLimitBuffer{limit: stderrLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if commandContext.Err() != nil {
		err = commandContext.Err()
	}
	return stdout.Bytes(), stderr.Bytes(), stdout.truncated, stderr.truncated, err
}

type automationLimitBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *automationLimitBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	}
	if original > remaining {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *automationLimitBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func decodeAutomationJSON(body []byte, target any) error {
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

func validateGitHubIssueMatch(
	repository string,
	trigger protocol.GitHubIssueTrigger,
	match protocol.GitHubIssueMatch,
) error {
	if match.Number < 1 || int64(match.Number) > int64(1<<31-1) {
		return errors.New("issue number must be a positive 32-bit integer")
	}
	if match.Title == "" || utf8.RuneCountInString(match.Title) > 500 || len([]byte(match.Title)) > 2<<10 {
		return errors.New("title must be nonblank, at most 500 characters, and at most 2 KiB")
	}
	if match.State != trigger.State {
		return fmt.Errorf("state %q does not match configured state %q", match.State, trigger.State)
	}
	parsed, err := url.Parse(match.URL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must be an HTTPS github.com issue URL without query or fragment")
	}
	expectedPath := "/" + strings.TrimPrefix(repository, "github.com/") + "/issues/" + strconv.Itoa(match.Number)
	if !strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), expectedPath) || len([]byte(match.URL)) > 2048 {
		return errors.New("URL does not identify the configured repository and issue number")
	}
	if len(match.Labels) > 100 {
		return errors.New("issue has more than 100 labels")
	}
	labelBytes := 0
	for _, label := range match.Labels {
		labelBytes += len([]byte(label))
		if label == "" || label != strings.TrimSpace(label) || len([]byte(label)) > 200 {
			return errors.New("issue contains an invalid label")
		}
	}
	if labelBytes > 8<<10 {
		return errors.New("issue labels exceed 8 KiB")
	}
	for _, required := range trigger.RequiredLabels {
		found := false
		for _, label := range match.Labels {
			if strings.EqualFold(required, label) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("issue is missing required label %q", required)
		}
	}
	encoded, err := json.Marshal(match)
	if err != nil || len(encoded) > maxObservationBytes {
		return errors.New("canonical issue metadata exceeds 16 KiB")
	}
	return nil
}

func equalGitHubIssueMatch(left, right protocol.GitHubIssueMatch) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

var githubHeadCommitPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func validateGitHubPullRequestMatch(
	repository string,
	trigger protocol.GitHubPullRequestTrigger,
	match protocol.GitHubPullRequestMatch,
) error {
	if match.Number < 1 || int64(match.Number) > int64(1<<31-1) {
		return errors.New("pull-request number must be a positive 32-bit integer")
	}
	if match.Title == "" || utf8.RuneCountInString(match.Title) > 500 || len([]byte(match.Title)) > 2<<10 {
		return errors.New("title must be nonblank, at most 500 characters, and at most 2 KiB")
	}
	if match.State != trigger.State {
		return fmt.Errorf("state %q does not match configured state %q", match.State, trigger.State)
	}
	if match.IsDraft && !trigger.IncludeDrafts {
		return errors.New("draft pull request does not match include_drafts=false")
	}
	parsed, err := url.Parse(match.URL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must be an HTTPS github.com pull-request URL without query or fragment")
	}
	expectedPath := "/" + strings.TrimPrefix(repository, "github.com/") + "/pull/" + strconv.Itoa(match.Number)
	if !strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), expectedPath) || len([]byte(match.URL)) > 2048 {
		return errors.New("URL does not identify the configured repository and pull-request number")
	}
	if match.BaseBranch == "" || match.BaseBranch != strings.TrimSpace(match.BaseBranch) || len([]byte(match.BaseBranch)) > 255 {
		return errors.New("base branch must be nonblank and at most 255 bytes")
	}
	if len(trigger.BaseBranches) > 0 {
		found := false
		for _, configured := range trigger.BaseBranches {
			if configured == match.BaseBranch {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("base branch %q does not match configured base branches", match.BaseBranch)
		}
	}
	if !githubHeadCommitPattern.MatchString(match.HeadCommit) {
		return errors.New("head commit must contain 40 through 64 lowercase hexadecimal characters")
	}
	if len(match.Labels) > 100 {
		return errors.New("pull request has more than 100 labels")
	}
	labelBytes := 0
	for _, label := range match.Labels {
		labelBytes += len([]byte(label))
		if label == "" || label != strings.TrimSpace(label) || len([]byte(label)) > 200 {
			return errors.New("pull request contains an invalid label")
		}
	}
	if labelBytes > 8<<10 {
		return errors.New("pull-request labels exceed 8 KiB")
	}
	for _, required := range trigger.RequiredLabels {
		found := false
		for _, label := range match.Labels {
			if strings.EqualFold(required, label) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pull request is missing required label %q", required)
		}
	}
	encoded, err := json.Marshal(match)
	if err != nil || len(encoded) > maxObservationBytes {
		return errors.New("canonical pull-request metadata exceeds 16 KiB")
	}
	return nil
}

func equalGitHubPullRequestMatch(left, right protocol.GitHubPullRequestMatch) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func truncateAutomationDiagnostic(value string) string {
	if len([]byte(value)) <= automationDiagnosticLimit {
		return value
	}
	return string([]byte(value)[:automationDiagnosticLimit])
}

type automationEvaluation struct {
	Automation           protocol.Automation
	WorkflowRevisionID   string
	WorkflowInstructions string
	Token                string
}

type AutomationService struct {
	store       *Store
	runner      githubIssueLister
	logger      *slog.Logger
	wake        chan struct{}
	slots       chan struct{}
	admissionMu sync.RWMutex
	admitting   bool
	mu          sync.Mutex
	cancel      map[string]automationCancellation
	// afterScheduleAdmission is a deterministic test seam for the shutdown race
	// between committing a due occurrence and dispatching committed work.
	afterScheduleAdmission func()
}

type automationCancellation struct {
	token  string
	cancel context.CancelFunc
}

func NewAutomationService(store *Store, logger *slog.Logger) *AutomationService {
	if logger == nil {
		logger = slog.Default()
	}
	return newAutomationService(store, logger, newGitHubIssueRunner())
}

func newAutomationService(store *Store, logger *slog.Logger, runner githubIssueLister) *AutomationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationService{
		store: store, runner: runner, logger: logger,
		wake: make(chan struct{}, 1), slots: make(chan struct{}, maxConcurrentAutomationChecks),
		admitting: true, cancel: make(map[string]automationCancellation),
	}
}

func (service *AutomationService) StopAdmission() {
	service.admissionMu.Lock()
	service.admitting = false
	service.admissionMu.Unlock()
}

func (service *AutomationService) RunNow(
	ctx context.Context,
	automationID string,
	input protocol.RunAutomationRequest,
) (protocol.AutomationDetail, error) {
	service.admissionMu.RLock()
	defer service.admissionMu.RUnlock()
	if !service.admitting {
		return protocol.AutomationDetail{}, conflict("automation_shutting_down", "Automation occurrence admission is closed during shutdown")
	}
	return service.store.RunAutomationNow(ctx, automationID, input)
}

func (service *AutomationService) admitDueSchedules(ctx context.Context, limit int) error {
	service.admissionMu.RLock()
	defer service.admissionMu.RUnlock()
	if !service.admitting {
		return nil
	}
	return service.store.processDueSchedules(ctx, limit)
}

func (service *AutomationService) acquireSlot(ctx context.Context) error {
	select {
	case service.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *AutomationService) tryAcquireSlot() bool {
	select {
	case service.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (service *AutomationService) releaseSlot() { <-service.slots }

func (service *AutomationService) Wake() {
	select {
	case service.wake <- struct{}{}:
	default:
	}
}

func (service *AutomationService) Cancel(automationID, token string) {
	if token == "" {
		return
	}
	service.mu.Lock()
	cancellation := service.cancel[automationID]
	service.mu.Unlock()
	if cancellation.token == token && cancellation.cancel != nil {
		cancellation.cancel()
	}
}

func (service *AutomationService) Test(
	ctx context.Context,
	automationID string,
) (protocol.TestAutomationResult, error) {
	detail, err := service.store.Automation(ctx, automationID)
	if err != nil {
		return protocol.TestAutomationResult{}, err
	}
	if detail.Automation.Trigger.Type == protocol.AutomationTriggerSchedule {
		schedule, _, _, parseErr := parseCronSchedule(detail.Automation.Trigger.Cron, detail.Automation.Trigger.Timezone)
		if parseErr != nil {
			return protocol.TestAutomationResult{}, unavailable(parseErr)
		}
		next, nextErr := schedule.Next(service.store.now())
		if nextErr != nil {
			return protocol.TestAutomationResult{}, invalid("invalid_cron", nextErr.Error())
		}
		return protocol.TestAutomationResult{Matches: []protocol.AutomationMatch{}, NextDueAt: &next}, nil
	}
	if err := service.acquireSlot(ctx); err != nil {
		return protocol.TestAutomationResult{}, &ServiceError{
			Code: "automation_test_busy", Message: "Automation Test could not start before the request deadline", Status: 503, Err: err,
		}
	}
	defer service.releaseSlot()
	result := protocol.TestAutomationResult{Matches: []protocol.AutomationMatch{}}
	switch detail.Automation.Trigger.Type {
	case protocol.AutomationTriggerGitHubIssue:
		var matches []protocol.GitHubIssueMatch
		matches, err = service.runner.ListIssues(ctx, detail.Automation.RepositoryIdentity, detail.Automation.Trigger.GitHubIssue())
		for _, match := range matches {
			result.Matches = append(result.Matches, protocol.AutomationMatch{
				Number: match.Number, Title: match.Title, URL: match.URL, State: match.State, Labels: match.Labels,
			})
		}
	case protocol.AutomationTriggerGitHubPullRequest:
		lister, ok := service.runner.(githubPullRequestLister)
		if !ok {
			return protocol.TestAutomationResult{}, unavailable(errors.New("GitHub pull-request evaluator is unavailable"))
		}
		var matches []protocol.GitHubPullRequestMatch
		matches, err = lister.ListPullRequests(ctx, detail.Automation.RepositoryIdentity, detail.Automation.Trigger.GitHubPullRequest())
		for _, match := range matches {
			draft := match.IsDraft
			result.Matches = append(result.Matches, protocol.AutomationMatch{
				Number: match.Number, Title: match.Title, URL: match.URL, State: match.State,
				Labels: match.Labels, IsDraft: &draft, BaseBranch: match.BaseBranch, HeadCommit: match.HeadCommit,
			})
		}
	default:
		return protocol.TestAutomationResult{}, unavailable(errors.New("Automation has an unsupported trigger type"))
	}
	if err != nil {
		var checkErr *automationCheckError
		if errors.As(err, &checkErr) {
			return protocol.TestAutomationResult{}, &ServiceError{Code: checkErr.code, Message: checkErr.message, Status: 503}
		}
		return protocol.TestAutomationResult{}, unavailable(err)
	}
	return result, nil
}

func (service *AutomationService) Run(ctx context.Context) {
	if err := service.store.recoverAutomationRuntime(ctx); err != nil {
		service.logger.Error("automation_recovery_failed", "error_class", "storage_unavailable")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var checks sync.WaitGroup
	runPass := func() {
		if ctx.Err() == nil {
			if err := service.admitDueSchedules(ctx, protocol.MaxAutomationMatches); err != nil {
				service.logger.Error("automation_schedule_failed", "error_class", "storage_unavailable")
			}
			if service.afterScheduleAdmission != nil {
				service.afterScheduleAdmission()
			}
		}
		for {
			if ctx.Err() != nil {
				break
			}
			if !service.tryAcquireSlot() {
				break
			}
			evaluation, found, err := service.store.reserveDueAutomation(ctx)
			if err != nil {
				service.releaseSlot()
				if ctx.Err() == nil {
					service.logger.Error("automation_reserve_failed", "error_class", "storage_unavailable")
				}
				break
			}
			if !found {
				service.releaseSlot()
				break
			}
			checks.Add(1)
			go func(evaluation automationEvaluation) {
				defer checks.Done()
				defer service.releaseSlot()
				service.evaluate(ctx, evaluation)
			}(evaluation)
		}
		if ctx.Err() == nil {
			if err := service.store.dispatchPendingOccurrences(ctx, protocol.MaxAutomationMatches); err != nil {
				service.logger.Error("automation_dispatch_failed", "error_class", "storage_unavailable")
			}
		}
	}
	runPass()
	for {
		select {
		case <-ctx.Done():
			service.StopAdmission()
			service.mu.Lock()
			for _, cancellation := range service.cancel {
				cancellation.cancel()
			}
			service.mu.Unlock()
			checks.Wait()
			service.drainCommittedOccurrences()
			return
		case <-service.wake:
			runPass()
		case <-ticker.C:
			runPass()
		}
	}
}

func (service *AutomationService) drainCommittedOccurrences() {
	ctx, cancel := context.WithTimeout(context.Background(), automationShutdownDrainTimeout)
	defer cancel()
	for {
		pending, err := service.store.hasDispatchableOccurrences(ctx)
		if err != nil {
			service.logger.Error("automation_shutdown_drain_failed", "error_class", "storage_unavailable")
			return
		}
		if !pending {
			return
		}
		if err := service.store.dispatchPendingOccurrences(ctx, protocol.MaxAutomationMatches); err != nil {
			service.logger.Error("automation_shutdown_drain_failed", "error_class", "storage_unavailable")
			return
		}
	}
}

func (service *AutomationService) evaluate(ctx context.Context, evaluation automationEvaluation) {
	checkContext, cancel := context.WithCancel(ctx)
	service.mu.Lock()
	service.cancel[evaluation.Automation.ID] = automationCancellation{token: evaluation.Token, cancel: cancel}
	service.mu.Unlock()
	defer func() {
		cancel()
		service.mu.Lock()
		if current := service.cancel[evaluation.Automation.ID]; current.token == evaluation.Token {
			delete(service.cancel, evaluation.Automation.ID)
		}
		service.mu.Unlock()
	}()
	current, err := service.store.automationEvaluationCurrent(checkContext, evaluation)
	if err != nil {
		if checkContext.Err() == nil {
			service.logger.Error("automation_check_revalidation_failed", "automation_id", evaluation.Automation.ID, "error_class", "storage_unavailable")
		}
		return
	}
	if !current || checkContext.Err() != nil {
		return
	}
	var issueMatches []protocol.GitHubIssueMatch
	var pullRequestMatches []protocol.GitHubPullRequestMatch
	switch evaluation.Automation.Trigger.Type {
	case protocol.AutomationTriggerGitHubIssue:
		issueMatches, err = service.runner.ListIssues(checkContext, evaluation.Automation.RepositoryIdentity, evaluation.Automation.Trigger.GitHubIssue())
	case protocol.AutomationTriggerGitHubPullRequest:
		lister, ok := service.runner.(githubPullRequestLister)
		if !ok {
			err = errors.New("GitHub pull-request evaluator is unavailable")
		} else {
			pullRequestMatches, err = lister.ListPullRequests(checkContext, evaluation.Automation.RepositoryIdentity, evaluation.Automation.Trigger.GitHubPullRequest())
		}
	default:
		err = errors.New("Automation has an unsupported trigger type")
	}
	if checkContext.Err() != nil {
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		var checkErr *automationCheckError
		if !errors.As(err, &checkErr) {
			checkErr = &automationCheckError{code: "gh_failed", message: truncateAutomationDiagnostic(err.Error())}
		}
		_ = service.store.completeAutomationFailure(context.Background(), evaluation, checkErr)
		return
	}
	if evaluation.Automation.Trigger.Type == protocol.AutomationTriggerGitHubIssue {
		err = service.store.completeAutomationSuccess(context.Background(), evaluation, issueMatches)
	} else {
		err = service.store.completePullRequestAutomationSuccess(context.Background(), evaluation, pullRequestMatches)
	}
	if err != nil && ctx.Err() == nil {
		service.logger.Error("automation_check_commit_failed", "automation_id", evaluation.Automation.ID, "error_class", "storage_unavailable")
	}
}

func (s *Store) recoverAutomationRuntime(ctx context.Context) error {
	now := s.now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = NULL, evaluation_started_at = NULL,
		    next_check_at = CASE WHEN enabled = 1 THEN ? ELSE NULL END,
		    health_status = CASE WHEN enabled = 1 THEN 'pending' ELSE 'disabled' END,
		    health_code = CASE WHEN enabled = 1 THEN 'check_recovered' ELSE '' END,
		    health_message = CASE WHEN enabled = 1 THEN 'Recovered an interrupted check; retrying.' ELSE 'Automation is disabled.' END
		WHERE evaluation_token IS NOT NULL
	`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'pending', diagnostic = 'legacy_resume_recovered', updated_at = ?
		WHERE state = 'dispatching' AND legacy_task_request_json IS NOT NULL
	`, now)
	return err
}

func (s *Store) reserveDueAutomation(ctx context.Context) (automationEvaluation, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	var id string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM automations
		WHERE enabled = 1 AND trigger_type != 'schedule' AND evaluation_token IS NULL
		  AND next_check_at IS NOT NULL AND next_check_at <= ?
		ORDER BY next_check_at, id LIMIT 1
	`, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return automationEvaluation{}, false, nil
	}
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	token, err := newID()
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = ?, evaluation_started_at = ?, health_status = 'checking',
		    health_code = '', health_message = 'Checking GitHub now.'
		WHERE id = ? AND enabled = 1 AND evaluation_token IS NULL
	`, token, now, id)
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return automationEvaluation{}, false, err
	}
	var evaluation automationEvaluation
	evaluation.Token = token
	evaluation.Automation, err = scanAutomation(tx.QueryRowContext(ctx, automationSelect+`
		WHERE automation.id = ?`, id))
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	err = tx.QueryRowContext(ctx, `
		SELECT revision.id, revision.instructions
		FROM workflows workflow
		JOIN workflow_revisions revision ON revision.id = workflow.current_revision_id
		WHERE workflow.id = ? AND workflow.enabled = 1
	`, evaluation.Automation.WorkflowID).Scan(&evaluation.WorkflowRevisionID, &evaluation.WorkflowInstructions)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = tx.ExecContext(ctx, `
			UPDATE automations SET evaluation_token = NULL, evaluation_started_at = NULL,
			    health_status = 'blocked', health_code = 'workflow_disabled',
			    health_message = 'Enable the selected Workflow before checks can run.', next_check_at = ?
			WHERE id = ? AND evaluation_token = ?
		`, now+time.Minute.Milliseconds(), id, token)
		if err := tx.Commit(); err != nil {
			return automationEvaluation{}, false, unavailable(err)
		}
		return automationEvaluation{}, false, nil
	}
	if err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	var repositoryEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM repositories WHERE id = ?`, evaluation.Automation.RepositoryID).Scan(&repositoryEnabled); err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	if repositoryEnabled == 0 {
		_, _ = tx.ExecContext(ctx, `
			UPDATE automations SET evaluation_token = NULL, evaluation_started_at = NULL,
			    health_status = 'blocked', health_code = 'repository_disabled',
			    health_message = 'Enable the selected repository before checks can run.', next_check_at = ?
			WHERE id = ? AND evaluation_token = ?
		`, now+time.Minute.Milliseconds(), id, token)
		if err := tx.Commit(); err != nil {
			return automationEvaluation{}, false, unavailable(err)
		}
		return automationEvaluation{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return automationEvaluation{}, false, unavailable(err)
	}
	return evaluation, true, nil
}

func (s *Store) automationEvaluationCurrent(ctx context.Context, evaluation automationEvaluation) (bool, error) {
	var current bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM automations
			WHERE id = ? AND enabled = 1 AND evaluation_token = ?
		)
	`, evaluation.Automation.ID, evaluation.Token).Scan(&current)
	if err != nil {
		return false, unavailable(err)
	}
	return current, nil
}

func (s *Store) completeAutomationFailure(
	ctx context.Context,
	evaluation automationEvaluation,
	checkErr *automationCheckError,
) error {
	now := s.now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = NULL, evaluation_started_at = NULL,
		    last_checked_at = ?, next_check_at = ?, health_status = 'error',
		    health_code = ?, health_message = ?
		WHERE id = ? AND enabled = 1 AND evaluation_token = ?
	`, now, now+int64(evaluation.Automation.Trigger.PollIntervalSeconds)*1000,
		checkErr.code, truncateAutomationDiagnostic(checkErr.message),
		evaluation.Automation.ID, evaluation.Token)
	return err
}

func (s *Store) completeAutomationSuccess(
	ctx context.Context,
	evaluation automationEvaluation,
	matches []protocol.GitHubIssueMatch,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	var eligible int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM automations automation
		JOIN workflows workflow ON workflow.id = automation.workflow_id AND workflow.enabled = 1
		JOIN repositories repository ON repository.id = automation.repository_id AND repository.enabled = 1
		WHERE automation.id = ? AND automation.enabled = 1 AND automation.evaluation_token = ?
	`, evaluation.Automation.ID, evaluation.Token).Scan(&eligible); err != nil {
		return unavailable(err)
	}
	if eligible == 0 {
		return nil
	}
	newMatches := make([]protocol.GitHubIssueMatch, 0, len(matches))
	for _, match := range matches {
		var exists int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM automation_github_issue_occurrences
			WHERE automation_id = ? AND issue_number = ?
		`, evaluation.Automation.ID, match.Number).Scan(&exists); err != nil {
			return unavailable(err)
		}
		if exists == 0 {
			newMatches = append(newMatches, match)
		}
	}
	var occurrenceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
		return unavailable(err)
	}
	if occurrenceCount+len(newMatches) > protocol.MaxAutomationOccurrences {
		return s.completeAutomationFailureTx(ctx, tx, evaluation, &automationCheckError{
			code: "occurrence_limit_reached", message: "The durable Occurrence limit has been reached. Archive history before retrying.",
		})
	}
	now := s.now().UnixMilli()
	requiredLabels, _ := json.Marshal(evaluation.Automation.Trigger.RequiredLabels)
	for _, match := range newMatches {
		occurrenceID, err := newID()
		if err != nil {
			return unavailable(err)
		}
		observedLabels, err := json.Marshal(match.Labels)
		if err != nil {
			return unavailable(err)
		}
		prompt, err := protocol.ResolveGitHubIssueAutomationPrompt(
			evaluation.WorkflowInstructions, evaluation.Automation.Context,
			evaluation.Automation.Trigger.State, evaluation.Automation.Trigger.RequiredLabels, match,
		)
		state, diagnostic := "pending", ""
		var storedPrompt any = prompt
		if err != nil || len([]byte(prompt)) > protocol.MaxResolvedPromptBytes ||
			!protocol.AgentPromptFits(evaluation.Automation.Name+": GitHub issue #"+strconv.Itoa(match.Number), evaluation.Automation.RepositoryIdentity, prompt) {
			state, diagnostic, storedPrompt = "failed", "resolved_prompt_too_large", nil
		}
		requestKey := "automation:" + evaluation.Automation.ID + ":github_issue:" + strconv.Itoa(match.Number)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_occurrences(
				id, automation_id, automation_version, automation_name,
				workflow_revision_id, repository_id, repository_identity,
				context, timeout_seconds, state, resolved_prompt, task_request_key,
				diagnostic, retry_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, occurrenceID, evaluation.Automation.ID, evaluation.Automation.Version,
			evaluation.Automation.Name, evaluation.WorkflowRevisionID,
			evaluation.Automation.RepositoryID, evaluation.Automation.RepositoryIdentity,
			evaluation.Automation.Context, evaluation.Automation.TimeoutSeconds,
			state, storedPrompt, requestKey, diagnostic, now, now, now); err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_github_issue_occurrences(
				occurrence_id, automation_id, issue_number, issue_url, issue_title,
				observed_state, observed_labels_json, configured_state, required_labels_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, occurrenceID, evaluation.Automation.ID, match.Number, match.URL, match.Title,
			match.State, observedLabels, evaluation.Automation.Trigger.State, requiredLabels); err != nil {
			return unavailable(err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = NULL, evaluation_started_at = NULL,
		    last_checked_at = ?, next_check_at = ?, health_status = 'healthy',
		    health_code = '', health_message = ?, matched_count = matched_count + ?,
		    skipped_count = skipped_count + ?
		WHERE id = ? AND enabled = 1 AND evaluation_token = ?
	`, now, now+int64(evaluation.Automation.Trigger.PollIntervalSeconds)*1000,
		fmt.Sprintf("GitHub check completed with %d matching issue(s).", len(matches)),
		len(matches), len(matches)-len(newMatches), evaluation.Automation.ID, evaluation.Token)
	if err != nil {
		return unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return unavailable(err)
	}
	if changed != 1 {
		return nil
	}
	return tx.Commit()
}

func (s *Store) completePullRequestAutomationSuccess(
	ctx context.Context,
	evaluation automationEvaluation,
	matches []protocol.GitHubPullRequestMatch,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	var eligible int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM automations automation
		JOIN workflows workflow ON workflow.id = automation.workflow_id AND workflow.enabled = 1
		JOIN repositories repository ON repository.id = automation.repository_id AND repository.enabled = 1
		WHERE automation.id = ? AND automation.enabled = 1 AND automation.evaluation_token = ?
		  AND automation.trigger_type = 'github_pull_request'
	`, evaluation.Automation.ID, evaluation.Token).Scan(&eligible); err != nil {
		return unavailable(err)
	}
	if eligible == 0 {
		return nil
	}
	newMatches := make([]protocol.GitHubPullRequestMatch, 0, len(matches))
	for _, match := range matches {
		var exists int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM automation_github_pull_request_occurrences
			WHERE automation_id = ? AND pull_request_number = ?
		`, evaluation.Automation.ID, match.Number).Scan(&exists); err != nil {
			return unavailable(err)
		}
		if exists == 0 {
			newMatches = append(newMatches, match)
		}
	}
	var occurrenceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_occurrences`).Scan(&occurrenceCount); err != nil {
		return unavailable(err)
	}
	if occurrenceCount+len(newMatches) > protocol.MaxAutomationOccurrences {
		return s.completeAutomationFailureTx(ctx, tx, evaluation, &automationCheckError{
			code: "occurrence_limit_reached", message: "The durable Occurrence limit has been reached. Archive history before retrying.",
		})
	}
	now := s.now().UnixMilli()
	requiredLabels, _ := json.Marshal(evaluation.Automation.Trigger.RequiredLabels)
	baseBranches, _ := json.Marshal(evaluation.Automation.Trigger.BaseBranches)
	for _, match := range newMatches {
		occurrenceID, err := newID()
		if err != nil {
			return unavailable(err)
		}
		observedLabels, err := json.Marshal(match.Labels)
		if err != nil {
			return unavailable(err)
		}
		prompt, err := protocol.ResolveGitHubPullRequestAutomationPrompt(
			evaluation.WorkflowInstructions, evaluation.Automation.Context,
			evaluation.Automation.Trigger.State, evaluation.Automation.Trigger.IncludeDrafts,
			evaluation.Automation.Trigger.RequiredLabels, evaluation.Automation.Trigger.BaseBranches, match,
		)
		state, diagnostic := "pending", ""
		var storedPrompt any = prompt
		title := evaluation.Automation.Name + ": GitHub pull request #" + strconv.Itoa(match.Number)
		if err != nil || len([]byte(prompt)) > protocol.MaxResolvedPromptBytes ||
			!protocol.AgentPromptFits(title, evaluation.Automation.RepositoryIdentity, prompt) {
			state, diagnostic, storedPrompt = "failed", "resolved_prompt_too_large", nil
		}
		requestKey := "automation:" + evaluation.Automation.ID + ":github_pull_request:" + strconv.Itoa(match.Number)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_occurrences(
				id, automation_id, automation_version, automation_name,
				workflow_revision_id, repository_id, repository_identity,
				context, timeout_seconds, state, resolved_prompt, task_request_key,
				diagnostic, retry_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, occurrenceID, evaluation.Automation.ID, evaluation.Automation.Version,
			evaluation.Automation.Name, evaluation.WorkflowRevisionID,
			evaluation.Automation.RepositoryID, evaluation.Automation.RepositoryIdentity,
			evaluation.Automation.Context, evaluation.Automation.TimeoutSeconds,
			state, storedPrompt, requestKey, diagnostic, now, now, now); err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO automation_github_pull_request_occurrences(
				occurrence_id, automation_id, pull_request_number, pull_request_url,
				pull_request_title, observed_state, observed_draft, observed_base_branch,
				observed_head_commit, observed_labels_json, configured_state,
				include_drafts, required_labels_json, base_branches_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, occurrenceID, evaluation.Automation.ID, match.Number, match.URL, match.Title,
			match.State, match.IsDraft, match.BaseBranch, match.HeadCommit, observedLabels,
			evaluation.Automation.Trigger.State, evaluation.Automation.Trigger.IncludeDrafts,
			requiredLabels, baseBranches); err != nil {
			return unavailable(err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = NULL, evaluation_started_at = NULL,
		    last_checked_at = ?, next_check_at = ?, health_status = 'healthy',
		    health_code = '', health_message = ?, matched_count = matched_count + ?,
		    skipped_count = skipped_count + ?
		WHERE id = ? AND enabled = 1 AND evaluation_token = ?
	`, now, now+int64(evaluation.Automation.Trigger.PollIntervalSeconds)*1000,
		fmt.Sprintf("GitHub check completed with %d matching pull request(s).", len(matches)),
		len(matches), len(matches)-len(newMatches), evaluation.Automation.ID, evaluation.Token)
	if err != nil {
		return unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return unavailable(err)
	}
	if changed != 1 {
		return nil
	}
	return tx.Commit()
}

func (s *Store) completeAutomationFailureTx(
	ctx context.Context,
	tx *sql.Tx,
	evaluation automationEvaluation,
	checkErr *automationCheckError,
) error {
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations
		SET evaluation_token = NULL, evaluation_started_at = NULL,
		    last_checked_at = ?, next_check_at = ?, health_status = 'error',
		    health_code = ?, health_message = ?
		WHERE id = ? AND enabled = 1 AND evaluation_token = ?
	`, now, now+int64(evaluation.Automation.Trigger.PollIntervalSeconds)*1000,
		checkErr.code, checkErr.message, evaluation.Automation.ID, evaluation.Token); err != nil {
		return unavailable(err)
	}
	return tx.Commit()
}

func (s *Store) dispatchPendingOccurrences(ctx context.Context, limit int) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT occurrence.id
		FROM automation_occurrences occurrence
		JOIN automations automation ON automation.id = occurrence.automation_id
		WHERE occurrence.state = 'pending' AND automation.enabled = 1
		  AND (occurrence.retry_at IS NULL OR occurrence.retry_at <= ?)
		ORDER BY occurrence.created_at, occurrence.id LIMIT ?
	`, s.now().UnixMilli(), limit)
	if err != nil {
		return unavailable(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return unavailable(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return unavailable(err)
	}
	var result error
	for _, id := range ids {
		if err := s.dispatchOccurrence(ctx, id); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Store) hasDispatchableOccurrences(ctx context.Context) (bool, error) {
	var pending int
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM automation_occurrences occurrence
			JOIN automations automation ON automation.id = occurrence.automation_id
			WHERE occurrence.state = 'pending' AND automation.enabled = 1
			  AND (occurrence.retry_at IS NULL OR occurrence.retry_at <= ?)
		)
	`, s.now().UnixMilli()).Scan(&pending); err != nil {
		return false, unavailable(err)
	}
	return pending != 0, nil
}

func (s *Store) dispatchOccurrence(ctx context.Context, occurrenceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	var automationID, automationName, triggerType, workflowRevisionID, repositoryID, repositoryIdentity string
	var contextValue, prompt, requestKey string
	var timeoutSeconds int
	var issueNumber, pullRequestNumber, scheduledAt sql.NullInt64
	var scheduleKind, runRequestKey sql.NullString
	var workflowID, workflowName string
	var workflowRevisionNumber, automationEnabled, workflowEnabled, repositoryEnabled int
	err = tx.QueryRowContext(ctx, `
		SELECT occurrence.automation_id, occurrence.automation_name, automation.trigger_type,
		       occurrence.workflow_revision_id, occurrence.repository_id,
		       occurrence.repository_identity, occurrence.context,
		       occurrence.timeout_seconds, occurrence.resolved_prompt,
		       occurrence.task_request_key,
		       issue.issue_number, pull_request.pull_request_number,
		       schedule.kind, schedule.scheduled_at, schedule.run_request_key,
		       revision.workflow_id, revision.name, revision.revision_number,
		       automation.enabled, workflow.enabled, repository.enabled
		FROM automation_occurrences occurrence
		JOIN automations automation ON automation.id = occurrence.automation_id
		LEFT JOIN automation_github_issue_occurrences issue
		  ON issue.occurrence_id = occurrence.id AND automation.trigger_type = 'github_issue'
		LEFT JOIN automation_github_pull_request_occurrences pull_request
		  ON pull_request.occurrence_id = occurrence.id AND automation.trigger_type = 'github_pull_request'
		LEFT JOIN automation_schedule_occurrences schedule
		  ON schedule.occurrence_id = occurrence.id AND automation.trigger_type = 'schedule'
		JOIN workflow_revisions revision ON revision.id = occurrence.workflow_revision_id
		JOIN workflows workflow ON workflow.id = revision.workflow_id
		JOIN repositories repository ON repository.id = occurrence.repository_id
		WHERE occurrence.id = ? AND occurrence.state = 'pending'
	`, occurrenceID).Scan(
		&automationID, &automationName, &triggerType, &workflowRevisionID, &repositoryID,
		&repositoryIdentity, &contextValue, &timeoutSeconds, &prompt, &requestKey,
		&issueNumber, &pullRequestNumber, &scheduleKind, &scheduledAt, &runRequestKey,
		&workflowID, &workflowName, &workflowRevisionNumber,
		&automationEnabled, &workflowEnabled, &repositoryEnabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	if automationEnabled == 0 || workflowEnabled == 0 || repositoryEnabled == 0 {
		return nil
	}
	var title string
	switch triggerType {
	case protocol.AutomationTriggerGitHubIssue:
		if !issueNumber.Valid {
			return unavailable(errors.New("GitHub issue Occurrence is missing typed identity"))
		}
		title = automationName + ": GitHub issue #" + strconv.Itoa(int(issueNumber.Int64))
	case protocol.AutomationTriggerGitHubPullRequest:
		if !pullRequestNumber.Valid {
			return unavailable(errors.New("GitHub pull-request Occurrence is missing typed identity"))
		}
		title = automationName + ": GitHub pull request #" + strconv.Itoa(int(pullRequestNumber.Int64))
	case protocol.AutomationTriggerSchedule:
		if !scheduleKind.Valid {
			return unavailable(errors.New("schedule Occurrence is missing typed identity"))
		}
		if scheduleKind.String == "scheduled" {
			if !scheduledAt.Valid {
				return unavailable(errors.New("scheduled Occurrence is missing due instant"))
			}
			title = automationName + ": scheduled " + fromMillis(scheduledAt.Int64).Format(time.RFC3339)
		} else if scheduleKind.String == "run_now" && runRequestKey.Valid {
			title = automationName + ": run now"
		} else {
			return unavailable(errors.New("Run now Occurrence is missing request identity"))
		}
	default:
		return unavailable(errors.New("Automation Occurrence has an invalid trigger type"))
	}
	route := protocol.TaskRoute{RepositoryRemoteIdentity: repositoryIdentity}
	requiresSourceAccess := false
	if triggerType != protocol.AutomationTriggerSchedule {
		route.SourceAccess = protocol.SourceAccess{Provider: "github", Hostname: "github.com"}
		requiresSourceAccess = true
	}
	now := s.now().UnixMilli()
	selection, err := s.selectTaskRouteWithSourceRequirement(ctx, tx, route, now, requiresSourceAccess)
	if err != nil {
		var serviceErr *ServiceError
		if errors.As(err, &serviceErr) && serviceErr.Code == "no_eligible_worker" {
			_, updateErr := tx.ExecContext(ctx, `
				UPDATE automation_occurrences
				SET diagnostic = ?, retry_at = ?, updated_at = ?
				WHERE id = ? AND state = 'pending'
			`, serviceErr.Message, now+5000, now, occurrenceID)
			if updateErr != nil {
				return unavailable(updateErr)
			}
			return tx.Commit()
		}
		return err
	}
	if selection.repositoryID != repositoryID {
		return unavailable(errors.New("Automation route selected a different repository"))
	}
	var taskID, existingTitle, existingDescription, existingRepositoryID string
	var existingTimeout int
	var existingWorkflowRevisionID, existingContext sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id, title, description, repository_id, timeout_seconds,
		       workflow_revision_id, context
		FROM tasks WHERE request_key = ?
	`, requestKey).Scan(&taskID, &existingTitle, &existingDescription, &existingRepositoryID,
		&existingTimeout, &existingWorkflowRevisionID, &existingContext)
	if err == nil {
		if existingTitle != title || existingDescription != prompt || existingRepositoryID != repositoryID ||
			existingTimeout != timeoutSeconds || existingWorkflowRevisionID.String != workflowRevisionID ||
			existingContext.String != contextValue {
			return unavailable(errors.New("Automation task request key conflicts with stored task data"))
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return unavailable(err)
	} else {
		if !protocol.AgentPromptFits(title, repositoryIdentity, prompt) {
			_, updateErr := tx.ExecContext(ctx, `
				UPDATE automation_occurrences
				SET state = 'failed', resolved_prompt = NULL,
				    diagnostic = 'agent_prompt_too_large', retry_at = NULL, updated_at = ?
				WHERE id = ? AND state = 'pending'
			`, now, occurrenceID)
			if updateErr != nil {
				return unavailable(updateErr)
			}
			return tx.Commit()
		}
		taskID, err = newID()
		if err != nil {
			return unavailable(err)
		}
		executionID, err := newID()
		if err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks(
				id, request_key, title, description, repository_id, timeout_seconds,
				created_at, workflow_id, workflow_revision_id, workflow_name,
				workflow_revision_number, context
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, taskID, requestKey, title, prompt, repositoryID, timeoutSeconds, now,
			workflowID, workflowRevisionID, workflowName, workflowRevisionNumber, contextValue); err != nil {
			return unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO executions(id, task_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'queued', ?, ?)
		`, executionID, taskID, selection.workerID, selection.runtime, now, now); err != nil {
			return unavailable(err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE automation_occurrences
		SET state = 'dispatched', task_id = ?, task_id_snapshot = ?,
		    diagnostic = '', retry_at = NULL, updated_at = ?
		WHERE id = ? AND state = 'pending'
	`, taskID, taskID, now, occurrenceID)
	if err != nil {
		return unavailable(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return unavailable(err)
	}
	if changed != 1 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automations SET dispatched_count = dispatched_count + 1 WHERE id = ?
	`, automationID); err != nil {
		return unavailable(err)
	}
	return tx.Commit()
}
