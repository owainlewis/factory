package protocol

import "strings"

const (
	MaxWorkflowInstructionsBytes = 48 << 10
	MaxResolvedPromptBytes       = 64 << 10
	MaxAgentPromptBytes          = 72 << 10
	MaxAgentBranchBytes          = 1 << 10
)

func ResolveWorkflowPrompt(instructions, context string) string {
	return "Workflow instructions:\n\n" + instructions + "\n\nTask context:\n\n" + context
}

func FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned task and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the task before returning a concise result.\n\n" +
		"Task title: " + title + "\n" +
		"Repository: " + repository + "\n" +
		"Working branch: " + workingBranch + "\n" +
		"Target base branch: " + targetBaseBranch + "\n\n" +
		resolvedPrompt
}

func AgentPromptFits(title, repository, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentPrompt(title, repository, maxBranch, maxBranch, resolvedPrompt))) <= MaxAgentPromptBytes
}
