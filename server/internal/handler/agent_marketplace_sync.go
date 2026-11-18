package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) claimAgentMarketplaceSync(ctx context.Context, runtimeID, daemonID, userID string) *protocol.DaemonHeartbeatPendingMarketplace {
	syncCtx, cancel := context.WithTimeout(ctx, heartbeatHasPendingTimeout)
	defer cancel()
	ownerID := pgtype.UUID{}
	if daemonID == "" && middleware.DaemonWorkspaceIDFromContext(ctx) == "" && userID != "" {
		var err error
		ownerID, err = util.ParseUUID(userID)
		if err != nil {
			return nil
		}
	}
	pending, err := h.Queries.ClaimAgentMarketplaceSync(syncCtx, db.ClaimAgentMarketplaceSyncParams{
		FetchRuntimeID: parseUUID(runtimeID), DaemonID: daemonID, OwnerID: ownerID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("marketplace sync claim failed", "runtime_id", runtimeID, "error", err)
		}
		return nil
	}
	return &protocol.DaemonHeartbeatPendingMarketplace{
		ID: uuidToString(pending.SyncRequestID), MarketplaceID: uuidToString(pending.ID),
		URL: pending.SourceUrl, Ref: pending.SourceRef,
	}
}

func (h *Handler) ReportAgentMarketplaceSync(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	if daemonWorkspace := middleware.DaemonWorkspaceIDFromContext(r.Context()); daemonWorkspace != "" {
		daemonID := middleware.DaemonIDFromContext(r.Context())
		if daemonID == "" || !runtime.DaemonID.Valid || runtime.DaemonID.String != daemonID {
			writeError(w, http.StatusForbidden, "this daemon does not own the reader runtime")
			return
		}
	} else if !runtime.OwnerID.Valid || uuidToString(runtime.OwnerID) != requestUserID(r) {
		writeError(w, http.StatusForbidden, "only the reader runtime owner can report its marketplace")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "marketplaceId"), "marketplace_id")
	if !ok {
		return
	}
	requestID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "requestId"), "request_id")
	if !ok {
		return
	}
	var report agentplugins.SyncResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentplugins.MaxReportBytes)).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid marketplace synchronization report")
		return
	}
	if report.Error == "" {
		if err := agentplugins.ValidateCatalog(report.Catalog); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	marketplace, err := q.LockAgentMarketplace(r.Context(), db.LockAgentMarketplaceParams{ID: id, WorkspaceID: runtime.WorkspaceID})
	if err != nil || marketplace.FetchRuntimeID != runtime.ID {
		writeError(w, http.StatusNotFound, "marketplace synchronization not found")
		return
	}
	if marketplace.SyncRequestID != requestID {
		writeError(w, http.StatusConflict, "this marketplace synchronization was superseded")
		return
	}
	if marketplace.SyncStatus == "ready" || marketplace.SyncStatus == "failed" {
		writeJSON(w, http.StatusOK, marketplaceResponse(marketplace))
		return
	}
	if marketplace.SyncStatus != "running" {
		writeError(w, http.StatusConflict, "marketplace synchronization was not claimed")
		return
	}
	if report.Error == "" && marketplace.Name != "" && marketplace.Name != report.Catalog.Name {
		report.Error = "the marketplace changed its name; connect the renamed source separately"
	}
	if report.Error != "" {
		message := sanitizeNullBytes(report.Error)
		if len(message) > 1000 {
			message = string([]rune(message)[:min(500, len([]rune(message)))])
		}
		if _, err := q.FailAgentMarketplaceSync(r.Context(), db.FailAgentMarketplaceSyncParams{
			ID: id, WorkspaceID: runtime.WorkspaceID, SyncRequestID: requestID, SyncError: message,
		}); err != nil {
			writeAgentMarketplaceError(w, err)
			return
		}
	} else {
		for _, bundle := range report.Catalog.Plugins {
			digest := agentplugins.Digest(bundle)
			files, _ := json.Marshal(bundle.Files)
			bundle.Files = nil
			metadata, _ := json.Marshal(bundle)
			_, err := q.CreateAgentPluginVersion(r.Context(), db.CreateAgentPluginVersionParams{
				WorkspaceID: runtime.WorkspaceID, MarketplaceID: id, PluginName: bundle.Name,
				Revision: report.Catalog.Revision, Digest: digest, Metadata: metadata, Files: files,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				existing, readErr := q.GetAgentPluginVersionBySource(r.Context(), db.GetAgentPluginVersionBySourceParams{
					WorkspaceID: runtime.WorkspaceID, MarketplaceID: id, Revision: report.Catalog.Revision, PluginName: bundle.Name,
				})
				if readErr != nil || existing.Digest != digest {
					writeError(w, http.StatusConflict, "the same Git revision returned different plugin content")
					return
				}
			} else if err != nil {
				writeAgentMarketplaceError(w, err)
				return
			}
		}
		if _, err := q.CompleteAgentMarketplaceSync(r.Context(), db.CompleteAgentMarketplaceSyncParams{
			ID: id, WorkspaceID: runtime.WorkspaceID, SyncRequestID: requestID,
			Name: report.Catalog.Name, DisplayName: report.Catalog.DisplayName, Revision: report.Catalog.Revision,
		}); err != nil {
			var conflict *pgconn.PgError
			if errors.As(err, &conflict) && conflict.Code == "23505" {
				_ = tx.Rollback(r.Context())
				if _, failErr := h.Queries.FailAgentMarketplaceSync(r.Context(), db.FailAgentMarketplaceSyncParams{
					ID: id, WorkspaceID: runtime.WorkspaceID, SyncRequestID: requestID,
					SyncError: "a marketplace with this name is already connected",
				}); failErr != nil {
					writeAgentMarketplaceError(w, failErr)
					return
				}
				h.publish(protocol.EventAgentPluginsChanged, uuidToString(runtime.WorkspaceID), "member", uuidToString(runtime.OwnerID), nil)
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
			writeAgentMarketplaceError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeAgentMarketplaceError(w, err)
		return
	}
	h.publish(protocol.EventAgentPluginsChanged, uuidToString(runtime.WorkspaceID), "member", uuidToString(runtime.OwnerID), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
