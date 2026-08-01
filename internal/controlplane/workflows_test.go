package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func createTestWorkflow(t *testing.T, store *Store, requestKey, name, instructions string) protocol.WorkflowDetail {
	t.Helper()
	detail, created, err := store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: requestKey, Name: name, Summary: "A test workflow", Instructions: instructions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected workflow creation")
	}
	return detail
}

func TestWorkflowMutationsAreIdempotentVersionedAndConflictSafe(t *testing.T) {
	store := newTestStore(t)
	first := createTestWorkflow(t, store, "workflow-create", "  Code Review  ", "Review carefully.")
	if first.Workflow.CurrentRevision.Name != "Code Review" || first.Workflow.CurrentRevision.RevisionNumber != 1 {
		t.Fatalf("normalized initial revision = %#v", first.Workflow.CurrentRevision)
	}
	replayed, created, err := store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "workflow-create", Name: "Code Review", Summary: "A test workflow", Instructions: "Review carefully.",
	})
	if err != nil || created || replayed.Workflow.ID != first.Workflow.ID {
		t.Fatalf("create replay = created %v, error %v, workflow %#v", created, err, replayed.Workflow)
	}
	_, _, err = store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "workflow-create", Name: "Different", Instructions: "Different instructions.",
	})
	assertErrorCode(t, err, "request_key_conflict")
	_, _, err = store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "normalized-name", Name: "code review", Instructions: "Duplicate name.",
	})
	assertErrorCode(t, err, "workflow_name_conflict")

	revisionInput := protocol.CreateWorkflowRevisionRequest{
		RequestKey: "workflow-revision-2", ExpectedRevisionID: first.Workflow.CurrentRevision.ID,
		Name: "Code Review", Summary: "Updated", Instructions: "Review carefully and run checks.",
	}
	second, created, err := store.CreateWorkflowRevision(context.Background(), first.Workflow.ID, revisionInput)
	if err != nil || !created || second.Workflow.CurrentRevision.RevisionNumber != 2 || len(second.Revisions) != 2 {
		t.Fatalf("second revision = created %v, error %v, detail %#v", created, err, second)
	}
	replayed, created, err = store.CreateWorkflowRevision(context.Background(), first.Workflow.ID, revisionInput)
	if err != nil || created || replayed.Workflow.CurrentRevision.ID != second.Workflow.CurrentRevision.ID {
		t.Fatalf("revision replay = created %v, error %v, detail %#v", created, err, replayed)
	}
	_, _, err = store.CreateWorkflowRevision(context.Background(), first.Workflow.ID, protocol.CreateWorkflowRevisionRequest{
		RequestKey: "stale-edit", ExpectedRevisionID: first.Workflow.CurrentRevision.ID,
		Name: "Code Review", Instructions: "Overwrite a newer edit.",
	})
	assertErrorCode(t, err, "workflow_revision_conflict")

	var results [2]error
	var wait sync.WaitGroup
	wait.Add(2)
	for index := range results {
		go func(index int) {
			defer wait.Done()
			_, _, results[index] = store.CreateWorkflowRevision(context.Background(), first.Workflow.ID,
				protocol.CreateWorkflowRevisionRequest{
					RequestKey:         "concurrent-edit-" + string(rune('a'+index)),
					ExpectedRevisionID: second.Workflow.CurrentRevision.ID,
					Name:               "Code Review", Instructions: "Concurrent revision " + string(rune('A'+index)),
				})
		}(index)
	}
	wait.Wait()
	var successes, conflicts int
	for _, result := range results {
		if result == nil {
			successes++
			continue
		}
		var service *ServiceError
		if errors.As(result, &service) && service.Code == "workflow_revision_conflict" {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent edit result: %v", result)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent edits = %d successes, %d conflicts", successes, conflicts)
	}
}

func TestWorkflowTaskSnapshotsSurviveDisableRevisionAndRetry(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	workflow := createTestWorkflow(t, store, "snapshot-workflow", "Implement", "Use the accepted contract.")
	contextText := "Work on issue #183.\nKeep this context exact."
	task, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "workflow-task", Title: "Implement workflows", Context: contextText,
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
	})
	if err != nil || !created {
		t.Fatalf("create workflow task = created %v, error %v", created, err)
	}
	wantPrompt := protocol.ResolveWorkflowPrompt("Use the accepted contract.", contextText)
	if task.Context != contextText || task.Task.Description != wantPrompt || task.ResolvedPrompt != wantPrompt || task.Workflow == nil {
		t.Fatalf("workflow task snapshot = %#v", task)
	}
	if task.Workflow.Name != "Implement" || task.Workflow.RevisionNumber != 1 {
		t.Fatalf("workflow snapshot identity = %#v", task.Workflow)
	}
	if _, err := store.SetWorkflowEnabled(context.Background(), workflow.Workflow.ID, false); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "disabled-workflow-task", Title: "Blocked", Context: "Context",
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
	})
	assertErrorCode(t, err, "workflow_disabled")
	replay, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "workflow-task", Title: "Implement workflows", Context: contextText,
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
	})
	if err != nil || created || replay.Task.ID != task.Task.ID {
		t.Fatalf("disabled workflow task replay = created %v, error %v, task %#v", created, err, replay.Task)
	}
	if _, err := store.CancelTask(context.Background(), task.Task.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryExecution(context.Background(), task.Execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ResolvedPrompt != wantPrompt || retried.Task.Description != wantPrompt || retried.Context != contextText {
		t.Fatalf("retry changed snapshot: %#v", retried)
	}
	claim := claimTestTask(t, store, workerA, "workflow-claim", tokenA)
	if claim.Task.Description != wantPrompt {
		t.Fatalf("claim prompt = %q; want %q", claim.Task.Description, wantPrompt)
	}
}

func TestBlankTaskAndOldClaimContractRemainCompatible(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createTestTask(t, store, "blank-task", workerA, worker.Repositories[0].ID)
	if task.Workflow != nil || task.ResolvedPrompt != task.Task.Description || task.Context != task.Task.Description {
		t.Fatalf("blank task changed contract: %#v", task)
	}
	claim := claimTestTask(t, store, workerA, "blank-claim", tokenA)
	if claim.Task.Description != "preserve this prompt\n" {
		t.Fatalf("legacy claim description = %q", claim.Task.Description)
	}
}

func TestTaskRejectsUnknownWorkflowRevisionAtomically(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	_, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "unknown-workflow-revision", Title: "Invalid revision", Context: "Context",
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: "00000000-0000-4000-8000-000000000000",
	})
	assertErrorCode(t, err, "workflow_revision_not_found")
	var taskCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Fatalf("unknown workflow revision created %d tasks", taskCount)
	}
}

func TestTaskPromptFormsAreExclusiveAndAtomic(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	workflow := createTestWorkflow(t, store, "exclusive-workflow", "Exclusive", "Follow the contract.")
	_, _, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "ambiguous-workflow-task", Title: "Ambiguous",
		Description: "Legacy prompt", Context: "Workflow context",
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
	})
	assertErrorCode(t, err, "ambiguous_task_prompt")
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "context-without-workflow", Title: "Missing workflow", Context: "Context",
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
	})
	assertErrorCode(t, err, "workflow_revision_required")
	var taskCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Fatalf("invalid prompt forms created %d tasks", taskCount)
	}
}

func TestWorkflowByteAndRevisionLimitsAreAtomic(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "oversized-instructions", Name: "Too large",
		Instructions: strings.Repeat("é", protocol.MaxWorkflowInstructionsBytes/2+1),
	})
	assertErrorCode(t, err, "invalid_workflow_instructions")

	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	workflow := createTestWorkflow(t, store, "bounded-workflow", "Bounded", strings.Repeat("é", protocol.MaxWorkflowInstructionsBytes/2))
	_, _, err = store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "oversized-resolved", Title: "Too large", Context: strings.Repeat("x", 20<<10),
		WorkerID: workerA, RepositoryID: worker.Repositories[0].ID, TimeoutSeconds: 60,
		WorkflowRevisionID: workflow.Workflow.CurrentRevision.ID,
	})
	assertErrorCode(t, err, "resolved_prompt_too_large")
	var taskCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 {
		t.Fatalf("oversized prompt created %d tasks", taskCount)
	}

	current := workflow.Workflow.CurrentRevision
	for revisionNumber := 2; revisionNumber <= protocol.MaxWorkflowRevisions; revisionNumber++ {
		detail, created, err := store.CreateWorkflowRevision(context.Background(), workflow.Workflow.ID,
			protocol.CreateWorkflowRevisionRequest{
				RequestKey:         "bounded-revision-" + string(rune(1000+revisionNumber)),
				ExpectedRevisionID: current.ID, Name: "Bounded",
				Instructions: "Revision " + string(rune(1000+revisionNumber)),
			})
		if err != nil || !created {
			t.Fatalf("create revision %d = created %v, error %v", revisionNumber, created, err)
		}
		current = detail.Workflow.CurrentRevision
	}
	_, _, err = store.CreateWorkflowRevision(context.Background(), workflow.Workflow.ID,
		protocol.CreateWorkflowRevisionRequest{
			RequestKey: "revision-101", ExpectedRevisionID: current.ID,
			Name: "Bounded", Instructions: "One too many",
		})
	assertErrorCode(t, err, "workflow_revision_limit")
	detail, err := store.Workflow(context.Background(), workflow.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Workflow.CurrentRevision.ID != current.ID || len(detail.Revisions) != protocol.MaxWorkflowRevisions {
		t.Fatalf("revision limit changed current history: %#v", detail)
	}
}

func TestWorkflowCountLimitIsAtomic(t *testing.T) {
	store := newTestStore(t)
	for index := 0; index < protocol.MaxWorkflows; index++ {
		_, created, err := store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
			RequestKey:   "workflow-limit-key-" + string(rune(1000+index)),
			Name:         "Workflow limit name " + string(rune(1000+index)),
			Instructions: "Bounded instructions.",
		})
		if err != nil || !created {
			t.Fatalf("create workflow %d = created %v, error %v", index+1, created, err)
		}
	}
	_, _, err := store.CreateWorkflow(context.Background(), protocol.CreateWorkflowRequest{
		RequestKey: "workflow-limit-overflow", Name: "One Workflow too many", Instructions: "Rejected.",
	})
	assertErrorCode(t, err, "workflow_limit_reached")
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workflows`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != protocol.MaxWorkflows {
		t.Fatalf("workflow count = %d; want %d", count, protocol.MaxWorkflows)
	}
}

func TestWorkflowListFiltersAndPaginates(t *testing.T) {
	store := newTestStore(t)
	first := createTestWorkflow(t, store, "list-a", "Alpha", "A")
	second := createTestWorkflow(t, store, "list-b", "Beta", "B")
	if _, err := store.SetWorkflowEnabled(context.Background(), first.Workflow.ID, false); err != nil {
		t.Fatal(err)
	}
	enabled := true
	page, err := store.Workflows(context.Background(), protocol.WorkflowPageRequest{Limit: 1, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workflows) != 1 || page.Workflows[0].ID != second.Workflow.ID || page.NextCursor != nil {
		t.Fatalf("enabled workflow page = %#v", page)
	}
	page, err = store.Workflows(context.Background(), protocol.WorkflowPageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workflows) != 1 || page.NextCursor == nil {
		t.Fatalf("first workflow page = %#v", page)
	}
	next, err := store.Workflows(context.Background(), protocol.WorkflowPageRequest{Limit: 1, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Workflows) != 1 || next.Workflows[0].ID == page.Workflows[0].ID {
		t.Fatalf("next workflow page = %#v", next)
	}
	byName, err := store.Workflows(context.Background(), protocol.WorkflowPageRequest{Limit: 10, Name: "ALPHA"})
	if err != nil || len(byName.Workflows) != 1 || byName.Workflows[0].ID != first.Workflow.ID {
		t.Fatalf("normalized name filter = %#v, %v", byName, err)
	}
}
