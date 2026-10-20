package mattermost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
	fx, q, agent := mattermostDBFixture(t)
	ctx := context.Background()
	runtime := fx.Runtime(t, "Mattermost public access")
	if _, err := fx.Pool.Exec(ctx, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, runtime, agent); err != nil {
		t.Fatal(err)
	}
	installation := fx.Insert(t, "channel_installation", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "agent_id": agent, "channel_type": "mattermost",
		"config": `{"app_id":"public-bot","access_mode":"anyone"}`, "status": "active", "installer_user_id": fx.UserID,
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
		if initiator != mmUUID(t, fx.UserID) || !strings.HasPrefix(key, "public:") {
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
