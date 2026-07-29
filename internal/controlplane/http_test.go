package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
		Name: id, WorkerVersion: "test", CodexVersion: "codex-test", Capacity: capacity, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{Key: key, RemoteIdentity: remote}},
	})
	requireStatus(t, response, http.StatusOK)
	return decodeResponse[protocol.Worker](t, response)
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
