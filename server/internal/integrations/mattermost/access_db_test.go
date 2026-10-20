package mattermost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAutomaticDenialNeverStartsAnAgentDB(t *testing.T) {
	for _, mode := range []string{"text", "image"} {
		t.Run(mode, func(t *testing.T) {
			fx, q, agent := mattermostDBFixture(t)
			ctx := context.Background()
			var mu sync.Mutex
			var posts []Post
			uploads := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == apiPath+"/files" {
					data, err := io.ReadAll(r.Body)
					if err != nil || string(data) != string(replyPNG) {
						t.Errorf("wrong image: %v", err)
					}
					uploads++
					_, _ = io.WriteString(w, `{"file_infos":[{"id":"automatic-image"}]}`)
					return
				}
				if r.URL.Path != apiPath+"/posts" {
					t.Errorf("unexpected call %s", r.URL.Path)
				}
				var post Post
				if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
					t.Error(err)
				}
				posts = append(posts, post)
				post.ID = fmt.Sprintf("reply-%d", len(posts))
				_ = json.NewEncoder(w).Encode(post)
			}))
			defer srv.Close()
			policy, err := NewServerPolicy(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			reply := DeniedReply{Mode: mode, Text: "Team accounts only."}
			if mode == "image" {
				reply.Text = ""
				reply.ImageAttachmentID = fx.Insert(t, "attachment", dbfx.Cols{
					"workspace_id": fx.WorkspaceID, "uploader_type": "member", "uploader_id": fx.UserID,
					"filename": "reply.png", "content_type": "image/png", "size_bytes": len(replyPNG), "url": "https://objects.test/reply",
				})
			}
			config, err := json.Marshal(installConfig{AppID: "denied-bot", ServerURL: srv.URL, BotUserID: "bot", BotUsername: "bot",
				AccessTokenEncrypted: base64.StdEncoding.EncodeToString([]byte("token")), AccessMode: AccessAllowlist, DeniedReply: reply})
			if err != nil {
				t.Fatal(err)
			}
			installation := fx.Insert(t, "channel_installation", dbfx.Cols{
				"workspace_id": fx.WorkspaceID, "agent_id": agent, "channel_type": "mattermost", "config": config,
				"status": "active", "installer_user_id": fx.UserID,
			})
			fx.Cleanup(t, `DELETE FROM channel_inbound_dedup WHERE installation_id=$1`, installation)
			replier := NewOutboundReplier(OutboundReplierConfig{Queries: q, Objects: &testObjectStore{data: map[string][]byte{"reply": replyPNG}}, Policy: policy, HTTPClient: srv.Client()})
			tasks := service.NewTaskService(q, fx.Pool, nil, events.New())
			router := engine.NewRouter(nil, tasks, q, engine.RouterConfig{})
			t.Cleanup(func() { router.Drain(context.Background()) })
			router.Register(TypeMattermost, NewMattermostResolverSet(q, fx.Pool, replier))
			for _, post := range []Post{
				{ID: "dm", ChannelID: "dm-room", Message: "hello"},
				{ID: "dm", ChannelID: "dm-room", Message: "hello"},
				{ID: "group", ChannelID: "channel", RootID: "root", Message: "@bot help"},
				{ID: "chatter", ChannelID: "channel", Message: "just chatting"},
			} {
				post.UserID = "outside-account"
				channelType := "O"
				if post.ChannelID == "dm-room" {
					channelType = "D"
				}
				msg, ok := inboundFromPosted(post, postedData{ChannelType: channelType}, inboundParams{appID: "denied-bot", botUserID: "bot", botUsername: "bot"})
				if !ok {
					t.Fatal("message rejected before routing")
				}
				if err := router.Handle(ctx, msg); err != nil {
					t.Fatal(err)
				}
			}
			router.Drain(ctx)
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != 2 {
				t.Fatalf("sent %d replies, want one per addressed message", len(posts))
			}
			for _, post := range posts {
				if mode == "text" && (post.Message != reply.Text || len(post.FileIDs) != 0) {
					t.Fatalf("wrong text reply: %+v", post)
				}
				if mode == "image" && (post.Message != "" || len(post.FileIDs) != 1 || post.FileIDs[0] != "automatic-image") {
					t.Fatalf("wrong image reply: %+v", post)
				}
				if post.ChannelID == "dm-room" && post.RootID != "" || post.ChannelID == "channel" && post.RootID != "root" {
					t.Fatalf("wrong reply placement: %+v", post)
				}
			}
			if mode == "text" && uploads != 0 || mode == "image" && uploads != 2 {
				t.Fatalf("unexpected uploads: %d", uploads)
			}
			if count := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); count != 0 {
				t.Fatalf("denied request queued %d agent runs", count)
			}
			for _, table := range []string{"chat_session", "issue"} {
				if count := fx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", fx.WorkspaceID); count != 0 {
					t.Fatalf("denied request created %s rows: %d", table, count)
				}
			}
		})
	}
}

func TestAnyoneAccessDoesNotRequireAnAccountBindingDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	installation := fx.Insert(t, "channel_installation", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "agent_id": agent, "channel_type": "mattermost",
		"config": `{"app_id":"public-bot","access_mode":"anyone"}`, "status": "active", "installer_user_id": fx.UserID,
	})
	row, err := q.GetChannelInstallation(context.Background(), db.GetChannelInstallationParams{
		ID: mmUUID(t, installation), ChannelType: "mattermost",
	})
	if err != nil {
		t.Fatal(err)
	}
	inst := engine.ResolvedInstallation{
		ID: row.ID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
		InstallerUserID: row.InstallerUserID, Active: true, Platform: row,
	}
	for _, chatType := range []channel.ChatType{channel.ChatTypeP2P, channel.ChatTypeGroup} {
		identity, err := (&identityResolver{q: q}).ResolveSender(context.Background(), inst, channel.InboundMessage{
			Source: channel.Source{SenderID: "external-user", ChatType: chatType},
		})
		if err != nil || identity.UserID != row.InstallerUserID {
			t.Fatalf("%s external sender was not admitted under the installer: identity=%+v, error=%v", chatType, identity, err)
		}
	}
	if count := fx.Count(t, `SELECT count(*) FROM channel_user_binding WHERE installation_id=$1`, installation); count != 0 {
		t.Fatal("public access created a fake account binding")
	}
	msg := channel.InboundMessage{Source: channel.Source{SenderID: "external-user"}}
	for _, config := range []string{`{}`, `{"access_mode":"members"}`, `{"access_mode":"future"}`, `{"access_mode":true}`} {
		closed := inst
		closedRow := row
		closedRow.Config = []byte(config)
		closed.Platform = closedRow
		if _, err := (&identityResolver{q: q}).ResolveSender(context.Background(), closed, msg); err == nil {
			t.Fatalf("unbound sender admitted with config %s", config)
		}
	}
	if _, err := (&identityResolver{q: q}).ResolveSender(context.Background(), inst, channel.InboundMessage{}); !errors.Is(err, engine.ErrSenderUnbound) {
		t.Fatalf("missing Mattermost identity accepted: %v", err)
	}
	if _, err := fx.Pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, fx.WorkspaceID, fx.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&identityResolver{q: q}).ResolveSender(context.Background(), inst, msg); !errors.Is(err, engine.ErrSenderNotMember) {
		t.Fatalf("removed installer still authorized public requests: %v", err)
	}
}

func TestPublicAccessRoutesAndRevocationDB(t *testing.T) {
	for _, mode := range []AccessMode{AccessAnyone, AccessAllowlist} {
		t.Run(string(mode), func(t *testing.T) { testUnlinkedAccessRoutesAndRevocation(t, mode) })
	}
}

func testUnlinkedAccessRoutesAndRevocation(t *testing.T, accessMode AccessMode) {
	fx, q, agent := mattermostDBFixture(t)
	ctx := context.Background()
	runtime := fx.Runtime(t, "Mattermost public access")
	if _, err := fx.Pool.Exec(ctx, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, runtime, agent); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(installConfig{
		AppID: "public-bot", AccessMode: accessMode,
		AllowedUsers: []AllowedUser{{ID: "alice", Username: "alice"}, {ID: "bob", Username: "bob"}, {ID: "carol", Username: "carol"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	installation := fx.Insert(t, "channel_installation", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "agent_id": agent, "channel_type": "mattermost",
		"config": config, "status": "active", "installer_user_id": fx.UserID,
	})
	fx.Cleanup(t, `DELETE FROM chat_session WHERE workspace_id=$1`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id=$1)`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM channel_chat_context_generation WHERE chat_session_id IN (SELECT id FROM chat_session WHERE workspace_id=$1)`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM channel_chat_session_binding WHERE installation_id=$1`, installation)
	fx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agent)
	fx.Cleanup(t, `DELETE FROM channel_task_delivery WHERE installation_id=$1`, installation)
	fx.Cleanup(t, `DELETE FROM channel_inbound_dedup WHERE installation_id=$1`, installation)
	tasks := service.NewTaskService(q, fx.Pool, nil, events.New())
	router := engine.NewRouter(nil, tasks, q, engine.RouterConfig{})
	t.Cleanup(func() { router.Drain(context.Background()) })
	router.Register(TypeMattermost, NewMattermostResolverSet(q, fx.Pool, nil))
	send := func(postID, sender, room, root, channelType, text string) {
		t.Helper()
		msg, ok := inboundFromPosted(Post{ID: postID, UserID: sender, ChannelID: room, RootID: root, Message: text},
			postedData{ChannelType: channelType}, inboundParams{appID: "public-bot", botUserID: "bot", botUsername: "bot"})
		if !ok {
			t.Fatal("message did not normalize")
		}
		if err := router.Handle(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	send("dm-a-first", "alice", "dm-a", "", "D", "hello")
	send("dm-b-first", "bob", "dm-b", "", "D", "hello")
	send("channel-request", "carol", "room", "thread-root", "O", "@bot hello")
	send("channel-chatter", "carol", "room", "", "O", "not addressed")
	if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
		t.Fatalf("queued tasks = %d, want three addressed requests", got)
	}
	if accessMode == AccessAllowlist {
		fx.Insert(t, "channel_user_binding", dbfx.Cols{
			"workspace_id": fx.WorkspaceID, "installation_id": installation, "channel_type": "mattermost",
			"channel_user_id": "unlisted-member", "multica_user_id": fx.UserID, "config": "{}",
		})
		send("unlisted-external", "outsider", "outside-dm", "", "D", "hello")
		send("unlisted-linked", "unlisted-member", "member-dm", "", "D", "hello")
		if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
			t.Fatal("allowlist admitted an unlisted sender")
		}
	}
	var aliceSession pgtype.UUID
	rows, err := fx.Pool.Query(ctx, `
		SELECT task.initiator_user_id, delivery.channel_sender_id, delivery.channel_chat_id,
		       delivery.channel_thread_id, delivery.config, task.chat_session_id
		FROM channel_task_delivery delivery
		JOIN agent_task_queue task ON task.id=delivery.task_id
		WHERE delivery.installation_id=$1`, installation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var initiator, session pgtype.UUID
		var sender, key string
		var root pgtype.Text
		var config []byte
		if err := rows.Scan(&initiator, &sender, &key, &root, &config, &session); err != nil {
			t.Fatal(err)
		}
		prefix := "public:"
		if accessMode == AccessAllowlist {
			prefix = "allowlist:"
		}
		if initiator != mmUUID(t, fx.UserID) || !strings.HasPrefix(key, prefix) {
			t.Fatalf("public authorization or history namespace lost: %v, %s", initiator, key)
		}
		room, thread := outboundTarget(db.ChannelChatSessionBinding{ChannelChatID: key, LastThreadID: root, Config: config})
		wantRoom, wantThread := "dm-a", ""
		if sender == "bob" {
			wantRoom = "dm-b"
		} else if sender == "carol" {
			wantRoom, wantThread = "room", "thread-root"
		} else if sender == "alice" {
			aliceSession = session
		} else {
			t.Fatalf("unexpected external sender %q", sender)
		}
		if room != wantRoom || thread != wantThread {
			t.Fatalf("%s reply target = %s/%s, want %s/%s", sender, room, thread, wantRoom, wantThread)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := fx.Count(t, `SELECT count(DISTINCT chat_session_id) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
		t.Fatalf("private conversations share state: %d sessions", got)
	}
	otherOwner := fx.User(t, "Other owner", "other-"+installation+"@example.test")
	if _, err := fx.Pool.Exec(ctx, `UPDATE agent SET owner_id=$1 WHERE id=$2`, otherOwner, agent); err != nil {
		t.Fatal(err)
	}
	send("installer-cannot-invoke", "alice", "dm-a", "", "D", "try again")
	if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
		t.Fatal("public mode bypassed the installer's agent permissions")
	}
	if _, err := fx.Pool.Exec(ctx, `UPDATE agent SET owner_id=$1 WHERE id=$2`, fx.UserID, agent); err != nil {
		t.Fatal(err)
	}
	mode := AccessMembers
	svc := &InstallService{q: dbInstallQueries{q}}
	if accessMode == AccessAllowlist {
		if _, err := svc.UpdateSettings(ctx, mmUUID(t, installation), mmUUID(t, fx.WorkspaceID), SettingsUpdate{
			AccessMode: &accessMode, AllowedUsernames: []string{},
		}); err != nil {
			t.Fatal(err)
		}
		for i, text := range []string{"hello", "@bot /new", "@bot /clear", "@bot /issue Forbidden"} {
			send(fmt.Sprintf("removed-account-%d", i), "alice", "dm-a", "", "D", text)
		}
		if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
			t.Fatal("removed account could still enqueue a request")
		}
		if got := fx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, fx.WorkspaceID); got != 0 {
			t.Fatal("removed account could still create an issue")
		}
		if got := fx.Count(t, `SELECT count(*) FROM channel_chat_session_binding WHERE installation_id=$1`, installation); got != 3 {
			t.Fatal("removed account could still start or replace a chat")
		}
	}
	if _, err := svc.UpdateSettings(ctx, mmUUID(t, installation), mmUUID(t, fx.WorkspaceID), SettingsUpdate{AccessMode: &mode}); err != nil {
		t.Fatal(err)
	}
	send("after-restriction", "alice", "dm-a", "", "D", "hello again")
	if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 3 {
		t.Fatal("revoked public access still queued a request")
	}
	fx.Insert(t, "channel_user_binding", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "installation_id": installation, "channel_type": "mattermost",
		"channel_user_id": "alice", "multica_user_id": fx.UserID, "config": "{}",
	})
	send("linked-request", "alice", "dm-a", "", "D", "linked hello")
	if got := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); got != 4 {
		t.Fatal("linked member was not admitted after restricting access")
	}
	binding, err := q.GetChannelChatSessionBinding(ctx, db.GetChannelChatSessionBindingParams{
		InstallationID: mmUUID(t, installation), ChannelChatID: "dm-a",
	})
	if err != nil || binding.ChatSessionID == aliceSession {
		t.Fatalf("restricted access resumed delegated history: %+v, %v", binding, err)
	}
}
