package controlplane

import (
	"net/http"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHTTPWorkflowLifecycleAndTaskSnapshot(t *testing.T) {
	fixture := newHTTPFixture(t)
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	response := fixture.request(http.MethodPost, "/api/v1/workflows", "application/json", "",
		protocol.CreateWorkflowRequest{
			RequestKey: "http-workflow", Name: "Implement", Summary: "Ship one change",
			Instructions: "Implement, verify, review, and open a pull request.",
		})
	requireStatus(t, response, http.StatusCreated)
	created := decodeResponse[protocol.WorkflowDetail](t, response)

	response = fixture.request(http.MethodGet, "/api/v1/workflows?enabled=true&limit=1", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	listed := decodeResponse[struct {
		Workflows  []protocol.Workflow `json:"workflows"`
		NextCursor *string             `json:"next_cursor"`
	}](t, response)
	if len(listed.Workflows) != 1 || listed.Workflows[0].ID != created.Workflow.ID || listed.NextCursor != nil {
		t.Fatalf("workflow list = %#v", listed)
	}

	response = fixture.request(http.MethodPost, "/api/v1/workflows/"+created.Workflow.ID+"/revisions", "application/json", "",
		protocol.CreateWorkflowRevisionRequest{
			RequestKey: "http-revision", ExpectedRevisionID: created.Workflow.CurrentRevision.ID,
			Name: "Implement", Summary: "Ship one reviewed change",
			Instructions: "Implement, verify twice, review, and open a pull request.",
		})
	requireStatus(t, response, http.StatusCreated)
	revised := decodeResponse[protocol.WorkflowDetail](t, response)
	if revised.Workflow.CurrentRevision.RevisionNumber != 2 || len(revised.Revisions) != 2 {
		t.Fatalf("revised workflow = %#v", revised)
	}

	response = fixture.request(http.MethodPost, "/api/v1/tasks", "application/json", "", protocol.CreateTaskRequest{
		RequestKey: "http-workflow-task", Title: "Issue #183", Context: "Keep context free text.",
		WorkerID: worker.ID, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: revised.Workflow.CurrentRevision.ID,
	})
	requireStatus(t, response, http.StatusCreated)
	task := decodeResponse[protocol.TaskDetail](t, response)
	if task.Workflow == nil || task.Workflow.RevisionNumber != 2 || task.Context != "Keep context free text." ||
		task.Task.Description != task.ResolvedPrompt {
		t.Fatalf("workflow task detail = %#v", task)
	}

	for name, body := range map[string]map[string]any{
		"empty legacy description": {
			"request_key": "mixed-empty-description", "title": "Mixed task",
			"description": "", "context": "Workflow context",
			"workflow_revision_id": revised.Workflow.CurrentRevision.ID,
			"worker_id":            worker.ID, "repository_id": worker.Repositories[0].ID,
			"timeout_seconds": 60,
		},
		"empty workflow fields": {
			"request_key": "mixed-empty-workflow", "title": "Mixed task",
			"description": "Legacy prompt", "context": "", "workflow_revision_id": "",
			"worker_id": worker.ID, "repository_id": worker.Repositories[0].ID,
			"timeout_seconds": 60,
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := fixture.request(http.MethodPost, "/api/v1/tasks", "application/json", "", body)
			requireStatus(t, response, http.StatusBadRequest)
			errorBody := decodeResponse[protocol.ErrorBody](t, response)
			if errorBody.Error.Code != "ambiguous_task_prompt" {
				t.Fatalf("mixed prompt error = %#v", errorBody)
			}
		})
	}

	for name, body := range map[string]string{
		"missing": `{}`,
		"null":    `{"enabled":null}`,
	} {
		t.Run("rejects "+name+" enabled", func(t *testing.T) {
			response := fixture.request(http.MethodPut, "/api/v1/workflows/"+created.Workflow.ID+"/enabled", "application/json", "", body)
			requireStatus(t, response, http.StatusBadRequest)
			errorBody := decodeResponse[protocol.ErrorBody](t, response)
			if errorBody.Error.Code != "invalid_workflow_enabled" {
				t.Fatalf("invalid enabled error = %#v", errorBody)
			}
			unchanged, err := fixture.store.Workflow(t.Context(), created.Workflow.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !unchanged.Workflow.Enabled {
				t.Fatal("invalid enabled request disabled the Workflow")
			}
		})
	}

	response = fixture.request(http.MethodPut, "/api/v1/workflows/"+created.Workflow.ID+"/enabled", "application/json", "",
		map[string]bool{"enabled": false})
	requireStatus(t, response, http.StatusOK)
	disabled := decodeResponse[protocol.WorkflowDetail](t, response)
	if disabled.Workflow.Enabled {
		t.Fatalf("disabled workflow = %#v", disabled.Workflow)
	}
	response = fixture.request(http.MethodPut, "/api/v1/workflows/"+created.Workflow.ID+"/enabled", "application/json", "",
		map[string]bool{"enabled": true})
	requireStatus(t, response, http.StatusOK)
	enabled := decodeResponse[protocol.WorkflowDetail](t, response)
	if !enabled.Workflow.Enabled {
		t.Fatalf("enabled workflow = %#v", enabled.Workflow)
	}

	response = fixture.request(http.MethodGet, "/api/v1/workflows?limit=201", "", "", nil)
	requireStatus(t, response, http.StatusBadRequest)
	errorBody := decodeResponse[protocol.ErrorBody](t, response)
	if errorBody.Error.Code != "invalid_limit" {
		t.Fatalf("invalid limit error = %#v", errorBody)
	}
}
