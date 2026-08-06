package controlplane

import (
	"errors"
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) listRunRepositories(w http.ResponseWriter, r *http.Request) {
	repositories, err := a.store.RunRepositories(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["cursor"]) > 1 || len(r.URL.Query()["limit"]) > 1 {
		writeError(w, invalid("invalid_query", "Run query parameters may be provided once"))
		return
	}
	for key := range r.URL.Query() {
		if key != "cursor" && key != "limit" {
			writeError(w, invalid("invalid_query", "unsupported Run query parameter"))
			return
		}
	}
	limit, err := pageLimit(r, protocol.DefaultRunPageSize, protocol.MaxRunPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	var cursor *protocol.RunCursor
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeRunCursor(encoded)
		if err != nil {
			writeError(w, invalid("invalid_cursor", "cursor is invalid"))
			return
		}
		cursor = &decoded
	}
	page, err := a.store.Runs(r.Context(), protocol.RunPageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, err)
		return
	}
	var nextCursor *string
	if page.NextCursor != nil {
		value, err := encodeOpaqueCursor(*page.NextCursor)
		if err != nil {
			writeError(w, unavailable(err))
			return
		}
		nextCursor = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": page.Runs, "next_cursor": nextCursor})
}

func decodeRunCursor(encoded string) (protocol.RunCursor, error) {
	var cursor protocol.RunCursor
	if err := decodeOpaqueCursor(encoded, &cursor); err != nil {
		return cursor, err
	}
	if cursor.AdmittedAtMillis <= 0 || !validUUID(cursor.ID) {
		return cursor, errors.New("invalid Run cursor")
	}
	return cursor, nil
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateRunRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.CreateRun(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.logStateChange("run", detail.Run.ID, detail.Run.State, "job_id", detail.Jobs[0].Job.ID)
	}
	writeJSON(w, status, detail)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.Run(r.Context(), r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.CancelJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("run", detail.Run.ID, detail.Run.State, "job_id", r.PathValue("job_id"))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) retryJob(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.RetryJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("run", detail.Run.ID, detail.Run.State, "job_id", r.PathValue("job_id"))
	writeJSON(w, http.StatusOK, detail)
}
