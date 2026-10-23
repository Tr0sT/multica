package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

const teamBridgeTokenLimit = 16 << 10

func newTeamBridgeTokenCommand() *cobra.Command {
	command := &cobra.Command{Use: "token", Short: "Back up or restore a bot token as a human workspace owner or admin"}
	export := &cobra.Command{
		Use: "export <installation-id>", Short: "Write the stored bot token to a new private file", Args: exactArgs(1), RunE: runTeamBridgeTokenExport,
	}
	export.Flags().String("output-file", "", "New destination file (0600 on POSIX; existing files and symlinks are refused)")
	importToken := &cobra.Command{
		Use: "import <installation-id>", Short: "Restore a bot token without changing settings, identity or active/revoked status", Args: exactArgs(1), RunE: runTeamBridgeTokenImport,
	}
	importToken.Flags().String("token-file", "", "Read a raw token from a private file of at most 16 KiB")
	importToken.Flags().Bool("token-stdin", false, "Read the raw token from stdin instead of a file")
	command.AddCommand(export, importToken)
	return command
}

// Bound and validate the complete secret response before APIClient decodes it.
// APIClient retains the normal profile headers, timeouts and error classification.
type teamBridgeTokenTransport struct{ base http.RoundTripper }

func (transport teamBridgeTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	if response.StatusCode >= 400 {
		return response, nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected teambridge token API status")
	}
	// A JSON string may encode each token byte as six characters.
	const responseLimit = 6*teamBridgeTokenLimit + 4096
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit || !json.Valid(data) {
		return nil, fmt.Errorf("invalid teambridge token response")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

func newTeamBridgeTokenClient(cmd *cobra.Command) (*cli.APIClient, error) {
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	client.HTTPClient.Transport = teamBridgeTokenTransport{base: client.HTTPClient.Transport}
	return client, nil
}

func runTeamBridgeTokenExport(cmd *cobra.Command, args []string) error {
	id, err := fullTeamBridgeUUID(args[0])
	if err != nil {
		return fmt.Errorf("teambridge token export requires a full installation UUID")
	}
	destination, _ := cmd.Flags().GetString("output-file")
	if destination == "" {
		return fmt.Errorf("teambridge token export requires --output-file; the token is never written to stdout")
	}
	client, err := newTeamBridgeTokenClient(cmd)
	if err != nil {
		return err
	}
	directory, name, err := openTeamBridgeTokenDirectory(destination, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	if _, err := directory.Lstat(name); !os.IsNotExist(err) {
		return fmt.Errorf("teambridge token output must be a new file; existing files and symlinks are refused")
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var response struct {
		InstallationID string `json:"installation_id"`
		WorkspaceID    string `json:"workspace_id"`
		AccessToken    string `json:"access_token"`
	}
	if err := client.GetJSON(ctx, teamBridgeInstallationPath(client)+"/"+id+"/token", &response); err != nil {
		return safeTeamBridgeError(err)
	}
	if response.InstallationID != id || response.WorkspaceID != client.WorkspaceID || !validTeamBridgeToken(response.AccessToken) {
		return fmt.Errorf("invalid teambridge token response; check server compatibility and installation scope")
	}
	if err := writeTeamBridgeToken(directory, name, []byte(response.AccessToken)); err != nil {
		return err
	}
	return printTeamBridgeTokenResult(cmd, map[string]any{"installation_id": id, "workspace_id": client.WorkspaceID, "exported": true})
}

func runTeamBridgeTokenImport(cmd *cobra.Command, args []string) error {
	id, err := fullTeamBridgeUUID(args[0])
	if err != nil {
		return fmt.Errorf("teambridge token import requires a full installation UUID")
	}
	client, err := newTeamBridgeTokenClient(cmd)
	if err != nil {
		return err
	}
	token, err := readTeamBridgeToken(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var response struct {
		InstallationID string `json:"installation_id"`
		WorkspaceID    string `json:"workspace_id"`
		Updated        *bool  `json:"updated"`
	}
	if err := client.PutJSON(ctx, teamBridgeInstallationPath(client)+"/"+id+"/token", map[string]string{"access_token": token}, &response); err != nil {
		var httpError *cli.HTTPError
		if errors.As(err, &httpError) && httpError.StatusCode == http.StatusBadRequest {
			return cli.WithUserMessage("teambridge token restore rejected; provide a valid token for this installation's existing bot account and server", safeTeamBridgeError(err))
		}
		return safeTeamBridgeError(err)
	}
	if response.InstallationID != id || response.WorkspaceID != client.WorkspaceID || response.Updated == nil {
		return fmt.Errorf("invalid teambridge token restore response; check server compatibility and installation scope")
	}
	return printTeamBridgeTokenResult(cmd, map[string]any{"installation_id": id, "workspace_id": client.WorkspaceID, "updated": *response.Updated})
}

func printTeamBridgeTokenResult(cmd *cobra.Command, result any) error {
	if cli.PrintJSON(cmd.OutOrStdout(), result) != nil {
		return fmt.Errorf("cannot write teambridge token operation confirmation")
	}
	return nil
}

func validTeamBridgeToken(token string) bool {
	if len(token) == 0 || len(token) > teamBridgeTokenLimit || strings.TrimSpace(token) != token || !utf8.ValidString(token) {
		return false
	}
	for _, c := range token {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func readTeamBridgeToken(cmd *cobra.Command) (string, error) {
	filename, _ := cmd.Flags().GetString("token-file")
	stdin, _ := cmd.Flags().GetBool("token-stdin")
	if (filename == "") == !stdin {
		return "", fmt.Errorf("provide exactly one of --token-file or --token-stdin")
	}
	var reader io.Reader = cmd.InOrStdin()
	if filename != "" {
		directory, name, err := openTeamBridgeTokenDirectory(filename, false)
		if err != nil {
			return "", err
		}
		defer directory.Close()
		info, err := directory.Lstat(name)
		if err != nil || !validTeamBridgeTokenFile(info) {
			return "", fmt.Errorf("teambridge token input must be a private regular file of at most 16 KiB, not a symlink")
		}
		file, err := openTeamBridgeTokenInput(directory, name)
		if err != nil {
			return "", fmt.Errorf("cannot open teambridge token input")
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !validTeamBridgeTokenFile(opened) || !os.SameFile(info, opened) {
			return "", fmt.Errorf("teambridge token input changed while opening it")
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, teamBridgeTokenLimit+1))
	if err != nil || len(data) > teamBridgeTokenLimit {
		return "", fmt.Errorf("cannot read teambridge token input, or input exceeds 16 KiB")
	}
	token := strings.TrimSpace(string(data))
	if !validTeamBridgeToken(token) {
		return "", fmt.Errorf("teambridge token input is empty or invalid")
	}
	return token, nil
}

func validTeamBridgeTokenFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() <= teamBridgeTokenLimit &&
		(runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0)
}

// Hold each directory open while descending: later path replacement cannot
// redirect file reads/writes. Reject symlinks and Windows junctions, including
// ancestor components, and compare opened handles with the inspected entries.
func openTeamBridgeTokenDirectory(filename string, create bool) (*os.Root, string, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve teambridge token path")
	}
	name := filepath.Base(abs)
	if !filepath.IsLocal(name) || name == "." || strings.ContainsAny(name, `\/:`) {
		return nil, "", fmt.Errorf("teambridge token path must name a regular file")
	}
	volume := filepath.VolumeName(abs)
	directory, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, "", fmt.Errorf("cannot open teambridge token directory")
	}
	path := strings.TrimPrefix(filepath.Dir(abs), volume)
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		info, statErr := directory.Lstat(component)
		if os.IsNotExist(statErr) && create {
			if err := directory.Mkdir(component, 0o700); err != nil {
				directory.Close()
				return nil, "", fmt.Errorf("cannot create private teambridge token directory")
			}
			info, statErr = directory.Lstat(component)
		}
		if statErr != nil || !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			directory.Close()
			return nil, "", fmt.Errorf("teambridge token path must use existing directories without symlinks or junctions")
		}
		next, openErr := directory.OpenRoot(component)
		if openErr != nil {
			directory.Close()
			return nil, "", fmt.Errorf("cannot open teambridge token directory")
		}
		opened, openedErr := next.Stat(".")
		directory.Close()
		if openedErr != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, "", fmt.Errorf("teambridge token directory changed while opening it")
		}
		directory = next
	}
	return directory, name, nil
}

func writeTeamBridgeToken(directory *os.Root, name string, data []byte) error {
	// O_EXCL rejects a file or symlink inserted after the preflight check.
	file, err := directory.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create teambridge token output; existing files are never overwritten")
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("cannot inspect private teambridge token output")
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			if current, err := directory.Lstat(name); err == nil && os.SameFile(info, current) {
				_ = directory.Remove(name)
			}
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("cannot restrict teambridge token output permissions")
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("cannot write teambridge token output")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cannot save teambridge token output")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close teambridge token output")
	}
	complete = true
	return nil
}
