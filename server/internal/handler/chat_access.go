package handler

import (
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Admin inspection grants transcript reads only; mutations retain creator ownership.
func (h *Handler) gatePublicChatSessionForReader(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "chat session id")
	if !ok {
		return db.ChatSession{}, false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.ChatSession{}, false
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID: sessionUUID, WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return db.ChatSession{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	inspecting := uuidToString(session.CreatorID) != userID
	if inspecting {
		member, ok := h.workspaceMember(w, r, workspaceID)
		if !ok {
			return db.ChatSession{}, false
		}
		if actorType != "member" || !isWorkspaceAdmin(member) {
			writeError(w, http.StatusForbidden, "not your chat session")
			return db.ChatSession{}, false
		}
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: session.AgentID, WorkspaceID: workspaceUUID})
	if err != nil || (inspecting && agent.Kind != "user") {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.ChatSession{}, false
	}
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return db.ChatSession{}, false
	}
	if _, err := h.Queries.GetPublicChatSessionInWorkspace(r.Context(), db.GetPublicChatSessionInWorkspaceParams{
		ID: session.ID, WorkspaceID: session.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return db.ChatSession{}, false
	}
	return session, true
}
