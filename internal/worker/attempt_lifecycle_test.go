package worker

import (
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestBuildPromptIncludesGrammaticalSafetyInstruction(t *testing.T) {
	claim := protocol.Claim{
		Task: protocol.Task{
			Title:       "Fix the prompt",
			Description: "Keep the change focused.",
		},
		Repository: protocol.Repository{RemoteIdentity: "github.com/owainlewis/factory"},
	}

	want := "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned task and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees, " +
		"and do not delete worktrees or branches. Complete and verify the task before returning a concise result.\n\n" +
		"Task title: Fix the prompt\n" +
		"Repository: github.com/owainlewis/factory\n\n" +
		"Keep the change focused."

	if got := buildPrompt(claim); got != want {
		t.Fatalf("buildPrompt() = %q, want %q", got, want)
	}
}
