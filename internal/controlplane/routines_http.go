package controlplane

import (
	"net/http"
	"strconv"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) listRoutines(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultRoutinePageSize, maxRoutinePageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	includeArchived := false
	if raw := r.URL.Query().Get("include_archived"); raw != "" {
		includeArchived, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, invalid("invalid_query", "include_archived must be true or false"))
			return
		}
	}
	page, err := a.store.Routines(r.Context(), includeArchived, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) createRoutine(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveRoutineRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	routine, err := a.store.CreateRoutine(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("routine", routine.ID, "created")
	writeJSON(w, http.StatusCreated, routine)
}

func (a *API) getRoutine(w http.ResponseWriter, r *http.Request) {
	routine, err := a.store.Routine(r.Context(), r.PathValue("routine_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, routine)
}

func (a *API) updateRoutine(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveRoutineRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	routine, err := a.store.UpdateRoutine(r.Context(), r.PathValue("routine_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("routine", routine.ID, "updated")
	writeJSON(w, http.StatusOK, routine)
}

func (a *API) setRoutineArchived(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SetRoutineArchivedRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	routine, err := a.store.SetRoutineArchived(r.Context(), r.PathValue("routine_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("routine", routine.ID, "updated")
	writeJSON(w, http.StatusOK, routine)
}

func (a *API) runRoutine(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.RunRoutineRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.RunRoutine(r.Context(), r.PathValue("routine_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.logStateChange("work", detail.Work.ID, string(detail.Work.State))
	}
	writeJSON(w, status, detail)
}

func (a *API) discardRoutineOccurrence(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.DiscardRoutineOccurrenceRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	routine, err := a.store.DiscardRoutineOccurrence(r.Context(), r.PathValue("routine_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("routine", routine.ID, "occurrence_discarded")
	writeJSON(w, http.StatusOK, routine)
}

func (a *API) listWork(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultRoutinePageSize, maxRoutinePageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.store.WorkPage(r.Context(), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) getWork(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.Work(r.Context(), r.PathValue("work_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) cancelWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.CancelWork(r.Context(), r.PathValue("work_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", detail.Work.ID, string(detail.Work.State))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) retryWorkTarget(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.RetryWorkTarget(r.Context(), r.PathValue("work_id"), r.PathValue("target_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", detail.Work.ID, string(detail.Work.State), "target_id", r.PathValue("target_id"))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) cancelWorkTarget(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.CancelWorkTarget(r.Context(), r.PathValue("work_id"), r.PathValue("target_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", detail.Work.ID, string(detail.Work.State), "target_id", r.PathValue("target_id"))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) getOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.store.Overview(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
