package controlplane

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAPIRouteContractIsCompleteAndUnique(t *testing.T) {
	wantFamilies := map[string]bool{
		"Health": false, "Workers": false, "Repositories": false,
		"Workflows": false, "Automations": false, "Legacy migration": false,
		"Metrics": false, "Tasks": false, "Executions": false,
		"Attempts and events": false,
	}
	operations := map[string]bool{}
	routes := map[string]bool{}
	for index, definition := range apiRouteDefinitions {
		contract := definition.RouteContract
		for field, value := range map[string]string{
			"family": contract.Family, "operation": contract.Operation,
			"method": contract.Method, "path": contract.Path,
			"summary": contract.Summary, "request": contract.Request,
			"response": contract.Response, "pagination": contract.Pagination,
			"errors": contract.Errors,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("route %d has empty %s", index, field)
			}
		}
		if definition.Handler == nil {
			t.Fatalf("route %s has no handler", contract.Operation)
		}
		for _, group := range []struct {
			description string
			schemas     []schemaRoot
		}{
			{description: contract.Request, schemas: definition.RequestSchemas},
			{description: contract.Response, schemas: definition.ResponseSchemas},
		} {
			for _, schema := range group.schemas {
				if schema.name == "" || schema.typeOf == nil {
					t.Fatalf("route %s has an invalid schema reference", contract.Operation)
				}
				if !strings.Contains(group.description, schema.name) {
					t.Fatalf("route %s metadata does not name referenced schema %s", contract.Operation, schema.name)
				}
			}
		}
		if _, known := wantFamilies[contract.Family]; !known {
			t.Fatalf("route %s has untested family %q", contract.Operation, contract.Family)
		}
		wantFamilies[contract.Family] = true
		if operations[contract.Operation] {
			t.Fatalf("duplicate operation %q", contract.Operation)
		}
		operations[contract.Operation] = true
		key := contract.Method + " " + contract.Path
		if routes[key] {
			t.Fatalf("duplicate route %q", key)
		}
		routes[key] = true
	}
	for family, covered := range wantFamilies {
		if !covered {
			t.Fatalf("route family %q has no contract check", family)
		}
	}
}

func TestCheckedInAPIContractMatchesRoutes(t *testing.T) {
	body, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(RenderAPIContract()) {
		t.Fatal("docs/api.md has drifted; run just api-contract")
	}
}

func TestCheckedInAPICompatibilityBaseline(t *testing.T) {
	body, err := os.ReadFile("../../docs/api-compat.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAPICompatibilityBaseline(body); err != nil {
		t.Fatal(err)
	}

	var baseline compatibilityContract
	if err := json.Unmarshal(body, &baseline); err != nil {
		t.Fatal(err)
	}
	workerRegistration, ok := baseline.Schemas["WorkerRegistrationRequest"]
	if !ok || !strings.Contains(workerRegistration, "codex_version: string | null (optional)") {
		t.Fatalf("worker registration wire schema does not track legacy codex_version: %q", workerRegistration)
	}
	for _, field := range []string{"runtime: string | null (optional)", "runtime_version: string | null (optional)"} {
		if !strings.Contains(workerRegistration, field) {
			t.Fatalf("worker registration wire schema does not track optional %s: %q", field, workerRegistration)
		}
	}
	for _, name := range []string{
		"SetManagedRepositoryEnabledRequest", "SetWorkflowEnabledRequest", "SetAutomationEnabledRequest",
	} {
		got := baseline.Schemas[name]
		if !strings.Contains(got, "\n  enabled: boolean\n") || strings.Contains(got, "enabled: boolean | null") {
			t.Fatalf("%s schema = %q, want a required non-null boolean", name, got)
		}
	}
	if got := baseline.Schemas["CreateTaskRequest"]; !strings.Contains(got, "timeout_seconds: integer (optional)") {
		t.Fatalf("CreateTaskRequest does not describe the defaulted timeout as optional: %q", got)
	}
	if got := baseline.Schemas["ListWorkersResponse"]; !strings.Contains(got, "workers: Worker[] | null") {
		t.Fatalf("ListWorkersResponse does not describe a nil slice as nullable: %q", got)
	}
	if got := baseline.Schemas["EmptyRequest"]; got != "{}" {
		t.Fatalf("EmptyRequest schema = %q, want {}", got)
	}

	var changed compatibilityContract
	if err := json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	for name, schema := range changed.Schemas {
		changed.Schemas[name] = schema + "\nremoved_field: string"
		break
	}
	encoded, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAPICompatibility(encoded); err == nil || !strings.Contains(err.Error(), "fields changed") {
		t.Fatalf("changed schema compatibility error = %v", err)
	}

	if err := json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	for key := range changed.Routes {
		delete(changed.Routes, key)
		break
	}
	encoded, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAPICompatibility(encoded); err != nil {
		t.Fatalf("compatible addition check = %v", err)
	}
	if err := CheckAPICompatibilityBaseline(encoded); err == nil || !strings.Contains(err.Error(), "missing current route") {
		t.Fatalf("incomplete baseline error = %v", err)
	}
}

func TestSensitiveAPIFamiliesDisableCaching(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, path := range []string{
		"/api/v1/tasks",
		"/api/v1/attempts/missing/events",
		"/api/v1/workflows",
		"/api/v1/automations",
	} {
		response := fixture.request(http.MethodGet, path, "", "", nil)
		if got := response.Header.Get("Cache-Control"); got != "no-store" {
			response.Body.Close()
			t.Fatalf("%s Cache-Control = %q, want no-store", path, got)
		}
		response.Body.Close()
	}
}

func TestEmptyClaimResponseDisablesCaching(t *testing.T) {
	fixture := newHTTPFixture(t)
	registerHTTPWorker(t, fixture, "idle-worker", "factory", "github.com/owainlewis/factory", 1)
	response := fixture.request(http.MethodPost, "/api/v1/workers/idle-worker/claims", "application/json", "", map[string]string{
		"request_id":  "empty-claim",
		"lease_token": strings.Repeat("a", 32),
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("claim status = %d, want 204", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("claim Cache-Control = %q, want no-store", got)
	}
}

func TestAPIRoutingErrorsUseContractErrorShape(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{name: "not found", method: http.MethodGet, path: "/api/v1/not-real", wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "method not allowed", method: http.MethodPut, path: "/api/v1/tasks", wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed", wantAllow: "GET, HEAD, POST"},
		{name: "health method not allowed", method: http.MethodPost, path: "/healthz", wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed", wantAllow: "GET, HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.request(test.method, test.path, "application/json", "", map[string]any{})
			if response.StatusCode != test.wantStatus {
				response.Body.Close()
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if got := response.Header.Get("Content-Type"); got != "application/json" {
				response.Body.Close()
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				response.Body.Close()
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := response.Header.Get("Allow"); got != test.wantAllow {
				response.Body.Close()
				t.Fatalf("Allow = %q, want %q", got, test.wantAllow)
			}
			body := decodeResponse[struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}](t, response)
			if body.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", body.Error.Code, test.wantCode)
			}
		})
	}
}
