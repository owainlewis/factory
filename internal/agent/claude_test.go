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
