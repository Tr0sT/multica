package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrAgentPluginUnavailable = errors.New("a selected agent plugin is unavailable")

func agentPluginContextKey(task db.AgentTaskQueue) string {
	if task.IssueID.Valid {
		return "issue:" + util.UUIDToString(task.IssueID)
	}
	if task.ChatSessionID.Valid {
		return "chat:" + util.UUIDToString(task.ChatSessionID)
	}
	return "task:" + util.UUIDToString(task.ID)
}

func (s *TaskService) LoadAgentPluginContext(ctx context.Context, agent db.Agent, task db.AgentTaskQueue) (*agentplugins.TaskContext, error) {
	if len(agent.PluginSelection) == 0 {
		return nil, nil
	}
	key := db.GetAgentPluginSnapshotParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, ContextKey: agentPluginContextKey(task),
	}
	snapshot, err := s.Queries.GetAgentPluginSnapshot(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		snapshot, err = s.createAgentPluginSnapshot(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	if !snapshot.Configured {
		return nil, nil
	}
	result := &agentplugins.TaskContext{Plugins: []agentplugins.TaskPlugin{}}
	if len(snapshot.VersionIds) == 0 {
		return result, nil
	}
	size, err := s.Queries.GetAgentPluginVersionsSize(ctx, db.GetAgentPluginVersionsSizeParams{
		WorkspaceID: agent.WorkspaceID, Ids: snapshot.VersionIds,
	})
	if err != nil {
		return nil, err
	}
	if size > agentplugins.MaxCatalogBytes {
		return nil, fmt.Errorf("%w: selected packages exceed the size limit", ErrAgentPluginUnavailable)
	}
	versions, err := s.Queries.GetAgentPluginVersions(ctx, db.GetAgentPluginVersionsParams{
		WorkspaceID: agent.WorkspaceID, Ids: snapshot.VersionIds,
	})
	if err != nil {
		return nil, err
	}
	if len(versions) != len(snapshot.VersionIds) {
		return nil, fmt.Errorf("%w: a pinned version is missing", ErrAgentPluginUnavailable)
	}
	total := 0
	for _, version := range versions {
		var bundle agentplugins.Bundle
		if err := json.Unmarshal(version.Metadata, &bundle); err != nil {
			return nil, fmt.Errorf("read plugin metadata: %w", err)
		}
		if err := json.Unmarshal(version.Files, &bundle.Files); err != nil {
			return nil, fmt.Errorf("read plugin files: %w", err)
		}
		bundle, err = agentplugins.ParseBundle(version.PluginName, bundle.Files)
		if err != nil || agentplugins.Digest(bundle) != version.Digest {
			return nil, fmt.Errorf("%w: %s failed integrity validation", ErrAgentPluginUnavailable, version.PluginName)
		}
		for _, file := range bundle.Files {
			total += len(file.Content)
		}
		if total > agentplugins.MaxCatalogBytes {
			return nil, fmt.Errorf("%w: selected packages exceed the size limit", ErrAgentPluginUnavailable)
		}
		result.Plugins = append(result.Plugins, agentplugins.TaskPlugin{
			ID: util.UUIDToString(version.ID), Marketplace: version.MarketplaceName,
			Revision: version.Revision, Digest: version.Digest, Bundle: bundle,
		})
	}
	return result, nil
}

func (s *TaskService) createAgentPluginSnapshot(ctx context.Context, key db.GetAgentPluginSnapshotParams) (db.AgentPluginSnapshot, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.AgentPluginSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	selection, err := q.LockAgentPluginSelection(ctx, db.LockAgentPluginSelectionParams{ID: key.AgentID, WorkspaceID: key.WorkspaceID})
	if err != nil {
		return db.AgentPluginSnapshot{}, err
	}
	// The agent lock serializes the first claim against checkbox changes.
	if existing, err := q.GetAgentPluginSnapshot(ctx, key); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.AgentPluginSnapshot{}, err
	}
	var selected []agentplugins.Selection
	if len(selection) > 0 {
		if err := json.Unmarshal(selection, &selected); err != nil {
			return db.AgentPluginSnapshot{}, err
		}
	}
	versionIDs := make([]pgtype.UUID, 0, len(selected))
	for _, item := range selected {
		marketplaceID, err := util.ParseUUID(item.MarketplaceID)
		if err != nil {
			return db.AgentPluginSnapshot{}, fmt.Errorf("%w: invalid marketplace identity", ErrAgentPluginUnavailable)
		}
		versionID, err := q.ResolveCurrentAgentPlugin(ctx, db.ResolveCurrentAgentPluginParams{
			WorkspaceID: key.WorkspaceID, ID: marketplaceID, PluginName: item.PluginName,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.AgentPluginSnapshot{}, fmt.Errorf("%w: %s", ErrAgentPluginUnavailable, item.PluginName)
		}
		if err != nil {
			return db.AgentPluginSnapshot{}, err
		}
		versionIDs = append(versionIDs, versionID)
	}
	size, err := q.GetAgentPluginVersionsSize(ctx, db.GetAgentPluginVersionsSizeParams{
		WorkspaceID: key.WorkspaceID, Ids: versionIDs,
	})
	if err != nil {
		return db.AgentPluginSnapshot{}, err
	}
	if size > agentplugins.MaxCatalogBytes {
		return db.AgentPluginSnapshot{}, fmt.Errorf("%w: selected packages exceed the size limit", ErrAgentPluginUnavailable)
	}
	snapshot, err := q.CreateAgentPluginSnapshot(ctx, db.CreateAgentPluginSnapshotParams{
		WorkspaceID: key.WorkspaceID, AgentID: key.AgentID, ContextKey: key.ContextKey,
		Configured: len(selection) > 0, VersionIds: versionIDs,
	})
	if err != nil {
		return db.AgentPluginSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AgentPluginSnapshot{}, err
	}
	return snapshot, nil
}
