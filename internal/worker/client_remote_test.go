package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRemoteClientRejectsRedirectsBeforeSendingSecrets(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client := newClient(redirect.URL, redirect.Client())
	client.credential = "factory_runner_credential"
	if _, err := client.request(context.Background(), http.MethodPost, "/credential", struct{}{}, nil); err == nil {
		t.Fatal("credential request followed a redirect")
	}
	if _, err := client.requestWithoutCredential(context.Background(), http.MethodPost, "/enrollment", map[string]string{
		"enrollment_token": "factory_enroll_secret",
	}, nil); err == nil {
		t.Fatal("enrollment request followed a redirect")
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target received %d secret-bearing requests", targetRequests)
	}
	if redirect.Client().CheckRedirect != nil {
		t.Fatal("newClient mutated the caller's HTTP client")
	}
}

func TestRemoteClientEnrollsOnceAndPersistsCredential(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	credential := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runner-enrollments/exchange":
			var input protocol.ExchangeRunnerEnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.WorkerID != "remote-worker" || input.EnrollmentToken != enrollment ||
				!strings.HasPrefix(input.Credential, "factory_runner_") {
				t.Errorf("exchange input = %#v", input)
			}
			credential = input.Credential
			_ = json.NewEncoder(w).Encode(protocol.RunnerCredential{Credential: input.Credential})
		case "/api/v1/workers/remote-worker":
			if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
				t.Errorf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(protocol.Worker{
				ID: "remote-worker", Name: "remote", Runtime: protocol.RuntimeCodex,
				Repositories: []protocol.Repository{}, RetainedWorktrees: []protocol.RetainedWorktree{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	path := filepath.Join(directory, "runner-credential")
	client := newClient(server.URL, server.Client())
	if err := client.enroll(context.Background(), "remote-worker", enrollment, path); err != nil {
		t.Fatal(err)
	}
	stored, err := loadCredentialFile(path, server.URL)
	if err != nil || stored != credential {
		t.Fatalf("stored credential = %q, err %v", stored, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v", info.Mode().Perm())
	}
	if _, err := client.register(context.Background(), "remote-worker", protocol.WorkerRegistration{}); err != nil {
		t.Fatal(err)
	}
	if err := client.enroll(context.Background(), "remote-worker", "wrong", path); err != nil {
		t.Fatalf("saved credential triggered re-enrollment: %v", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want one exchange and one registration", requests)
	}
	if _, err := loadCredentialFile(path, "https://other-factory.example.com:7443"); err == nil {
		t.Fatal("Runner credential was accepted for a different Factory server")
	}
}

func TestRemoteClientRetriesTheSamePendingCredentialAfterResponseLoss(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	firstCredential := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input protocol.ExchangeRunnerEnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if requests == 1 {
			firstCredential = input.Credential
			panic(http.ErrAbortHandler)
		}
		if input.Credential != firstCredential {
			t.Errorf("retried credential = %q; want %q", input.Credential, firstCredential)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.RunnerCredential{Credential: input.Credential})
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "runner-credential")
	first := newClient(server.URL, server.Client())
	if err := first.enroll(context.Background(), "remote-worker", enrollment, path); err == nil {
		t.Fatal("enrollment unexpectedly survived a lost response")
	}
	if _, err := os.Stat(path + ".pending"); err != nil {
		t.Fatalf("pending credential was not preserved: %v", err)
	}
	second := newClient(server.URL, server.Client())
	if err := second.enroll(context.Background(), "remote-worker", enrollment, path); err != nil {
		t.Fatal(err)
	}
	stored, err := loadCredentialFile(path, server.URL)
	if err != nil || stored != firstCredential {
		t.Fatalf("recovered credential = %q, err %v; want %q", stored, err, firstCredential)
	}
	if _, err := os.Stat(path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending credential still exists after recovery: %v", err)
	}
}

func TestRemoteClientRemovesStalePendingCredentialAfterCompletedEnrollment(t *testing.T) {
	const credential = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("completed enrollment unexpectedly contacted the server")
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "runner-credential")
	if err := writeCredentialFile(path, server.URL, credential); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialFile(path+".pending", server.URL, credential); err != nil {
		t.Fatal(err)
	}
	client := newClient(server.URL, server.Client())
	client.credential = credential
	if err := client.enroll(context.Background(), "remote-worker", "", path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pending credential still exists: %v", err)
	}
}

func TestRunnerCredentialRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-credential")
	if err := os.WriteFile(path, []byte(`{"server":"https://factory.example.com:7443","credential":"factory_runner_secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentialFile(path, "https://factory.example.com:7443"); err == nil {
		t.Fatal("accepted broadly readable Runner credential")
	}
}
