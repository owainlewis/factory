package agent

import "testing"

func TestClaudeBlockedDetectsCreditFailure(t *testing.T) {
	if !claudeBlocked("Credit balance is too low") {
		t.Fatal("expected blocked credit failure")
	}
}

func TestClaudeBlockedIgnoresNormalOutput(t *testing.T) {
	if claudeBlocked("Factory hello run complete") {
		t.Fatal("did not expect blocked status")
	}
}

func TestClaudePermissionMode(t *testing.T) {
	if got := claudePermissionMode("plan"); got != "plan" {
		t.Fatalf("plan permission = %q", got)
	}
	if got := claudePermissionMode(""); got != "plan" {
		t.Fatalf("default permission = %q", got)
	}
	if got := claudePermissionMode("execute"); got != "auto" {
		t.Fatalf("execute permission = %q", got)
	}
}
