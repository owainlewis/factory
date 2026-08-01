package controlplane

import (
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) previewLegacyPoller(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.PreviewLegacyPollerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	migration, err := a.store.PreviewLegacyPoller(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, migration)
}

func (a *API) activeLegacyPollerMigration(w http.ResponseWriter, r *http.Request) {
	migration, err := a.store.ActiveLegacyPollerMigration(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ActiveLegacyPollerMigrationResponse{Migration: migration})
}

func (a *API) importLegacyPoller(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ImportLegacyPollerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	migration, err := a.store.ImportLegacyPoller(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.automations.Wake()
	writeJSON(w, http.StatusOK, migration)
}

func (a *API) getLegacyPollerMigration(w http.ResponseWriter, r *http.Request) {
	migration, err := a.store.LegacyPollerMigration(r.Context(), r.PathValue("migration_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, migration)
}

func (a *API) finalizeLegacyPoller(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.FinalizeLegacyPollerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.MigrationID == "" {
		input.MigrationID = r.PathValue("migration_id")
	}
	if input.MigrationID != r.PathValue("migration_id") {
		writeError(w, invalid("invalid_migration", "migration_id must match the request path"))
		return
	}
	migration, err := a.store.FinalizeLegacyPoller(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, migration)
}

func (a *API) resumeLegacyPollerOccurrence(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	occurrence, err := a.store.ResumeLegacyPollerOccurrence(r.Context(), r.PathValue("occurrence_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, occurrence)
}

func (a *API) skipLegacyPollerOccurrence(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	occurrence, err := a.store.SkipLegacyPollerOccurrence(r.Context(), r.PathValue("occurrence_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, occurrence)
}
