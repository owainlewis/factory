package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRemoteClientEnrollsOnceAndPersistsCredential(t *testing.T) {
	const credential = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runner-enrollments/exchange":
			var input protocol.ExchangeRunnerEnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.WorkerID != "remote-worker" || input.EnrollmentToken != enrollment {
				t.Errorf("exchange input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(protocol.RunnerCredential{Credential: credential})
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

func TestRunnerCredentialRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-credential")
	if err := os.WriteFile(path, []byte(`{"server":"https://factory.example.com:7443","credential":"factory_runner_secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentialFile(path, "https://factory.example.com:7443"); err == nil {
		t.Fatal("accepted broadly readable Runner credential")
	}
}
