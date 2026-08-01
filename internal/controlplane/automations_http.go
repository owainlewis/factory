package controlplane

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) listAutomations(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, protocol.DefaultAutomationPageSize, protocol.MaxAutomationPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(r.URL.Query()["limit"]) > 1 {
		writeError(w, invalid("invalid_query", "limit may be provided once"))
		return
	}
	var cursor *protocol.AutomationCursor
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeAutomationCursor(encoded)
		if err != nil {
			writeError(w, invalid("invalid_cursor", "cursor is invalid"))
			return
		}
		cursor = &decoded
	}
	if len(r.URL.Query()["cursor"]) > 1 {
		writeError(w, invalid("invalid_query", "cursor may be provided once"))
		return
	}
	page, err := a.store.AutomationsPage(r.Context(), limit, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	var nextCursor *string
	if page.NextCursor != nil {
		encoded, err := encodeAutomationCursor(*page.NextCursor)
		if err != nil {
			writeError(w, unavailable(err))
			return
		}
		nextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": page.Automations, "next_cursor": nextCursor})
}

func (a *API) createAutomation(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.CreateAutomationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.CreateAutomation(r.Context(), input)
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

func (a *API) getAutomation(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.Automation(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) updateAutomation(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.UpdateAutomationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, err := a.store.UpdateAutomation(r.Context(), r.PathValue("automation_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) setAutomationEnabled(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SetAutomationEnabledRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, invalid("invalid_automation_enabled", "enabled is required"))
		return
	}
	id := r.PathValue("automation_id")
	detail, invalidatedToken, err := a.store.setAutomationEnabled(r.Context(), id, *input.Enabled, input.ConfirmLegacyPollerStopped)
	if err != nil {
		writeError(w, err)
		return
	}
	if !*input.Enabled {
		a.automations.Cancel(id, invalidatedToken)
	}
	a.automations.Wake()
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) testAutomation(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := a.automations.Test(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) checkAutomation(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, err := a.store.RequestAutomationCheck(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.automations.Wake()
	writeJSON(w, http.StatusAccepted, detail)
}

func (a *API) runAutomation(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.RunAutomationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, err := a.automations.RunNow(r.Context(), r.PathValue("automation_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.automations.Wake()
	writeJSON(w, http.StatusAccepted, detail)
}

func (a *API) listAutomationOccurrences(w http.ResponseWriter, r *http.Request) {
	limit := protocol.DefaultAutomationPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > protocol.MaxAutomationPageSize {
			writeError(w, invalid("invalid_limit", "limit must be between 1 and 200"))
			return
		}
		limit = value
	}
	if len(r.URL.Query()["limit"]) > 1 {
		writeError(w, invalid("invalid_query", "limit may be provided once"))
		return
	}
	var cursor *protocol.AutomationOccurrenceCursor
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeAutomationOccurrenceCursor(encoded)
		if err != nil {
			writeError(w, invalid("invalid_cursor", "cursor is invalid"))
			return
		}
		cursor = &decoded
	}
	if len(r.URL.Query()["cursor"]) > 1 {
		writeError(w, invalid("invalid_query", "cursor may be provided once"))
		return
	}
	page, err := a.store.AutomationOccurrencesPage(r.Context(), r.PathValue("automation_id"), limit, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	var nextCursor *string
	if page.NextCursor != nil {
		encoded, err := encodeAutomationOccurrenceCursor(*page.NextCursor)
		if err != nil {
			writeError(w, unavailable(err))
			return
		}
		nextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"occurrences": page.Occurrences, "next_cursor": nextCursor})
}

func encodeAutomationCursor(cursor protocol.AutomationCursor) (string, error) {
	return encodeOpaqueCursor(cursor)
}

func decodeAutomationCursor(encoded string) (protocol.AutomationCursor, error) {
	var cursor protocol.AutomationCursor
	if err := decodeOpaqueCursor(encoded, &cursor); err != nil {
		return cursor, err
	}
	if cursor.UpdatedAtMillis <= 0 || strings.TrimSpace(cursor.ID) == "" {
		return cursor, errors.New("invalid Automation cursor")
	}
	return cursor, nil
}

func encodeAutomationOccurrenceCursor(cursor protocol.AutomationOccurrenceCursor) (string, error) {
	return encodeOpaqueCursor(cursor)
}

func decodeAutomationOccurrenceCursor(encoded string) (protocol.AutomationOccurrenceCursor, error) {
	var cursor protocol.AutomationOccurrenceCursor
	if err := decodeOpaqueCursor(encoded, &cursor); err != nil {
		return cursor, err
	}
	if cursor.CreatedAtMillis <= 0 || strings.TrimSpace(cursor.ID) == "" {
		return cursor, errors.New("invalid Automation Occurrence cursor")
	}
	return cursor, nil
}

func encodeOpaqueCursor(cursor any) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeOpaqueCursor(encoded string, cursor any) error {
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, cursor)
}
