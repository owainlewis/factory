package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestTitleRenamePreservesLegacyMutationDigests(t *testing.T) {
	workflow := normalizedWorkflowRevision{
		RequestKey: "workflow-request", Title: "Implement", Summary: "Ship one change", Instructions: "Implement safely.",
	}
	workflowDigest, err := workflowMutationDigest(workflow)
	if err != nil {
		t.Fatal(err)
	}
	legacyWorkflow, err := json.Marshal(struct {
		RequestKey         string `json:"request_key"`
		WorkflowID         string `json:"workflow_id,omitempty"`
		ExpectedRevisionID string `json:"expected_revision_id,omitempty"`
		Name               string `json:"name"`
		Summary            string `json:"summary"`
		Instructions       string `json:"instructions"`
	}{workflow.RequestKey, workflow.WorkflowID, workflow.ExpectedRevisionID, workflow.Title, workflow.Summary, workflow.Instructions})
	if err != nil {
		t.Fatal(err)
	}
	wantWorkflow := sha256.Sum256(legacyWorkflow)
	if !bytes.Equal(workflowDigest, wantWorkflow[:]) {
		t.Fatal("Workflow title rename changed the idempotency digest")
	}

	automation := normalizedAutomation{
		RequestKey: "automation-request", Title: "Ready issues", WorkflowID: "workflow",
		RepositoryID: "repository", Context: "", TimeoutSeconds: 60,
		Trigger: protocol.AutomationTrigger{
			Type: protocol.AutomationTriggerGitHubIssue, State: "open",
			RequiredLabels: []string{"needs-agent"}, PollIntervalSeconds: 30,
		},
	}
	automationDigestValue, err := automationDigest(automation)
	if err != nil {
		t.Fatal(err)
	}
	legacyAutomation, err := json.Marshal(struct {
		RequestKey     string                     `json:"request_key,omitempty"`
		Name           string                     `json:"name"`
		WorkflowID     string                     `json:"workflow_id"`
		RepositoryID   string                     `json:"repository_id,omitempty"`
		Context        string                     `json:"context"`
		TimeoutSeconds int                        `json:"timeout_seconds"`
		Trigger        protocol.AutomationTrigger `json:"trigger"`
	}{automation.RequestKey, automation.Title, automation.WorkflowID, automation.RepositoryID,
		automation.Context, automation.TimeoutSeconds, automation.Trigger})
	if err != nil {
		t.Fatal(err)
	}
	wantAutomation := sha256.Sum256(legacyAutomation)
	if !bytes.Equal(automationDigestValue, wantAutomation[:]) {
		t.Fatal("Automation title rename changed the idempotency digest")
	}
}
