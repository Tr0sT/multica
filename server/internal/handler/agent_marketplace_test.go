package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func marketplaceTestRuntime(t *testing.T, owner string) string {
	t.Helper()
	return dbfx.Runtime(t, "Marketplace reader", testutil.Cols{
		"provider": "codex", "owner_id": owner, "status": "online", "visibility": "public",
		"metadata": []byte(`{"capabilities":["agent-plugin-bundles-v1"]}`),
	})
}

func marketplaceTestCatalog(t *testing.T) *agentplugins.Catalog {
	t.Helper()
	bundle, err := agentplugins.ParseBundle("sample", []agentplugins.File{
		{Path: ".codex-plugin/plugin.json", Content: []byte(`{"name":"sample","version":"1.0"}`)},
		{Path: "skills/sample/SKILL.md", Content: []byte("Fixture instructions that must not be in the catalog response.")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &agentplugins.Catalog{Name: "fixture-marketplace", Revision: strings.Repeat("a", 40), Plugins: []agentplugins.Bundle{bundle}}
}

func TestAgentMarketplaceConnectSyncSelectAndDisconnect(t *testing.T) {
	runtimeID := marketplaceTestRuntime(t, testUserID)
	var source agentMarketplaceResponse
	testutil.Call(t, testHandler.CreateAgentMarketplace, newRequest(http.MethodPost, "/api/agent-marketplaces", map[string]any{
		"url": "ssh://git@example.test/team/plugins.git", "ref": "main", "runtime_id": runtimeID,
	})).Want(http.StatusCreated).JSON(&source)
	dbfx.Cleanup(t, "DELETE FROM agent_marketplace WHERE id=$1", source.ID)
	dbfx.Cleanup(t, "DELETE FROM agent_plugin_version WHERE marketplace_id=$1", source.ID)
	if source.Status != "pending" {
		t.Fatalf("connect should queue a read, got %s", source.Status)
	}
	pending, err := testHandler.Queries.ClaimAgentMarketplaceSync(context.Background(), db.ClaimAgentMarketplaceSyncParams{
		FetchRuntimeID: parseUUID(runtimeID), OwnerID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	report := func() *http.Request {
		return testutil.WithURLParams(newRequest(http.MethodPost, "/sync", agentplugins.SyncResult{Catalog: marketplaceTestCatalog(t)}),
			"runtimeId", runtimeID, "marketplaceId", source.ID, "requestId", uuidToString(pending.SyncRequestID))
	}
	testutil.Call(t, testHandler.ReportAgentMarketplaceSync, report()).Want(http.StatusOK)
	testutil.Call(t, testHandler.ReportAgentMarketplaceSync, report()).Want(http.StatusOK)
	var versionCount int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_version WHERE marketplace_id=$1", source.ID).Scan(&versionCount)
	if versionCount != 1 {
		t.Fatalf("repeated report created %d versions", versionCount)
	}
	var catalog struct {
		Marketplaces []agentMarketplaceResponse       `json:"marketplaces"`
		Plugins      []agentMarketplacePluginResponse `json:"plugins"`
	}
	response := testutil.Call(t, testHandler.ListAgentMarketplaces, newRequest(http.MethodGet, "/api/agent-marketplaces", nil)).
		Want(http.StatusOK).JSON(&catalog)
	if len(catalog.Plugins) != 1 || len(catalog.Plugins[0].Skills) != 1 ||
		strings.Contains(response.Body.String(), `"files"`) {
		t.Fatalf("unexpected catalog response: %s", response.Body.String())
	}
	agentID := dbfx.Agent(t, "Plugin worker", runtimeID)
	dbfx.Cleanup(t, "DELETE FROM agent_plugin_snapshot WHERE agent_id=$1", agentID)
	oldIssue := dbfx.Issue(t, "Already started before plugin configuration")
	dbfx.Task(t, agentID, testutil.Cols{"issue_id": oldIssue, "runtime_id": runtimeID, "status": "completed", "dispatched_at": testutil.Raw("now() - interval '1 minute'")})
	save := func(plugins []agentplugins.Selection) *http.Request {
		return withURLParam(newRequest(http.MethodPut, "/marketplace-plugins", map[string]any{"plugins": plugins}), "id", agentID)
	}
	selected := []agentplugins.Selection{{MarketplaceID: source.ID, PluginName: "sample"}}
	testutil.Call(t, testHandler.SetAgentPluginSelection, save(selected)).Want(http.StatusOK)
	snapshot, err := testHandler.Queries.GetAgentPluginSnapshot(context.Background(), db.GetAgentPluginSnapshotParams{
		WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID), ContextKey: "issue:" + oldIssue,
	})
	if err != nil || snapshot.Configured {
		t.Fatalf("first opt-in changed an existing issue: %+v %v", snapshot, err)
	}
	deleteRequest := func() *http.Request {
		return withURLParam(newRequest(http.MethodDelete, "/api/agent-marketplaces/"+source.ID, nil), "id", source.ID)
	}
	testutil.Call(t, testHandler.DeleteAgentMarketplace, deleteRequest()).Want(http.StatusConflict)
	testutil.Call(t, testHandler.SetAgentPluginSelection, save([]agentplugins.Selection{})).Want(http.StatusOK)
	testutil.Call(t, testHandler.DeleteAgentMarketplace, deleteRequest()).Want(http.StatusOK)
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_version WHERE marketplace_id=$1", source.ID).Scan(&versionCount)
	if versionCount != 1 {
		t.Fatal("disconnect removed immutable versions needed by existing issues")
	}
}

func TestAgentMarketplaceAccessAndInputValidation(t *testing.T) {
	runtimeID := marketplaceTestRuntime(t, testUserID)
	agentID := dbfx.Agent(t, "Plugin worker", runtimeID)
	for _, selections := range []any{
		nil,
		[]agentplugins.Selection{{MarketplaceID: "not-a-uuid", PluginName: "sample"}},
		[]agentplugins.Selection{{MarketplaceID: runtimeID, PluginName: "../sample"}},
		[]agentplugins.Selection{{MarketplaceID: strings.ToUpper(runtimeID), PluginName: "sample"}, {MarketplaceID: runtimeID, PluginName: "sample"}},
	} {
		req := withURLParam(newRequest(http.MethodPut, "/marketplace-plugins", map[string]any{"plugins": selections}), "id", agentID)
		testutil.Call(t, testHandler.SetAgentPluginSelection, req).Want(http.StatusBadRequest)
	}
	otherUser := dbfx.User(t, "Other runtime owner", "marketplace-other-"+runtimeID+"@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	otherRuntime := marketplaceTestRuntime(t, otherUser)
	testutil.Call(t, testHandler.CreateAgentMarketplace, newRequest(http.MethodPost, "/api/agent-marketplaces", map[string]any{
		"url": "https://example.test/team/plugins.git", "runtime_id": otherRuntime,
	})).Want(http.StatusForbidden)
	for _, url := range []string{"file:///etc", "https://token:secret@example.test/plugins.git", "ssh://-oProxyCommand=bad/path"} {
		testutil.Call(t, testHandler.CreateAgentMarketplace, newRequest(http.MethodPost, "/api/agent-marketplaces", map[string]any{
			"url": url, "runtime_id": runtimeID,
		})).Want(http.StatusBadRequest)
	}
	foreignWorkspace := dbfx.Workspace(t, "Foreign source workspace", "foreign-plugin-"+runtimeID)
	sourceID := dbfx.Insert(t, "agent_marketplace", testutil.Cols{
		"workspace_id": foreignWorkspace, "source_url": "https://example.test/foreign.git",
		"fetch_runtime_id": runtimeID, "created_by": testUserID,
	})
	req := withURLParam(newRequest(http.MethodPut, "/marketplace-plugins", map[string]any{
		"plugins": []agentplugins.Selection{{MarketplaceID: sourceID, PluginName: "sample"}},
	}), "id", agentID)
	testutil.Call(t, testHandler.SetAgentPluginSelection, req).Want(http.StatusNotFound)
	req = withURLParam(newRequest(http.MethodPut, "/marketplace-plugins", map[string]any{"plugins": []agentplugins.Selection{}}), "id", agentID)
	req.Header.Set("X-User-ID", otherUser)
	testutil.Call(t, testHandler.SetAgentPluginSelection, req).Want(http.StatusForbidden)
}

func TestAgentMarketplaceReportRejectsStaleAndUnsafeData(t *testing.T) {
	runtimeID := marketplaceTestRuntime(t, testUserID)
	sourceID := dbfx.Insert(t, "agent_marketplace", testutil.Cols{
		"workspace_id": testWorkspaceID, "source_url": "https://example.test/unsafe.git",
		"fetch_runtime_id": runtimeID, "created_by": testUserID,
	})
	pending, err := testHandler.Queries.ClaimAgentMarketplaceSync(context.Background(), db.ClaimAgentMarketplaceSyncParams{
		FetchRuntimeID: parseUUID(runtimeID), OwnerID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	report := marketplaceTestCatalog(t)
	report.Plugins[0].Files = append(report.Plugins[0].Files, agentplugins.File{Path: "../escape", Content: []byte("bad")})
	req := testutil.WithURLParams(newRequest(http.MethodPost, "/sync", agentplugins.SyncResult{Catalog: report}),
		"runtimeId", runtimeID, "marketplaceId", sourceID, "requestId", uuidToString(pending.SyncRequestID))
	testutil.Call(t, testHandler.ReportAgentMarketplaceSync, req).Want(http.StatusBadRequest)
	req = testutil.WithURLParams(newRequest(http.MethodPost, "/sync", agentplugins.SyncResult{Catalog: marketplaceTestCatalog(t)}),
		"runtimeId", runtimeID, "marketplaceId", sourceID, "requestId", sourceID)
	testutil.Call(t, testHandler.ReportAgentMarketplaceSync, req).Want(http.StatusConflict)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_version WHERE marketplace_id=$1", sourceID).Scan(&count)
	if count != 0 {
		t.Fatal("unsafe or stale callback stored package files")
	}
}

func TestAgentMarketplaceSyncCannotBeClaimedOrForgedByAnotherMember(t *testing.T) {
	runtimeID := marketplaceTestRuntime(t, testUserID)
	otherUser := dbfx.User(t, "Reader impostor", "reader-impostor-"+runtimeID+"@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	sourceID := dbfx.Insert(t, "agent_marketplace", testutil.Cols{
		"workspace_id": testWorkspaceID, "source_url": "https://example.test/owned.git",
		"fetch_runtime_id": runtimeID, "created_by": testUserID,
	})
	_, err := testHandler.Queries.ClaimAgentMarketplaceSync(context.Background(), db.ClaimAgentMarketplaceSyncParams{
		FetchRuntimeID: parseUUID(runtimeID), OwnerID: parseUUID(otherUser),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another member claimed the reader's job: %v", err)
	}
	pending, err := testHandler.Queries.ClaimAgentMarketplaceSync(context.Background(), db.ClaimAgentMarketplaceSyncParams{
		FetchRuntimeID: parseUUID(runtimeID), OwnerID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := testutil.WithURLParams(newRequest(http.MethodPost, "/sync", agentplugins.SyncResult{Catalog: marketplaceTestCatalog(t)}),
		"runtimeId", runtimeID, "marketplaceId", sourceID, "requestId", uuidToString(pending.SyncRequestID))
	req.Header.Set("X-User-ID", otherUser)
	testutil.Call(t, testHandler.ReportAgentMarketplaceSync, req).Want(http.StatusNotFound)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_version WHERE marketplace_id=$1", sourceID).Scan(&count)
	if count != 0 {
		t.Fatal("another member replaced trusted package content")
	}
}

func TestDeleteIssueRemovesOnlyItsWorkspacePluginSnapshot(t *testing.T) {
	agentID := dbfx.Agent(t, "Snapshot cleanup", handlerTestRuntimeID(t))
	issueID := dbfx.Issue(t, "Plugin snapshot cleanup")
	dbfx.InsertNoID(t, "agent_plugin_snapshot", testutil.Cols{
		"workspace_id": testWorkspaceID, "agent_id": agentID, "context_key": "issue:" + issueID, "configured": true,
	}, "agent_id=$1", agentID)
	foreign := dbfx.Workspace(t, "Wrong workspace", "wrong-snapshot-"+issueID)
	if err := testHandler.Queries.DeleteIssue(context.Background(), db.DeleteIssueParams{ID: parseUUID(issueID), WorkspaceID: parseUUID(foreign)}); err != nil {
		t.Fatal(err)
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_snapshot WHERE agent_id=$1", agentID).Scan(&count)
	if count != 1 {
		t.Fatal("foreign workspace deletion removed the snapshot")
	}
	if err := testHandler.Queries.DeleteIssue(context.Background(), db.DeleteIssueParams{ID: parseUUID(issueID), WorkspaceID: parseUUID(testWorkspaceID)}); err != nil {
		t.Fatal(err)
	}
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_plugin_snapshot WHERE agent_id=$1", agentID).Scan(&count)
	if count != 0 {
		t.Fatal("issue deletion left a plugin snapshot")
	}
}

func TestAgentPluginSelectionCanBeClearedAfterRuntimeChange(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "Different provider", testutil.Cols{"provider": "claude-code", "owner_id": testUserID})
	agentID := dbfx.Agent(t, "Clear old plugin choices", runtimeID, testutil.Cols{
		"plugin_selection": []byte(`[{"marketplace_id":"00000000-0000-0000-0000-000000000001","plugin_name":"old"}]`),
	})
	request := withURLParam(newRequest(http.MethodPut, "/marketplace-plugins", map[string]any{
		"plugins": []agentplugins.Selection{},
	}), "id", agentID)
	testutil.Call(t, testHandler.SetAgentPluginSelection, request).Want(http.StatusOK)
	updated, err := testHandler.Queries.GetAgent(context.Background(), parseUUID(agentID))
	if err != nil || string(updated.PluginSelection) != "[]" {
		t.Fatalf("could not clear plugins on another provider: %s %v", updated.PluginSelection, err)
	}
}

func TestAgentPluginClaimGatesManagedCodexHomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provider   string
		configured bool
		capability bool
		status     int
		managed    bool
		rejected   bool
	}{
		{"legacy", "codex", false, false, http.StatusOK, false, false},
		{"managed", "codex", true, true, http.StatusOK, true, false},
		{"old-daemon", "codex", true, false, http.StatusOK, false, true},
		{"cleared-other-provider", "claude-code", true, false, http.StatusOK, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			runtimeID := createClaimReclaimRuntime(t, ctx, "Plugins "+tc.name)
			dbfx.Exec(t, "UPDATE agent_runtime SET provider=$2 WHERE id=$1", runtimeID, tc.provider)
			agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Plugins "+tc.name)
			if tc.configured {
				dbfx.Exec(t, "UPDATE agent SET plugin_selection='[]'::jsonb WHERE id=$1", agentID)
			}
			dbfx.Cleanup(t, "DELETE FROM agent_plugin_snapshot WHERE agent_id=$1", agentID)
			taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
			req := withURLParam(newRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil), "runtimeId", runtimeID)
			if tc.capability {
				req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityAgentPluginBundlesV1)
			}
			var response struct {
				Task AgentTaskResponse `json:"task"`
			}
			call := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(tc.status)
			if tc.status == http.StatusOK {
				call.JSON(&response)
				if tc.rejected {
					var status string
					dbfx.QueryRow(t, "SELECT status FROM agent_task_queue WHERE id=$1", taskID).Scan(&status)
					if response.Task.ID != "" || status != "failed" {
						t.Fatalf("unsupported daemon received a task: id=%s status=%s", response.Task.ID, status)
					}
					return
				}
				if response.Task.ID == "" || (response.Task.AgentPlugins != nil) != tc.managed {
					t.Fatalf("wrong plugin dispatch: task=%s plugins=%+v", response.Task.ID, response.Task.AgentPlugins)
				}
			}
		})
	}
}
