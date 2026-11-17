package mattermost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type testObjectStore struct {
	mu    sync.Mutex
	data  map[string][]byte
	reads []string
}

func (s *testObjectStore) KeyFromURL(raw string) string {
	if !strings.HasPrefix(raw, "https://objects.test/") {
		return ""
	}
	return strings.TrimPrefix(raw, "https://objects.test/")
}

func (s *testObjectStore) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, key)
	data, ok := s.data[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestUploadAttachmentUsesStoredBytesAndFilename(t *testing.T) {
	objects := &testObjectStore{data: map[string][]byte{"chart": {0x89, 'P', 'N', 'G'}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "диаграмма.png" || r.URL.Query().Get("channel_id") != "room" {
			t.Errorf("upload query = %v", r.URL.Query())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, objects.data["chart"]) {
			t.Errorf("body = %v, %v", body, err)
		}
		_, _ = io.WriteString(w, `{"file_infos":[{"id":"chart-id"}]}`)
	}))
	defer srv.Close()
	o := &Outbound{}
	o.EnableFileDelivery(objects)
	id, err := o.uploadAttachment(context.Background(), newRESTClient(srv.URL, "token", srv.Client()), "room", db.Attachment{
		Url: "https://objects.test/chart", Filename: `C:\private\диаграмма.png`, ContentType: "image/png", SizeBytes: 4,
	})
	if err != nil || id != "chart-id" {
		t.Fatalf("upload = %q, %v", id, err)
	}
}

func TestUploadAttachmentRefusesUnstoredAndOversizedFiles(t *testing.T) {
	for _, row := range []db.Attachment{
		{Url: "https://untrusted.test/private"},
		{Url: "file:///etc/passwd"},
		{Url: "https://objects.test/chart", SizeBytes: maxOutboundFileBytes + 1},
	} {
		objects := &testObjectStore{}
		o := &Outbound{objects: objects}
		if _, err := o.uploadAttachment(context.Background(), nil, "room", row); err == nil {
			t.Fatal("invalid attachment was accepted")
		}
		if len(objects.reads) != 0 {
			t.Fatal("refused attachment was read")
		}
	}
}

func TestUploadAttachmentMissingStorage(t *testing.T) {
	o := &Outbound{}
	if _, err := o.uploadAttachment(context.Background(), nil, "room", db.Attachment{}); err == nil {
		t.Fatal("missing storage was accepted")
	}
}

func TestRetryableUploadError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&apiError{StatusCode: 429}, true},
		{&apiError{StatusCode: 503}, true},
		{&requestError{cause: errors.New("lost response")}, true},
		{context.DeadlineExceeded, true},
		{&apiError{StatusCode: 403}, false},
		{&apiError{StatusCode: 413}, false},
		{fs.ErrNotExist, false},
	} {
		if got := retryableUploadError(tc.err); got != tc.want {
			t.Errorf("retryableUploadError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
