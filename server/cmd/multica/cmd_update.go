package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var updateDownloadTimeout time.Duration = cli.DefaultUpdateDownloadTimeout

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update multica to the latest version",
	RunE:  runUpdate,
}

func init() {
	updateCmd.Flags().DurationVar(&updateDownloadTimeout, "download-timeout", cli.DefaultUpdateDownloadTimeout, "Maximum time to wait for the release archive download")
}

func runUpdate(_ *cobra.Command, _ []string) error {
	if updateDownloadTimeout <= 0 {
		return fmt.Errorf("download timeout must be greater than zero")
	}

	fmt.Fprintf(os.Stderr, "Current version: %s (commit: %s, built: %s)\n", version, commit, date)

	// Check the configured release source.
	latest, err := cli.FetchLatestRelease()
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	latestVer := strings.TrimPrefix(latest.TagName, "v")
	currentVer := strings.TrimPrefix(version, "v")
	if currentVer == latestVer {
		fmt.Fprintln(os.Stderr, "Already up to date.")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Latest version:  %s\n\n", latest.TagName)

	// The upstream Homebrew tap would replace the fork, so download the fork release directly.
	targetVersion := latest.TagName
	fmt.Fprintf(os.Stderr, "Downloading %s from the configured release source...\n", targetVersion)
	output, err := cli.UpdateViaDownloadWithTimeout(targetVersion, updateDownloadTimeout)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s\nUpdate complete.\n", output)
	return nil
}
