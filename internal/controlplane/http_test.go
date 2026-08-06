package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type httpFixture struct {
	t      *testing.T
	store  *Store
	server *httptest.Server
	logs   *bytes.Buffer
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	store := newTestStore(t)
	logs := &bytes.Buffer{}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewJSONHandler(logs, nil))))
	t.Cleanup(server.Close)
	return &httpFixture{t: t, store: store, server: server, logs: logs}
}

func (f *httpFixture) request(method, path, contentType, origin string, body any) *http.Response {
	f.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case string:
		reader = strings.NewReader(value)
	case []byte:
		reader = bytes.NewReader(value)
	case nil:
		reader = nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	return response
}

func decodeResponse[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	defer response.Body.Close()
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func requireStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode != expected {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("status %d, want %d: %s", response.StatusCode, expected, body)
	}
}

func registerHTTPWorker(t *testing.T, fixture *httpFixture, id, key, remote string, capacity int) protocol.Worker {
	t.Helper()
	response := fixture.request(http.MethodPut, "/api/v1/workers/"+id, "application/json", "", protocol.WorkerRegistration{
		Name: id, WorkerVersion: "test", RuntimeVersion: "codex-test", Capacity: capacity, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{Key: key, RemoteIdentity: remote}},
	})
	requireStatus(t, response, http.StatusOK)
	return decodeResponse[protocol.Worker](t, response)
}

func TestHTTPManagedRepositoryCatalog(t *testing.T) {
	fixture := newHTTPFixture(t)
	response := fixture.request(http.MethodPost, "/api/v1/repositories", "application/json", "", map[string]string{
		"remote_identity": "github.com/OwainLewis/Factory.git",
	})
	requireStatus(t, response, http.StatusCreated)
	repository := decodeResponse[protocol.ManagedRepository](t, response)
	if repository.RemoteIdentity != "github.com/owainlewis/factory" || !repository.Enabled {
		t.Fatalf("created repository = %#v", repository)
	}

	response = fixture.request(http.MethodPost, "/api/v1/repositories", "application/json", "", map[string]string{
		"remote_identity": "github.com/owainlewis/factory",
	})
	requireStatus(t, response, http.StatusOK)
	replayed := decodeResponse[protocol.ManagedRepository](t, response)
	if replayed.ID != repository.ID {
		t.Fatalf("replayed repository = %#v", replayed)
	}

	response = fixture.request(http.MethodGet, "/api/v1/repositories", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	listed := decodeResponse[struct {
		Repositories []protocol.ManagedRepository `json:"repositories"`
	}](t, response)
	if len(listed.Repositories) != 1 || listed.Repositories[0].ID != repository.ID {
		t.Fatalf("listed repositories = %#v", listed.Repositories)
	}

	response = fixture.request(
		http.MethodPut, "/api/v1/workers/managed-worker", "application/json", "",
		protocol.WorkerRegistration{
			Name: "managed-worker", WorkerVersion: "test", RuntimeVersion: "test",
			Capacity: 1, Health: "healthy",
			SourceAccess:               []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
			AcceptsManagedRepositories: true,
		},
	)
	requireStatus(t, response, http.StatusOK)
	response = fixture.request(
		http.MethodGet, "/api/v1/repositories/"+repository.ID+"/readiness", "", "", nil,
	)
	requireStatus(t, response, http.StatusOK)
	readiness := decodeResponse[protocol.ManagedRepositoryReadiness](t, response)
	if !readiness.RoutingReady || len(readiness.Workers) != 1 || !readiness.Workers[0].Ready {
		t.Fatalf("repository readiness = %#v", readiness)
	}

	response = fixture.request(
		http.MethodGet, "/api/v1/workers/managed-worker/repository-options", "", "", nil,
	)
	requireStatus(t, response, http.StatusOK)
	options := decodeResponse[struct {
		Repositories []protocol.WorkerRepositoryOption `json:"repositories"`
	}](t, response)
	if len(options.Repositories) != 1 || options.Repositories[0].ID != repository.ID || !options.Repositories[0].Ready {
		t.Fatalf("worker repository options = %#v", options.Repositories)
	}

	response = fixture.request(
		http.MethodPut,
		"/api/v1/repositories/"+repository.ID+"/enabled",
		"application/json",
		"",
		protocol.SetManagedRepositoryEnabledRequest{Enabled: false},
	)
	requireStatus(t, response, http.StatusOK)
	disabled := decodeResponse[protocol.ManagedRepository](t, response)
	if disabled.Enabled {
		t.Fatalf("disabled repository = %#v", disabled)
	}

	response = fixture.request(http.MethodGet, "/api/v1/repositories/"+repository.ID, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	if fetched := decodeResponse[protocol.ManagedRepository](t, response); fetched.Enabled {
		t.Fatalf("fetched repository = %#v", fetched)
	}

	response = fixture.request(
		http.MethodPut,
		"/api/v1/repositories/"+repository.ID+"/enabled",
		"application/json",
		"",
		map[string]any{},
	)
	requireStatus(t, response, http.StatusBadRequest)
	if body := decodeResponse[protocol.ErrorBody](t, response); body.Error.Code != "invalid_repository" {
		t.Fatalf("missing enabled error = %#v", body)
	}
}

func TestHTTPMetricsUseABoundedWindowContract(t *testing.T) {
	fixture := newHTTPFixture(t)
	response := fixture.request(http.MethodGet, "/api/v1/metrics/summary", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	summary := decodeResponse[protocol.MetricsSummary](t, response)
	if summary.Window != metricsWindow7Days || summary.ExecutionsCreated != 0 {
		t.Fatalf("default metrics = %#v", summary)
	}

	for _, path := range []string{
		"/api/v1/metrics/summary?window=quarter",
		"/api/v1/metrics/summary?window=7d&window=30d",
	} {
		response = fixture.request(http.MethodGet, path, "", "", nil)
		requireStatus(t, response, http.StatusBadRequest)
		body := decodeResponse[protocol.ErrorBody](t, response)
		if body.Error.Code != "invalid_window" {
			t.Fatalf("%s error = %#v", path, body)
		}
	}
}

func TestHTTPWorkerRegistrationSupportsLegacyAndRuntimeAwareContracts(t *testing.T) {
	fixture := newHTTPFixture(t)
	type legacyWorker struct {
		ID                string                      `json:"id"`
		Name              string                      `json:"name"`
		WorkerVersion     string                      `json:"worker_version"`
		CodexVersion      string                      `json:"codex_version"`
		Capacity          int                         `json:"capacity"`
		ActiveCount       int                         `json:"active_count"`
		Health            string                      `json:"health"`
		Online            bool                        `json:"online"`
		Repositories      []protocol.Repository       `json:"repositories"`
		RetainedWorktrees []protocol.RetainedWorktree `json:"retained_worktrees"`
		CurrentTaskTitle  string                      `json:"current_task_title,omitempty"`
		RegisteredAt      time.Time                   `json:"registered_at"`
		LastHeartbeat     time.Time                   `json:"last_heartbeat"`
	}
	legacyRequest := func(name string) map[string]any {
		return map[string]any{
			"name": name, "worker_version": "legacy", "capacity": 1, "active_count": 0,
			"health": "healthy",
			"repositories": []protocol.RepositoryRegistration{{
				Key: "factory", RemoteIdentity: "github.com/example/" + name,
			}},
		}
	}
	legacyCases := []struct {
		name         string
		codexVersion any
		includeField bool
		wantVersion  string
	}{
		{name: "legacy-string", codexVersion: "0.42.0", includeField: true, wantVersion: "0.42.0"},
		{name: "legacy-omitted"},
		{name: "legacy-null", codexVersion: nil, includeField: true},
	}
	for _, test := range legacyCases {
		t.Run(test.name, func(t *testing.T) {
			requestBody := legacyRequest(test.name)
			if test.includeField {
				requestBody["codex_version"] = test.codexVersion
			}
			response := fixture.request(
				http.MethodPut, "/api/v1/workers/"+test.name, "application/json", "", requestBody,
			)
			requireStatus(t, response, http.StatusOK)
			var legacyResponse legacyWorker
			decoder := json.NewDecoder(response.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&legacyResponse); err != nil {
				response.Body.Close()
				t.Fatalf("strict legacy response decode: %v", err)
			}
			response.Body.Close()
			if legacyResponse.CodexVersion != test.wantVersion {
				t.Fatalf("legacy codex version = %q", legacyResponse.CodexVersion)
			}
			stored, err := fixture.store.Worker(context.Background(), test.name)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Runtime != protocol.RuntimeCodex || stored.RuntimeVersion != test.wantVersion {
				t.Fatalf("stored legacy runtime = %q %q", stored.Runtime, stored.RuntimeVersion)
			}
		})
	}

	mixedRequest := legacyRequest("mixed-worker")
	mixedRequest["codex_version"] = ""
	mixedRequest["runtime"] = ""
	mixedRequest["runtime_version"] = ""
	response := fixture.request(
		http.MethodPut, "/api/v1/workers/mixed-worker", "application/json", "", mixedRequest,
	)
	requireStatus(t, response, http.StatusBadRequest)
	response.Body.Close()

	response = fixture.request(
		http.MethodPut, "/api/v1/workers/claude-worker", "application/json", "",
		protocol.WorkerRegistration{
			Name: "claude-worker", WorkerVersion: "test", Runtime: protocol.RuntimeClaudeCode,
			RuntimeVersion: "2.1.220", Capacity: 1, Health: "healthy",
			Repositories: []protocol.RepositoryRegistration{{
				Key: "factory", RemoteIdentity: "github.com/example/claude-factory",
			}},
		},
	)
	requireStatus(t, response, http.StatusOK)
	runtimeAware := decodeResponse[protocol.Worker](t, response)
	if runtimeAware.Runtime != protocol.RuntimeClaudeCode || runtimeAware.RuntimeVersion != "2.1.220" {
		t.Fatalf("runtime-aware response = %q %q", runtimeAware.Runtime, runtimeAware.RuntimeVersion)
	}
}

func TestWaitForWorkerRegistrationRequiresAFreshHeartbeat(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1)

	t.Run("fresh registration", func(t *testing.T) {
		registrationError := make(chan error, 1)
		go func() {
			time.Sleep(20 * time.Millisecond)
			_, err := store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
				Name: workerA, WorkerVersion: "test", RuntimeVersion: "codex-test",
				Capacity: 1, Health: "healthy",
			})
			registrationError <- err
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		connected, err := waitForWorkerRegistration(ctx, store, workerA, worker.LastHeartbeat, 5*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-registrationError; err != nil {
			t.Fatal(err)
		}
		if !connected.LastHeartbeat.After(worker.LastHeartbeat) {
			t.Fatalf("connection heartbeat = %s; want after %s", connected.LastHeartbeat, worker.LastHeartbeat)
		}
	})

	t.Run("stale registration", func(t *testing.T) {
		current, err := store.Worker(context.Background(), workerA)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = waitForWorkerRegistration(ctx, store, workerA, current.LastHeartbeat, 5*time.Millisecond)
		var serviceError *ServiceError
		if !errors.As(err, &serviceError) || serviceError.Code != "worker_connection_timeout" {
			t.Fatalf("stale registration error = %v", err)
		}
	})

	t.Run("fresh liveness heartbeat", func(t *testing.T) {
		current, err := store.Worker(context.Background(), workerA)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		if _, err := store.HeartbeatWorker(context.Background(), workerA); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		connected, err := waitForWorkerRegistration(ctx, store, workerA, current.LastHeartbeat, 5*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if !connected.LastHeartbeat.After(current.LastHeartbeat) || !connected.Online {
			t.Fatalf("liveness heartbeat did not refresh connection evidence: %#v", connected)
		}
	})
}

func TestHTTPWorkerHeartbeatPreservesRunnerRegistration(t *testing.T) {
	fixture := newHTTPFixture(t)
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/example/factory", 1)
	time.Sleep(2 * time.Millisecond)
	response := fixture.request(
		http.MethodPut, "/api/v1/workers/"+worker.ID+"/heartbeat",
		"application/json", fixture.server.URL, "{}",
	)
	requireStatus(t, response, http.StatusOK)
	updated := decodeResponse[protocol.Worker](t, response)
	if !updated.LastHeartbeat.After(worker.LastHeartbeat) || updated.Capacity != worker.Capacity ||
		updated.Runtime != worker.Runtime || len(updated.Repositories) != len(worker.Repositories) {
		t.Fatalf("worker heartbeat changed registration = %#v; before %#v", updated, worker)
	}
}

func TestHTTPDeleteTaskHistory(t *testing.T) {
	fixture := newHTTPFixture(t)
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	task := createTestTask(t, fixture.store, "http-delete", workerA, worker.Repositories[0].ID)

	response := fixture.request(
		http.MethodDelete, "/api/v1/tasks/"+task.Task.ID,
		"application/json", fixture.server.URL, "{}",
	)
	requireStatus(t, response, http.StatusConflict)
	errorBody := decodeResponse[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, response)
	if errorBody.Error.Code != "task_not_terminal" {
		t.Fatalf("error code = %q", errorBody.Error.Code)
	}

	if _, err := fixture.store.CancelTask(context.Background(), task.Task.ID); err != nil {
		t.Fatal(err)
	}
	response = fixture.request(
		http.MethodDelete, "/api/v1/tasks/"+task.Task.ID,
		"application/json", "https://attacker.example", "{}",
	)
	requireStatus(t, response, http.StatusForbidden)
	response.Body.Close()

	response = fixture.request(
		http.MethodDelete, "/api/v1/tasks/"+task.Task.ID,
		"application/json", fixture.server.URL, "{}",
	)
	requireStatus(t, response, http.StatusOK)
	deleted := decodeResponse[map[string]bool](t, response)
	if !deleted["deleted"] {
		t.Fatal("delete response did not confirm deletion")
	}
	response = fixture.request(http.MethodGet, "/api/v1/tasks/"+task.Task.ID, "", "", nil)
	requireStatus(t, response, http.StatusNotFound)
	response.Body.Close()

	response = fixture.request(
		http.MethodDelete, "/api/v1/tasks/"+task.Task.ID,
		"application/json", fixture.server.URL, "{}",
	)
	requireStatus(t, response, http.StatusNotFound)
	response.Body.Close()

	retainedTask := createTestTask(t, fixture.store, "http-delete-retained", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, fixture.store, workerA, "http-delete-retained-claim", tokenA)
	if _, err := fixture.store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "retained",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.RegisterWorker(context.Background(), workerA, protocol.WorkerRegistration{
		Name: workerA, WorkerVersion: "test", RuntimeVersion: "codex-test",
		Capacity: 1, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/owainlewis/factory", RetainedCount: 1,
		}},
		RetainedWorktrees: []protocol.RetainedWorktree{{
			AttemptID: claim.Attempt.ID, RepositoryID: worker.Repositories[0].ID,
			Path: "/tmp/http-retained", Reason: "failed",
			CleanupCommand: "factory-worker cleanup " + claim.Attempt.ID,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	response = fixture.request(
		http.MethodDelete, "/api/v1/tasks/"+retainedTask.Task.ID,
		"application/json", fixture.server.URL, "{}",
	)
	requireStatus(t, response, http.StatusConflict)
	errorBody = decodeResponse[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, response)
	if errorBody.Error.Code != "retained_worktree" {
		t.Fatalf("retained error code = %q", errorBody.Error.Code)
	}
	if !strings.Contains(fixture.logs.String(), `"msg":"task_history_deleted"`) {
		t.Fatal("successful history deletion was not logged")
	}
}

func TestHTTPContractLifecycleAndIdempotency(t *testing.T) {
	fixture := newHTTPFixture(t)
	a := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	b := registerHTTPWorker(t, fixture, workerB, "other", "github.com/owainlewis/other", 2)
	if a.Capacity != 1 || b.Capacity != 2 || len(a.Repositories) != 1 || len(b.Repositories) != 1 {
		t.Fatalf("worker registrations were not preserved: %#v %#v", a, b)
	}

	response := fixture.request(http.MethodGet, "/api/v1/workers", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	workers := decodeResponse[struct {
		Workers []protocol.Worker `json:"workers"`
	}](t, response)
	if len(workers.Workers) != 2 {
		t.Fatalf("worker list has %d entries", len(workers.Workers))
	}
	response = fixture.request(http.MethodGet, "/api/v1/workers/"+workerA, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.Worker](t, response)

	const sensitivePrompt = "PROMPT-MUST-NOT-ENTER-LOGS"
	taskInput := protocol.CreateTaskRequest{
		RequestKey: "http-task", Title: "HTTP lifecycle", Description: sensitivePrompt,
		WorkerID: workerA, RepositoryID: a.Repositories[0].ID, TimeoutSeconds: 60,
	}
	response = fixture.request(http.MethodPost, "/api/v1/tasks", "application/json", fixture.server.URL, taskInput)
	requireStatus(t, response, http.StatusCreated)
	task := decodeResponse[protocol.TaskDetail](t, response)
	response = fixture.request(http.MethodPost, "/api/v1/tasks", "application/json", fixture.server.URL, taskInput)
	requireStatus(t, response, http.StatusOK)
	duplicate := decodeResponse[protocol.TaskDetail](t, response)
	if duplicate.Task.ID != task.Task.ID {
		t.Fatal("duplicate request key returned another task")
	}

	response = fixture.request(http.MethodPost, "/api/v1/workers/"+workerB+"/claims", "application/json", "", protocol.ClaimRequest{
		RequestID: "wrong-worker", LeaseToken: tokenB,
	})
	requireStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	response = fixture.request(http.MethodPost, "/api/v1/workers/"+workerA+"/claims", "application/json", "", protocol.ClaimRequest{
		RequestID: "right-worker", LeaseToken: tokenA,
	})
	requireStatus(t, response, http.StatusOK)
	claim := decodeResponse[protocol.Claim](t, response)

	response = fixture.request(http.MethodGet, "/api/v1/attempts/"+claim.Attempt.ID, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.Attempt](t, response)
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/start", "application/json", "", protocol.StartAttemptRequest{
		LeaseToken: tokenA, ProcessIdentity: "process-observation",
	})
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.Attempt](t, response)
	response = fixture.request(http.MethodGet, "/api/v1/workers/"+workerA, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	activeWorker := decodeResponse[protocol.Worker](t, response)
	if activeWorker.CurrentTaskTitle != task.Task.Title {
		t.Fatalf("current task title = %q, want %q", activeWorker.CurrentTaskTitle, task.Task.Title)
	}
	response = fixture.request(http.MethodPut, "/api/v1/attempts/"+claim.Attempt.ID+"/heartbeat", "application/json", "", protocol.LeaseRequest{LeaseToken: tokenA})
	requireStatus(t, response, http.StatusOK)
	heartbeat := decodeResponse[protocol.HeartbeatResponse](t, response)
	if heartbeat.CancellationRequested {
		t.Fatal("unexpected cancellation")
	}

	const sensitiveEvent = "EVENT-MUST-NOT-ENTER-LOGS"
	events := protocol.EventBatchRequest{LeaseToken: tokenA, Events: []protocol.AttemptEvent{
		{Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"text":"` + sensitiveEvent + `"}`)},
	}}
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/events", "application/json", "", events)
	requireStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/events", "application/json", "", events)
	requireStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	response = fixture.request(http.MethodGet, "/api/v1/attempts/"+claim.Attempt.ID+"/events?after=-1", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	eventList := decodeResponse[struct {
		Events []protocol.AttemptEvent `json:"events"`
	}](t, response)
	if len(eventList.Events) != 1 {
		t.Fatalf("event replay stored %d events", len(eventList.Events))
	}

	response = fixture.request(http.MethodPost, "/api/v1/tasks/"+task.Task.ID+"/cancel", "application/json", "", map[string]any{})
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.TaskDetail](t, response)
	response = fixture.request(http.MethodPut, "/api/v1/attempts/"+claim.Attempt.ID+"/heartbeat", "application/json", "", protocol.LeaseRequest{LeaseToken: tokenA})
	requireStatus(t, response, http.StatusOK)
	heartbeat = decodeResponse[protocol.HeartbeatResponse](t, response)
	if !heartbeat.CancellationRequested {
		t.Fatal("cancellation was not delivered")
	}

	const sensitiveResult = "RESULT-MUST-NOT-ENTER-LOGS"
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/complete", "application/json", "", protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "cancelled", Result: sensitiveResult,
	})
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.Attempt](t, response)
	response = fixture.request(http.MethodGet, "/api/v1/workers/"+workerA, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	idleWorker := decodeResponse[protocol.Worker](t, response)
	if idleWorker.CurrentTaskTitle != "" {
		t.Fatalf("terminal task remained current: %q", idleWorker.CurrentTaskTitle)
	}
	response = fixture.request(http.MethodPost, "/api/v1/executions/"+task.Execution.ID+"/retry", "application/json", "", map[string]any{})
	requireStatus(t, response, http.StatusOK)
	retried := decodeResponse[protocol.TaskDetail](t, response)
	if retried.Execution.State != "queued" || len(retried.Attempts) != 1 {
		t.Fatalf("retry response: %#v", retried)
	}
	response = fixture.request(http.MethodGet, "/api/v1/tasks/"+task.Task.ID, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	decodeResponse[protocol.TaskDetail](t, response)
	response = fixture.request(http.MethodGet, "/api/v1/tasks", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	decodeResponse[map[string]any](t, response)

	logText := fixture.logs.String()
	for _, secret := range []string{sensitivePrompt, sensitiveEvent, sensitiveResult, tokenA} {
		if strings.Contains(logText, secret) {
			t.Fatalf("structured request log leaked sensitive value %q", secret)
		}
	}
	for _, field := range []string{
		`"msg":"state_change"`,
		`"resource_id":"` + claim.Attempt.ID + `"`,
		`"new_state":"cancelled"`,
		`"new_state":"queued"`,
		`"cancellation_requested":true`,
	} {
		if !strings.Contains(logText, field) {
			t.Fatalf("structured state log is missing %s", field)
		}
	}
}

func TestHTTPTaskPaginationKeepsEqualTimestampOrdering(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.store.now = func() time.Time {
		return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	}
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	var expected []string
	for index := 0; index < 3; index++ {
		task := createTestTask(t, fixture.store, "http-page-"+strconv.Itoa(index), workerA, worker.Repositories[0].ID)
		expected = append(expected, task.Task.ID)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(expected)))

	type taskPage struct {
		Tasks      []protocol.Task `json:"tasks"`
		NextCursor *string         `json:"next_cursor"`
	}
	response := fixture.request(http.MethodGet, "/api/v1/tasks?limit=2", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	first := decodeResponse[taskPage](t, response)
	if len(first.Tasks) != 2 || first.NextCursor == nil {
		t.Fatalf("first task page = %#v", first)
	}
	response = fixture.request(http.MethodGet, "/api/v1/tasks?limit=2&cursor="+*first.NextCursor, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	second := decodeResponse[taskPage](t, response)
	if len(second.Tasks) != 1 || second.NextCursor != nil {
		t.Fatalf("second task page = %#v", second)
	}
	actual := []string{first.Tasks[0].ID, first.Tasks[1].ID, second.Tasks[0].ID}
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("paged task IDs = %v; want %v", actual, expected)
	}
}

func TestHTTPTaskPaginationUsesABoundedDefault(t *testing.T) {
	fixture := newHTTPFixture(t)
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	for index := 0; index < protocol.DefaultTaskPageSize+1; index++ {
		createTestTask(t, fixture.store, "default-page-"+strconv.Itoa(index), workerA, worker.Repositories[0].ID)
	}
	response := fixture.request(http.MethodGet, "/api/v1/tasks", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	page := decodeResponse[struct {
		Tasks      []protocol.Task `json:"tasks"`
		NextCursor *string         `json:"next_cursor"`
	}](t, response)
	if len(page.Tasks) != protocol.DefaultTaskPageSize || page.NextCursor == nil {
		t.Fatalf("default task page = %#v", page)
	}
}

func TestHTTPEventPaginationReturnsBoundedMetadata(t *testing.T) {
	fixture := newHTTPFixture(t)
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	createTestTask(t, fixture.store, "http-event-page", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, fixture.store, workerA, "http-event-page-claim", tokenA)
	if err := fixture.store.AppendEvents(context.Background(), claim.Attempt.ID, protocol.EventBatchRequest{
		LeaseToken: tokenA,
		Events: []protocol.AttemptEvent{
			{Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"step":0}`)},
			{Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"step":1}`)},
			{Sequence: 2, Kind: "progress", Payload: json.RawMessage(`{"step":2}`)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	type eventPage struct {
		Events    []protocol.AttemptEvent `json:"events"`
		NextAfter int64                   `json:"next_after"`
		HasMore   bool                    `json:"has_more"`
	}
	response := fixture.request(http.MethodGet, "/api/v1/attempts/"+claim.Attempt.ID+"/events?after=-1&limit=2", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	first := decodeResponse[eventPage](t, response)
	if len(first.Events) != 2 || first.NextAfter != 1 || !first.HasMore {
		t.Fatalf("first event page = %#v", first)
	}
	response = fixture.request(http.MethodGet, "/api/v1/attempts/"+claim.Attempt.ID+"/events?after=1&limit=2", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	second := decodeResponse[eventPage](t, response)
	if len(second.Events) != 1 || second.Events[0].Sequence != 2 ||
		second.NextAfter != 2 || second.HasMore {
		t.Fatalf("second event page = %#v", second)
	}
	response = fixture.request(http.MethodGet, "/api/v1/attempts/"+claim.Attempt.ID+"/events", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	defaults := decodeResponse[eventPage](t, response)
	if len(defaults.Events) != 3 || defaults.NextAfter != 2 || defaults.HasMore {
		t.Fatalf("default event page = %#v", defaults)
	}
}

func TestHTTPPaginationRejectsInvalidParameters(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, test := range []struct {
		path string
		code string
	}{
		{path: "/api/v1/tasks?limit=0", code: "invalid_limit"},
		{path: "/api/v1/tasks?limit=201", code: "invalid_limit"},
		{path: "/api/v1/tasks?limit=many", code: "invalid_limit"},
		{path: "/api/v1/tasks?cursor=not-a-cursor", code: "invalid_cursor"},
		{path: "/api/v1/attempts/missing/events?after=-2", code: "invalid_after"},
		{path: "/api/v1/attempts/missing/events?after=-1&limit=0", code: "invalid_limit"},
		{path: "/api/v1/attempts/missing/events?after=-1&limit=501", code: "invalid_limit"},
		{path: "/api/v1/attempts/missing/events?after=-1&limit=many", code: "invalid_limit"},
	} {
		response := fixture.request(http.MethodGet, test.path, "", "", nil)
		requireStatus(t, response, http.StatusBadRequest)
		body := decodeResponse[protocol.ErrorBody](t, response)
		if body.Error.Code != test.code {
			t.Fatalf("%s error code = %q; want %q", test.path, body.Error.Code, test.code)
		}
	}
}

func TestHTTPRejectsMalformedOversizedAndCrossOriginMutations(t *testing.T) {
	fixture := newHTTPFixture(t)
	cases := []struct {
		name        string
		contentType string
		origin      string
		body        any
		status      int
		code        string
	}{
		{name: "malformed", contentType: "application/json", body: `{`, status: 400, code: "malformed_json"},
		{name: "unknown field", contentType: "application/json", body: `{"unexpected":true}`, status: 400, code: "malformed_json"},
		{name: "form", contentType: "application/x-www-form-urlencoded", body: `title=bad`, status: 415, code: "json_required"},
		{name: "cross origin", contentType: "application/json", origin: "https://evil.example", body: `{}`, status: 403, code: "cross_origin_request"},
		{name: "oversized", contentType: "application/json", body: `{"description":"` + strings.Repeat("x", protocol.MaxBodyBytes) + `"}`, status: 413, code: "body_too_large"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.request(http.MethodPost, "/api/v1/tasks", test.contentType, test.origin, test.body)
			requireStatus(t, response, test.status)
			body := decodeResponse[protocol.ErrorBody](t, response)
			if body.Error.Code != test.code {
				t.Fatalf("error code %q, want %q", body.Error.Code, test.code)
			}
			if response.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("response included a permissive CORS header")
			}
		})
	}
	logText := fixture.logs.String()
	for _, code := range []string{"malformed_json", "json_required", "cross_origin_request", "body_too_large"} {
		if !strings.Contains(logText, `"error_class":"`+code+`"`) {
			t.Fatalf("request log is missing error class %q", code)
		}
	}
}

func TestHTTPReadsDoNotEmitFalseStateChanges(t *testing.T) {
	fixture := newHTTPFixture(t)
	registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	fixture.logs.Reset()
	for _, path := range []string{"/healthz", "/api/v1/workers", "/api/v1/workers/" + workerA, "/api/v1/tasks"} {
		response := fixture.request(http.MethodGet, path, "", "", nil)
		requireStatus(t, response, http.StatusOK)
		response.Body.Close()
	}
	if strings.Contains(fixture.logs.String(), `"msg":"state_change"`) {
		t.Fatal("read-only polling emitted a state change log")
	}
}

func TestHTTPRejectsDNSRebindingHostEvenWhenItResolvesToLoopback(t *testing.T) {
	fixture := newHTTPFixture(t)
	originalLookup := lookupIP
	t.Cleanup(func() { lookupIP = originalLookup })
	lookupIP = func(host string) ([]net.IP, error) {
		if host == "attacker.example" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return originalLookup(host)
	}
	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+"/api/v1/tasks", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.example:7337"
	request.Header.Set("Origin", "http://attacker.example:7337")
	request.Header.Set("Content-Type", "application/json")
	response, err := fixture.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusForbidden)
	body := decodeResponse[protocol.ErrorBody](t, response)
	if body.Error.Code != "invalid_host" {
		t.Fatalf("DNS rebinding error code = %q", body.Error.Code)
	}
	request, err = http.NewRequest(http.MethodGet, fixture.server.URL+"/api/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.example:7337"
	response, err = fixture.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, response, http.StatusForbidden)
	body = decodeResponse[protocol.ErrorBody](t, response)
	if body.Error.Code != "invalid_host" {
		t.Fatalf("DNS rebinding GET error code = %q", body.Error.Code)
	}
}

func TestHTTPRejectsStaleLeaseAndConflictingEventReplay(t *testing.T) {
	fixture := newHTTPFixture(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fixture.store.now = func() time.Time { return now }
	worker := registerHTTPWorker(t, fixture, workerA, "factory", "github.com/owainlewis/factory", 1)
	task := createTestTask(t, fixture.store, "stale-http", workerA, worker.Repositories[0].ID)
	claim := claimTestTask(t, fixture.store, workerA, "stale-http-claim", tokenA)

	first := protocol.EventBatchRequest{LeaseToken: tokenA, Events: []protocol.AttemptEvent{
		{Sequence: 0, Kind: "progress", Payload: json.RawMessage(`{"value":1}`)},
	}}
	response := fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/events", "application/json", "", first)
	requireStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	first.Events[0].Payload = json.RawMessage(`{"value":2}`)
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/events", "application/json", "", first)
	requireStatus(t, response, http.StatusConflict)
	errorBody := decodeResponse[protocol.ErrorBody](t, response)
	if errorBody.Error.Code != "event_conflict" {
		t.Fatalf("event conflict code = %q", errorBody.Error.Code)
	}

	now = now.Add(protocol.LeaseDuration + time.Millisecond)
	response = fixture.request(http.MethodPost, "/api/v1/attempts/"+claim.Attempt.ID+"/complete", "application/json", "", protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "late",
	})
	requireStatus(t, response, http.StatusConflict)
	errorBody = decodeResponse[protocol.ErrorBody](t, response)
	if errorBody.Error.Code != "lease_not_owner" {
		t.Fatalf("stale completion code = %q", errorBody.Error.Code)
	}
	detail, err := fixture.store.Task(context.Background(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Execution.State != "preparing" {
		t.Fatalf("stale completion changed state to %s", detail.Execution.State)
	}
}

func TestValidateListenAddressRejectsPublicBindingsAndExternalHostnames(t *testing.T) {
	originalLookup := lookupIP
	t.Cleanup(func() { lookupIP = originalLookup })
	lookupIP = func(host string) ([]net.IP, error) {
		switch host {
		case "localhost":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, nil
		case "external.test":
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		default:
			return originalLookup(host)
		}
	}
	for _, address := range []string{"127.0.0.1:7337", "[::1]:7337", "localhost:7337"} {
		if err := ValidateListenAddress(address); err != nil {
			t.Errorf("%s should be accepted: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:7337", "192.0.2.1:7337", "external.test:7337", ":7337"} {
		if err := ValidateListenAddress(address); err == nil {
			t.Errorf("%s should be rejected", address)
		}
	}
}

func TestResolveListenAddressUsesOneValidatedDNSAnswer(t *testing.T) {
	originalLookup := lookupIP
	t.Cleanup(func() { lookupIP = originalLookup })
	calls := 0
	lookupIP = func(host string) ([]net.IP, error) {
		calls++
		if calls == 1 {
			return []net.IP{net.ParseIP("127.0.0.2")}, nil
		}
		return []net.IP{net.ParseIP("0.0.0.0")}, nil
	}
	address, err := ResolveListenAddress("localhost:7337")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !address.IP.Equal(net.ParseIP("127.0.0.2")) {
		t.Fatalf("resolver calls=%d address=%v", calls, address)
	}
}

func TestPeriodicSweeperExpiresAttempts(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.sweepEvery = 5 * time.Millisecond
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createTestTask(t, store, "periodic-expiry", workerA, worker.Repositories[0].ID)
	claimTestTask(t, store, workerA, "periodic-claim", tokenA)
	now = now.Add(protocol.LeaseDuration + time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.RunSweeper(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		detail, err := store.Task(context.Background(), task.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Execution.State == "failed" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("periodic sweep did not expire the attempt")
}
