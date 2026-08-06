package controlplane

import (
	"net/http"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHTTPDefinitionCreateEditArchiveLifecycle(t *testing.T) {
	fixture := newHTTPFixture(t)
	response := fixture.request(http.MethodPost, "/api/v1/definitions", "application/json", "",
		protocol.CreateDefinitionRequest{
			RequestKey: "http-definition", Name: "Review Code", Prompt: "Review the repository.",
			Runtime: protocol.RuntimeCodex, AllowedTools: []string{"git", "gh"},
			TimeoutSeconds: 3600, Inputs: map[string]string{"focus": "correctness"},
		})
	requireStatus(t, response, http.StatusCreated)
	created := decodeResponse[protocol.Definition](t, response)

	response = fixture.request(http.MethodGet, "/api/v1/definitions?limit=1", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	listed := decodeResponse[struct {
		Definitions []protocol.Definition `json:"definitions"`
		NextCursor  *string               `json:"next_cursor"`
	}](t, response)
	if len(listed.Definitions) != 1 || listed.Definitions[0].ID != created.ID || listed.NextCursor != nil {
		t.Fatalf("Definition list = %#v", listed)
	}

	response = fixture.request(http.MethodPut, "/api/v1/definitions/"+created.ID, "application/json", "",
		protocol.UpdateDefinitionRequest{
			RequestKey: "http-definition-update", ExpectedGeneration: created.Generation,
			Name: "Review Pull Request", Prompt: "Review the pull request and report findings.",
			Runtime: protocol.RuntimeClaudeCode, AllowedTools: []string{"gh"},
			TimeoutSeconds: 1800, Inputs: map[string]string{"focus": "security"},
		})
	requireStatus(t, response, http.StatusOK)
	updated := decodeResponse[protocol.Definition](t, response)
	if updated.Generation != 2 || updated.Name != "Review Pull Request" || updated.Runtime != protocol.RuntimeClaudeCode {
		t.Fatalf("updated Definition = %#v", updated)
	}

	response = fixture.request(http.MethodPut, "/api/v1/definitions/"+created.ID+"/archived", "application/json", "",
		protocol.SetDefinitionArchivedRequest{Archived: boolPointer(true), ExpectedGeneration: updated.Generation})
	requireStatus(t, response, http.StatusOK)
	archived := decodeResponse[protocol.Definition](t, response)
	if !archived.Archived || archived.Generation != 3 {
		t.Fatalf("archived Definition = %#v", archived)
	}

	response = fixture.request(http.MethodGet, "/api/v1/definitions", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	active := decodeResponse[struct {
		Definitions []protocol.Definition `json:"definitions"`
	}](t, response)
	if len(active.Definitions) != 0 {
		t.Fatalf("archived Definition remained active: %#v", active.Definitions)
	}
	response = fixture.request(http.MethodGet, "/api/v1/definitions?archived=true", "", "", nil)
	requireStatus(t, response, http.StatusOK)
	archivedPage := decodeResponse[struct {
		Definitions []protocol.Definition `json:"definitions"`
	}](t, response)
	if len(archivedPage.Definitions) != 1 || archivedPage.Definitions[0].ID != created.ID {
		t.Fatalf("archived Definition list = %#v", archivedPage.Definitions)
	}
}

func TestHTTPDefinitionRejectsUnknownFieldsAndStaleGeneration(t *testing.T) {
	fixture := newHTTPFixture(t)
	response := fixture.request(http.MethodPost, "/api/v1/definitions", "application/json", "", map[string]any{
		"request_key": "unknown-field", "name": "Review", "prompt": "Review.",
		"runtime": "codex", "timeout_seconds": 60, "revision": 1,
	})
	requireStatus(t, response, http.StatusBadRequest)

	created, _, err := fixture.store.CreateDefinition(t.Context(), protocol.CreateDefinitionRequest{
		RequestKey: "stale-http-create", Name: "Review", Prompt: "Review.",
		Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = fixture.request(http.MethodPut, "/api/v1/definitions/"+created.ID, "application/json", "",
		protocol.UpdateDefinitionRequest{
			RequestKey: "stale-http-update", ExpectedGeneration: created.Generation + 1,
			Name: "Stale", Prompt: "Stale edit.", Runtime: protocol.RuntimeCodex, TimeoutSeconds: 60,
		})
	requireStatus(t, response, http.StatusConflict)
	errorBody := decodeResponse[protocol.ErrorBody](t, response)
	if errorBody.Error.Code != "definition_generation_conflict" {
		t.Fatalf("stale Definition error = %#v", errorBody)
	}
}

func boolPointer(value bool) *bool { return &value }
