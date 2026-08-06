package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestLocalOperatorCreatesBoundEnrollmentWithoutLoggingSecret(t *testing.T) {
	store := newTestStore(t)
	logs := &bytes.Buffer{}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewJSONHandler(logs, nil))))
	t.Cleanup(server.Close)
	response := remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/runner-enrollments", "",
		protocol.CreateRunnerEnrollmentRequest{WorkerID: "remote-a"})
	requireStatus(t, response, http.StatusCreated)
	enrollment := decodeResponse[protocol.RunnerEnrollment](t, response)
	if enrollment.WorkerID != "remote-a" || enrollment.EnrollmentToken == "" ||
		enrollment.ExpiresAt.IsZero() {
		t.Fatalf("enrollment = %#v", enrollment)
	}
	if strings.Contains(logs.String(), enrollment.EnrollmentToken) {
		t.Fatal("operator request log contained the enrollment secret")
	}
}

func remoteRunnerRequest(
	t *testing.T,
	client *http.Client,
	baseURL, method, path, credential string,
	body any,
) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func issueRemoteRunnerCredential(
	t *testing.T,
	store *Store,
	server *httptest.Server,
	workerID string,
) string {
	t.Helper()
	enrollment, err := store.CreateRunnerEnrollment(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	response := remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/runner-enrollments/exchange", "", protocol.ExchangeRunnerEnrollmentRequest{
			WorkerID: workerID, EnrollmentToken: enrollment.EnrollmentToken,
		})
	requireStatus(t, response, http.StatusCreated)
	return decodeResponse[protocol.RunnerCredential](t, response).Credential
}

func TestRemoteRunnerTLSLifecycleAndIsolation(t *testing.T) {
	store := newTestStore(t)
	server := httptest.NewTLSServer(NewRemoteRunnerHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	enrollment, err := store.CreateRunnerEnrollment(context.Background(), "remote-a")
	if err != nil {
		t.Fatal(err)
	}
	exchange := protocol.ExchangeRunnerEnrollmentRequest{
		WorkerID: "remote-a", EnrollmentToken: enrollment.EnrollmentToken,
	}
	response := remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/runner-enrollments/exchange", "", protocol.ExchangeRunnerEnrollmentRequest{
			WorkerID: "remote-b", EnrollmentToken: enrollment.EnrollmentToken,
		})
	requireStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/runner-enrollments/exchange", "", exchange)
	requireStatus(t, response, http.StatusCreated)
	credential := decodeResponse[protocol.RunnerCredential](t, response).Credential
	if credential == "" || credential == enrollment.EnrollmentToken {
		t.Fatal("enrollment did not return a distinct Runner credential")
	}
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/runner-enrollments/exchange", "", exchange)
	requireStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()

	registration := protocol.WorkerRegistration{
		Name: "Remote build VM", Labels: map[string]string{"region": "eu-west", "host": "build-01"},
		WorkerVersion: "test", Runtime: protocol.RuntimeCodex, RuntimeVersion: "test",
		Capabilities: []protocol.Capability{{
			Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady,
		}},
		Capacity: 2, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{Key: "factory", RemoteIdentity: "github.com/example/factory"}},
	}
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPut,
		"/api/v1/workers/remote-a", credential, registration)
	requireStatus(t, response, http.StatusOK)
	worker := decodeResponse[protocol.Worker](t, response)
	if worker.Labels["region"] != "eu-west" || worker.Capacity != 2 || len(worker.Capabilities) != 1 {
		t.Fatalf("remote Runner registration = %#v", worker)
	}

	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "remote-job", Title: "Review remote repository", Description: "Review it.",
		WorkerID: worker.ID, RepositoryID: worker.Repositories[0].ID,
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	if err != nil || !created {
		t.Fatalf("create remote task: created %t, err %v", created, err)
	}
	leaseToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/workers/remote-a/claims", credential,
		protocol.ClaimRequest{RequestID: "remote-claim", LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	claim := decodeResponse[protocol.Claim](t, response)
	if claim.Task.ID != detail.Task.ID {
		t.Fatalf("claimed task = %s, want %s", claim.Task.ID, detail.Task.ID)
	}

	otherCredential := issueRemoteRunnerCredential(t, store, server, "remote-b")
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodGet,
		"/api/v1/attempts/"+claim.Attempt.ID, otherCredential, nil)
	requireStatus(t, response, http.StatusForbidden)
	response.Body.Close()

	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/attempts/"+claim.Attempt.ID+"/start", credential,
		protocol.StartAttemptRequest{LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	response.Body.Close()
	if _, err := store.CancelTask(context.Background(), detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPut,
		"/api/v1/attempts/"+claim.Attempt.ID+"/heartbeat", credential,
		protocol.LeaseRequest{LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	heartbeat := decodeResponse[protocol.HeartbeatResponse](t, response)
	if !heartbeat.CancellationRequested {
		t.Fatal("remote Runner heartbeat did not expose cancellation")
	}
	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/attempts/"+claim.Attempt.ID+"/complete", credential,
		protocol.CompleteAttemptRequest{LeaseToken: leaseToken, State: "cancelled", Result: "stopped"})
	requireStatus(t, response, http.StatusOK)
	completed := decodeResponse[protocol.Attempt](t, response)
	if completed.State != "cancelled" || completed.Result != "stopped" {
		t.Fatalf("completed remote attempt = %#v", completed)
	}

	response = remoteRunnerRequest(t, server.Client(), server.URL, http.MethodGet,
		"/api/v1/workers", credential, nil)
	requireStatus(t, response, http.StatusNotFound)
	response.Body.Close()
}

func TestRemoteRunnerDisconnectReconnectAndLostAttemptRemainVisible(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, "remote-status", 1,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/example/factory"})
	detail, created, err := store.CreateTask(context.Background(), protocol.CreateTaskRequest{
		RequestKey: "lost-remote-job", Title: "Remote job", Description: "Run remotely.", WorkerID: worker.ID,
		RepositoryID: worker.Repositories[0].ID, Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	if err != nil || !created {
		t.Fatalf("create task: created %t, err %v", created, err)
	}
	claim := claimTestTask(t, store, worker.ID, "lost-claim", tokenA)
	now = now.Add(2 * protocol.WorkerOnlineWindow)
	disconnected, err := store.Worker(context.Background(), worker.ID)
	if err != nil || disconnected.Online {
		t.Fatalf("disconnected Runner = %#v, err %v", disconnected, err)
	}
	expired, err := store.SweepExpired(context.Background())
	if err != nil || len(expired) != 1 || expired[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("expired remote attempt = %#v, err %v", expired, err)
	}
	lost, err := store.Task(context.Background(), detail.Task.ID)
	if err != nil || len(lost.Attempts) != 1 || lost.Attempts[0].State != "lost" {
		t.Fatalf("lost task detail = %#v, err %v", lost, err)
	}
	if _, err := store.HeartbeatWorker(context.Background(), worker.ID); err != nil {
		t.Fatal(err)
	}
	reconnected, err := store.Worker(context.Background(), worker.ID)
	if err != nil || !reconnected.Online {
		t.Fatalf("reconnected Runner = %#v, err %v", reconnected, err)
	}
}
