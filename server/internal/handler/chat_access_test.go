package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAdminChatInspection(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	admin := dbfx.User(t, "Chat admin", "chat-inspection-admin@test.invalid")
	peerAdmin := dbfx.User(t, "Peer admin", "chat-inspection-peer@test.invalid")
	dbfx.Member(t, testWorkspaceID, peerAdmin, "admin")
	member := dbfx.User(t, "Chat member", "chat-inspection-member@test.invalid")
	dbfx.Member(t, testWorkspaceID, admin, "admin")
	dbfx.Member(t, testWorkspaceID, member, "member")
	agent := dbfx.Agent(t, "Private chat agent", "", testutil.Cols{"owner_id": member})
	memberChat := dbfx.ChatSession(t, agent, testutil.Cols{"creator_id": member, "explicitly_created_at": testutil.Raw("now()")})
	adminChat := dbfx.ChatSession(t, agent, testutil.Cols{"creator_id": peerAdmin, "explicitly_created_at": testutil.Raw("now()")})
	dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": memberChat, "role": "assistant", "content": "Private reply"})

	for _, viewer := range []string{testUserID, admin} {
		for _, creator := range []string{"all", member, peerAdmin} {
			req := chatPendingCtxAs(t, newRequestAs(viewer, "GET", "/api/chat/sessions?status=all&creator_id="+creator, nil), viewer)
			var sessions []ChatSessionResponse
			testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusOK).JSON(&sessions)
			found := map[string]bool{}
			for _, session := range sessions {
				found[session.ID] = true
				if creator != "all" && session.CreatorID != creator {
					t.Fatalf("creator filter returned another member's chat: %+v", session)
				}
				if session.CreatorID != viewer && (session.HasUnread || session.UnreadCount != 0) {
					t.Fatalf("inspection exposed another member's unread state: %+v", session)
				}
			}
			if (creator == "all" || creator == member) && !found[memberChat] {
				t.Fatal("admin cannot list member chat")
			}
			if (creator == "all" || creator == peerAdmin) && !found[adminChat] {
				t.Fatal("admin cannot list another admin's chat")
			}
		}
		for _, read := range []http.HandlerFunc{testHandler.GetChatSession, testHandler.ListChatMessages, testHandler.ListChatMessagesPage, testHandler.GetPendingChatTask} {
			req := chatPendingCtxAs(t, newRequestAs(viewer, "GET", "/api/chat/sessions/"+memberChat, nil), viewer)
			testutil.Call(t, read, testutil.WithURLParams(req, "sessionId", memberChat)).Want(http.StatusOK)
		}
	}

	for _, filter := range []string{"all", admin} {
		req := chatPendingCtxAs(t, newRequestAs(member, "GET", "/api/chat/sessions?creator_id="+filter, nil), member)
		testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusForbidden)
	}
	req := chatPendingCtxAs(t, newRequestAs(member, "GET", "/api/chat/sessions", nil), member)
	var own []ChatSessionResponse
	testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusOK).JSON(&own)
	for _, session := range own {
		if session.CreatorID != member {
			t.Fatalf("default list leaked another creator: %+v", session)
		}
	}
	for _, read := range []http.HandlerFunc{testHandler.GetChatSession, testHandler.ListChatMessages, testHandler.ListChatMessagesPage, testHandler.GetPendingChatTask} {
		req := chatPendingCtxAs(t, newRequestAs(member, "GET", "/api/chat/sessions/"+adminChat, nil), member)
		testutil.Call(t, read, testutil.WithURLParams(req, "sessionId", adminChat)).Want(http.StatusForbidden)
	}
	for _, mutation := range []struct {
		handler http.HandlerFunc
		body    any
	}{
		{testHandler.SendChatMessage, map[string]any{"content": "Should not send"}},
		{testHandler.UpdateChatSession, map[string]any{"title": "Should not rename"}},
		{testHandler.SetChatSessionPinned, map[string]any{"pinned": true}},
		{testHandler.SetChatSessionArchived, map[string]any{"archived": true}},
		{testHandler.DeleteChatSession, nil},
		{testHandler.MarkChatSessionRead, nil},
	} {
		req := chatPendingCtxAs(t, newRequestAs(admin, "POST", "/api/chat/sessions/"+memberChat, mutation.body), admin)
		testutil.Call(t, mutation.handler, testutil.WithURLParams(req, "sessionId", memberChat)).Want(http.StatusForbidden)
	}
	otherWorkspace := dbfx.Workspace(t, "Other chat workspace", "other-chat-inspection")
	otherChat := dbfx.ChatSession(t, agent, testutil.Cols{"workspace_id": otherWorkspace, "creator_id": member, "explicitly_created_at": testutil.Raw("now()")})
	req = chatPendingCtxAs(t, newRequestAs(admin, "GET", "/api/chat/sessions/"+otherChat, nil), admin)
	testutil.Call(t, testHandler.GetChatSession, testutil.WithURLParams(req, "sessionId", otherChat)).Want(http.StatusNotFound)
	req = chatPendingCtxAs(t, newRequestAs(admin, "GET", "/api/chat/sessions?creator_id=not-a-uuid", nil), admin)
	testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusBadRequest)
	req = chatPendingCtxAs(t, newRequestAs(admin, "GET", "/api/chat/sessions?creator_id=all", nil), admin)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agent)
	testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusForbidden)
}

func TestMattermostChatMetadataProjection(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agent := dbfx.Agent(t, "Mattermost metadata", "")
	session := dbfx.ChatSession(t, agent, testutil.Cols{"explicitly_created_at": testutil.Raw("now()")})
	installation := dbfx.Insert(t, "channel_installation", testutil.Cols{
		"workspace_id": testWorkspaceID, "agent_id": agent, "channel_type": "mattermost", "installer_user_id": testUserID,
	})
	dbfx.Insert(t, "channel_chat_session_binding", testutil.Cols{
		"chat_session_id": session, "installation_id": installation, "channel_type": "mattermost", "chat_type": "p2p",
		"channel_chat_id": "direct-channel", "config": testutil.Raw(`'{"channel_id":"direct-channel","preserved":"value"}'::jsonb`),
	})
	err := testHandler.Queries.UpdateMattermostChatSessionMetadata(context.Background(), db.UpdateMattermostChatSessionMetadataParams{
		ChatSessionID: parseUUID(session), InstallationID: parseUUID(installation),
		Config: []byte(`{"sender_username":"alice.example"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var response ChatSessionResponse
	req := withChatTestWorkspaceCtx(t, newRequest("GET", "/api/chat/sessions/"+session, nil))
	testutil.Call(t, testHandler.GetChatSession, testutil.WithURLParams(req, "sessionId", session)).Want(http.StatusOK).JSON(&response)
	if response.ChannelSource == nil || response.ChannelSource.SenderUsername != "alice.example" || response.ChannelSource.ChannelType != "mattermost" {
		t.Fatalf("missing Mattermost identity: %+v", response.ChannelSource)
	}
	var channelID, preserved string
	dbfx.QueryRow(t, "SELECT config->>'channel_id', config->>'preserved' FROM channel_chat_session_binding WHERE chat_session_id=$1", session).Scan(&channelID, &preserved)
	if channelID != "direct-channel" || preserved != "value" {
		t.Fatalf("metadata update destroyed routing config: %q / %q", channelID, preserved)
	}
}
