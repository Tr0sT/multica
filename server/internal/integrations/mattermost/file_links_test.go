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
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const testFilePost = "pppppppppppppppppppppppppp"
const testFileChannel = "cccccccccccccccccccccccccc"
const testFileID = "ffffffffffffffffffffffffff"

type fileLinkQueryStub struct {
	row db.ChannelInstallation
}

func (q *fileLinkQueryStub) GetChannelInstallationByAppID(_ context.Context, p db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	if p.ChannelType != "mattermost" || p.AppID != DecodePublicConfig(q.row.Config).ServerURL+":bot" {
		return db.ChannelInstallation{}, errors.New("wrong installation scope")
	}
	return q.row, nil
}

func (q *fileLinkQueryStub) GetChannelInstallationInWorkspace(_ context.Context, p db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error) {
	if p.ChannelType != "mattermost" || p.ID != q.row.ID || p.WorkspaceID != q.row.WorkspaceID {
		return db.ChannelInstallation{}, errors.New("wrong installation or workspace")
	}
	return q.row, nil
}

func fileLinkFixture(t *testing.T, failure string) (*mattermostChannel, *fileLinkQueryStub, *atomic.Int32) {
	t.Helper()
	downloads := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-bot-token" {
			t.Error("file API request lost bot authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == apiPath+"/posts/"+testFilePost:
			post := Post{ID: testFilePost, ChannelID: testFileChannel, FileIDs: []string{testFileID}}
			switch failure {
			case "deleted post":
				post.DeleteAt = 1
			case "different channel":
				post.ChannelID = strings.Repeat("x", 26)
			case "detached file":
				post.FileIDs = nil
			}
			_ = json.NewEncoder(w).Encode(post)
		case r.URL.Path == apiPath+"/channels/"+testFileChannel+"/posts":
			post := Post{ID: testFilePost, ChannelID: testFileChannel, CreateAt: 1, Message: "Here are ten videos."}
			for i := 0; i < 10; i++ {
				post.FileIDs = append(post.FileIDs, fmt.Sprintf("%026d", i))
			}
			_ = json.NewEncoder(w).Encode(postList{Order: []string{post.ID}, Posts: map[string]Post{post.ID: post}})
		case strings.HasSuffix(r.URL.Path, "/info"):
			if failure == "denied metadata" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, apiPath+"/files/"), "/info")
			info := FileInfo{ID: id, PostID: testFilePost, Name: "clip.mp4", MimeType: "video/mp4", Size: 500 << 20}
			if failure == "different post" {
				info.PostID = strings.Repeat("x", 26)
			}
			_ = json.NewEncoder(w).Encode(info)
		case r.URL.Path == apiPath+"/files/"+testFileID:
			downloads.Add(1)
			if failure == "redirect" {
				w.Header().Set("Location", "/should-not-follow")
				w.WriteHeader(http.StatusFound)
				return
			}
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-3/11")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "file")
				return
			}
			_, _ = io.WriteString(w, "file bytes!")
		default:
			t.Errorf("unexpected Mattermost request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := installConfig{AppID: srv.URL + ":bot", ServerURL: srv.URL, BotUserID: "bot", BotUsername: "bot",
		AccessTokenEncrypted: base64.StdEncoding.EncodeToString([]byte("secret-bot-token")), AttachmentMode: AttachmentsLinks}
	encoded, _ := json.Marshal(cfg)
	q := &fileLinkQueryStub{row: db.ChannelInstallation{ID: mmUUID(t, "11111111-1111-4111-8111-111111111111"),
		WorkspaceID: mmUUID(t, "22222222-2222-4222-8222-222222222222"), Config: encoded, Status: "active"}}
	s, err := NewFileLinks(nil, []byte(strings.Repeat("k", 32)), nil, testServerPolicy(srv.URL), "https://multica.test")
	if err != nil {
		t.Fatal(err)
	}
	s.q = q
	return &mattermostChannel{appID: cfg.AppID, botUsername: "bot", serverURL: srv.URL, installConfig: cfg,
		attachmentMode: AttachmentsLinks, fileLinks: s, rest: newRESTClient(srv.URL, "secret-bot-token", srv.Client())}, q, downloads
}

func issueFileTicket(t *testing.T, c *mattermostChannel, q *fileLinkQueryStub) string {
	t.Helper()
	link, err := c.fileLinks.issue(q.row, c.installConfig, Post{ID: testFilePost, ChannelID: testFileChannel}, testFileID)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil || u.Host != "multica.test" || strings.Contains(link, "secret-bot-token") {
		t.Fatal("unsafe file link")
	}
	return u.Query().Get("ticket")
}

func TestFileLinksDownloadOnlyWhenOpened(t *testing.T) {
	c, q, downloads := fileLinkFixture(t, "")
	ticket := issueFileTicket(t, c, q)
	if downloads.Load() != 0 {
		t.Fatal("issuing a link downloaded file content")
	}
	for _, byteRange := range []string{"", "bytes=0-3"} {
		resp, info, err := c.fileLinks.Open(context.Background(), ticket, byteRange)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		want := "file bytes!"
		if byteRange != "" {
			want = "file"
			if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != "bytes 0-3/11" {
				t.Fatal("range response lost")
			}
		}
		if err != nil || string(data) != want || info.Name != "clip.mp4" {
			t.Fatalf("download = %q, info=%+v, err=%v", data, info, err)
		}
	}
	if downloads.Load() != 2 {
		t.Fatal("unexpected download count")
	}
}

func TestFileLinksRejectInvalidOrRevokedGrants(t *testing.T) {
	for _, reason := range []string{"tampered", "expired", "future expiry", "revoked", "workspace moved", "reinstalled", "settings changed", "different file"} {
		t.Run(reason, func(t *testing.T) {
			c, q, downloads := fileLinkFixture(t, "")
			ticket := issueFileTicket(t, c, q)
			switch reason {
			case "tampered":
				sealed, _ := base64.RawURLEncoding.DecodeString(ticket)
				sealed[0] ^= 1
				ticket = base64.RawURLEncoding.EncodeToString(sealed)
			case "revoked":
				q.row.Status = "revoked"
			case "workspace moved":
				q.row.WorkspaceID = mmUUID(t, "33333333-3333-4333-8333-333333333333")
			case "reinstalled":
				q.row.ID = mmUUID(t, "33333333-3333-4333-8333-333333333333")
			case "settings changed":
				cfg := c.installConfig
				cfg.AttachmentMode = AttachmentsNone
				q.row.Config, _ = json.Marshal(cfg)
			default:
				sealed, _ := base64.RawURLEncoding.DecodeString(ticket)
				plain, _ := c.fileLinks.box.Open(sealed)
				var grant fileTicket
				_ = json.Unmarshal(plain, &grant)
				switch reason {
				case "expired":
					grant.Expires = time.Now().Add(-time.Second).Unix()
				case "future expiry":
					grant.Expires = time.Now().Add(48 * time.Hour).Unix()
				case "different file":
					grant.FileID = strings.Repeat("x", 26)
				}
				plain, _ = json.Marshal(grant)
				sealed, _ = c.fileLinks.box.Seal(plain)
				ticket = base64.RawURLEncoding.EncodeToString(sealed)
			}
			if _, _, err := c.fileLinks.Open(context.Background(), ticket, ""); !errors.Is(err, ErrFileLinkUnavailable) {
				t.Fatalf("accepted %s grant: %v", reason, err)
			}
			if downloads.Load() != 0 {
				t.Fatal("rejected grant downloaded bytes")
			}
		})
	}
}

func TestFileLinksRecheckMattermostAccessAndRefuseRedirects(t *testing.T) {
	for _, failure := range []string{"deleted post", "different channel", "detached file", "denied metadata", "different post", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			c, q, downloads := fileLinkFixture(t, failure)
			if _, _, err := c.fileLinks.Open(context.Background(), issueFileTicket(t, c, q), ""); !errors.Is(err, ErrFileLinkUnavailable) {
				t.Fatalf("unsafe file download: %v", err)
			}
			if failure != "redirect" && downloads.Load() != 0 {
				t.Fatal("downloaded an inaccessible file")
			}
		})
	}
}

func TestIncomingFilesBecomeBoundedLinksWithoutDownloading(t *testing.T) {
	c, _, downloads := fileLinkFixture(t, "")
	post := Post{ID: testFilePost, ChannelID: testFileChannel}
	for i := 0; i < maxFileReferences+1; i++ {
		post.FileIDs = append(post.FileIDs, fmt.Sprintf("%026d", i))
	}
	msg := channel.InboundMessage{Text: "summarize the discussion", CommandText: "summarize the discussion", AddressedToBot: true}
	got := c.withHistory(context.Background(), msg, post)
	if strings.Count(got, `"download_url"`) != maxFileReferences || !strings.Contains(got, "truncated") || !strings.Contains(got, `"size_bytes":524288000`) || downloads.Load() != 0 {
		t.Fatal("file references downloaded bytes, lost metadata or exceeded the limit")
	}
	c.attachmentMode = AttachmentsNone
	c.rest = nil
	if got := c.withHistory(context.Background(), msg, post); got != msg.Text {
		t.Fatal("disabled attachments changed the prompt")
	}
}

func TestIncomingMediaOnlyDMReachesAgentAsLinks(t *testing.T) {
	c, _, downloads := fileLinkFixture(t, "")
	var got channel.InboundMessage
	c.handler = func(_ context.Context, msg channel.InboundMessage) error { got = msg; return nil }
	encoded, _ := json.Marshal(postedFrame(Post{ID: testFilePost, ChannelID: testFileChannel, UserID: "human", FileIDs: []string{testFileID}}, "D", "human"))
	var frame wsFrame
	_ = json.Unmarshal(encoded, &frame)
	if err := c.dispatchPosted(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	if got.Type != channel.MsgTypeText || got.CommandText != "Shared attachments" || !strings.Contains(got.Text, `"download_url"`) || downloads.Load() != 0 {
		t.Fatalf("media-only DM lost or eagerly downloaded: %+v", got)
	}
}

func TestHistoryIncludesTenVideoLinksWithoutReadingThem(t *testing.T) {
	c, _, downloads := fileLinkFixture(t, "")
	c.contextSettings = ContextSettings{Mode: ContextChannel, LookbackHours: 24}
	trigger := Post{ID: strings.Repeat("t", 26), ChannelID: testFileChannel, CreateAt: 2}
	msg := channel.InboundMessage{Text: "summarize the discussion", CommandText: "summarize the discussion", AddressedToBot: true}
	got := c.withHistory(context.Background(), msg, trigger)
	if strings.Count(got, `"download_url"`) != 10 || !strings.Contains(got, "Here are ten videos.") || downloads.Load() != 0 {
		t.Fatal("history did not preserve ten lazy file references")
	}
	c.attachmentMode = AttachmentsNone
	got = c.withHistory(context.Background(), msg, trigger)
	if strings.Contains(got, `"download_url"`) || !strings.Contains(got, "Here are ten videos.") {
		t.Fatal("disabling attachments lost text or kept file links")
	}
}
