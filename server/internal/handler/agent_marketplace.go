package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentplugins"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type agentMarketplaceResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	SourceURL      string `json:"source_url"`
	SourceRef      string `json:"source_ref"`
	FetchRuntimeID string `json:"fetch_runtime_id"`
	Revision       string `json:"revision"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	CreatedBy      string `json:"created_by"`
	UpdatedAt      string `json:"updated_at"`
}

type agentMarketplacePluginResponse struct {
	VersionID     string   `json:"version_id"`
	MarketplaceID string   `json:"marketplace_id"`
	Marketplace   string   `json:"marketplace"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	Description   string   `json:"description"`
	Version       string   `json:"version"`
	Revision      string   `json:"revision"`
	Skills        []string `json:"skills"`
	HasMCP        bool     `json:"has_mcp"`
	HasHooks      bool     `json:"has_hooks"`
}

func marketplaceResponse(m db.AgentMarketplace) agentMarketplaceResponse {
	return agentMarketplaceResponse{
		ID: uuidToString(m.ID), Name: m.Name, DisplayName: m.DisplayName,
		SourceURL: m.SourceUrl, SourceRef: m.SourceRef, FetchRuntimeID: uuidToString(m.FetchRuntimeID),
		Revision: m.Revision, Status: m.SyncStatus, Error: m.SyncError,
		CreatedBy: uuidToString(m.CreatedBy), UpdatedAt: timestampToString(m.UpdatedAt),
	}
}

func (h *Handler) marketplaceWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	id, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	_, ok = h.requireWorkspaceMember(w, r, uuidToString(id), "marketplace not found")
	return id, ok
}

func (h *Handler) marketplaceFetchRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, runtimeID string) (db.AgentRuntime, bool) {
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.AgentRuntime{}, false
	}
	if rt.WorkspaceID != workspaceID || !rt.OwnerID.Valid || uuidToString(rt.OwnerID) != userID {
		writeError(w, http.StatusForbidden, "only the runtime owner can read a marketplace using its Git credentials")
		return db.AgentRuntime{}, false
	}
	if rt.Status != "online" {
		writeError(w, http.StatusConflict, "choose an online runtime to read the marketplace")
		return db.AgentRuntime{}, false
	}
	if !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityAgentPluginBundlesV1) {
		writeError(w, http.StatusConflict, "update the Multica daemon to enable agent marketplaces")
		return db.AgentRuntime{}, false
	}
	return rt, true
}

func (h *Handler) loadAgentMarketplace(w http.ResponseWriter, r *http.Request, manage bool) (db.AgentMarketplace, bool) {
	workspaceID, ok := h.marketplaceWorkspace(w, r)
	if !ok {
		return db.AgentMarketplace{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "marketplace_id")
	if !ok {
		return db.AgentMarketplace{}, false
	}
	marketplace, err := h.Queries.GetAgentMarketplace(r.Context(), db.GetAgentMarketplaceParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "marketplace not found")
		} else {
			writeAgentMarketplaceError(w, err)
		}
		return db.AgentMarketplace{}, false
	}
	if manage {
		member, ok := h.requireWorkspaceMember(w, r, uuidToString(workspaceID), "marketplace not found")
		if !ok {
			return db.AgentMarketplace{}, false
		}
		userID, ok := requireUserID(w, r)
		if !ok {
			return db.AgentMarketplace{}, false
		}
		if uuidToString(marketplace.CreatedBy) != userID && !roleAllowed(member.Role, "owner", "admin") {
			writeError(w, http.StatusForbidden, "only the marketplace creator or a workspace admin can manage it")
			return db.AgentMarketplace{}, false
		}
	}
	return marketplace, true
}

func (h *Handler) ListAgentMarketplaces(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.marketplaceWorkspace(w, r)
	if !ok {
		return
	}
	if err := h.Queries.ExpireAgentMarketplaceSyncs(r.Context(), workspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read marketplace synchronization state")
		return
	}
	marketplaces, err := h.Queries.ListAgentMarketplaces(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list marketplaces")
		return
	}
	versions, err := h.Queries.ListCurrentAgentPlugins(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list marketplace plugins")
		return
	}
	sources := make([]agentMarketplaceResponse, 0, len(marketplaces))
	for _, m := range marketplaces {
		sources = append(sources, marketplaceResponse(m))
	}
	plugins := make([]agentMarketplacePluginResponse, 0, len(versions))
	for _, v := range versions {
		var metadata agentplugins.Bundle
		if err := json.Unmarshal(v.Metadata, &metadata); err != nil {
			writeError(w, http.StatusInternalServerError, "invalid stored plugin metadata")
			return
		}
		plugins = append(plugins, agentMarketplacePluginResponse{
			VersionID: uuidToString(v.ID), MarketplaceID: uuidToString(v.MarketplaceID),
			Marketplace: v.MarketplaceName, Name: v.PluginName, DisplayName: metadata.DisplayName,
			Description: metadata.Description, Version: metadata.Version, Revision: v.Revision, Skills: metadata.Skills,
			HasMCP: metadata.HasMCP, HasHooks: metadata.HasHooks,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"marketplaces": sources, "plugins": plugins})
}

func (h *Handler) CreateAgentMarketplace(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.marketplaceWorkspace(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		URL       string `json:"url"`
		Ref       string `json:"ref"`
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid marketplace request")
		return
	}
	req.URL, req.Ref = strings.TrimSpace(req.URL), strings.TrimSpace(req.Ref)
	if err := agentplugins.ValidateSource(req.URL, req.Ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, ok := h.marketplaceFetchRuntime(w, r, workspaceID, req.RuntimeID)
	if !ok {
		return
	}
	marketplace, err := h.Queries.CreateAgentMarketplace(r.Context(), db.CreateAgentMarketplaceParams{
		WorkspaceID: workspaceID, SourceUrl: req.URL, SourceRef: req.Ref,
		FetchRuntimeID: runtime.ID, CreatedBy: parseUUID(userID),
	})
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	h.publish(protocol.EventAgentPluginsChanged, uuidToString(workspaceID), "member", userID, nil)
	writeJSON(w, http.StatusCreated, marketplaceResponse(marketplace))
}

func (h *Handler) RefreshAgentMarketplace(w http.ResponseWriter, r *http.Request) {
	marketplace, ok := h.loadAgentMarketplace(w, r, true)
	if !ok {
		return
	}
	var req struct {
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	runtime, ok := h.marketplaceFetchRuntime(w, r, marketplace.WorkspaceID, req.RuntimeID)
	if !ok {
		return
	}
	if err := h.Queries.ExpireAgentMarketplaceSyncs(r.Context(), marketplace.WorkspaceID); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	updated, err := h.Queries.RefreshAgentMarketplace(r.Context(), db.RefreshAgentMarketplaceParams{
		ID: marketplace.ID, WorkspaceID: marketplace.WorkspaceID, FetchRuntimeID: runtime.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "marketplace synchronization is already running")
		return
	}
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	h.publish(protocol.EventAgentPluginsChanged, uuidToString(marketplace.WorkspaceID), "member", uuidToString(runtime.OwnerID), nil)
	writeJSON(w, http.StatusOK, marketplaceResponse(updated))
}

func (h *Handler) DeleteAgentMarketplace(w http.ResponseWriter, r *http.Request) {
	marketplace, ok := h.loadAgentMarketplace(w, r, true)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if _, err := q.LockAgentMarketplace(r.Context(), db.LockAgentMarketplaceParams{ID: marketplace.ID, WorkspaceID: marketplace.WorkspaceID}); err != nil {
		writeError(w, http.StatusNotFound, "marketplace not found")
		return
	}
	selection, _ := json.Marshal([]map[string]string{{"marketplace_id": uuidToString(marketplace.ID)}})
	count, err := q.CountAgentMarketplaceBindings(r.Context(), db.CountAgentMarketplaceBindingsParams{
		WorkspaceID: marketplace.WorkspaceID, Selection: selection,
	})
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "remove this marketplace's plugins from agents before disconnecting it")
		return
	}
	if _, err := q.DeleteAgentMarketplace(r.Context(), db.DeleteAgentMarketplaceParams{ID: marketplace.ID, WorkspaceID: marketplace.WorkspaceID}); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(marketplace.WorkspaceID))
	h.publish(protocol.EventAgentPluginsChanged, uuidToString(marketplace.WorkspaceID), actorType, actorID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) GetAgentPluginSelection(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	selected := []agentplugins.Selection{}
	if len(agent.PluginSelection) > 0 {
		if err := json.Unmarshal(agent.PluginSelection, &selected); err != nil {
			writeError(w, http.StatusInternalServerError, "invalid plugin selection")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": len(agent.PluginSelection) > 0, "plugins": selected})
}

func (h *Handler) SetAgentPluginSelection(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	var req struct {
		Plugins *[]agentplugins.Selection `json:"plugins"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Plugins == nil || len(*req.Plugins) > agentplugins.MaxPlugins {
		writeError(w, http.StatusBadRequest, "plugins must be an array of at most 32 selections")
		return
	}
	runtime, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, agent.RuntimeID)
	if len(*req.Plugins) > 0 && (err != nil || runtime.Provider != "codex" || runtime.WorkspaceID != agent.WorkspaceID) {
		writeError(w, http.StatusConflict, "bind a Codex runtime before configuring marketplace plugins")
		return
	}
	if len(*req.Plugins) > 0 && !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilityAgentPluginBundlesV1) {
		writeError(w, http.StatusConflict, "update the Multica daemon before configuring marketplace plugins")
		return
	}
	selected := *req.Plugins
	for i := range selected {
		id, ok := parseUUIDOrBadRequest(w, selected[i].MarketplaceID, "marketplace_id")
		if !ok {
			return
		}
		selected[i].MarketplaceID = uuidToString(id)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].MarketplaceID != selected[j].MarketplaceID {
			return selected[i].MarketplaceID < selected[j].MarketplaceID
		}
		return selected[i].PluginName < selected[j].PluginName
	})
	marketplaceIDs := make([]pgtype.UUID, len(selected))
	for i, item := range selected {
		if !agentplugins.ValidName(item.PluginName) || (i > 0 && item == selected[i-1]) {
			writeError(w, http.StatusBadRequest, "plugin selections must have valid, unique identities")
			return
		}
		marketplaceIDs[i] = parseUUID(item.MarketplaceID)
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	previous, err := q.LockAgentPluginSelection(r.Context(), db.LockAgentPluginSelectionParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	currentAgent, err := q.GetAgent(r.Context(), agent.ID)
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	if currentAgent.RuntimeID != agent.RuntimeID {
		writeError(w, http.StatusConflict, "the agent runtime changed; reload before saving plugins")
		return
	}
	versionIDs := make([]pgtype.UUID, 0, len(selected))
	for i, item := range selected {
		if _, err := q.LockAgentMarketplace(r.Context(), db.LockAgentMarketplaceParams{ID: marketplaceIDs[i], WorkspaceID: agent.WorkspaceID}); err != nil {
			writeError(w, http.StatusNotFound, "marketplace not found")
			return
		}
		versionID, err := q.ResolveCurrentAgentPlugin(r.Context(), db.ResolveCurrentAgentPluginParams{WorkspaceID: agent.WorkspaceID, ID: marketplaceIDs[i], PluginName: item.PluginName})
		if err != nil {
			writeError(w, http.StatusConflict, "a selected plugin is no longer available; refresh the catalog")
			return
		}
		versionIDs = append(versionIDs, versionID)
	}
	size, err := q.GetAgentPluginVersionsSize(r.Context(), db.GetAgentPluginVersionsSizeParams{
		WorkspaceID: agent.WorkspaceID, Ids: versionIDs,
	})
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	if size > agentplugins.MaxCatalogBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "selected plugin packages exceed the task size limit")
		return
	}
	if len(previous) == 0 {
		if err := q.PinExistingAgentPluginContexts(r.Context(), db.PinExistingAgentPluginContextsParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}); err != nil {
			writeAgentMarketplaceError(w, err)
			return
		}
	}
	data, _ := json.Marshal(selected)
	if _, err := q.SetAgentPluginSelection(r.Context(), db.SetAgentPluginSelectionParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, PluginSelection: data}); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	h.publish(protocol.EventAgentPluginsChanged, uuidToString(agent.WorkspaceID), actorType, actorID, map[string]any{"agent_id": uuidToString(agent.ID)})
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "plugins": selected})
}

func writeAgentMarketplaceError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "a marketplace with this source or name is already connected")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to update agent marketplaces")
}
