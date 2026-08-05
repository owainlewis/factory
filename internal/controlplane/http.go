package controlplane

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type API struct {
	store       *Store
	logger      *slog.Logger
	automations *AutomationService
}

type workerRegistrationRequest struct {
	protocol.WorkerRegistration
	Runtime        registrationString `json:"runtime"`
	RuntimeVersion registrationString `json:"runtime_version"`
	CodexVersion   registrationString `json:"codex_version"`
}

type registrationString struct {
	Value   string
	Present bool
}

func (value *registrationString) UnmarshalJSON(body []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil
	}
	return json.Unmarshal(body, &value.Value)
}

type legacyWorkerResponse struct {
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

func NewHandler(store *Store, logger *slog.Logger) http.Handler {
	return NewHandlerWithAutomation(store, logger, NewAutomationService(store, logger))
}

func NewHandlerWithAutomation(store *Store, logger *slog.Logger, automations *AutomationService) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	api := &API{store: store, logger: logger, automations: automations}
	mux := http.NewServeMux()
	for _, route := range apiRouteDefinitions {
		route := route
		mux.HandleFunc(route.Method+" "+route.Path, func(w http.ResponseWriter, r *http.Request) {
			route.Handler(api, w, r)
		})
	}
	mux.HandleFunc("/healthz", api.apiRouteFallback)
	mux.HandleFunc("/api", api.apiRouteFallback)
	mux.HandleFunc("/api/", api.apiRouteFallback)
	return api.requestLog(mux)
}

func (a *API) apiRouteFallback(w http.ResponseWriter, r *http.Request) {
	allowed := make([]string, 0, 1)
	for _, route := range apiRouteDefinitions {
		if contractPathMatches(route.Path, r.URL.Path) {
			allowed = append(allowed, route.Method)
			if route.Method == http.MethodGet {
				allowed = append(allowed, http.MethodHead)
			}
		}
	}
	if len(allowed) > 0 {
		slices.Sort(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, &ServiceError{Code: "method_not_allowed", Message: "method is not allowed for this API route", Status: http.StatusMethodNotAllowed})
		return
	}
	writeError(w, ErrNotFound)
}

func contractPathMatches(pattern, path string) bool {
	patternParts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	pathParts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index := range patternParts {
		part := patternParts[index]
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[index] == "" {
				return false
			}
			continue
		}
		if part != pathParts[index] {
			return false
		}
	}
	return true
}

func (a *API) listWorkflows(w http.ResponseWriter, r *http.Request) {
	if _, retired := r.URL.Query()["name"]; retired {
		writeError(w, invalid("invalid_query", "workflow filtering uses title, not name"))
		return
	}
	limit, err := pageLimit(r, protocol.DefaultWorkflowPageSize, protocol.MaxWorkflowPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	var cursor *protocol.WorkflowCursor
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeWorkflowCursor(encoded)
		if err != nil {
			writeError(w, invalid("invalid_cursor", "cursor is invalid"))
			return
		}
		cursor = &decoded
	}
	var enabled *bool
	if values, exists := r.URL.Query()["enabled"]; exists {
		if len(values) != 1 {
			writeError(w, invalid("invalid_enabled", "enabled may be provided once"))
			return
		}
		value, err := strconv.ParseBool(values[0])
		if err != nil {
			writeError(w, invalid("invalid_enabled", "enabled must be true or false"))
			return
		}
		enabled = &value
	}
	if len(r.URL.Query()["title"]) > 1 || len(r.URL.Query()["cursor"]) > 1 || len(r.URL.Query()["limit"]) > 1 {
		writeError(w, invalid("invalid_query", "workflow query parameters may be provided once"))
		return
	}
	page, err := a.store.Workflows(r.Context(), protocol.WorkflowPageRequest{
		Limit: limit, Cursor: cursor, Title: r.URL.Query().Get("title"), Enabled: enabled,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	var nextCursor *string
	if page.NextCursor != nil {
		value, err := encodeWorkflowCursor(*page.NextCursor)
		if err != nil {
			writeError(w, unavailable(err))
			return
		}
		nextCursor = &value
	}
	writeJSON(w, http.StatusOK, listWorkflowsResponse{Workflows: page.Workflows, NextCursor: nextCursor})
}

func (a *API) createWorkflow(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateWorkflowRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.CreateWorkflow(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, detail)
}

func (a *API) getWorkflow(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.Workflow(r.Context(), r.PathValue("workflow_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) createWorkflowRevision(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateWorkflowRevisionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.CreateWorkflowRevision(r.Context(), r.PathValue("workflow_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, detail)
}

func (a *API) setWorkflowEnabled(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SetWorkflowEnabledRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, invalid("invalid_workflow_enabled", "enabled is required"))
		return
	}
	detail, err := a.store.SetWorkflowEnabled(r.Context(), r.PathValue("workflow_id"), *input.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func encodeWorkflowCursor(cursor protocol.WorkflowCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeWorkflowCursor(encoded string) (protocol.WorkflowCursor, error) {
	var cursor protocol.WorkflowCursor
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return cursor, err
	}
	if err := json.Unmarshal(body, &cursor); err != nil {
		return cursor, err
	}
	if cursor.UpdatedAtMillis <= 0 || strings.TrimSpace(cursor.ID) == "" {
		return cursor, errors.New("invalid workflow cursor")
	}
	return cursor, nil
}

func (a *API) getMetrics(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["window"]) > 1 {
		writeError(w, invalid("invalid_window", "window may be provided once"))
		return
	}
	summary, err := a.store.Metrics(r.Context(), r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

type responseRecorder struct {
	http.ResponseWriter
	status     int
	errorClass string
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (a *API) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		requestID, err := newID()
		if err != nil {
			requestID = "unavailable"
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &responseRecorder{ResponseWriter: w}
		start := time.Now()
		if err := validateRequestHost(r.Host); err != nil {
			writeError(recorder, &ServiceError{Code: "invalid_host", Message: "Host must identify a loopback address", Status: 403})
		} else {
			next.ServeHTTP(recorder, r)
		}
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		attributes := []any{
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if recorder.errorClass != "" {
			attributes = append(attributes, "error_class", recorder.errorClass)
		}
		a.logger.Info("http_request", attributes...)
	})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.db.PingContext(r.Context()); err != nil {
		writeError(w, unavailable(err))
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (a *API) registerWorker(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input workerRegistrationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	legacyRequest := !input.Runtime.Present && !input.RuntimeVersion.Present
	if input.CodexVersion.Present && !legacyRequest {
		writeError(w, invalid("invalid_runtime", "use codex_version or runtime fields, not both"))
		return
	}
	if legacyRequest {
		input.WorkerRegistration.Runtime = protocol.RuntimeCodex
		input.WorkerRegistration.RuntimeVersion = input.CodexVersion.Value
	} else {
		input.WorkerRegistration.Runtime = input.Runtime.Value
		input.WorkerRegistration.RuntimeVersion = input.RuntimeVersion.Value
	}
	worker, err := a.store.RegisterWorker(r.Context(), r.PathValue("worker_id"), input.WorkerRegistration)
	if err != nil {
		writeError(w, err)
		return
	}
	if legacyRequest {
		writeJSON(w, http.StatusOK, legacyWorkerResponse{
			ID: worker.ID, Name: worker.Name, WorkerVersion: worker.WorkerVersion,
			CodexVersion: worker.RuntimeVersion, Capacity: worker.Capacity,
			ActiveCount: worker.ActiveCount, Health: worker.Health, Online: worker.Online,
			Repositories: worker.Repositories, RetainedWorktrees: worker.RetainedWorktrees,
			CurrentTaskTitle: worker.CurrentTaskTitle, RegisteredAt: worker.RegisteredAt,
			LastHeartbeat: worker.LastHeartbeat,
		})
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (a *API) claim(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ClaimRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	claim, err := a.store.Claim(r.Context(), r.PathValue("worker_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if claim == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.logStateChange("execution", claim.Execution.ID, claim.Execution.State, "attempt_id", claim.Attempt.ID)
	writeJSON(w, http.StatusOK, claim)
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := a.store.Workers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listWorkersResponse{Workers: workers})
}

func (a *API) getWorker(w http.ResponseWriter, r *http.Request) {
	worker, err := a.store.Worker(r.Context(), r.PathValue("worker_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

func (a *API) listManagedRepositories(w http.ResponseWriter, r *http.Request) {
	repositories, err := a.store.ManagedRepositories(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listManagedRepositoriesResponse{Repositories: repositories})
}

func (a *API) createManagedRepository(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateManagedRepositoryRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	repository, created, err := a.store.CreateManagedRepository(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, repository)
}

func (a *API) getManagedRepository(w http.ResponseWriter, r *http.Request) {
	repository, err := a.store.ManagedRepository(r.Context(), r.PathValue("repository_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (a *API) getManagedRepositoryReadiness(w http.ResponseWriter, r *http.Request) {
	readiness, err := a.store.ManagedRepositoryReadiness(
		r.Context(), r.PathValue("repository_id"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

func (a *API) setManagedRepositoryEnabled(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SetManagedRepositoryEnabledRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, invalid("invalid_repository", "enabled is required"))
		return
	}
	repository, err := a.store.SetManagedRepositoryEnabled(
		r.Context(),
		r.PathValue("repository_id"),
		*input.Enabled,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, protocol.DefaultTaskPageSize, protocol.MaxTaskPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, err := decodeTaskCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.store.Tasks(r.Context(), protocol.TaskPageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, err)
		return
	}
	var nextCursor *string
	if page.NextCursor != nil {
		encoded, err := encodeTaskCursor(*page.NextCursor)
		if err != nil {
			writeError(w, unavailable(err))
			return
		}
		nextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, listTasksResponse{Tasks: page.Tasks, NextCursor: nextCursor})
}

func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateTaskRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	task, created, err := a.store.CreateTask(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.logStateChange("execution", task.Execution.ID, task.Execution.State, "task_id", task.Task.ID)
	}
	writeJSON(w, status, task)
}

func (a *API) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.store.Task(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *API) deleteTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	if !decodeEmptyJSON(w, r) {
		return
	}
	taskID := r.PathValue("task_id")
	if err := a.store.DeleteTask(r.Context(), taskID); err != nil {
		writeError(w, err)
		return
	}
	a.logger.Info("task_history_deleted", "task_id", taskID)
	writeJSON(w, http.StatusOK, deleteTaskResponse{Deleted: true})
}

func (a *API) cancelTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	if !decodeEmptyJSON(w, r) {
		return
	}
	task, err := a.store.CancelTask(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("execution", task.Execution.ID, task.Execution.State,
		"task_id", task.Task.ID, "cancellation_requested", task.Execution.CancellationRequested)
	writeJSON(w, http.StatusOK, task)
}

func (a *API) retryExecution(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	if !decodeEmptyJSON(w, r) {
		return
	}
	task, err := a.store.RetryExecution(r.Context(), r.PathValue("execution_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("execution", task.Execution.ID, task.Execution.State, "task_id", task.Task.ID)
	writeJSON(w, http.StatusOK, task)
}

func (a *API) getAttempt(w http.ResponseWriter, r *http.Request) {
	attempt, err := a.store.Attempt(r.Context(), r.PathValue("attempt_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (a *API) startAttempt(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.StartAttemptRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	attempt, err := a.store.StartAttempt(r.Context(), r.PathValue("attempt_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("attempt", attempt.ID, attempt.State, "execution_id", attempt.ExecutionID)
	writeJSON(w, http.StatusOK, attempt)
}

func (a *API) logStateChange(resourceType, resourceID, state string, extra ...any) {
	attributes := []any{
		"resource_type", resourceType,
		"resource_id", resourceID,
		"new_state", state,
	}
	a.logger.Info("state_change", append(attributes, extra...)...)
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.LeaseRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, err := a.store.Heartbeat(r.Context(), r.PathValue("attempt_id"), input.LeaseToken)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) appendEvents(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxEventBatchBytes) {
		return
	}
	var input protocol.EventBatchRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := a.store.AppendEvents(r.Context(), r.PathValue("attempt_id"), input); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getEvents(w http.ResponseWriter, r *http.Request) {
	after := int64(-1)
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < -1 {
			writeError(w, invalid("invalid_after", "after must be an integer of at least -1"))
			return
		}
		after = value
	}
	limit, err := pageLimit(r, protocol.DefaultEventPageSize, protocol.MaxEventPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.store.Events(r.Context(), r.PathValue("attempt_id"), after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listAttemptEventsResponse{
		Events: page.Events, NextAfter: page.NextAfter, HasMore: page.HasMore,
	})
}

func pageLimit(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLimit {
		return 0, invalid("invalid_limit", fmt.Sprintf("limit must be an integer between 1 and %d", maxLimit))
	}
	return limit, nil
}

func decodeTaskCursor(raw string) (*protocol.TaskCursor, error) {
	if raw == "" {
		return nil, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("invalid_cursor", "cursor is invalid")
	}
	var value struct {
		CreatedAtMillis int64  `json:"created_at"`
		ID              string `json:"id"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || value.CreatedAtMillis < 0 || value.ID == "" || len(value.ID) > 200 {
		return nil, invalid("invalid_cursor", "cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, invalid("invalid_cursor", "cursor is invalid")
	}
	return &protocol.TaskCursor{CreatedAtMillis: value.CreatedAtMillis, ID: value.ID}, nil
}

func encodeTaskCursor(cursor protocol.TaskCursor) (string, error) {
	value, err := json.Marshal(struct {
		CreatedAtMillis int64  `json:"created_at"`
		ID              string `json:"id"`
	}{CreatedAtMillis: cursor.CreatedAtMillis, ID: cursor.ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (a *API) completeAttempt(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CompleteAttemptRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	attempt, err := a.store.CompleteAttempt(r.Context(), r.PathValue("attempt_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("attempt", attempt.ID, attempt.State, "execution_id", attempt.ExecutionID)
	writeJSON(w, http.StatusOK, attempt)
}

func prepareMutation(w http.ResponseWriter, r *http.Request, limit int64) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || !sameAuthority(parsed.Host, r.Host) {
			writeError(w, &ServiceError{Code: "cross_origin_request", Message: "browser mutations must be same-origin", Status: 403})
			return false
		}
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, &ServiceError{Code: "json_required", Message: "Content-Type must be application/json", Status: 415})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

func validateRequestHost(authority string) error {
	host := authority
	if parsed, _, err := net.SplitHostPort(authority); err == nil {
		host = parsed
	} else if strings.Contains(authority, ":") && net.ParseIP(strings.Trim(authority, "[]")) == nil {
		return errors.New("invalid host")
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return errors.New("host is not loopback")
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return nil
	}
	return errors.New("host must be a loopback IP or localhost")
}

func sameAuthority(left, right string) bool {
	return strings.EqualFold(strings.TrimSuffix(left, "."), strings.TrimSuffix(right, "."))
}

func validateResolvedLoopback(ips []net.IP) error {
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return errors.New("hostname resolves outside loopback")
		}
	}
	return nil
}

var lookupIP = net.LookupIP

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeDecodeError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeDecodeError(w, err)
		return false
	}
	return true
}

func decodeEmptyJSON(w http.ResponseWriter, r *http.Request) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var value map[string]any
	if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
		return true
	} else if err != nil {
		writeDecodeError(w, err)
		return false
	}
	if len(value) != 0 {
		writeError(w, invalid("invalid_request", "request body must be an empty JSON object"))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		writeDecodeError(w, err)
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, &ServiceError{Code: "body_too_large", Message: "request body exceeds its size limit", Status: 413})
		return
	}
	writeError(w, invalid("malformed_json", "request body must contain one valid JSON object"))
}

func writeError(w http.ResponseWriter, err error) {
	service := &ServiceError{Code: "internal_error", Message: "internal server error", Status: 500, Err: err}
	var typed *ServiceError
	if errors.As(err, &typed) {
		service = typed
	}
	if recorder, ok := w.(*responseRecorder); ok {
		recorder.errorClass = service.Code
	}
	writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{Code: service.Code, Message: service.Message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		fmt.Fprint(w, "\n")
	}
}
