package controlplane

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHTTPRunOnceLifecycle(t *testing.T) {
	fixture := newHTTPFixture(t)
	repository := createManagedTestRepository(t, fixture.store, "github.com/example/http-run")
	definition := createTestDefinition(t, fixture.store, "http-run-definition", "Review Repository")
	registerDefinitionWorker(
		t, fixture.store, workerA,
		protocol.RepositoryRegistration{Key: "http-run", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	response := fixture.request(http.MethodGet, "/api/v1/run-repositories", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	choices := decodeResponse[struct {
		Repositories []protocol.RunRepository `json:"repositories"`
	}](t, response)
	if len(choices.Repositories) != 1 || choices.Repositories[0].ID != repository.ID {
		t.Fatalf("Run repository choices = %#v", choices.Repositories)
	}

	response = fixture.request(http.MethodPost, "/api/v1/runs", "application/json", "", protocol.CreateRunRequest{
		RequestKey: "http-manual-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
		Parameters: map[string]string{"severity": "critical"},
	})
	requireStatus(t, response, http.StatusCreated)
	created := decodeResponse[protocol.RunDetail](t, response)
	if created.Run.State != "queued" || len(created.Jobs) != 1 || created.Jobs[0].Job.State != "queued" {
		t.Fatalf("created Run = %#v", created)
	}

	response = fixture.request(http.MethodPost, "/api/v1/runs", "application/json", "", protocol.CreateRunRequest{
		RequestKey: "http-manual-run", DefinitionID: definition.ID, RepositoryID: repository.ID,
		Parameters: map[string]string{"severity": "critical"},
	})
	requireStatus(t, response, http.StatusOK)
	replayed := decodeResponse[protocol.RunDetail](t, response)
	if replayed.Run.ID != created.Run.ID {
		t.Fatalf("replayed Run = %#v", replayed.Run)
	}

	response = fixture.request(http.MethodGet, "/api/v1/runs", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	page := decodeResponse[protocol.RunPage](t, response)
	if len(page.Runs) != 1 || page.Runs[0].ID != created.Run.ID {
		t.Fatalf("Run list = %#v", page)
	}

	response = fixture.request(http.MethodGet, "/api/v1/runs/"+created.Run.ID, "", "", nil)
	requireStatus(t, response, http.StatusOK)
	loaded := decodeResponse[protocol.RunDetail](t, response)
	if loaded.Run.Definition.ID != definition.ID || loaded.Jobs[0].Job.RepositoryID != repository.ID {
		t.Fatalf("loaded Run = %#v", loaded)
	}

	response = fixture.request(http.MethodPost, "/api/v1/jobs/"+created.Jobs[0].Job.ID+"/cancel", "application/json", "", struct{}{})
	requireStatus(t, response, http.StatusOK)
	cancelled := decodeResponse[protocol.RunDetail](t, response)
	if cancelled.Run.State != "cancelled" || cancelled.Jobs[0].Job.State != "cancelled" {
		t.Fatalf("cancelled Run = %#v", cancelled)
	}
}

func TestHTTPRunRejectsUnknownFields(t *testing.T) {
	fixture := newHTTPFixture(t)
	response := fixture.request(http.MethodPost, "/api/v1/runs", "application/json", "", map[string]any{
		"request_key": "unknown-run-field", "definition_id": "definition", "repository_id": "repository",
		"repositories": []string{"repository"},
	})
	requireStatus(t, response, http.StatusBadRequest)
}

func TestHTTPRunHistoryExposesTheNextPageCursor(t *testing.T) {
	fixture := newHTTPFixture(t)
	repository := createManagedTestRepository(t, fixture.store, "github.com/example/http-run-pages")
	definition := createTestDefinition(t, fixture.store, "http-run-pages-definition", "Review Pages")
	registerDefinitionWorker(
		t, fixture.store, workerA,
		protocol.RepositoryRegistration{Key: "http-run-pages", RemoteIdentity: repository.RemoteIdentity},
		protocol.CapabilityReady,
		[]protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	)
	for _, requestKey := range []string{"http-run-page-one", "http-run-page-two"} {
		response := fixture.request(http.MethodPost, "/api/v1/runs", "application/json", "", protocol.CreateRunRequest{
			RequestKey: requestKey, DefinitionID: definition.ID, RepositoryID: repository.ID,
		})
		requireStatus(t, response, http.StatusCreated)
	}
	type pageResponse struct {
		Runs       []protocol.Run `json:"runs"`
		NextCursor *string        `json:"next_cursor"`
	}
	response := fixture.request(http.MethodGet, "/api/v1/runs?limit=1", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	first := decodeResponse[pageResponse](t, response)
	if len(first.Runs) != 1 || first.NextCursor == nil {
		t.Fatalf("first HTTP Run page = %#v", first)
	}
	response = fixture.request(
		http.MethodGet, "/api/v1/runs?limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", nil,
	)
	requireStatus(t, response, http.StatusOK)
	second := decodeResponse[pageResponse](t, response)
	if len(second.Runs) != 1 || second.NextCursor != nil || second.Runs[0].ID == first.Runs[0].ID {
		t.Fatalf("second HTTP Run page = %#v after %#v", second, first)
	}
}
