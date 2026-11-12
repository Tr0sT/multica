package handler

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
)

func (h *Handler) DownloadMattermostFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.MattermostFiles == nil {
		writeError(w, http.StatusServiceUnavailable, "Mattermost file links are unavailable")
		return
	}
	byteRange := r.Header.Get("Range")
	if len(byteRange) > 128 || strings.Contains(byteRange, ",") || byteRange != "" && !strings.HasPrefix(byteRange, "bytes=") {
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "invalid file range")
		return
	}
	response, info, err := h.MattermostFiles.Open(r.Context(), r.URL.Query().Get("ticket"), byteRange)
	if err != nil {
		if errors.Is(err, mattermost.ErrFileLinkUnavailable) {
			writeError(w, http.StatusGone, err.Error())
		} else {
			writeError(w, http.StatusBadGateway, "could not download the Mattermost file")
		}
		return
	}
	defer response.Body.Close()
	filename := path.Base(strings.ReplaceAll(info.Name, "\\", "/"))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		// Abort a truncated response instead of presenting a partial file as complete.
		panic(http.ErrAbortHandler)
	}
}
