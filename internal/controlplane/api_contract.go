package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// RouteContract is the public HTTP contract consumed by both the router and
// the generated API inventory.
type RouteContract struct {
	Family     string
	Operation  string
	Method     string
	Path       string
	Summary    string
	Request    string
	Response   string
	Pagination string
	Errors     string
}

type apiHandler func(*API, http.ResponseWriter, *http.Request)

type apiRouteDefinition struct {
	RouteContract
	Handler         apiHandler
	RequestSchemas  []schemaRoot
	ResponseSchemas []schemaRoot
}

type bodyContract struct {
	description string
	schemas     []schemaRoot
}

// workerRegistrationContract describes the accepted JSON wire shape. The
// decoder keeps presence information separately so it can distinguish legacy
// codex_version requests from runtime-aware registrations.
type workerRegistrationContract struct {
	Name                       string                            `json:"name"`
	WorkerVersion              string                            `json:"worker_version"`
	Runtime                    *string                           `json:"runtime,omitempty"`
	RuntimeVersion             *string                           `json:"runtime_version,omitempty"`
	CodexVersion               *string                           `json:"codex_version,omitempty"`
	Capacity                   int                               `json:"capacity"`
	ActiveCount                int                               `json:"active_count"`
	Health                     string                            `json:"health"`
	Repositories               []protocol.RepositoryRegistration `json:"repositories"`
	SourceAccess               []protocol.SourceAccess           `json:"source_access,omitempty"`
	AcceptsManagedRepositories bool                              `json:"accepts_managed_repositories,omitempty"`
	ManagedRepositoryIDs       []string                          `json:"managed_repository_ids,omitempty"`
	RetainedWorktrees          []protocol.RetainedWorktree       `json:"retained_worktrees"`
	CapacityHandoffVersion     int                               `json:"capacity_handoff_version,omitempty"`
	DisposedAttemptIDs         []string                          `json:"disposed_attempt_ids,omitempty"`
	WeeklyLimit                *protocol.WeeklyLimit             `json:"weekly_limit,omitempty"`
}

type listWorkersResponse struct {
	Workers []protocol.Worker `json:"workers"`
}

type listManagedRepositoriesResponse struct {
	Repositories []protocol.ManagedRepository `json:"repositories"`
}

type listWorkflowsResponse struct {
	Workflows  []protocol.Workflow `json:"workflows"`
	NextCursor *string             `json:"next_cursor"`
}

type listAutomationsResponse struct {
	Automations []protocol.Automation `json:"automations"`
	NextCursor  *string               `json:"next_cursor"`
}

type listAutomationOccurrencesResponse struct {
	Occurrences []protocol.AutomationOccurrence `json:"occurrences"`
	NextCursor  *string                         `json:"next_cursor"`
}

type listTasksResponse struct {
	Tasks      []protocol.Task `json:"tasks"`
	NextCursor *string         `json:"next_cursor"`
}

type listAttemptEventsResponse struct {
	Events    []protocol.AttemptEvent `json:"events"`
	NextAfter int64                   `json:"next_after"`
	HasMore   bool                    `json:"has_more"`
}

func body(description string, schemas ...schemaRoot) bodyContract {
	return bodyContract{description: description, schemas: schemas}
}

func schema[T any]() schemaRoot {
	typeOf := reflect.TypeOf((*T)(nil)).Elem()
	return schemaRoot{name: typeOf.Name(), typeOf: typeOf}
}

func namedSchema(name string, value any) schemaRoot {
	return schemaRoot{name: name, typeOf: reflect.TypeOf(value)}
}

func route(
	family, operation, method, path, summary string,
	request, response bodyContract,
	pagination string,
	handler apiHandler,
) apiRouteDefinition {
	return apiRouteDefinition{
		RouteContract: RouteContract{
			Family: family, Operation: operation, Method: method, Path: path,
			Summary: summary, Request: request.description, Response: response.description,
			Pagination: pagination, Errors: "ErrorBody: {error: {code: string, message: string}}",
		},
		Handler:         handler,
		RequestSchemas:  request.schemas,
		ResponseSchemas: response.schemas,
	}
}

var apiRouteDefinitions = []apiRouteDefinition{
	route("Health", "health", "GET", "/healthz", "Check SQLite availability.", body("none"), body(`200 {status: "ok"}`), "none", (*API).health),

	route("Workers", "registerWorker", "PUT", "/api/v1/workers/{worker_id}", "Register or heartbeat a worker.", body("WorkerRegistrationRequest JSON; legacy codex_version is accepted only without runtime fields", namedSchema("WorkerRegistrationRequest", workerRegistrationContract{})), body("200 Worker JSON; legacy requests receive LegacyWorkerResponse JSON", schema[protocol.Worker](), namedSchema("LegacyWorkerResponse", legacyWorkerResponse{})), "none", (*API).registerWorker),
	route("Workers", "claim", "POST", "/api/v1/workers/{worker_id}/claims", "Claim the next eligible execution.", body("ClaimRequest JSON", schema[protocol.ClaimRequest]()), body("200 Claim JSON or 204 with no body", schema[protocol.Claim]()), "none", (*API).claim),
	route("Workers", "listWorkers", "GET", "/api/v1/workers", "List workers.", body("none"), body("200 ListWorkersResponse JSON", namedSchema("ListWorkersResponse", listWorkersResponse{})), "none", (*API).listWorkers),
	route("Workers", "getWorker", "GET", "/api/v1/workers/{worker_id}", "Get one worker.", body("none"), body("200 Worker JSON", schema[protocol.Worker]()), "none", (*API).getWorker),

	route("Repositories", "listManagedRepositories", "GET", "/api/v1/repositories", "List managed repositories.", body("none"), body("200 ListManagedRepositoriesResponse JSON", namedSchema("ListManagedRepositoriesResponse", listManagedRepositoriesResponse{})), "none", (*API).listManagedRepositories),
	route("Repositories", "createManagedRepository", "POST", "/api/v1/repositories", "Create or replay a managed repository.", body("CreateManagedRepositoryRequest JSON", schema[protocol.CreateManagedRepositoryRequest]()), body("200 or 201 ManagedRepository JSON", schema[protocol.ManagedRepository]()), "none", (*API).createManagedRepository),
	route("Repositories", "getManagedRepository", "GET", "/api/v1/repositories/{repository_id}", "Get one managed repository.", body("none"), body("200 ManagedRepository JSON", schema[protocol.ManagedRepository]()), "none", (*API).getManagedRepository),
	route("Repositories", "getManagedRepositoryReadiness", "GET", "/api/v1/repositories/{repository_id}/readiness", "Inspect worker readiness for a repository.", body("none"), body("200 ManagedRepositoryReadiness JSON", schema[protocol.ManagedRepositoryReadiness]()), "none", (*API).getManagedRepositoryReadiness),
	route("Repositories", "setManagedRepositoryEnabled", "PUT", "/api/v1/repositories/{repository_id}/enabled", "Enable or disable a repository.", body("SetManagedRepositoryEnabledRequest JSON", schema[protocol.SetManagedRepositoryEnabledRequest]()), body("200 ManagedRepository JSON", schema[protocol.ManagedRepository]()), "none", (*API).setManagedRepositoryEnabled),

	route("Workflows", "listWorkflows", "GET", "/api/v1/workflows", "List and filter workflows.", body("none"), body("200 ListWorkflowsResponse JSON", namedSchema("ListWorkflowsResponse", listWorkflowsResponse{})), "Query: title, enabled, limit 1..200, opaque cursor; stable updated-at/ID ordering", (*API).listWorkflows),
	route("Workflows", "createWorkflow", "POST", "/api/v1/workflows", "Create or replay a workflow.", body("CreateWorkflowRequest JSON", schema[protocol.CreateWorkflowRequest]()), body("200 or 201 WorkflowDetail JSON", schema[protocol.WorkflowDetail]()), "none", (*API).createWorkflow),
	route("Workflows", "getWorkflow", "GET", "/api/v1/workflows/{workflow_id}", "Get workflow revisions and current state.", body("none"), body("200 WorkflowDetail JSON", schema[protocol.WorkflowDetail]()), "none", (*API).getWorkflow),
	route("Workflows", "createWorkflowRevision", "POST", "/api/v1/workflows/{workflow_id}/revisions", "Create or replay a workflow revision.", body("CreateWorkflowRevisionRequest JSON", schema[protocol.CreateWorkflowRevisionRequest]()), body("200 or 201 WorkflowDetail JSON", schema[protocol.WorkflowDetail]()), "none", (*API).createWorkflowRevision),
	route("Workflows", "setWorkflowEnabled", "PUT", "/api/v1/workflows/{workflow_id}/enabled", "Enable or disable a workflow.", body("SetWorkflowEnabledRequest JSON", schema[protocol.SetWorkflowEnabledRequest]()), body("200 WorkflowDetail JSON", schema[protocol.WorkflowDetail]()), "none", (*API).setWorkflowEnabled),

	route("Automations", "listAutomations", "GET", "/api/v1/automations", "List automations.", body("none"), body("200 ListAutomationsResponse JSON", namedSchema("ListAutomationsResponse", listAutomationsResponse{})), "Query: limit 1..200 and opaque cursor; stable updated-at/ID ordering", (*API).listAutomations),
	route("Automations", "createAutomation", "POST", "/api/v1/automations", "Create or replay an automation.", body("CreateAutomationRequest JSON", schema[protocol.CreateAutomationRequest]()), body("200 or 201 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).createAutomation),
	route("Automations", "getAutomation", "GET", "/api/v1/automations/{automation_id}", "Get one automation.", body("none"), body("200 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).getAutomation),
	route("Automations", "updateAutomation", "PUT", "/api/v1/automations/{automation_id}", "Update an automation with optimistic versioning.", body("UpdateAutomationRequest JSON", schema[protocol.UpdateAutomationRequest]()), body("200 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).updateAutomation),
	route("Automations", "setAutomationEnabled", "PUT", "/api/v1/automations/{automation_id}/enabled", "Enable or disable an automation.", body("SetAutomationEnabledRequest JSON", schema[protocol.SetAutomationEnabledRequest]()), body("200 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).setAutomationEnabled),
	route("Automations", "testAutomation", "POST", "/api/v1/automations/{automation_id}/test", "Test an automation without dispatch.", body("empty JSON object"), body("200 TestAutomationResult JSON", schema[protocol.TestAutomationResult]()), "none", (*API).testAutomation),
	route("Automations", "checkAutomation", "POST", "/api/v1/automations/{automation_id}/check", "Request an immediate provider check.", body("empty JSON object"), body("202 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).checkAutomation),
	route("Automations", "runAutomation", "POST", "/api/v1/automations/{automation_id}/run", "Run a schedule automation now.", body("RunAutomationRequest JSON", schema[protocol.RunAutomationRequest]()), body("202 AutomationDetail JSON", schema[protocol.AutomationDetail]()), "none", (*API).runAutomation),
	route("Automations", "listAutomationOccurrences", "GET", "/api/v1/automations/{automation_id}/occurrences", "List retained automation occurrences.", body("none"), body("200 ListAutomationOccurrencesResponse JSON", namedSchema("ListAutomationOccurrencesResponse", listAutomationOccurrencesResponse{})), "Query: limit 1..200 and opaque cursor; stable created-at/ID ordering", (*API).listAutomationOccurrences),

	route("Legacy migration", "previewLegacyPoller", "POST", "/api/v1/migrations/legacy-poller/preview", "Preview a locked legacy poller snapshot.", body("PreviewLegacyPollerRequest JSON", schema[protocol.PreviewLegacyPollerRequest]()), body("201 LegacyPollerMigration JSON", schema[protocol.LegacyPollerMigration]()), "none", (*API).previewLegacyPoller),
	route("Legacy migration", "importLegacyPoller", "POST", "/api/v1/migrations/legacy-poller/import", "Import a reviewed legacy poller snapshot.", body("ImportLegacyPollerRequest JSON", schema[protocol.ImportLegacyPollerRequest]()), body("200 LegacyPollerMigration JSON", schema[protocol.LegacyPollerMigration]()), "none", (*API).importLegacyPoller),
	route("Legacy migration", "activeLegacyPollerMigration", "GET", "/api/v1/migrations/legacy-poller/active", "Get the active legacy migration.", body("none"), body("200 ActiveLegacyPollerMigrationResponse JSON", schema[protocol.ActiveLegacyPollerMigrationResponse]()), "none", (*API).activeLegacyPollerMigration),
	route("Legacy migration", "getLegacyPollerMigration", "GET", "/api/v1/migrations/legacy-poller/{migration_id}", "Get one legacy migration.", body("none"), body("200 LegacyPollerMigration JSON", schema[protocol.LegacyPollerMigration]()), "none", (*API).getLegacyPollerMigration),
	route("Legacy migration", "finalizeLegacyPoller", "POST", "/api/v1/migrations/legacy-poller/{migration_id}/finalize", "Finalize and archive a legacy migration.", body("FinalizeLegacyPollerRequest JSON", schema[protocol.FinalizeLegacyPollerRequest]()), body("200 LegacyPollerMigration JSON", schema[protocol.LegacyPollerMigration]()), "none", (*API).finalizeLegacyPoller),
	route("Legacy migration", "resumeLegacyPollerOccurrence", "POST", "/api/v1/occurrences/{occurrence_id}/resume", "Resume one pending legacy occurrence.", body("empty JSON object"), body("200 AutomationOccurrence JSON", schema[protocol.AutomationOccurrence]()), "none", (*API).resumeLegacyPollerOccurrence),
	route("Legacy migration", "skipLegacyPollerOccurrence", "POST", "/api/v1/occurrences/{occurrence_id}/skip", "Skip one pending legacy occurrence.", body("empty JSON object"), body("200 AutomationOccurrence JSON", schema[protocol.AutomationOccurrence]()), "none", (*API).skipLegacyPollerOccurrence),

	route("Metrics", "getMetrics", "GET", "/api/v1/metrics/summary", "Get bounded execution metrics.", body("none"), body("200 MetricsSummary JSON", schema[protocol.MetricsSummary]()), "Query: window is 24h, 7d, 30d, or all; default 7d", (*API).getMetrics),

	route("Tasks", "listTasks", "GET", "/api/v1/tasks", "List retained task summaries.", body("none"), body("200 ListTasksResponse JSON", namedSchema("ListTasksResponse", listTasksResponse{})), "Query: limit 1..200 and opaque cursor; stable created-at/ID ordering", (*API).listTasks),
	route("Tasks", "createTask", "POST", "/api/v1/tasks", "Create or replay a task.", body("CreateTaskRequest JSON", schema[protocol.CreateTaskRequest]()), body("200 or 201 TaskDetail JSON", schema[protocol.TaskDetail]()), "none", (*API).createTask),
	route("Tasks", "getTask", "GET", "/api/v1/tasks/{task_id}", "Get task, execution, attempts, and resolved prompt.", body("none"), body("200 TaskDetail JSON", schema[protocol.TaskDetail]()), "none", (*API).getTask),
	route("Tasks", "deleteTask", "DELETE", "/api/v1/tasks/{task_id}", "Delete eligible terminal task history.", body("empty JSON object or empty body"), body(`200 {deleted: true}`), "none", (*API).deleteTask),
	route("Tasks", "cancelTask", "POST", "/api/v1/tasks/{task_id}/cancel", "Request task cancellation.", body("empty JSON object or empty body"), body("200 TaskDetail JSON", schema[protocol.TaskDetail]()), "none", (*API).cancelTask),
	route("Executions", "retryExecution", "POST", "/api/v1/executions/{execution_id}/retry", "Retry a terminal execution.", body("empty JSON object or empty body"), body("200 TaskDetail JSON", schema[protocol.TaskDetail]()), "none", (*API).retryExecution),

	route("Attempts and events", "getAttempt", "GET", "/api/v1/attempts/{attempt_id}", "Get one attempt.", body("none"), body("200 Attempt JSON", schema[protocol.Attempt]()), "none", (*API).getAttempt),
	route("Attempts and events", "startAttempt", "POST", "/api/v1/attempts/{attempt_id}/start", "Record attempt startup and worktree metadata.", body("StartAttemptRequest JSON", schema[protocol.StartAttemptRequest]()), body("200 Attempt JSON", schema[protocol.Attempt]()), "none", (*API).startAttempt),
	route("Attempts and events", "heartbeat", "PUT", "/api/v1/attempts/{attempt_id}/heartbeat", "Renew an attempt lease.", body("LeaseRequest JSON", schema[protocol.LeaseRequest]()), body("200 HeartbeatResponse JSON", schema[protocol.HeartbeatResponse]()), "none", (*API).heartbeat),
	route("Attempts and events", "getEvents", "GET", "/api/v1/attempts/{attempt_id}/events", "Read an attempt event page.", body("none"), body("200 ListAttemptEventsResponse JSON", namedSchema("ListAttemptEventsResponse", listAttemptEventsResponse{})), "Query: after >= -1 and limit 1..500; next page starts after next_after", (*API).getEvents),
	route("Attempts and events", "appendEvents", "POST", "/api/v1/attempts/{attempt_id}/events", "Append an ordered event batch.", body("EventBatchRequest JSON", schema[protocol.EventBatchRequest]()), body("204 with no body"), "none", (*API).appendEvents),
	route("Attempts and events", "completeAttempt", "POST", "/api/v1/attempts/{attempt_id}/complete", "Complete an attempt.", body("CompleteAttemptRequest JSON", schema[protocol.CompleteAttemptRequest]()), body("200 Attempt JSON", schema[protocol.Attempt]()), "none", (*API).completeAttempt),
}

// RouteContracts returns a copy so documentation tools cannot change routing.
func RouteContracts() []RouteContract {
	contracts := make([]RouteContract, len(apiRouteDefinitions))
	for index, definition := range apiRouteDefinitions {
		contracts[index] = definition.RouteContract
	}
	return contracts
}

// RenderAPIContract produces the checked-in route inventory.
func RenderAPIContract() []byte {
	var output bytes.Buffer
	output.WriteString("# HTTP API contract\n\n")
	output.WriteString("This file is generated from the route definitions used by the server. ")
	output.WriteString("Run `just api-contract` after changing a route. CI rejects drift.\n\n")
	output.WriteString("All request and response bodies are JSON unless the response is `204`. ")
	output.WriteString("Named shapes correspond to JSON-tagged types in `internal/protocol/types.go`. ")
	output.WriteString("Unknown request fields are rejected. JSON responses use `Cache-Control: no-store`.\n\n")
	output.WriteString("Every error body has this shape:\n\n")
	output.WriteString("```json\n{\"error\":{\"code\":\"stable_machine_code\",\"message\":\"human-readable message\"}}\n```\n\n")
	currentFamily := ""
	for _, contract := range RouteContracts() {
		if contract.Family != currentFamily {
			if currentFamily != "" {
				output.WriteByte('\n')
			}
			currentFamily = contract.Family
			fmt.Fprintf(&output, "## %s\n\n", currentFamily)
			output.WriteString("| Method and path | Operation | Request | Success response | Pagination | Errors |\n")
			output.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		}
		fmt.Fprintf(
			&output,
			"| `%s %s` | %s: %s | %s | %s | %s | %s |\n",
			contract.Method,
			contract.Path,
			markdownCell(contract.Operation),
			markdownCell(contract.Summary),
			markdownCell(contract.Request),
			markdownCell(contract.Response),
			markdownCell(contract.Pagination),
			markdownCell(contract.Errors),
		)
	}
	output.WriteString("\n## JSON shapes\n\n")
	output.WriteString("These field inventories are generated from the JSON-tagged Go types. ")
	output.WriteString("A field marked optional may be omitted; `null` is stated separately.\n\n")
	for _, schema := range apiSchemas() {
		fmt.Fprintf(&output, "### %s\n\n```text\n%s\n```\n\n", schema.name, schema.body)
	}
	return append(bytes.TrimRight(output.Bytes(), "\n"), '\n')
}

type compatibilityContract struct {
	Version int                           `json:"version"`
	Routes  map[string]compatibilityRoute `json:"routes"`
	Schemas map[string]string             `json:"schemas"`
}

type compatibilityRoute struct {
	Operation       string   `json:"operation"`
	Request         string   `json:"request"`
	RequestSchemas  []string `json:"request_schemas,omitempty"`
	Response        string   `json:"response"`
	ResponseSchemas []string `json:"response_schemas,omitempty"`
	Pagination      string   `json:"pagination"`
	Errors          string   `json:"errors"`
}

// RenderAPICompatibilityBaseline returns the conservative, reviewable baseline
// used to reject route removal and any change to an existing API shape.
func RenderAPICompatibilityBaseline() []byte {
	encoded, err := json.MarshalIndent(currentCompatibilityContract(), "", "  ")
	if err != nil {
		panic(err)
	}
	return append(encoded, '\n')
}

// CheckAPICompatibility rejects changes to existing routes and schema fields.
// New routes and schemas remain compatible.
func CheckAPICompatibility(encoded []byte) error {
	var baseline compatibilityContract
	if err := json.Unmarshal(encoded, &baseline); err != nil {
		return fmt.Errorf("decode API compatibility baseline: %w", err)
	}
	if baseline.Version != 1 {
		return fmt.Errorf("unsupported API compatibility baseline version %d", baseline.Version)
	}
	current := currentCompatibilityContract()
	for key, previous := range baseline.Routes {
		next, exists := current.Routes[key]
		if !exists {
			return fmt.Errorf("incompatible API change: route %s was removed", key)
		}
		if !reflect.DeepEqual(previous, next) {
			return fmt.Errorf("incompatible API change: route %s metadata or schema references changed", key)
		}
	}
	for name, previous := range baseline.Schemas {
		next, exists := current.Schemas[name]
		if !exists {
			return fmt.Errorf("incompatible API change: schema %s was removed", name)
		}
		if previous != next {
			return fmt.Errorf("incompatible API change: schema %s fields changed", name)
		}
	}
	return nil
}

// CheckAPICompatibilityBaseline also requires the baseline to contain every
// current route and schema. This ratchets compatible additions into protection.
func CheckAPICompatibilityBaseline(encoded []byte) error {
	if err := CheckAPICompatibility(encoded); err != nil {
		return err
	}
	var baseline compatibilityContract
	if err := json.Unmarshal(encoded, &baseline); err != nil {
		return fmt.Errorf("decode API compatibility baseline: %w", err)
	}
	current := currentCompatibilityContract()
	for key := range current.Routes {
		if _, exists := baseline.Routes[key]; !exists {
			return fmt.Errorf("API compatibility baseline is missing current route %s", key)
		}
	}
	for name := range current.Schemas {
		if _, exists := baseline.Schemas[name]; !exists {
			return fmt.Errorf("API compatibility baseline is missing current schema %s", name)
		}
	}
	return nil
}

func currentCompatibilityContract() compatibilityContract {
	contract := compatibilityContract{
		Version: 1,
		Routes:  make(map[string]compatibilityRoute, len(apiRouteDefinitions)),
		Schemas: map[string]string{},
	}
	for _, definition := range apiRouteDefinitions {
		key := definition.Method + " " + definition.Path
		contract.Routes[key] = compatibilityRoute{
			Operation: definition.Operation, Request: definition.Request,
			RequestSchemas:  schemaNames(definition.RequestSchemas),
			Response:        definition.Response,
			ResponseSchemas: schemaNames(definition.ResponseSchemas),
			Pagination:      definition.Pagination, Errors: definition.Errors,
		}
	}
	for _, schema := range apiSchemas() {
		contract.Schemas[schema.name] = schema.body
	}
	return contract
}

func schemaNames(schemas []schemaRoot) []string {
	if len(schemas) == 0 {
		return nil
	}
	names := make([]string, len(schemas))
	for index, schema := range schemas {
		names[index] = schema.name
	}
	return names
}

func markdownCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

type schemaRoot struct {
	name   string
	typeOf reflect.Type
}

type renderedSchema struct {
	name string
	body string
}

func apiSchemas() []renderedSchema {
	types := map[string]reflect.Type{}
	aliases := map[string]reflect.Type{}
	roots := []schemaRoot{{name: "ErrorBody", typeOf: reflect.TypeOf(protocol.ErrorBody{})}}
	for _, definition := range apiRouteDefinitions {
		roots = append(roots, definition.RequestSchemas...)
		roots = append(roots, definition.ResponseSchemas...)
	}
	for _, root := range roots {
		aliases[root.name] = root.typeOf
		collectSchemaTypes(root.typeOf, types)
	}
	for name, typeOf := range aliases {
		types[name] = typeOf
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	schemas := make([]renderedSchema, 0, len(names))
	for _, name := range names {
		schemas = append(schemas, renderedSchema{name: name, body: renderSchemaType(types[name])})
	}
	return schemas
}

func collectSchemaTypes(typeOf reflect.Type, types map[string]reflect.Type) {
	for typeOf.Kind() == reflect.Pointer || typeOf.Kind() == reflect.Slice || typeOf.Kind() == reflect.Array {
		typeOf = typeOf.Elem()
	}
	if typeOf == reflect.TypeOf(time.Time{}) || typeOf == reflect.TypeOf(json.RawMessage{}) || typeOf.Kind() != reflect.Struct {
		return
	}
	if typeOf == reflect.TypeOf(protocol.AutomationTrigger{}) {
		for _, concrete := range []reflect.Type{
			reflect.TypeOf(protocol.GitHubIssueTrigger{}),
			reflect.TypeOf(protocol.GitHubPullRequestTrigger{}),
			reflect.TypeOf(protocol.ScheduleTrigger{}),
		} {
			collectSchemaTypes(concrete, types)
		}
	}
	if name := typeOf.Name(); name != "" && name[0] >= 'A' && name[0] <= 'Z' {
		if _, exists := types[name]; exists {
			return
		}
		types[name] = typeOf
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" || strings.Split(field.Tag.Get("json"), ",")[0] == "-" {
			continue
		}
		collectSchemaTypes(field.Type, types)
	}
}

func renderSchemaType(typeOf reflect.Type) string {
	if typeOf == reflect.TypeOf(protocol.AutomationTrigger{}) {
		return "GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger"
	}
	var lines []string
	renderSchemaFields(typeOf, &lines)
	if len(lines) == 0 {
		return "{}"
	}
	return "{\n  " + strings.Join(lines, "\n  ") + "\n}"
}

func renderSchemaFields(typeOf reflect.Type, lines *[]string) {
	for typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		if tag[0] == "-" {
			continue
		}
		if field.Anonymous && tag[0] == "" {
			renderSchemaFields(field.Type, lines)
			continue
		}
		name := tag[0]
		if name == "" {
			name = field.Name
		}
		optional := false
		for _, option := range tag[1:] {
			optional = optional || option == "omitempty"
		}
		description := name + ": " + schemaTypeName(field.Type)
		if optional {
			description += " (optional)"
		}
		*lines = append(*lines, description)
	}
}

func schemaTypeName(typeOf reflect.Type) string {
	if typeOf.Kind() == reflect.Pointer {
		return schemaTypeName(typeOf.Elem()) + " | null"
	}
	if typeOf == reflect.TypeOf(time.Time{}) {
		return "string (RFC3339 timestamp)"
	}
	if typeOf == reflect.TypeOf(json.RawMessage{}) {
		return "any JSON value"
	}
	switch typeOf.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		return schemaTypeName(typeOf.Elem()) + "[]"
	case reflect.Map:
		return "object<string," + schemaTypeName(typeOf.Elem()) + ">"
	case reflect.Struct:
		if typeOf == reflect.TypeOf(protocol.AutomationTrigger{}) {
			return "GitHubIssueTrigger | GitHubPullRequestTrigger | ScheduleTrigger"
		}
		if typeOf.Name() != "" {
			return typeOf.Name()
		}
		return "object"
	default:
		return "any JSON value"
	}
}
