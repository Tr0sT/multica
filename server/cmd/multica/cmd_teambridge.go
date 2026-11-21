package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	teamBridgeConfigLimit = 32 << 10
	teamBridgeImageLimit  = 5 << 20
)

var (
	teamBridgeCmd      = newTeamBridgeCommand()
	teamBridgeUserID   = regexp.MustCompile(`^[a-z0-9]{26}$`)
	teamBridgeUsername = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
)

func newTeamBridgeCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "teambridge", Aliases: []string{"mattermost"}, Short: "Manage TeamBridge / Mattermost bot integrations",
		Long: "Manage workspace bot installations, portable reply images and administrator-only token backups. Tokens are transferred through private files or stdin, never command arguments or JSON output.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireHumanLocalCommand("teambridge"); err != nil {
				return err
			}
			if inDaemonManagedExecutionContext() || strings.HasPrefix(resolveToken(cmd), cli.TaskTokenPrefix) {
				return fmt.Errorf("teambridge administration is not available inside a daemon-managed task or with a task token")
			}
			output, _ := cmd.Flags().GetString("output")
			if output != "json" {
				return fmt.Errorf("teambridge supports only --output json")
			}
			return nil
		},
	}
	command.PersistentFlags().String("output", "json", "Output format (json)")
	list := &cobra.Command{Use: "list", Short: "List installations and server capabilities", Args: cobra.NoArgs, RunE: runTeamBridgeList}
	install := &cobra.Command{Use: "install", Short: "Connect a bot using a JSON config file or stdin", Args: cobra.NoArgs, RunE: runTeamBridgeInstall}
	install.Flags().String("agent", "", "Full UUID of the Multica agent")
	addTeamBridgeConfigFlags(install)
	update := &cobra.Command{Use: "update <installation-id>", Short: "Update context, access and denied replies without changing the bot token", Args: exactArgs(1), RunE: runTeamBridgeUpdate}
	addTeamBridgeConfigFlags(update)
	revoke := &cobra.Command{Use: "revoke <installation-id>", Short: "Disconnect a bot while retaining its chat history", Args: exactArgs(1), RunE: runTeamBridgeRevoke}
	verify := &cobra.Command{Use: "verify", Short: "Verify a bot token and allowlist account identities without changing anything", Args: cobra.NoArgs, RunE: runTeamBridgeVerify}
	addTeamBridgeConfigFlags(verify)
	images := &cobra.Command{Use: "image", Short: "Transfer standalone images for automatic access-denied replies"}
	download := &cobra.Command{Use: "download <attachment-id>", Short: "Download an access-denied reply image to a local file", Args: exactArgs(1), RunE: runTeamBridgeImageDownload}
	download.Flags().String("output-file", "", "Destination file (written atomically)")
	upload := &cobra.Command{Use: "upload <path>", Short: "Upload a standalone workspace image (PNG, JPEG, GIF or WebP; at most 5 MiB)", Args: exactArgs(1), RunE: runTeamBridgeImageUpload}
	images.AddCommand(download, upload)
	command.AddCommand(list, install, update, revoke, verify, images, newTeamBridgeTokenCommand())
	return command
}

func addTeamBridgeConfigFlags(command *cobra.Command) {
	command.Flags().String("config-file", "", "Read JSON from this file; tokens must never be passed as command arguments")
	command.Flags().Bool("config-stdin", false, "Read JSON from stdin instead of a file")
}

type teamBridgeContext struct {
	Mode          string `json:"mode"`
	LookbackHours int    `json:"lookback_hours,omitempty"`
}

type teamBridgeReply struct {
	Mode               string   `json:"mode"`
	Text               string   `json:"text,omitempty"`
	ImageAttachmentIDs []string `json:"image_attachment_ids,omitempty"`
}

type teamBridgeInstallConfig struct {
	ServerURL        string            `json:"server_url"`
	AccessToken      string            `json:"access_token"`
	Context          teamBridgeContext `json:"context"`
	AttachmentMode   *string           `json:"attachment_mode,omitempty"`
	AccessMode       string            `json:"access_mode"`
	AllowedUsernames *[]string         `json:"allowed_usernames,omitempty"`
	DeniedReply      teamBridgeReply   `json:"denied_reply"`
}

type teamBridgeUpdateConfig struct {
	Context          *teamBridgeContext `json:"context,omitempty"`
	AttachmentMode   *string            `json:"attachment_mode,omitempty"`
	AccessMode       *string            `json:"access_mode,omitempty"`
	AllowedUsernames *[]string          `json:"allowed_usernames,omitempty"`
	DeniedReply      *teamBridgeReply   `json:"denied_reply,omitempty"`
}

type teamBridgeAllowedAccount struct {
	ID       string `json:"id,omitempty"`
	Username string `json:"username"`
}

type teamBridgeVerifyConfig struct {
	ServerURL     string                     `json:"server_url"`
	AccessToken   string                     `json:"access_token"`
	ExpectedBotID string                     `json:"expected_bot_user_id,omitempty"`
	AllowedUsers  []teamBridgeAllowedAccount `json:"allowed_users,omitempty"`
}

type teamBridgeListResponse struct {
	Installations    *[]json.RawMessage `json:"installations"`
	Configured       *bool              `json:"configured"`
	InstallSupported *bool              `json:"install_supported"`
}

func readTeamBridgeConfig(cmd *cobra.Command, target any) error {
	filename, _ := cmd.Flags().GetString("config-file")
	stdin, _ := cmd.Flags().GetBool("config-stdin")
	if (filename == "") == !stdin {
		return fmt.Errorf("provide exactly one of --config-file or --config-stdin")
	}
	var reader io.Reader = cmd.InOrStdin()
	if filename != "" {
		info, err := os.Stat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Size() > teamBridgeConfigLimit {
			return fmt.Errorf("teambridge config must be a readable regular file of at most 32 KiB")
		}
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("cannot read teambridge config file")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, teamBridgeConfigLimit+1))
	if err != nil || len(data) > teamBridgeConfigLimit {
		return fmt.Errorf("cannot read teambridge config, or config exceeds 32 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		// Decoder errors can repeat an unknown field containing a token.
		return fmt.Errorf("invalid teambridge JSON config: check field names, types and that it contains exactly one object")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("teambridge config must be a JSON object")
	}
	return nil
}

func newTeamBridgeAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, safeTeamBridgeError(err)
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, fmt.Errorf("teambridge requires a selected workspace; set --workspace-id or select a CLI profile")
	}
	client.WorkspaceID, err = fullTeamBridgeUUID(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("teambridge requires a full workspace UUID")
	}
	if strings.TrimSpace(client.Token) == "" || strings.HasPrefix(client.Token, cli.TaskTokenPrefix) {
		return nil, fmt.Errorf("teambridge requires a human Multica token; run multica login with the selected profile")
	}
	u, err := url.Parse(client.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid Multica server base URL for teambridge")
	}
	protectTeamBridgeClient(client)
	return client, nil
}

// APIClient supplies the established auth/profile headers and timeout behavior.
// This adapter additionally refuses redirects and sanitizes all errors, including
// --debug output, because install requests carry a bot credential.
func protectTeamBridgeClient(client *cli.APIClient) {
	copy := *client.HTTPClient
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copy.Transport = teamBridgeTransport{base: transport}
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("teambridge API redirects are refused") }
	client.HTTPClient = &copy
}

type teamBridgeTransport struct{ base http.RoundTripper }

func (transport teamBridgeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	if response.StatusCode < 200 || (response.StatusCode >= 300 && response.StatusCode < 400) ||
		(request.Method == http.MethodDelete && response.StatusCode < 300 && response.StatusCode != http.StatusNoContent) {
		response.Body.Close()
		return nil, fmt.Errorf("unexpected teambridge API status %d", response.StatusCode)
	}
	return response, nil
}

func safeTeamBridgeError(err error) error {
	var response *cli.HTTPError
	if errors.As(err, &response) {
		// Preserve the CLI's stable exit classification without retaining a
		// response body or URL that could contain a bot token, even in --debug.
		return cli.WithUserMessage(
			fmt.Sprintf("teambridge request failed (HTTP %d); check server compatibility, workspace permissions and configuration", response.StatusCode),
			&cli.HTTPError{Method: "REQUEST", Path: "teambridge", StatusCode: response.StatusCode, Body: "response omitted"},
		)
	}
	var network *cli.NetworkError
	if errors.As(err, &network) {
		return cli.WithUserMessage("teambridge request failed; check server connectivity and configuration",
			&cli.NetworkError{Kind: network.Kind, Op: "teambridge", Err: errors.New("network request failed")})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cli.WithUserMessage("teambridge request timed out",
			&cli.NetworkError{Kind: cli.KindNetworkTimeout, Op: "teambridge", Err: context.DeadlineExceeded})
	}
	return fmt.Errorf("teambridge request failed; check server connectivity and configuration")
}

func fullTeamBridgeUUID(raw string) (string, error) {
	value, err := util.ParseUUID(raw)
	if err != nil || len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return "", fmt.Errorf("a full UUID is required")
	}
	return util.UUIDToString(value), nil
}

func teamBridgeInstallationPath(client *cli.APIClient) string {
	return "/api/workspaces/" + client.WorkspaceID + "/mattermost/installations"
}

func getTeamBridgeList(ctx context.Context, client *cli.APIClient) (teamBridgeListResponse, error) {
	var response teamBridgeListResponse
	if err := client.GetJSON(ctx, teamBridgeInstallationPath(client), &response); err != nil {
		return response, safeTeamBridgeError(err)
	}
	if response.Installations == nil || response.Configured == nil || response.InstallSupported == nil {
		return response, fmt.Errorf("teambridge server returned an incomplete capability response")
	}
	public := make([]json.RawMessage, 0, len(*response.Installations))
	for _, raw := range *response.Installations {
		value, err := publicTeamBridgeInstallation(raw, client.WorkspaceID)
		if err != nil {
			return response, err
		}
		encoded, _ := json.Marshal(value)
		public = append(public, encoded)
	}
	response.Installations = &public
	return response, nil
}

func runTeamBridgeList(cmd *cobra.Command, _ []string) error {
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	response, err := getTeamBridgeList(ctx, client)
	if err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), response)
}

func runTeamBridgeInstall(cmd *cobra.Command, _ []string) error {
	agent, _ := cmd.Flags().GetString("agent")
	agentID, err := fullTeamBridgeUUID(agent)
	if err != nil {
		return fmt.Errorf("teambridge install requires --agent with a full agent UUID")
	}
	var config teamBridgeInstallConfig
	if err := readTeamBridgeConfig(cmd, &config); err != nil {
		return err
	}
	if err := validateTeamBridgeCredential(config.ServerURL, config.AccessToken); err != nil {
		return err
	}
	if config.Context.Mode == "" {
		config.Context.Mode = "message"
	}
	if config.AccessMode == "" {
		config.AccessMode = "members"
	}
	if config.DeniedReply.Mode == "" {
		config.DeniedReply.Mode = "text"
	}
	if err := validateTeamBridgeSettings(&config.Context, &config.AccessMode, config.AllowedUsernames, &config.DeniedReply, config.AttachmentMode); err != nil {
		return err
	}
	if err := validateTeamBridgeWireSize(config); err != nil {
		return err
	}
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	capabilities, err := getTeamBridgeList(ctx, client)
	if err != nil {
		return err
	}
	if !*capabilities.Configured || !*capabilities.InstallSupported {
		return fmt.Errorf("teambridge integration is disabled or installation is unsupported on this server")
	}
	var raw json.RawMessage
	endpoint := "/api/workspaces/" + client.WorkspaceID + "/mattermost/install?agent_id=" + url.QueryEscape(agentID)
	if err := client.PostJSON(ctx, endpoint, config, &raw); err != nil {
		return safeTeamBridgeError(err)
	}
	public, err := publicTeamBridgeInstallation(raw, client.WorkspaceID)
	if err != nil {
		return err
	}
	if string(public["agent_id"]) != `"`+agentID+`"` || string(public["status"]) != `"active"` {
		return fmt.Errorf("teambridge install returned an unexpected agent or inactive installation")
	}
	return cli.PrintJSON(cmd.OutOrStdout(), public)
}

func runTeamBridgeUpdate(cmd *cobra.Command, args []string) error {
	id, err := fullTeamBridgeUUID(args[0])
	if err != nil {
		return fmt.Errorf("teambridge update requires a full installation UUID")
	}
	var config teamBridgeUpdateConfig
	if err := readTeamBridgeConfig(cmd, &config); err != nil {
		return err
	}
	if config.Context == nil && config.AttachmentMode == nil && config.AccessMode == nil && config.DeniedReply == nil {
		return fmt.Errorf("teambridge update requires context, attachment_mode, access_mode or denied_reply")
	}
	if err := validateTeamBridgeSettings(config.Context, config.AccessMode, config.AllowedUsernames, config.DeniedReply, config.AttachmentMode); err != nil {
		return err
	}
	if err := validateTeamBridgeWireSize(config); err != nil {
		return err
	}
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var raw json.RawMessage
	if err := client.PatchJSON(ctx, teamBridgeInstallationPath(client)+"/"+id, config, &raw); err != nil {
		return safeTeamBridgeError(err)
	}
	public, err := publicTeamBridgeInstallation(raw, client.WorkspaceID)
	if err != nil {
		return err
	}
	if string(public["id"]) != `"`+id+`"` || string(public["status"]) != `"active"` {
		return fmt.Errorf("teambridge update returned an unexpected or inactive installation")
	}
	return cli.PrintJSON(cmd.OutOrStdout(), public)
}

func runTeamBridgeRevoke(cmd *cobra.Command, args []string) error {
	id, err := fullTeamBridgeUUID(args[0])
	if err != nil {
		return fmt.Errorf("teambridge revoke requires a full installation UUID")
	}
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if err := client.DeleteJSON(ctx, teamBridgeInstallationPath(client)+"/"+id); err != nil {
		return safeTeamBridgeError(err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"id": id, "revoked": true})
}

// Keep the server's public wire shapes (including legacy context/image forms),
// while preventing an unexpected credential field in a response from reaching stdout.
func publicTeamBridgeInstallation(raw json.RawMessage, workspaceID string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, fmt.Errorf("invalid teambridge installation response")
	}
	public := map[string]json.RawMessage{}
	for _, key := range []string{"id", "workspace_id", "agent_id", "server_url", "bot_user_id", "bot_username", "context", "access_mode", "allowed_users", "denied_reply", "installer_user_id", "status", "installed_at", "created_at", "updated_at"} {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return nil, fmt.Errorf("incomplete teambridge installation response")
		}
		public[key] = fields[key]
	}
	if value, ok := fields["attachment_mode"]; ok {
		var mode string
		if json.Unmarshal(value, &mode) != nil || mode == "" {
			return nil, fmt.Errorf("invalid teambridge attachment mode")
		}
		public["attachment_mode"] = value
	}
	var observedWorkspace string
	if json.Unmarshal(public["workspace_id"], &observedWorkspace) != nil || observedWorkspace != workspaceID {
		return nil, fmt.Errorf("teambridge installation belongs to a different workspace")
	}
	for key, allowed := range map[string][]string{"context": {"mode", "lookback_hours", "extended_lookback_hours"}, "denied_reply": {"mode", "text", "image_attachment_id", "image_attachment_ids"}} {
		var object map[string]json.RawMessage
		if json.Unmarshal(public[key], &object) != nil || object == nil {
			return nil, fmt.Errorf("invalid teambridge installation settings")
		}
		filtered := map[string]json.RawMessage{}
		for _, name := range allowed {
			if value, ok := object[name]; ok {
				filtered[name] = value
			}
		}
		public[key], _ = json.Marshal(filtered)
	}
	var users []teamBridgeAllowedAccount
	if json.Unmarshal(public["allowed_users"], &users) != nil {
		return nil, fmt.Errorf("invalid teambridge allowed accounts")
	}
	public["allowed_users"], _ = json.Marshal(users)
	return public, nil
}

func validateTeamBridgeCredential(serverURL, token string) error {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("teambridge server_url must be an HTTPS base URL without credentials, query or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("teambridge access_token is required in the config file or stdin")
	}
	for _, character := range token {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("invalid teambridge bot token")
		}
	}
	return nil
}

func validateTeamBridgeSettings(settings *teamBridgeContext, mode *string, names *[]string, reply *teamBridgeReply, attachments *string) error {
	if attachments != nil && *attachments != "none" && *attachments != "links" {
		return fmt.Errorf("teambridge attachment_mode must be none or links")
	}
	if settings != nil {
		valid := false
		switch settings.Mode {
		case "message", "thread":
			valid = settings.LookbackHours == 0
		case "channel":
			switch settings.LookbackHours {
			case 0, 1, 6, 24, 72, 168, 720, 1440, 2160, 8760:
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unsupported teambridge context settings")
		}
	}
	if names != nil && mode == nil {
		return fmt.Errorf("allowed_usernames requires access_mode allowlist")
	}
	if mode != nil {
		switch *mode {
		case "members", "anyone":
			if names != nil {
				return fmt.Errorf("allowed_usernames requires access_mode allowlist")
			}
		case "allowlist":
			if names == nil {
				return fmt.Errorf("allowlist requires allowed_usernames; use [] to block everyone")
			}
			var err error
			*names, err = normalizeTeamBridgeUsernames(*names)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("teambridge access_mode must be members, anyone or allowlist")
		}
	}
	if reply != nil {
		switch reply.Mode {
		case "text":
			if len(reply.ImageAttachmentIDs) != 0 || !utf8.ValidString(reply.Text) || utf8.RuneCountInString(reply.Text) > 4000 {
				return fmt.Errorf("teambridge text replies require at most 4000 characters and no images")
			}
		case "images":
			if reply.Text != "" || len(reply.ImageAttachmentIDs) == 0 {
				return fmt.Errorf("teambridge image replies require image_attachment_ids and no text")
			}
			seen := map[string]bool{}
			for _, raw := range reply.ImageAttachmentIDs {
				id, err := fullTeamBridgeUUID(raw)
				if err != nil || seen[id] {
					return fmt.Errorf("teambridge reply images require unique full attachment UUIDs")
				}
				seen[id] = true
			}
		default:
			return fmt.Errorf("teambridge denied_reply mode must be text or images")
		}
	}
	return nil
}

func normalizeTeamBridgeUsernames(values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, fmt.Errorf("teambridge allows at most 100 selected accounts")
	}
	result, seen := make([]string, 0, len(values)), map[string]bool{}
	for _, raw := range values {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
		if !teamBridgeUsername.MatchString(name) {
			return nil, fmt.Errorf("invalid teambridge username in config")
		}
		if !seen[name] {
			result = append(result, name)
			seen[name] = true
		}
	}
	return result, nil
}

func validateTeamBridgeWireSize(config any) error {
	// JSON escaping and install defaults can make the actual request larger
	// than its input file. Match the server's cap before issuing any mutation.
	data, err := json.Marshal(config)
	if err != nil || len(data) > teamBridgeConfigLimit {
		return fmt.Errorf("encoded teambridge config exceeds the server's 32 KiB request limit")
	}
	return nil
}

func runTeamBridgeVerify(cmd *cobra.Command, _ []string) error {
	var config teamBridgeVerifyConfig
	if err := readTeamBridgeConfig(cmd, &config); err != nil {
		return err
	}
	if err := validateTeamBridgeCredential(config.ServerURL, config.AccessToken); err != nil {
		return err
	}
	client := cli.NewAPIClient(config.ServerURL, "", strings.TrimSpace(config.AccessToken))
	protectTeamBridgeClient(client)
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if err := verifyTeamBridgeAccounts(ctx, client, config); err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]bool{"verified": true})
}

func verifyTeamBridgeAccounts(ctx context.Context, client *cli.APIClient, config teamBridgeVerifyConfig) error {
	if config.ExpectedBotID != "" && !teamBridgeUserID.MatchString(config.ExpectedBotID) {
		return fmt.Errorf("invalid expected TeamBridge bot ID")
	}
	names := make([]string, 0, len(config.AllowedUsers))
	expected, identities := map[string]string{}, map[string]bool{}
	for _, account := range config.AllowedUsers {
		normalized, err := normalizeTeamBridgeUsernames([]string{account.Username})
		if err != nil {
			return err
		}
		name := normalized[0]
		if _, exists := expected[name]; exists || (account.ID != "" && (!teamBridgeUserID.MatchString(account.ID) || identities[account.ID])) {
			return fmt.Errorf("invalid or duplicate expected TeamBridge accounts")
		}
		names = append(names, name)
		expected[name] = account.ID
		if account.ID != "" {
			identities[account.ID] = true
		}
	}
	if len(names) > 100 {
		return fmt.Errorf("teambridge allows at most 100 selected accounts")
	}
	type account struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		IsBot    bool   `json:"is_bot"`
		DeleteAt int64  `json:"delete_at"`
	}
	var bot account
	if err := client.GetJSON(ctx, "/api/v4/users/me", &bot); err != nil {
		return safeTeamBridgeError(err)
	}
	if !bot.IsBot || bot.DeleteAt != 0 || !teamBridgeUserID.MatchString(bot.ID) || !teamBridgeUsername.MatchString(bot.Username) || (config.ExpectedBotID != "" && bot.ID != config.ExpectedBotID) {
		return fmt.Errorf("TeamBridge token does not belong to the expected active bot; nothing was changed")
	}
	if len(names) == 0 {
		return nil
	}
	var users []account
	if err := client.PostJSON(ctx, "/api/v4/users/usernames", names, &users); err != nil {
		return safeTeamBridgeError(err)
	}
	if len(users) != len(names) {
		return fmt.Errorf("could not verify all TeamBridge accounts; nothing was changed")
	}
	seen, ids := map[string]bool{}, map[string]bool{}
	for _, user := range users {
		expectedID, requested := expected[user.Username]
		if !requested || seen[user.Username] || ids[user.ID] || user.IsBot || user.DeleteAt != 0 || !teamBridgeUserID.MatchString(user.ID) || (expectedID != "" && user.ID != expectedID) {
			return fmt.Errorf("TeamBridge username does not identify the expected active account; check renamed or reused usernames; nothing was changed")
		}
		seen[user.Username], ids[user.ID] = true, true
	}
	return nil
}

type teamBridgeAttachment struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	Filename        string  `json:"filename"`
	DownloadURL     string  `json:"download_url"`
	SizeBytes       *int64  `json:"size_bytes"`
	IssueID         *string `json:"issue_id"`
	CommentID       *string `json:"comment_id"`
	ChatSessionID   *string `json:"chat_session_id"`
	ChatMessageID   *string `json:"chat_message_id"`
	TaskID          *string `json:"task_id"`
	SourceContextID *string `json:"source_context_id"`
}

func getTeamBridgeImageMetadata(ctx context.Context, client *cli.APIClient, id string) (teamBridgeAttachment, error) {
	var metadata teamBridgeAttachment
	if err := client.GetJSON(ctx, "/api/attachments/"+id, &metadata); err != nil {
		return metadata, safeTeamBridgeError(err)
	}
	if metadata.ID != id || metadata.WorkspaceID != client.WorkspaceID || metadata.SizeBytes == nil || *metadata.SizeBytes < 1 || *metadata.SizeBytes > teamBridgeImageLimit || metadata.Filename == "" ||
		metadata.IssueID != nil || metadata.CommentID != nil || metadata.ChatSessionID != nil || metadata.ChatMessageID != nil || metadata.TaskID != nil || metadata.SourceContextID != nil {
		return metadata, fmt.Errorf("teambridge reply image must be a standalone image in the selected workspace, at most 5 MiB")
	}
	metadata.Filename = path.Base(strings.ReplaceAll(metadata.Filename, `\`, "/"))
	if metadata.Filename == "." || metadata.Filename == "/" || strings.ContainsAny(metadata.Filename, "\x00\r\n") {
		return metadata, fmt.Errorf("invalid teambridge image filename")
	}
	return metadata, nil
}

func runTeamBridgeImageUpload(cmd *cobra.Command, args []string) error {
	info, err := os.Stat(args[0])
	if err != nil || !info.Mode().IsRegular() || info.Size() > teamBridgeImageLimit {
		return fmt.Errorf("teambridge image must be a readable regular file of at most 5 MiB")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("cannot read teambridge image")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, teamBridgeImageLimit+1))
	if err != nil {
		return fmt.Errorf("cannot read teambridge image")
	}
	contentType, err := teamBridgeImageContentType(data)
	if err != nil {
		return err
	}
	filename := filepath.Base(args[0])
	if strings.ContainsAny(filename, "\x00\r\n") {
		return fmt.Errorf("invalid teambridge image filename")
	}
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	id, _, err := client.UploadFileWithURL(ctx, data, filename)
	if err != nil {
		return safeTeamBridgeError(err)
	}
	if _, err := fullTeamBridgeUUID(id); err != nil {
		return fmt.Errorf("teambridge image upload returned no valid attachment ID")
	}
	metadata, err := getTeamBridgeImageMetadata(ctx, client, id)
	if err != nil {
		return err
	}
	if *metadata.SizeBytes != int64(len(data)) {
		return fmt.Errorf("teambridge image upload returned an unexpected size")
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"id": id, "filename": metadata.Filename, "content_type": contentType, "size": len(data)})
}

func runTeamBridgeImageDownload(cmd *cobra.Command, args []string) error {
	id, err := fullTeamBridgeUUID(args[0])
	if err != nil {
		return fmt.Errorf("teambridge image download requires a full attachment UUID")
	}
	destination, _ := cmd.Flags().GetString("output-file")
	if destination == "" {
		return fmt.Errorf("teambridge image download requires --output-file")
	}
	client, err := newTeamBridgeAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	metadata, err := getTeamBridgeImageMetadata(ctx, client, id)
	if err != nil {
		return err
	}
	data, err := downloadTeamBridgeImage(ctx, client, metadata.DownloadURL)
	if err != nil {
		return err
	}
	if int64(len(data)) != *metadata.SizeBytes {
		return fmt.Errorf("teambridge image download size does not match metadata")
	}
	contentType, err := teamBridgeImageContentType(data)
	if err != nil {
		return err
	}
	outputPath, err := writeTeamBridgeImage(destination, data)
	if err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"id": id, "filename": metadata.Filename, "content_type": contentType, "size": len(data), "path": outputPath})
}

func teamBridgeImageContentType(data []byte) (string, error) {
	if len(data) == 0 || len(data) > teamBridgeImageLimit {
		return "", fmt.Errorf("teambridge reply images must be non-empty and at most 5 MiB")
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return contentType, nil
	}
	return "", fmt.Errorf("teambridge reply images must contain PNG, JPEG, GIF or WebP image data")
}

func downloadTeamBridgeImage(ctx context.Context, client *cli.APIClient, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || rawURL == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("invalid teambridge image download URL")
	}
	relative := !u.IsAbs()
	if relative {
		if u.Host != "" || !strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "//") {
			return nil, fmt.Errorf("invalid relative teambridge image download URL")
		}
		u, err = url.Parse(client.BaseURL + rawURL)
		if err != nil {
			return nil, fmt.Errorf("invalid teambridge image download URL")
		}
	}
	if err := validateTeamBridgeImageURL(u, strings.HasPrefix(client.BaseURL, "https://")); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("cannot construct teambridge image download")
	}
	if relative {
		request.Header.Set("Authorization", "Bearer "+client.Token)
		request.Header.Set("X-Workspace-ID", client.WorkspaceID)
	}
	downloadClient := *client.HTTPClient
	// API redirects are forbidden, but the authenticated attachment endpoint
	// deliberately redirects to signed storage URLs. Those carry their own auth.
	if transport, ok := downloadClient.Transport.(teamBridgeTransport); ok {
		downloadClient.Transport = transport.base
	}
	downloadClient.Jar = nil
	downloadClient.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 {
			return errors.New("too many image redirects")
		}
		if err := validateTeamBridgeImageURL(next.URL, previous[len(previous)-1].URL.Scheme == "https"); err != nil {
			return err
		}
		next.Header.Del("Authorization")
		next.Header.Del("X-Workspace-ID")
		next.Header.Del("Cookie")
		next.Header.Del("Referer")
		return nil
	}
	response, err := downloadClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("teambridge image download failed; check attachment availability")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("teambridge image download failed (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, teamBridgeImageLimit+1))
	if err != nil || len(data) > teamBridgeImageLimit {
		return nil, fmt.Errorf("teambridge image download failed or exceeds 5 MiB")
	}
	return data, nil
}

func validateTeamBridgeImageURL(u *url.URL, requireHTTPS bool) error {
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || (requireHTTPS && u.Scheme != "https") {
		return fmt.Errorf("unsafe teambridge image download URL")
	}
	return nil
}

func writeTeamBridgeImage(destination string, data []byte) (string, error) {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("cannot resolve teambridge image output path")
	}
	if info, err := os.Lstat(abs); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("teambridge image output must be a regular file, not a symlink or directory")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot inspect teambridge image output path")
	}
	directory := filepath.Dir(abs)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("cannot create teambridge image output directory")
	}
	file, err := os.CreateTemp(directory, ".teambridge-image-*")
	if err != nil {
		return "", fmt.Errorf("cannot create private teambridge image output")
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", fmt.Errorf("cannot write teambridge image output")
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("cannot close teambridge image output")
	}
	if err := os.Rename(temp, abs); err != nil {
		return "", fmt.Errorf("cannot replace teambridge image output")
	}
	return abs, nil
}
