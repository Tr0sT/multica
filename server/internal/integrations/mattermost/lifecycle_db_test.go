package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func mattermostDBFixture(t *testing.T) (*dbfx.Fixture, *db.Queries, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is required for Mattermost database tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	fx := dbfx.New(pool, "", "")
	suffix := uuid.NewString()
	fx.UserID = fx.User(t, "Mattermost tester", "mm-"+suffix+"@example.test")
	fx.WorkspaceID = fx.Workspace(t, "Mattermost test", "mm-"+suffix)
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	agentID := fx.Agent(t, "Mattermost agent", "")
	fx.Cleanup(t, `DELETE FROM channel_installation WHERE workspace_id=$1`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM channel_reply_delivery WHERE installation_id IN (SELECT id FROM channel_installation WHERE workspace_id=$1)`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM channel_binding_token WHERE workspace_id=$1`, fx.WorkspaceID)
	fx.Cleanup(t, `DELETE FROM channel_user_binding WHERE workspace_id=$1`, fx.WorkspaceID)
	return fx, db.New(pool), agentID
}

func mmUUID(t *testing.T, text string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(text); err != nil {
		t.Fatal(err)
	}
	return id
}

func mattermostInstallHarness(t *testing.T, fx *dbfx.Fixture, q *db.Queries, agentID string) (*InstallService, RegisterParams) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, apiPath+"/users/external-user") {
			_ = json.NewEncoder(w).Encode(User{ID: "external-user", Username: "alice"})
			return
		}
		if !strings.HasSuffix(r.URL.Path, apiPath+"/users/me") {
			http.NotFound(w, r)
			return
		}
		id := "bot-a"
		if r.Header.Get("Authorization") == "Bearer other-bot" {
			id = "bot-b"
		}
		_ = json.NewEncoder(w).Encode(User{ID: id, Username: "multica", IsBot: r.Header.Get("Authorization") != "Bearer personal"})
	}))
	t.Cleanup(srv.Close)
	policy, err := NewServerPolicy(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{7}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewInstallService(q, fx.Pool, box, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.httpClient = srv.Client()
	return svc, RegisterParams{WorkspaceID: mmUUID(t, fx.WorkspaceID), AgentID: mmUUID(t, agentID),
		InitiatorID: mmUUID(t, fx.UserID), ServerURL: srv.URL, AccessToken: "original-secret-sentinel"}
}

func TestInstallRotationAndSwapDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	personal := params
	personal.AccessToken = "personal"
	if _, err := svc.Register(ctx, personal); !errors.Is(err, ErrNotABotAccount) {
		t.Fatalf("personal account accepted: %v", err)
	}
	old, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := decodeCredentials(old.Config, svc.box.Open)
	if err != nil || creds.AccessToken != params.AccessToken {
		t.Fatalf("encrypted credential did not round-trip: %v", err)
	}
	if bytes.Contains(old.Config, []byte(params.AccessToken)) {
		t.Fatal("plaintext credential persisted")
	}
	bindingID := fx.Insert(t, "channel_user_binding", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "multica_user_id": fx.UserID, "installation_id": old.ID,
		"channel_type": "mattermost", "channel_user_id": "external-user", "config": "{}",
	})
	chatID := fx.ChatSession(t, agent)
	fx.Insert(t, "channel_chat_session_binding", dbfx.Cols{
		"chat_session_id": chatID, "installation_id": old.ID, "channel_type": "mattermost",
		"channel_chat_id": "room", "chat_type": "p2p", "config": "{}",
	})
	bindingSvc := NewBindingTokenService(q, fx.Pool, svc.box.Open, svc.policy)
	token, err := bindingSvc.Mint(ctx, old.WorkspaceID, old.ID, "external-user")
	if err != nil {
		t.Fatal(err)
	}
	params.AccessToken = "rotated-secret"
	rotated, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != old.ID || fx.Count(t, `SELECT count(*) FROM channel_user_binding WHERE id=$1`, bindingID) != 1 {
		t.Fatal("same-bot token rotation destroyed identity")
	}
	params.AccessToken = "other-bot"
	swapped, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if swapped.ID == old.ID {
		t.Fatal("bot replacement reused the old identity")
	}
	if fx.Count(t, `SELECT count(*) FROM channel_user_binding WHERE installation_id=$1`, old.ID) != 0 {
		t.Fatal("old user binding survived")
	}
	if fx.Count(t, `SELECT count(*) FROM channel_chat_session_binding WHERE installation_id=$1`, old.ID) != 0 {
		t.Fatal("old route survived")
	}
	if fx.Count(t, `SELECT count(*) FROM chat_session WHERE id=$1`, chatID) != 1 {
		t.Fatal("swap deleted chat history")
	}
	if _, err := bindingSvc.RedeemAndBind(ctx, token.Raw, params.InitiatorID); !errors.Is(err, ErrBindingTokenInvalid) {
		t.Fatalf("old token survived swap: %v", err)
	}
	if _, err := q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{ID: old.ID, ChannelType: "mattermost"}); err == nil {
		t.Fatal("old in-flight work can still resolve the replacement")
	}
}

func TestMultipleBotsKeepIndependentAgentsAndContextDB(t *testing.T) {
	fx, q, firstAgent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, firstAgent)
	ctx := context.Background()
	params.Context = ContextSettings{Mode: ContextThread}
	first, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	secondAgent := fx.Agent(t, "Summary agent", "")
	params.AgentID = mmUUID(t, secondAgent)
	if _, err := svc.Register(ctx, params); !errors.Is(err, ErrBotOwnedBySameWorkspace) {
		t.Fatalf("one bot claimed by two agents: %v", err)
	}
	params.AccessToken = "other-bot"
	params.Context = ContextSettings{Mode: ContextChannel, LookbackHours: 24}
	second, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListByWorkspace(ctx, params.WorkspaceID)
	if err != nil || len(rows) != 2 || first.ID == second.ID || first.AgentID == second.AgentID {
		t.Fatalf("distinct bots did not coexist: rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		info := DecodePublicConfig(row.Config)
		want := ContextSettings{Mode: ContextThread}
		if row.ID == second.ID {
			want = params.Context
		}
		if info.Context != want {
			t.Fatalf("context crossed installations: %+v, want %+v", info.Context, want)
		}
	}
}

func TestFailedSwapRollsBackBindingsDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	old, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	bindingID := fx.Insert(t, "channel_user_binding", dbfx.Cols{
		"workspace_id": fx.WorkspaceID, "multica_user_id": fx.UserID, "installation_id": old.ID,
		"channel_type": "mattermost", "channel_user_id": "external-user", "config": "{}",
	})
	otherParams := params
	otherParams.AgentID = mmUUID(t, fx.Agent(t, "Other agent", ""))
	otherParams.AccessToken = "other-bot"
	if _, err := svc.Register(ctx, otherParams); err != nil {
		t.Fatal(err)
	}
	params.AccessToken = "other-bot"
	if _, err := svc.Register(ctx, params); !errors.Is(err, ErrBotOwnedBySameWorkspace) {
		t.Fatalf("conflict = %v", err)
	}
	if fx.Count(t, `SELECT count(*) FROM channel_user_binding WHERE id=$1`, bindingID) != 1 {
		t.Fatal("failed swap deleted a binding")
	}
	if fx.Count(t, `SELECT count(*) FROM channel_installation WHERE id=$1`, old.ID) != 1 {
		t.Fatal("failed swap changed the original identity")
	}
}

func TestServerSwapWithTheSameBotIDGetsANewIdentityDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	first, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	otherServer := params.ServerURL + "/other-instance"
	svc.policy, err = NewServerPolicy(params.ServerURL + "," + otherServer)
	if err != nil {
		t.Fatal(err)
	}
	params.ServerURL = otherServer
	second, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("a different server inherited the first server's installation UUID")
	}
}

func TestBindingPreviewConsentAndRollbackDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	inst, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	bindingSvc := NewBindingTokenService(q, fx.Pool, svc.box.Open, svc.policy)
	bindingSvc.client = svc.httpClient
	token, err := bindingSvc.Mint(ctx, inst.WorkspaceID, inst.ID, "external-user")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := bindingSvc.Preview(ctx, token.Raw, params.InitiatorID)
	if err != nil || preview.Username != "alice" || preview.ServerURL != params.ServerURL {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := q.GetLiveMattermostBindingToken(ctx, hashBindingToken(token.Raw)); err != nil {
		t.Fatal("preview consumed the token")
	}
	outsider := mmUUID(t, fx.User(t, "Outsider", uuid.NewString()+"@example.test"))
	if _, err := bindingSvc.RedeemAndBind(ctx, token.Raw, outsider); !errors.Is(err, ErrBindingNotWorkspaceMember) {
		t.Fatalf("non-member = %v", err)
	}
	if _, err := q.GetLiveMattermostBindingToken(ctx, hashBindingToken(token.Raw)); err != nil {
		t.Fatal("failed redemption burned the token")
	}
	if _, err := bindingSvc.RedeemAndBind(ctx, token.Raw, params.InitiatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := bindingSvc.RedeemAndBind(ctx, token.Raw, params.InitiatorID); !errors.Is(err, ErrBindingTokenInvalid) {
		t.Fatalf("replay = %v", err)
	}
	bound, err := q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{InstallationID: inst.ID, ChannelUserID: "external-user"})
	if err != nil || bound.MulticaUserID != params.InitiatorID {
		t.Fatalf("binding = %+v, %v", bound, err)
	}
	fx.Member(t, fx.WorkspaceID, outsider.String(), "member")
	conflictToken, err := bindingSvc.Mint(ctx, inst.WorkspaceID, inst.ID, "external-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindingSvc.RedeemAndBind(ctx, conflictToken.Raw, outsider); !errors.Is(err, ErrBindingAlreadyAssigned) {
		t.Fatalf("account reassignment = %v", err)
	}
	if _, err := q.GetLiveMattermostBindingToken(ctx, hashBindingToken(conflictToken.Raw)); err != nil {
		t.Fatal("conflict burned the token")
	}
	newToken, err := bindingSvc.Mint(ctx, inst.WorkspaceID, inst.ID, "external-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bindingSvc.RedeemAndBind(ctx, newToken.Raw, params.InitiatorID); !errors.Is(err, ErrBindingTokenInvalid) {
		t.Fatalf("revoked installation accepted token: %v", err)
	}
}

func TestReplyRecoveryAndPartialProgressDB(t *testing.T) {
	for _, mode := range []string{"rate-limit", "lost-response", "unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			fx, q, agent := mattermostDBFixture(t)
			var mu sync.Mutex
			posts := []Post{}
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					list := postList{Posts: map[string]Post{}}
					for i := len(posts) - 1; i >= 0; i-- {
						list.Order = append(list.Order, posts[i].ID)
						list.Posts[posts[i].ID] = posts[i]
					}
					_ = json.NewEncoder(w).Encode(list)
					return
				}
				calls++
				var p Post
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if mode == "rate-limit" && calls == 2 {
					w.WriteHeader(429)
					return
				}
				if mode == "unconfirmed" {
					w.WriteHeader(500)
					return
				}
				p.ID = "post-" + uuid.NewString()
				p.UserID = "bot"
				p.CreateAt = time.Now().UnixMilli()
				posts = append(posts, p)
				if mode == "lost-response" && calls == 1 {
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(p)
			}))
			t.Cleanup(srv.Close)
			policy, err := NewServerPolicy(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(installConfig{ServerURL: srv.URL, BotUserID: "bot", AppID: srv.URL + ":bot"})
			if err != nil {
				t.Fatal(err)
			}
			installation := fx.Insert(t, "channel_installation", dbfx.Cols{
				"workspace_id": fx.WorkspaceID, "agent_id": agent, "channel_type": "mattermost",
				"config": string(config), "status": "active", "installer_user_id": fx.UserID,
			})
			chat := fx.ChatSession(t, agent)
			binding := fx.Insert(t, "channel_chat_session_binding", dbfx.Cols{
				"chat_session_id": chat, "installation_id": installation, "channel_type": "mattermost",
				"channel_chat_id": "room", "chat_type": "p2p", "config": `{"channel_id":"room"}`,
			})
			runtime := fx.Runtime(t, "Mattermost test runtime")
			task := fx.Task(t, agent, dbfx.Cols{"status": "completed", "chat_session_id": chat, "runtime_id": runtime})
			taskID := mmUUID(t, task)
			fx.Cleanup(t, `DELETE FROM channel_reply_delivery WHERE task_id=$1`, task)
			fx.Cleanup(t, `DELETE FROM channel_outbound_message WHERE task_id=$1`, task)
			fx.InsertNoID(t, "channel_task_delivery", dbfx.Cols{
				"task_id": task, "binding_id": binding, "installation_id": installation,
				"channel_type": "mattermost", "channel_chat_id": "room", "chat_type": "p2p", "route_revision": 1, "config": `{"channel_id":"room"}`,
			}, "task_id=$1", task)
			fx.Insert(t, "chat_message", dbfx.Cols{"chat_session_id": chat, "role": "assistant", "task_id": task, "content": strings.Repeat("x", maxPostRunes+10)})
			ctx := context.Background()
			pending, err := q.ListPendingMattermostReplies(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, id := range pending {
				found = found || id == taskID
			}
			if !found {
				t.Fatal("a reply without an event was not discoverable")
			}
			firstWorker := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			if err := firstWorker.processTask(ctx, taskID); err == nil {
				t.Fatal("injected failure was ignored")
			}
			// A new instance has no in-memory knowledge of the first delivery.
			secondWorker := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			if mode == "unconfirmed" {
				if err := secondWorker.processTask(ctx, taskID); err == nil {
					t.Fatal("ambiguous result was treated as delivered")
				}
				mu.Lock()
				defer mu.Unlock()
				if calls != 1 {
					t.Fatal("an unconfirmed POST was blindly repeated")
				}
				return
			}
			if err := secondWorker.processTask(ctx, taskID); err != nil {
				t.Fatal(err)
			}
			if err := secondWorker.processTask(ctx, taskID); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != 2 {
				t.Fatalf("published %d posts, want exactly two chunks", len(posts))
			}
			if posts[1].RootID != posts[0].ID {
				t.Fatal("resumed chunk escaped its original thread")
			}
			row, err := q.GetChatMessageByTaskAssistant(ctx, taskID)
			if err != nil || len(row.ChannelOutboundMessageIds) != 2 {
				t.Fatalf("provenance = %+v, %v", row, err)
			}
			state, err := q.GetChannelReplyDelivery(ctx, taskID)
			if err != nil || state.Phase != "settled" || state.ChunksSent != 2 {
				t.Fatalf("delivery = %+v, %v", state, err)
			}
		})
	}
}
