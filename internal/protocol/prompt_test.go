package protocol

import "testing"

func TestResolveWorkflowPromptUsesCanonicalSections(t *testing.T) {
	want := "Workflow instructions:\n\nReview carefully.\n\nTask context:\n\nIssue #183"
	if got := ResolveWorkflowPrompt("Review carefully.", "Issue #183"); got != want {
		t.Fatalf("ResolveWorkflowPrompt() = %q, want %q", got, want)
	}
}

func TestResolveDefinitionPromptIncludesDeterministicParameters(t *testing.T) {
	got, err := ResolveDefinitionPrompt("Find confirmed bugs.", map[string]string{
		"severity": "critical", "focus": "correctness",
	})
	want := "Find confirmed bugs.\n\nTrusted Factory Run parameters:\n\n{\"focus\":\"correctness\",\"severity\":\"critical\"}"
	if err != nil || got != want {
		t.Fatalf("ResolveDefinitionPrompt() = %q, %v; want %q", got, err, want)
	}
	plain, err := ResolveDefinitionPrompt("Run the review.", nil)
	if err != nil || plain != "Run the review." {
		t.Fatalf("ResolveDefinitionPrompt() without parameters = %q, %v", plain, err)
	}
}

func TestFormatAgentPromptPreservesSafetyAndBranchContract(t *testing.T) {
	want := "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned task and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the task before returning a concise result.\n\n" +
		"Task title: Fix the prompt\n" +
		"Repository: github.com/owainlewis/factory\n" +
		"Working branch: factory/123456789abc-abcdef123456\n" +
		"Target base branch: main\n\n" +
		"Keep the change focused."

	if got := FormatAgentPrompt(
		"Fix the prompt",
		"github.com/owainlewis/factory",
		"factory/123456789abc-abcdef123456",
		"main",
		"Keep the change focused.",
	); got != want {
		t.Fatalf("FormatAgentPrompt() = %q, want %q", got, want)
	}
}
