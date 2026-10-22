package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestAttachmentLinkStreamsPrivatelyToConfiguredServerWithoutOverwriting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Path != "/api/mattermost/files/download" || r.URL.Query().Get("ticket") != "private-ticket" {
			t.Error("link download leaked credentials or used the wrong endpoint")
		}
		w.Header().Set("Content-Disposition", `attachment; filename="../clip.mp4"`)
		_, _ = io.WriteString(w, "video bytes")
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	cmd := newAttachmentDownloadTestCmd()
	cmd.SetContext(t.Context())
	dir := filepath.Join(t.TempDir(), "downloads")
	_ = cmd.Flags().Set("output-dir", dir)
	var out bytes.Buffer
	cmd.SetOut(&out)
	link := "https://public-multica.test/api/mattermost/files/download?ticket=private-ticket"
	if err := runAttachmentDownload(cmd, []string{link}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "clip.mp4"))
	if err != nil || string(data) != "video bytes" || strings.Contains(out.String(), "private-ticket") || !strings.Contains(out.String(), `"filename": "clip.mp4"`) {
		t.Fatalf("download failed: %s, %v", out.String(), err)
	}
	info, _ := os.Stat(filepath.Join(dir, "clip.mp4"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("download is readable by other users")
	}
	if err := runAttachmentDownload(cmd, []string{link}); err == nil {
		t.Fatal("existing file was overwritten")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("temporary downloads were left behind")
	}
}

func TestAttachmentLinkRejectsRedirectsAndTruncatedDownloads(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusFound, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/mattermost/files/download" {
					t.Error("download followed a redirect")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Location", "/redirect-target")
				w.Header().Set("Content-Disposition", `attachment; filename="video.mp4"`)
				w.Header().Set("Content-Length", "100")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "short")
			}))
			defer srv.Close()
			cmd := newAttachmentDownloadTestCmd()
			cmd.SetContext(t.Context())
			dir := t.TempDir()
			_ = cmd.Flags().Set("output-dir", dir)
			client := &cli.APIClient{BaseURL: srv.URL, HTTPClient: srv.Client()}
			err := downloadMattermostFileLink(cmd, client, srv.URL+"/api/mattermost/files/download?ticket=never-print-me")
			if err == nil || strings.Contains(err.Error(), "never-print-me") {
				t.Fatalf("failed download was accepted or leaked its link: %v", err)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("failed download left a partial file")
			}
		})
	}
}
