package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	MaxResolvedPromptBytes = 64 << 10
	MaxAgentPromptBytes    = 72 << 10
	MaxAgentBranchBytes    = 1 << 10
)

func ResolveTaskSchedulePrompt(prompt string, scheduledAt time.Time, cron, timezone string) (string, error) {
	occurrence, err := json.Marshal(struct {
		Type        string    `json:"type"`
		ScheduledAt time.Time `json:"scheduled_at"`
		Cron        string    `json:"cron"`
		Timezone    string    `json:"timezone"`
	}{"schedule", scheduledAt.UTC(), cron, timezone})
	if err != nil {
		return "", err
	}
	return prompt +
		"\n\nSchedule instruction:\n\n" +
		"Execute this Task for the scheduled occurrence. There is no provider item to revalidate." +
		"\n\nTrusted schedule occurrence:\n\n" + string(occurrence), nil
}

func FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the Session before returning a concise result.\n\n" +
		"Task: " + title + "\n" +
		"Repository: " + repository + "\n" +
		"Working branch: " + workingBranch + "\n" +
		"Target base branch: " + targetBaseBranch + "\n\n" +
		resolvedPrompt
}

func AgentPromptFits(title, repository, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentPrompt(title, repository, maxBranch, maxBranch, resolvedPrompt))) <= MaxAgentPromptBytes
}
