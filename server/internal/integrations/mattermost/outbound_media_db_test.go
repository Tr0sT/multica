package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
)

func TestReplyAttachmentsDB(t *testing.T) {
	for _, tc := range []struct {
		name        string
		text        string
		failure     string
		wantNotices int
	}{
		{name: "text and files", text: "Here are the files"},
		{name: "files only"},
		{name: "whitespace and files", text: " \n\t"},
		{name: "chunked text", text: strings.Repeat("x", maxPostRunes+10)},
		{name: "retry upload", text: "Files", failure: "upload"},
		{name: "retry post", text: "Files", failure: "post"},
		{name: "recover lost post response", text: "Files", failure: "lost-response"},
		{name: "deletion preserves positions", text: "Files", failure: "deleted", wantNotices: 1},
		{name: "rejected upload reports failure", text: "Files", failure: "rejected", wantNotices: 1},
		{name: "no storage reports failure", text: "Files", failure: "storage", wantNotices: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, q, agent := mattermostDBFixture(t)
			ctx := context.Background()
			var mu sync.Mutex
			var posts []Post
			uploads := map[string]int{}
			failed := false
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet:
					list := postList{Posts: map[string]Post{}}
					for i := len(posts) - 1; i >= 0; i-- {
						list.Order = append(list.Order, posts[i].ID)
						list.Posts[posts[i].ID] = posts[i]
					}
					_ = json.NewEncoder(w).Encode(list)
				case r.URL.Path == apiPath+"/files":
					name := r.URL.Query().Get("filename")
					uploads[name]++
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "contents of "+name || r.URL.Query().Get("channel_id") != "room" {
						t.Errorf("unexpected upload: %s, %q, %v", r.URL, body, err)
					}
					if name == "report.pdf" && !failed && (tc.failure == "upload" || tc.failure == "deleted" || tc.failure == "rejected") {
						failed = true
						if tc.failure == "rejected" {
							w.WriteHeader(http.StatusRequestEntityTooLarge)
						} else {
							w.WriteHeader(http.StatusTooManyRequests)
						}
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"file_infos": []map[string]string{{"id": "file-" + name}}})
				case r.URL.Path == apiPath+"/posts":
					var post Post
					if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					fail := len(post.FileIDs) > 0 && post.FileIDs[0] == "file-report.pdf" && !failed
					if fail && tc.failure == "post" {
						failed = true
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					post.ID = fmt.Sprintf("post-%d", len(posts)+1)
					post.UserID = "bot"
					post.CreateAt = time.Now().UnixMilli()
					posts = append(posts, post)
					if fail && tc.failure == "lost-response" {
						failed = true
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_ = json.NewEncoder(w).Encode(post)
				default:
					http.NotFound(w, r)
				}
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
			runtime := fx.Runtime(t, "Mattermost attachment test")
			task := fx.Task(t, agent, dbfx.Cols{"status": "completed", "chat_session_id": chat, "runtime_id": runtime})
			taskID := mmUUID(t, task)
			fx.Cleanup(t, `DELETE FROM channel_reply_delivery WHERE task_id=$1`, task)
			fx.Cleanup(t, `DELETE FROM channel_outbound_message WHERE task_id=$1`, task)
			fx.InsertNoID(t, "channel_task_delivery", dbfx.Cols{
				"task_id": task, "binding_id": binding, "installation_id": installation,
				"channel_type": "mattermost", "channel_chat_id": "room:thread", "channel_thread_id": "thread",
				"chat_type": "group", "route_revision": 1, "config": `{"channel_id":"room"}`,
			}, "task_id=$1", task)
			messageID := fx.Insert(t, "chat_message", dbfx.Cols{"chat_session_id": chat, "role": "assistant", "task_id": task, "content": tc.text})
			objects := &testObjectStore{data: map[string][]byte{}}
			var middleID string
			for i, name := range []string{"chart.png", "report.pdf", "notes.txt"} {
				objects.data[name] = []byte("contents of " + name)
				id := fx.Insert(t, "attachment", dbfx.Cols{
					"workspace_id": fx.WorkspaceID, "chat_session_id": chat, "chat_message_id": messageID, "task_id": task,
					"uploader_type": "agent", "uploader_id": agent, "filename": name,
					"url": "https://objects.test/" + name, "size_bytes": len(objects.data[name]),
					"content_type": "application/octet-stream", "created_at": time.Unix(1+int64(i), 0),
				})
				if name == "report.pdf" {
					middleID = id
				}
			}
			// Even a corrupt row referencing this message cannot export another workspace's file.
			otherWorkspace := fx.Workspace(t, "Other workspace", "other-"+uuid.NewString())
			fx.Insert(t, "attachment", dbfx.Cols{
				"workspace_id": otherWorkspace, "chat_session_id": chat, "chat_message_id": messageID,
				"uploader_type": "agent", "uploader_id": agent, "filename": "secret.txt",
				"url": "https://objects.test/secret.txt", "size_bytes": 10, "content_type": "text/plain",
			})
			pending, err := q.ListPendingMattermostReplies(ctx)
			if err != nil || !slices.Contains(pending, taskID) {
				t.Fatalf("reply not discoverable without an event: %v, %v", pending, err)
			}
			worker := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			if tc.failure != "storage" {
				worker.EnableFileDelivery(objects)
			}
			err = worker.processTask(ctx, taskID)
			wantRetry := slices.Contains([]string{"upload", "post", "lost-response", "deleted"}, tc.failure)
			if (err != nil) != wantRetry {
				t.Fatalf("first delivery error = %v, want retry = %v", err, wantRetry)
			}
			if tc.failure == "deleted" {
				if _, err := fx.Pool.Exec(ctx, `DELETE FROM attachment WHERE id=$1`, middleID); err != nil {
					t.Fatal(err)
				}
			}
			// Recovery uses a new process's state, then a repeated event must do nothing.
			restarted := NewOutbound(q, fx.Pool, nil, policy, srv.Client(), nil)
			if tc.failure != "storage" {
				restarted.EnableFileDelivery(objects)
			}
			for range 2 {
				if err := restarted.processTask(ctx, taskID); err != nil {
					t.Fatal(err)
				}
			}
			textChunks := chunkMessage(tc.text, maxPostRunes)
			if strings.TrimSpace(tc.text) == "" {
				textChunks = nil
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != len(textChunks)+3 {
				t.Fatalf("posts = %d, want %d", len(posts), len(textChunks)+3)
			}
			var fileIDs []string
			notices := 0
			for _, post := range posts {
				if post.ChannelID != "room" || post.RootID != "thread" {
					t.Fatalf("post escaped its delivery target: %+v", post)
				}
				fileIDs = append(fileIDs, post.FileIDs...)
				if post.Message == attachmentUnavailableText {
					notices++
				}
			}
			wantFiles := []string{"file-chart.png", "file-report.pdf", "file-notes.txt"}
			if tc.wantNotices == 1 {
				wantFiles = []string{"file-chart.png", "file-notes.txt"}
			} else if tc.wantNotices == 3 {
				wantFiles = nil
			}
			if !slices.Equal(fileIDs, wantFiles) || notices != tc.wantNotices {
				t.Fatalf("files = %v, notices = %d; want %v, %d", fileIDs, notices, wantFiles, tc.wantNotices)
			}
			if tc.failure != "storage" && (uploads["chart.png"] != 1 || uploads["notes.txt"] != 1) {
				t.Fatalf("recovery uploaded delivered attachments again: %v", uploads)
			}
			if tc.failure == "lost-response" && uploads["report.pdf"] != 1 {
				t.Fatal("reconciliation repeated a confirmed upload")
			}
			state, err := q.GetChannelReplyDelivery(ctx, taskID)
			if err != nil || state.Phase != "settled" || int(state.ChunksSent) != len(posts) {
				t.Fatalf("delivery progress = %+v, %v", state, err)
			}
			message, err := q.GetChatMessageByTaskAssistant(ctx, taskID)
			if err != nil || len(message.ChannelOutboundMessageIds) != len(posts) {
				t.Fatalf("reply provenance = %+v, %v", message, err)
			}
			snapshot, err := q.GetChannelTaskDelivery(ctx, taskID)
			if err != nil || !strings.Contains(string(snapshot.Config), `"channel_id": "room"`) {
				t.Fatalf("attachment snapshot changed routing: %s, %v", snapshot.Config, err)
			}
		})
	}
}
