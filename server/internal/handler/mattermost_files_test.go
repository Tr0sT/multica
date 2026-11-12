package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMattermostFileDownloadRejectsMissingInvalidAndUnsupportedRequests(t *testing.T) {
	h := &Handler{}
	testutil.Call(t, h.DownloadMattermostFile, testutil.JSONRequest(http.MethodGet, "/", "")).Want(http.StatusServiceUnavailable)
	links, err := mattermost.NewFileLinks(nil, []byte(strings.Repeat("k", 32)), nil, nil, "https://multica.test")
	if err != nil {
		t.Fatal(err)
	}
	h.MattermostFiles = links
	for _, query := range []string{"", "?ticket=invalid", "?ticket=" + strings.Repeat("x", 5000)} {
		testutil.Call(t, h.DownloadMattermostFile, testutil.JSONRequest(http.MethodGet, "/"+query, "")).Want(http.StatusGone)
	}
	req := testutil.JSONRequest(http.MethodGet, "/", "")
	req.Header.Set("Range", "bytes=0-1,2-3")
	testutil.Call(t, h.DownloadMattermostFile, req).Want(http.StatusRequestedRangeNotSatisfiable)
}
