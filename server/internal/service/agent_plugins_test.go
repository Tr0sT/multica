package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func pluginTestID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAgentPluginSnapshotPinsIssueVersionsAndHandlesEmptySelection(t *testing.T) {
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	suffix := uuid.NewString()
	fx.UserID = fx.User(t, "Plugin owner", suffix+"@plugins.test")
	fx.WorkspaceID = fx.Workspace(t, "Plugin workspace", "plugins-"+suffix)
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	runtimeID := fx.Runtime(t, "Codex", testutil.Cols{"provider": "codex"})
	agentID := fx.Agent(t, "Worker", runtimeID)
	marketID := fx.Insert(t, "agent_marketplace", testutil.Cols{
		"workspace_id": fx.WorkspaceID, "source_url": "https://example.test/plugins.git",
		"fetch_runtime_id": runtimeID, "created_by": fx.UserID, "name": "studio", "sync_status": "ready",
	})
	addVersion := func(revision, content string) string {
		t.Helper()
		bundle, err := agentplugins.ParseBundle("sample", []agentplugins.File{
			{Path: ".codex-plugin/plugin.json", Content: []byte(`{"name":"sample","version":"1"}`)},
			{Path: "skills/check/SKILL.md", Content: []byte(content)},
		})
		if err != nil {
			t.Fatal(err)
		}
		digest := agentplugins.Digest(bundle)
		files, _ := json.Marshal(bundle.Files)
		bundle.Files = nil
		metadata, _ := json.Marshal(bundle)
		id := fx.Insert(t, "agent_plugin_version", testutil.Cols{
			"workspace_id": fx.WorkspaceID, "marketplace_id": marketID, "plugin_name": "sample",
			"revision": revision, "digest": digest, "metadata": metadata, "files": files,
		})
		fx.Exec(t, "UPDATE agent_marketplace SET revision=$2 WHERE id=$1", marketID, revision)
		return id
	}
	firstVersion := addVersion(strings.Repeat("a", 40), "First version")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool}
	selection, _ := json.Marshal([]agentplugins.Selection{{MarketplaceID: marketID, PluginName: "sample"}})
	agent, err := q.SetAgentPluginSelection(context.Background(), db.SetAgentPluginSelectionParams{
		ID: pluginTestID(t, agentID), WorkspaceID: pluginTestID(t, fx.WorkspaceID), PluginSelection: selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	fx.Cleanup(t, "DELETE FROM agent_plugin_snapshot WHERE workspace_id=$1", fx.WorkspaceID)
	task := db.AgentTaskQueue{AgentID: agent.ID, IssueID: pluginTestID(t, fx.Issue(t, "Original issue"))}
	var group sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			result, err := svc.LoadAgentPluginContext(context.Background(), agent, task)
			if err == nil && (result == nil || len(result.Plugins) != 1 || result.Plugins[0].ID != firstVersion) {
				err = errors.New("first claim did not pin the selected version")
			}
			failures <- err
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	secondVersion := addVersion(strings.Repeat("b", 40), "Second version")
	agent, err = q.SetAgentPluginSelection(context.Background(), db.SetAgentPluginSelectionParams{
		ID: agent.ID, WorkspaceID: agent.WorkspaceID, PluginSelection: []byte("[]"),
	})
	if err != nil {
		t.Fatal(err)
	}
	continued, err := svc.LoadAgentPluginContext(context.Background(), agent, task)
	if err != nil || continued == nil || continued.Plugins[0].ID != firstVersion {
		t.Fatalf("continuation drifted after refresh/selection change: %+v %v", continued, err)
	}
	empty, err := svc.LoadAgentPluginContext(context.Background(), agent, db.AgentTaskQueue{
		AgentID: agent.ID, IssueID: pluginTestID(t, fx.Issue(t, "New issue without plugins")),
	})
	if err != nil || empty == nil || len(empty.Plugins) != 0 {
		t.Fatalf("explicit empty selection was lost: %+v %v", empty, err)
	}
	agent, err = q.SetAgentPluginSelection(context.Background(), db.SetAgentPluginSelectionParams{
		ID: agent.ID, WorkspaceID: agent.WorkspaceID, PluginSelection: selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := svc.LoadAgentPluginContext(context.Background(), agent, db.AgentTaskQueue{
		AgentID: agent.ID, IssueID: pluginTestID(t, fx.Issue(t, "New issue with latest plugin")),
	})
	if err != nil || latest == nil || latest.Plugins[0].ID != secondVersion {
		t.Fatalf("new issue did not use the new revision: %+v %v", latest, err)
	}
	fx.Exec(t, "UPDATE agent_marketplace SET revision=$2 WHERE id=$1", marketID, strings.Repeat("c", 40))
	_, err = svc.LoadAgentPluginContext(context.Background(), agent, db.AgentTaskQueue{
		AgentID: agent.ID, IssueID: pluginTestID(t, fx.Issue(t, "Missing plugin")),
	})
	if !errors.Is(err, ErrAgentPluginUnavailable) {
		t.Fatalf("missing selection silently fell back: %v", err)
	}
}
