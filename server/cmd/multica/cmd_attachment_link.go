package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func downloadMattermostFileLink(cmd *cobra.Command, client *cli.APIClient, link string) error {
	const endpoint = "/api/mattermost/files/download"
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || !strings.HasSuffix(u.Path, endpoint) || u.RawPath != "" || len(u.RawQuery) > 5000 {
		return fmt.Errorf("expected a temporary Mattermost file link")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["ticket"]) != 1 || query.Get("ticket") == "" {
		return fmt.Errorf("expected a temporary Mattermost file link")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), cli.AtLeastAPITimeout(5*time.Minute))
	defer cancel()
	// Redeem at the configured backend, including when the daemon uses a local API URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("invalid Multica server URL")
	}
	httpClient := *client.HTTPClient
	httpClient.Timeout = cli.AtLeastAPITimeout(5 * time.Minute)
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Mattermost file download failed; check server connectivity")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Mattermost file download failed (HTTP %d); ask the bot for a fresh link if it expired", response.StatusCode)
	}
	_, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	filename := filepath.Base(strings.ReplaceAll(params["filename"], "\\", "/"))
	if err != nil || filename == "." || !filepath.IsLocal(filename) {
		return fmt.Errorf("Mattermost download returned an invalid filename")
	}
	dir, _ := cmd.Flags().GetString("output-dir")
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".multica-download-*")
	if err != nil {
		return fmt.Errorf("create download: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	size, err := io.Copy(file, response.Body)
	if err != nil {
		return fmt.Errorf("Mattermost download was interrupted; no file was saved")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("finish download: %w", err)
	}
	destination := filepath.Join(dir, filename)
	// Publish only a complete download and never replace an existing file or symlink.
	if err := os.Link(file.Name(), destination); err != nil {
		return fmt.Errorf("save download (choose another directory if the file exists): %w", err)
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"filename": filename, "path": abs, "size": size})
}
