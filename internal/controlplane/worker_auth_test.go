package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestWorkerNamingMigrationPreservesRemoteCredentials(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "worker-naming.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := database.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		);
		CREATE TABLE runner_enrollments (
			id TEXT PRIMARY KEY,
			worker_id TEXT NOT NULL,
			token_digest BLOB NOT NULL UNIQUE,
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX runner_enrollments_expiry ON runner_enrollments(expires_at);
		CREATE TABLE remote_runner_credentials (
			worker_id TEXT PRIMARY KEY,
			token_digest BLOB NOT NULL UNIQUE,
			created_at INTEGER NOT NULL,
			last_used_at INTEGER NOT NULL
		);
		CREATE TABLE jobs (
			id TEXT PRIMARY KEY,
			blocked_reason TEXT
		);
		INSERT INTO runner_enrollments(
			id, worker_id, token_digest, expires_at, used_at, created_at
		) VALUES ('enrollment', 'worker-a', X'0102', 40, 30, 20);
		INSERT INTO remote_runner_credentials(
			worker_id, token_digest, created_at, last_used_at
		) VALUES ('worker-a', X'0304', 20, 30);
		INSERT INTO jobs(id, blocked_reason) VALUES
			('legacy-blocked', 'Waiting for a healthy compatible Runner with repository access.'),
			('custom-blocked', 'Waiting for an operator.');
	`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 25; version++ {
		if _, err := database.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, 1)`, version,
		); err != nil {
			t.Fatal(err)
		}
	}

	store := &Store{db: database, now: time.Now}
	if err := store.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	var enrollmentWorker string
	var enrollmentDigest []byte
	if err := database.QueryRow(`
		SELECT worker_id, token_digest FROM worker_enrollments WHERE id = 'enrollment'
	`).Scan(&enrollmentWorker, &enrollmentDigest); err != nil {
		t.Fatal(err)
	}
	if enrollmentWorker != "worker-a" || !bytes.Equal(enrollmentDigest, []byte{1, 2}) {
		t.Fatalf("migrated enrollment = worker %q, digest %x", enrollmentWorker, enrollmentDigest)
	}
	var credentialDigest []byte
	if err := database.QueryRow(`
		SELECT token_digest FROM remote_worker_credentials WHERE worker_id = 'worker-a'
	`).Scan(&credentialDigest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credentialDigest, []byte{3, 4}) {
		t.Fatalf("migrated credential digest = %x", credentialDigest)
	}
	var migratedReason string
	if err := database.QueryRow(`
		SELECT blocked_reason FROM jobs WHERE id = 'legacy-blocked'
	`).Scan(&migratedReason); err != nil {
		t.Fatal(err)
	}
	if migratedReason != "Waiting for a healthy compatible Worker with repository access." {
		t.Fatalf("migrated blocked reason = %q", migratedReason)
	}
	var customReason string
	if err := database.QueryRow(`
		SELECT blocked_reason FROM jobs WHERE id = 'custom-blocked'
	`).Scan(&customReason); err != nil {
		t.Fatal(err)
	}
	if customReason != "Waiting for an operator." {
		t.Fatalf("custom blocked reason changed to %q", customReason)
	}
	for _, legacyTable := range []string{"runner_enrollments", "remote_runner_credentials"} {
		var count int
		if err := database.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?
		`, legacyTable).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy table %s remains after migration", legacyTable)
		}
	}
}

func TestLocalOperatorCreatesBoundEnrollmentWithoutLoggingSecret(t *testing.T) {
	store := newTestStore(t)
	logs := &bytes.Buffer{}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewJSONHandler(logs, nil))))
	t.Cleanup(server.Close)
	response := remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments", "",
		protocol.CreateWorkerEnrollmentRequest{WorkerID: "remote-a"})
	requireStatus(t, response, http.StatusCreated)
	enrollment := decodeResponse[protocol.WorkerEnrollment](t, response)
	if enrollment.WorkerID != "remote-a" || enrollment.EnrollmentToken == "" ||
		enrollment.ExpiresAt.IsZero() {
		t.Fatalf("enrollment = %#v", enrollment)
	}
	if strings.Contains(logs.String(), enrollment.EnrollmentToken) {
		t.Fatal("operator request log contained the enrollment secret")
	}
}

func TestLegacyWorkerCredentialCanOnlyReplayMigratedEnrollment(t *testing.T) {
	store := newTestStore(t)
	enrollment, err := store.CreateWorkerEnrollment(context.Background(), "legacy-worker")
	if err != nil {
		t.Fatal(err)
	}
	const credential = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := store.now().UTC().UnixMilli()
	if _, err := store.db.Exec(`
		UPDATE worker_enrollments SET used_at = ? WHERE worker_id = ?
	`, now, "legacy-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO remote_worker_credentials(worker_id, token_digest, created_at, last_used_at)
		VALUES (?, ?, ?, ?)
	`, "legacy-worker", digestToken(credential), now, now); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ExchangeWorkerEnrollment(
		context.Background(), "legacy-worker", enrollment.EnrollmentToken, credential,
	)
	if err != nil || replayed.Credential != credential {
		t.Fatalf("replayed legacy credential = %#v, err %v", replayed, err)
	}

	fresh, err := store.CreateWorkerEnrollment(context.Background(), "new-worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExchangeWorkerEnrollment(
		context.Background(), "new-worker", fresh.EnrollmentToken, credential,
	); err == nil {
		t.Fatal("accepted a legacy credential for a new enrollment")
	} else {
		var serviceError *ServiceError
		if !errors.As(err, &serviceError) || serviceError.Code != "worker_credential_regeneration_required" {
			t.Fatalf("fresh legacy exchange error = %v", err)
		}
	}
}

func remoteWorkerRequest(
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

func issueRemoteWorkerCredential(
	t *testing.T,
	store *Store,
	server *httptest.Server,
	workerID string,
) string {
	t.Helper()
	enrollment, err := store.CreateWorkerEnrollment(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := randomWorkerSecret("factory_worker_")
	if err != nil {
		t.Fatal(err)
	}
	response := remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments/exchange", "", protocol.ExchangeWorkerEnrollmentRequest{
			WorkerID: workerID, EnrollmentToken: enrollment.EnrollmentToken, Credential: credential,
		})
	requireStatus(t, response, http.StatusCreated)
	return decodeResponse[protocol.WorkerCredential](t, response).Credential
}

func TestRemoteWorkerTLSLifecycleAndIsolation(t *testing.T) {
	store := newTestStore(t)
	server := httptest.NewTLSServer(NewRemoteWorkerHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	enrollment, err := store.CreateWorkerEnrollment(context.Background(), "remote-a")
	if err != nil {
		t.Fatal(err)
	}
	exchange := protocol.ExchangeWorkerEnrollmentRequest{
		WorkerID: "remote-a", EnrollmentToken: enrollment.EnrollmentToken,
		Credential: "factory_worker_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	response := remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments/exchange", "", protocol.ExchangeWorkerEnrollmentRequest{
			WorkerID: "remote-b", EnrollmentToken: enrollment.EnrollmentToken, Credential: exchange.Credential,
		})
	requireStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments/exchange", "", exchange)
	requireStatus(t, response, http.StatusCreated)
	credential := decodeResponse[protocol.WorkerCredential](t, response).Credential
	if credential == "" || credential == enrollment.EnrollmentToken {
		t.Fatal("enrollment did not return a distinct Worker credential")
	}
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments/exchange", "", exchange)
	requireStatus(t, response, http.StatusCreated)
	if replayed := decodeResponse[protocol.WorkerCredential](t, response).Credential; replayed != credential {
		t.Fatalf("replayed credential = %q; want %q", replayed, credential)
	}
	exchange.Credential = "factory_worker_ccccccccccccccccccccccccccccccccccccccccccc"
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/worker-enrollments/exchange", "", exchange)
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
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPut,
		"/api/v1/workers/remote-a", credential, registration)
	requireStatus(t, response, http.StatusOK)
	worker := decodeResponse[protocol.Worker](t, response)
	if worker.Labels["region"] != "eu-west" || worker.Capacity != 2 || len(worker.Capabilities) != 1 {
		t.Fatalf("remote Worker registration = %#v", worker)
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
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/workers/remote-a/claims", credential,
		protocol.ClaimRequest{RequestID: "remote-claim", LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	claim := decodeResponse[protocol.Claim](t, response)
	if claim.Task.ID != detail.Task.ID {
		t.Fatalf("claimed task = %s, want %s", claim.Task.ID, detail.Task.ID)
	}

	otherCredential := issueRemoteWorkerCredential(t, store, server, "remote-b")
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodGet,
		"/api/v1/attempts/"+claim.Attempt.ID, otherCredential, nil)
	requireStatus(t, response, http.StatusForbidden)
	response.Body.Close()

	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/attempts/"+claim.Attempt.ID+"/start", credential,
		protocol.StartAttemptRequest{LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	response.Body.Close()
	if _, err := store.CancelTask(context.Background(), detail.Task.ID); err != nil {
		t.Fatal(err)
	}
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPut,
		"/api/v1/attempts/"+claim.Attempt.ID+"/heartbeat", credential,
		protocol.LeaseRequest{LeaseToken: leaseToken})
	requireStatus(t, response, http.StatusOK)
	heartbeat := decodeResponse[protocol.HeartbeatResponse](t, response)
	if !heartbeat.CancellationRequested {
		t.Fatal("remote Worker heartbeat did not expose cancellation")
	}
	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodPost,
		"/api/v1/attempts/"+claim.Attempt.ID+"/complete", credential,
		protocol.CompleteAttemptRequest{LeaseToken: leaseToken, State: "cancelled", Result: "stopped"})
	requireStatus(t, response, http.StatusOK)
	completed := decodeResponse[protocol.Attempt](t, response)
	if completed.State != "cancelled" || completed.Result != "stopped" {
		t.Fatalf("completed remote attempt = %#v", completed)
	}

	response = remoteWorkerRequest(t, server.Client(), server.URL, http.MethodGet,
		"/api/v1/workers", credential, nil)
	requireStatus(t, response, http.StatusNotFound)
	response.Body.Close()
}

func TestRemoteWorkerDisconnectReconnectAndLostAttemptRemainVisible(t *testing.T) {
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
		t.Fatalf("disconnected Worker = %#v, err %v", disconnected, err)
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
		t.Fatalf("reconnected Worker = %#v, err %v", reconnected, err)
	}
}
