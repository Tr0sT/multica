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
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const replyImageID = "11111111-1111-4111-8111-111111111111"

var replyPNG = []byte("\x89PNG\r\n\x1a\nimage data")

type replyImageQueryStub struct {
	installQueries
	row    db.Attachment
	err    error
	params db.GetAttachmentParams
	rows   map[pgtype.UUID]db.Attachment
}

func (q *replyImageQueryStub) GetAttachment(_ context.Context, params db.GetAttachmentParams) (db.Attachment, error) {
	q.params = params
	if q.rows != nil {
		row, ok := q.rows[params.ID]
		if !ok {
			return db.Attachment{}, pgx.ErrNoRows
		}
		return row, nil
	}
	return q.row, q.err
}

func TestDeniedReplyValidation(t *testing.T) {
	for _, reply := range []DeniedReply{
		{Mode: "unknown"}, {Mode: "text", Text: strings.Repeat("x", 4001)},
		{Mode: "text", ImageAttachmentID: replyImageID}, {Mode: "image"},
		{Mode: "image", ImageAttachmentID: "invalid"}, {Mode: "image", ImageAttachmentID: replyImageID, Text: "caption"},
		{Mode: "images"}, {Mode: "images", ImageAttachmentIDs: []string{"invalid"}},
		{Mode: "images", ImageAttachmentIDs: []string{replyImageID, replyImageID}},
		{Mode: "images", ImageAttachmentIDs: []string{replyImageID}, Text: "caption"},
		{Mode: "images", ImageAttachmentIDs: []string{replyImageID}, ImageAttachmentID: replyImageID},
		{Mode: "text", ImageAttachmentIDs: []string{replyImageID}},
	} {
		if _, err := reply.normalized(); !errors.Is(err, ErrInvalidDeniedReply) {
			t.Fatalf("accepted %+v: %v", reply, err)
		}
	}
	for _, reply := range []DeniedReply{{}, {Mode: "text", Text: "Not today\nPlease contact us."}, {Mode: "image", ImageAttachmentID: replyImageID}} {
		if _, err := reply.normalized(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeniedReplyKeepsSingleImagesAndAllowsLargerSets(t *testing.T) {
	legacy, err := (DeniedReply{Mode: "image", ImageAttachmentID: replyImageID}).normalized()
	if err != nil || !reflect.DeepEqual(legacy, DeniedReply{Mode: "images", ImageAttachmentIDs: []string{replyImageID}}) {
		t.Fatalf("legacy image lost: %+v, %v", legacy, err)
	}
	ids := make([]string, 11)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	set, err := (DeniedReply{Mode: "images", ImageAttachmentIDs: ids}).normalized()
	if err != nil || !reflect.DeepEqual(set.ImageAttachmentIDs, ids) {
		t.Fatalf("image set was limited or changed: %+v, %v", set, err)
	}
}

func TestEveryReplyImageIsValidatedBeforeSaving(t *testing.T) {
	ws := mmUUID(t, "22222222-2222-4222-8222-222222222222")
	first := mmUUID(t, replyImageID)
	second := mmUUID(t, "33333333-3333-4333-8333-333333333333")
	q := &replyImageQueryStub{rows: map[pgtype.UUID]db.Attachment{
		first:  {WorkspaceID: ws, Url: "https://objects.test/reply"},
		second: {WorkspaceID: ws, Url: "https://objects.test/reply", ChatSessionID: first},
	}}
	svc := &InstallService{q: q, objects: &testObjectStore{data: map[string][]byte{"reply": replyPNG}}}
	reply := DeniedReply{Mode: "images", ImageAttachmentIDs: []string{replyImageID, second.String()}}
	if _, err := svc.UpdateSettings(context.Background(), first, ws, SettingsUpdate{DeniedReply: &reply}); !errors.Is(err, ErrInvalidDeniedReply) {
		t.Fatalf("private image later in the set was accepted: %v", err)
	}
	if q.params.ID != second {
		t.Fatal("only the first image was validated")
	}
}

func TestReplierChoosesOneRandomImagePerMessage(t *testing.T) {
	ws := mmUUID(t, "22222222-2222-4222-8222-222222222222")
	ids := []string{replyImageID, "33333333-3333-4333-8333-333333333333"}
	q := &replyImageQueryStub{rows: make(map[pgtype.UUID]db.Attachment)}
	objects := &testObjectStore{data: make(map[string][]byte)}
	for i, id := range ids {
		key := fmt.Sprintf("reply-%d", i)
		q.rows[mmUUID(t, id)] = db.Attachment{WorkspaceID: ws, Url: "https://objects.test/" + key, Filename: key + ".png"}
		objects.data[key] = append(append([]byte{}, replyPNG...), byte(i))
	}
	var mu sync.Mutex
	selected := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == apiPath+"/files" {
			data, _ := io.ReadAll(r.Body)
			fileID := ""
			for key, bytes := range objects.data {
				if string(bytes) == string(data) {
					fileID = key
				}
			}
			if fileID == "" {
				t.Error("uploaded an image outside the configured set")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_infos": []map[string]string{{"id": fileID}}})
			return
		}
		var post Post
		if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
			t.Error(err)
		}
		if post.Message != "" || len(post.FileIDs) != 1 {
			t.Errorf("reply did not contain exactly one image: %+v", post)
		} else {
			selected[post.FileIDs[0]]++
		}
		post.ID = "created"
		_ = json.NewEncoder(w).Encode(post)
	}))
	defer srv.Close()
	config, err := json.Marshal(installConfig{ServerURL: srv.URL, AccessTokenEncrypted: base64.StdEncoding.EncodeToString([]byte("token")), AccessMode: AccessAllowlist,
		DeniedReply: DeniedReply{Mode: "images", ImageAttachmentIDs: ids}})
	if err != nil {
		t.Fatal(err)
	}
	replier := NewOutboundReplier(OutboundReplierConfig{Queries: q, Objects: objects, Policy: testServerPolicy(srv.URL), HTTPClient: srv.Client()})
	for i := 0; i < 128; i++ {
		replier.Reply(context.Background(), engine.ResolvedInstallation{WorkspaceID: ws, Platform: db.ChannelInstallation{Config: config}}, dmInbound(), engine.Result{Outcome: engine.OutcomeDropped, DropReason: engine.DropReasonNonWorkspaceMember})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(selected) != 2 || selected["reply-0"]+selected["reply-1"] != 128 {
		t.Fatalf("replies did not select randomly from the set: %v", selected)
	}
}

func TestDeniedReplyImageRequiresStandaloneWorkspaceImage(t *testing.T) {
	wsID := mmUUID(t, "22222222-2222-4222-8222-222222222222")
	valid := db.Attachment{WorkspaceID: wsID, Url: "https://objects.test/reply", Filename: "reply.png"}
	for _, change := range []string{"none", "other workspace", "issue", "comment", "session", "message", "task", "source context", "size", "html", "missing", "external", "missing object", "actual size"} {
		t.Run(change, func(t *testing.T) {
			q := &replyImageQueryStub{row: valid}
			objects := &testObjectStore{data: map[string][]byte{"reply": replyPNG}}
			foreign := mmUUID(t, replyImageID)
			switch change {
			case "other workspace":
				q.row.WorkspaceID = foreign
			case "issue":
				q.row.IssueID = foreign
			case "comment":
				q.row.CommentID = foreign
			case "session":
				q.row.ChatSessionID = foreign
			case "message":
				q.row.ChatMessageID = foreign
			case "task":
				q.row.TaskID = foreign
			case "source context":
				q.row.SourceContextID = foreign
			case "size":
				q.row.SizeBytes = maxDeniedReplyImageBytes + 1
			case "html":
				objects.data["reply"] = []byte("<html><script>alert(1)</script></html>")
			case "missing":
				q.err = pgx.ErrNoRows
			case "external":
				q.row.Url = "https://private.example/secret.png"
			case "missing object":
				objects.data = nil
			case "actual size":
				objects.data["reply"] = append(replyPNG, make([]byte, maxDeniedReplyImageBytes)...)
			}
			data, _, err := loadDeniedReplyImage(context.Background(), q, objects, wsID, replyImageID)
			if q.params.WorkspaceID != wsID || q.params.ID != foreign {
				t.Fatal("image lookup lost its workspace or attachment scope")
			}
			if change == "none" {
				if err != nil || string(data) != string(replyPNG) {
					t.Fatalf("valid image rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("unsafe image accepted: %s", change)
			}
		})
	}
}

func TestUpdateDeniedReplyDoesNotResetAccessOrContext(t *testing.T) {
	q := &contextUpdateQueries{}
	reply := DeniedReply{Mode: "text", Text: "Members only"}
	if _, err := (&InstallService{q: q}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{DeniedReply: &reply}); err != nil {
		t.Fatal(err)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(q.params.Settings, &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || string(patch["denied_reply"]) != `{"mode":"text","text":"Members only"}` {
		t.Fatalf("unexpected patch: %s", q.params.Settings)
	}
}

func TestDeniedReplyImageFailureSendsOnlyTheStandardRefusal(t *testing.T) {
	for _, failure := range []string{"missing", "private", "upload"} {
		t.Run(failure, func(t *testing.T) {
			var posts []Post
			uploads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == apiPath+"/files" {
					uploads++
					w.WriteHeader(http.StatusForbidden)
					return
				}
				var post Post
				if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
					t.Error(err)
				}
				posts = append(posts, post)
				post.ID = "created"
				_ = json.NewEncoder(w).Encode(post)
			}))
			defer srv.Close()
			wsID := mmUUID(t, "22222222-2222-4222-8222-222222222222")
			q := &replyImageQueryStub{row: db.Attachment{WorkspaceID: wsID, Url: "https://objects.test/reply", Filename: "reply.png"}}
			if failure == "missing" {
				q.err = pgx.ErrNoRows
			}
			if failure == "private" {
				q.row.ChatSessionID = mmUUID(t, replyImageID)
			}
			config, err := json.Marshal(installConfig{ServerURL: srv.URL, AccessTokenEncrypted: base64.StdEncoding.EncodeToString([]byte("token")),
				AccessMode: AccessAllowlist, DeniedReply: DeniedReply{Mode: "image", ImageAttachmentID: replyImageID}})
			if err != nil {
				t.Fatal(err)
			}
			replier := NewOutboundReplier(OutboundReplierConfig{Queries: q, Objects: &testObjectStore{data: map[string][]byte{"reply": replyPNG}}, Policy: testServerPolicy(srv.URL), HTTPClient: srv.Client()})
			replier.Reply(context.Background(), engine.ResolvedInstallation{WorkspaceID: wsID, Platform: db.ChannelInstallation{Config: config}}, dmInbound(), engine.Result{
				Outcome: engine.OutcomeDropped, DropReason: engine.DropReasonNonWorkspaceMember,
			})
			if len(posts) != 1 || posts[0].Message != defaultDeniedReplyText || len(posts[0].FileIDs) != 0 {
				t.Fatalf("unexpected failure reply: %+v", posts)
			}
			if failure != "upload" && uploads != 0 {
				t.Fatal("uploaded an unavailable or private image")
			}
		})
	}
}
