package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTClientGetMe(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(User{ID: "bot1", Username: "multica", IsBot: true})
	}))
	defer srv.Close()

	me, err := newRESTClient(srv.URL, "tok123", srv.Client()).GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != "bot1" || me.Username != "multica" || !me.IsBot {
		t.Errorf("user = %+v, want the bot identity", me)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want a bearer token", gotAuth)
	}
	if gotPath != "/api/v4/users/me" {
		t.Errorf("path = %q, want /api/v4/users/me", gotPath)
	}
}

// A Mattermost mounted at a sub-path must have every API call land under it.
func TestRESTClientHonorsSubPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(User{ID: "bot1"})
	}))
	defer srv.Close()

	if _, err := newRESTClient(srv.URL+"/mattermost", "tok", srv.Client()).GetMe(context.Background()); err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if gotPath != "/mattermost/api/v4/users/me" {
		t.Errorf("path = %q, want the sub-path preserved", gotPath)
	}
}

func TestRESTClientErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "structured mattermost error",
			status:     http.StatusUnauthorized,
			body:       `{"id":"api.context.session_expired","message":"Invalid or expired session"}`,
			wantStatus: http.StatusUnauthorized,
			wantMsg:    "Invalid or expired session",
		},
		{
			// A reverse proxy in front of Mattermost may return plain text.
			name:       "unstructured proxy error",
			status:     http.StatusBadGateway,
			body:       "upstream unavailable",
			wantStatus: http.StatusBadGateway,
			wantMsg:    "upstream unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := newRESTClient(srv.URL, "tok", srv.Client()).GetMe(context.Background())
			if err == nil {
				t.Fatal("GetMe succeeded, want an error")
			}
			if got := statusOf(err); got != tc.wantStatus {
				t.Errorf("statusOf = %d, want %d", got, tc.wantStatus)
			}
			var ae *apiError
			if !errors.As(err, &ae) {
				t.Fatalf("error %v is not an *apiError", err)
			}
			if !strings.Contains(ae.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", ae.Message, tc.wantMsg)
			}
		})
	}
}

// A redirect must not be followed: it is the one way a misconfigured or
// hostile deployment could bounce the bearer token to another host.
func TestRESTClientRefusesRedirects(t *testing.T) {
	var leakedTo string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedTo = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(User{ID: "attacker"})
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/v4/users/me", http.StatusFound)
	}))
	defer srv.Close()

	// newRESTClient's own client is the one under test here, so do not pass the
	// httptest client (which follows redirects by default).
	_, err := newRESTClient(srv.URL, "tok", nil).GetMe(context.Background())
	if err == nil {
		t.Fatal("GetMe followed the redirect and succeeded, want an error")
	}
	if statusOf(err) != http.StatusFound {
		t.Errorf("statusOf = %d, want the 302 surfaced as the failure", statusOf(err))
	}
	if leakedTo != "" {
		t.Fatalf("token was sent to the redirect target (%q)", leakedTo)
	}
}

// Bot API errors must not quote the request in a way that could carry a
// credential into a log line.
func TestRequestErrorHidesTheURL(t *testing.T) {
	err := &requestError{method: http.MethodGet, cause: errors.New(`Get "https://mm.example.com/api/v4/users/me": dial tcp: refused`)}
	if strings.Contains(err.Error(), "mm.example.com") {
		t.Fatalf("requestError.Error() quoted the URL: %s", err)
	}
	// Unwrap still exposes the cause for errors.Is / errors.As classification.
	if err.Unwrap() == nil {
		t.Fatal("Unwrap returned nil, want the transport cause")
	}
}

func TestRESTClientCreateAndUpdatePost(t *testing.T) {
	var created, updated Post
	var updatePath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiPath+"/posts":
			_ = json.NewDecoder(r.Body).Decode(&created)
			created.ID = "new1"
			_ = json.NewEncoder(w).Encode(created)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, apiPath+"/posts/"):
			updatePath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_ = json.NewEncoder(w).Encode(updated)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newRESTClient(srv.URL, "tok", srv.Client())
	got, err := c.CreatePost(context.Background(), Post{ChannelID: "c1", Message: "hi", RootID: "r1"})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if got.ID != "new1" {
		t.Errorf("created id = %q, want new1", got.ID)
	}
	if created.ChannelID != "c1" || created.Message != "hi" || created.RootID != "r1" {
		t.Errorf("sent post = %+v, want the fields preserved", created)
	}

	if _, err := c.UpdatePost(context.Background(), "new1", "edited"); err != nil {
		t.Fatalf("UpdatePost: %v", err)
	}
	if updatePath != apiPath+"/posts/new1" {
		t.Errorf("update path = %q, want the post id in it", updatePath)
	}
	if updated.Message != "edited" {
		t.Errorf("updated message = %q, want edited", updated.Message)
	}
}

func TestRESTClientGetPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath+"/posts/root1" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Post{ID: "root1", UserID: "bot1", Message: "thread root"})
	}))
	defer srv.Close()

	got, err := newRESTClient(srv.URL, "tok", srv.Client()).GetPost(context.Background(), "root1")
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if got.UserID != "bot1" {
		t.Errorf("author = %q, want bot1", got.UserID)
	}
}

func TestRESTClientUploadFile(t *testing.T) {
	data := []byte{0, 1, 0xff, '\n'}
	const filename = "схема & report.png"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/subpath"+apiPath+"/files" ||
			r.URL.Query().Get("channel_id") != "room" || r.URL.Query().Get("filename") != filename {
			t.Errorf("unexpected upload request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("upload headers missing")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != string(data) {
			t.Errorf("uploaded bytes = %v, %v", body, err)
		}
		_, _ = io.WriteString(w, `{"file_infos":[{"id":"file-1"}]}`)
	}))
	defer srv.Close()
	id, err := newRESTClient(srv.URL+"/subpath", "test-token", srv.Client()).UploadFile(context.Background(), "room", filename, data)
	if err != nil || id != "file-1" {
		t.Fatalf("UploadFile = %q, %v", id, err)
	}
}

func TestRESTClientUploadRejectsInvalidAcknowledgements(t *testing.T) {
	for _, body := range []string{`{}`, `{"file_infos":[]}`, `{"file_infos":[{}]}`, `{"file_infos":[{"id":"a"},{"id":"b"}]}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			if _, err := newRESTClient(srv.URL, "test-token", srv.Client()).UploadFile(context.Background(), "room", "file.txt", []byte("hello")); err == nil {
				t.Fatal("malformed upload response accepted")
			}
		})
	}
}

func TestRESTClientUploadRefusesRedirects(t *testing.T) {
	var leaked bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := newRESTClient(srv.URL, "test-token", nil).UploadFile(context.Background(), "room", "secret.txt", []byte("private"))
	if statusOf(err) != http.StatusTemporaryRedirect || leaked {
		t.Fatalf("redirect result = %v, leaked = %v", err, leaked)
	}
}
