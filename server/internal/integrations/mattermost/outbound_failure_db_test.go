package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestFailureRepliesDB(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		wantSend bool
	}{
		{name: "agent failure", wantSend: true},
		{name: "empty error", mode: "empty", wantSend: true},
		{name: "sweeper without assistant message", mode: "sweeper", wantSend: true},
		{name: "resume recorded sweeper notice", mode: "sweeper-resume", wantSend: true},
		{name: "retry after rate limit", mode: "rate-limit", wantSend: true},
		{name: "recover lost response", mode: "lost-response", wantSend: true},
		{name: "automatic retry stays silent", mode: "retry"},
		{name: "failed final retry", mode: "retry-failed", wantSend: true},
		{name: "direct web request stays private", mode: "direct"},
		{name: "revoked installation", mode: "revoked"},
		{name: "historical failure stays silent", mode: "historical"},
		{name: "cancelled run stays silent", mode: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, q, agent := mattermostDBFixture(t)
			ctx := context.Background()
			var mu sync.Mutex
			var posts []Post
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					list := postList{Posts: map[string]Post{}}
					for _, post := range posts {
						list.Order = append(list.Order, post.ID)
						list.Posts[post.ID] = post
					}
					_ = json.NewEncoder(w).Encode(list)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != apiPath+"/posts" {
					t.Errorf("unexpected Mattermost request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				calls++
				if tc.mode == "rate-limit" && calls == 1 {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				var post Post
				if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				post.ID = fmt.Sprintf("post-%d", len(posts)+1)
				post.UserID = "bot"
				post.CreateAt = time.Now().UnixMilli()
				posts = append(posts, post)
				if tc.mode == "lost-response" && calls == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(post)
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
				"channel_chat_id": "room:thread", "chat_type": "group", "config": `{"channel_id":"room"}`,
			})
			runtime := fx.Runtime(t, "Mattermost failure test")
			status := "running"
			if tc.mode == "historical" || tc.mode == "sweeper" || tc.mode == "sweeper-resume" {
				status = "failed"
			} else if tc.mode == "cancelled" {
				status = "cancelled"
			}
			task := fx.Task(t, agent, dbfx.Cols{
				"status": status, "chat_session_id": chat, "runtime_id": runtime,
				"failure_reason": "agent_error", "error": "private diagnostic details",
				"attempt": 1, "max_attempts": 2,
			})
			taskID := mmUUID(t, task)
			turnID := taskID
			fx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agent)
			fx.Cleanup(t, `DELETE FROM chat_message WHERE chat_session_id=$1`, chat)
			fx.Cleanup(t, `DELETE FROM channel_task_delivery WHERE installation_id=$1`, installation)
			fx.Cleanup(t, `DELETE FROM channel_outbound_message WHERE installation_id=$1`, installation)
			fx.Cleanup(t, `DELETE FROM channel_reply_delivery WHERE installation_id=$1`, installation)
			fx.InsertNoID(t, "channel_task_delivery", dbfx.Cols{
				"task_id": task, "binding_id": binding, "installation_id": installation,
				"channel_type": "mattermost", "channel_chat_id": "room:thread", "channel_thread_id": "thread",
				"chat_type": "group", "route_revision": 1, "config": `{"channel_id":"room"}`,
			}, "task_id=$1", task)
			fx.Exec(t, `UPDATE agent_task_queue SET chat_input_task_id=id WHERE id=$1`, task)
			fx.Insert(t, "chat_message", dbfx.Cols{
				"chat_session_id": chat, "task_id": task, "role": "user", "content": "help",
				"channel_ingested": tc.mode != "direct",
			})
			bus := events.New()
			tasks := service.NewTaskService(q, fx.Pool, nil, bus)
			worker := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			worker.Register(bus)
			switch tc.mode {
			case "historical", "cancelled":
			case "sweeper", "sweeper-resume":
				failed, err := q.GetAgentTask(ctx, taskID)
				if err != nil {
					t.Fatal(err)
				}
				tasks.HandleFailedTasks(ctx, []db.AgentTaskQueue{failed})
			default:
				errorText, reason := "private diagnostic details", "agent_error"
				if tc.mode == "empty" {
					errorText = ""
				}
				if tc.mode == "retry" || tc.mode == "retry-failed" {
					reason = string(taskfailure.ReasonAgentProviderNetwork)
				}
				if _, err := tasks.FailTask(ctx, taskID, errorText, "", "", "", reason, false, "", ""); err != nil {
					t.Fatal(err)
				}
				if tc.mode == "retry" || tc.mode == "retry-failed" {
					var retryID pgtype.UUID
					fx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE retry_of_task_id=$1`, taskID).Scan(&retryID)
					if tc.mode == "retry-failed" {
						fx.Exec(t, `UPDATE agent_task_queue SET status='running' WHERE id=$1`, retryID)
						if _, err := tasks.FailTask(ctx, retryID, errorText, "", "", "", "agent_error", false, "", ""); err != nil {
							t.Fatal(err)
						}
						taskID = retryID
					}
				}
			}
			if tc.mode == "revoked" {
				fx.Exec(t, `UPDATE channel_installation SET status='revoked' WHERE id=$1`, installation)
			}
			pending, err := q.ListPendingMattermostReplies(ctx)
			if err != nil || slices.Contains(pending, taskID) != tc.wantSend {
				t.Fatalf("queued failure = %v, want %v; error: %v", slices.Contains(pending, taskID), tc.wantSend, err)
			}
			err = worker.processTask(ctx, taskID)
			wantRetry := tc.mode == "rate-limit" || tc.mode == "lost-response"
			if (err != nil) != wantRetry {
				t.Fatalf("first attempt error = %v, want retry = %v", err, wantRetry)
			}
			if tc.mode == "sweeper-resume" {
				// Recreate a restart after recording the post but before settling the turn.
				fx.Exec(t, `UPDATE channel_reply_delivery SET phase='terminal' WHERE turn_id=$1`, turnID)
			}
			restarted := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			for range 2 {
				if err := restarted.processTask(ctx, taskID); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantPosts := 0
			if tc.wantSend {
				wantPosts = 1
			}
			if len(posts) != wantPosts {
				t.Fatalf("posts = %d, want %d", len(posts), wantPosts)
			}
			if !tc.wantSend {
				return
			}
			post := posts[0]
			if post.Message != "❌ The agent run failed. Please try again." || post.ChannelID != "room" || post.RootID != "thread" || len(post.FileIDs) != 0 {
				t.Fatalf("unexpected failure notice: %+v", post)
			}
			state, err := q.GetChannelReplyDelivery(ctx, turnID)
			if err != nil || state.Phase != "settled" || state.ChunksSent != 1 {
				t.Fatalf("failure delivery = %+v, %v", state, err)
			}
			pending, err = q.ListPendingMattermostReplies(ctx)
			if err != nil || slices.Contains(pending, taskID) || slices.Contains(pending, turnID) {
				t.Fatalf("delivered failure is still pending: %v, %v", pending, err)
			}
			if tc.mode == "lost-response" && calls != 1 {
				t.Fatal("recovery duplicated the accepted post")
			}
		})
	}
}
