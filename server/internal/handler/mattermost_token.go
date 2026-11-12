package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// These dedicated endpoints never share the member-visible installation shape.
// Both the router and this boundary require a human workspace owner/admin.
func (h *Handler) requireMattermostTokenAdmin(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, ok := requireUserID(w, r); !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	wsID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(wsID), "workspace not found", "owner", "admin"); !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if h.MattermostInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "mattermost integration not configured")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return id, wsID, true
}

// ExportMattermostToken reads the stored value without contacting Mattermost.
func (h *Handler) ExportMattermostToken(w http.ResponseWriter, r *http.Request) {
	id, wsID, ok := h.requireMattermostTokenAdmin(w, r)
	if !ok {
		return
	}
	token, err := h.MattermostInstall.ExportToken(r.Context(), id, wsID)
	if err != nil {
		writeMattermostTokenError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		InstallationID string `json:"installation_id"`
		WorkspaceID    string `json:"workspace_id"`
		AccessToken    string `json:"access_token"`
	}{uuidToString(id), uuidToString(wsID), token})
}

// ImportMattermostToken changes only the credential, including for revoked rows.
func (h *Handler) ImportMattermostToken(w http.ResponseWriter, r *http.Request) {
	id, wsID, ok := h.requireMattermostTokenAdmin(w, r)
	if !ok {
		return
	}
	var body *struct {
		AccessToken string `json:"access_token"`
	}
	// JSON escaping can expand a valid 16 KiB token to six times its length.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 6*(16<<10)+1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body == nil || decoder.Decode(new(any)) != io.EOF {
		// Decoder errors can include a secret used as an unknown field name.
		writeError(w, http.StatusBadRequest, "invalid token request body")
		return
	}
	updated, err := h.MattermostInstall.ImportToken(r.Context(), id, wsID, body.AccessToken)
	if err != nil {
		writeMattermostTokenError(w, err)
		return
	}
	if updated {
		h.publish(protocol.EventMattermostInstallationUpdated, uuidToString(wsID), "user", r.Header.Get("X-User-ID"), map[string]any{
			"id": uuidToString(id),
		})
	}
	writeJSON(w, http.StatusOK, struct {
		InstallationID string `json:"installation_id"`
		WorkspaceID    string `json:"workspace_id"`
		Updated        bool   `json:"updated"`
	}{uuidToString(id), uuidToString(wsID), updated})
}

func writeMattermostTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mattermost.ErrInstallationNotFound):
		writeError(w, http.StatusNotFound, "mattermost installation not found in this workspace")
	case errors.Is(err, mattermost.ErrInvalidAccessToken):
		writeError(w, http.StatusBadRequest, "bot access token must be nonempty, valid and at most 16 KiB")
	case errors.Is(err, mattermost.ErrCredentialsRejected):
		writeError(w, http.StatusBadRequest, "Mattermost rejected this access token; use a current token for the same bot account; nothing was changed")
	case errors.Is(err, mattermost.ErrTokenIdentityMismatch), errors.Is(err, mattermost.ErrNotABotAccount):
		writeError(w, http.StatusBadRequest, "access token must belong to the existing bot account on the same Mattermost server; nothing was changed")
	case errors.Is(err, mattermost.ErrCredentialsUnverifiable):
		writeError(w, http.StatusServiceUnavailable, "could not verify the bot access token with Mattermost; nothing was changed")
	case errors.Is(err, mattermost.ErrServerNotAllowed):
		writeError(w, http.StatusBadRequest, "the saved Mattermost server is not allowed by the operator; nothing was changed")
	case errors.Is(err, mattermost.ErrTokenInstallationChanged):
		writeError(w, http.StatusConflict, "installation identity changed during verification; retry the operation")
	case errors.Is(err, mattermost.ErrTokenUnavailable):
		writeError(w, http.StatusServiceUnavailable, "saved Mattermost access token could not be read; check server encryption configuration")
	default:
		writeError(w, http.StatusInternalServerError, "could not update Mattermost access token")
	}
}
