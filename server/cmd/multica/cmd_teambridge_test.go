package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const (
	tbWorkspaceID    = "11111111-1111-4111-8111-111111111111"
	tbAgentID        = "22222222-2222-4222-8222-222222222222"
	tbInstallationID = "33333333-3333-4333-8333-333333333333"
	tbImageID        = "44444444-4444-4444-8444-444444444444"
	tbBotID          = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	tbAccountID      = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	tbSecret         = "bot-token-must-not-appear-in-output"
)

func teamBridgeTestEnvironment(t *testing.T) {
	t.Helper()
	redirectTestHome(t, t.TempDir())
	t.Chdir(t.TempDir())
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT", cli.TaskConfigRootEnv, "MULTICA_PROFILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("MULTICA_TOKEN", "mul_test-human-token")
	t.Setenv("MULTICA_WORKSPACE_ID", tbWorkspaceID)
}

func teamBridgeTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	teamBridgeTestEnvironment(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mul_test-human-token" || r.Header.Get("X-Workspace-ID") != tbWorkspaceID {
			t.Error("request did not use the selected human token and workspace")
		}
		if r.Header.Get("X-Agent-ID") != "" || r.Header.Get("X-Task-ID") != "" {
			t.Error("integration request included task attribution")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	return server
}

func executeTeamBridge(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "multica", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("server-url", "", "")
	root.PersistentFlags().String("workspace-id", "", "")
	root.PersistentFlags().String("profile", "", "")
	root.PersistentFlags().Bool("debug", false, "")
	root.AddCommand(newTeamBridgeCommand())
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return output.String(), err
}

func teamBridgeTestFile(t *testing.T, data []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func teamBridgeTestInstallation() map[string]any {
	return map[string]any{
		"id": tbInstallationID, "workspace_id": tbWorkspaceID, "agent_id": tbAgentID,
		"server_url": "https://teambridge.example.test", "bot_user_id": tbBotID, "bot_username": "helpbot",
		"context":     map[string]any{"mode": "channel_extended", "extended_lookback_hours": 8760},
		"access_mode": "allowlist", "allowed_users": []map[string]string{{"id": tbAccountID, "username": "alice"}},
		"denied_reply":      map[string]any{"mode": "image", "image_attachment_id": tbImageID},
		"installer_user_id": tbWorkspaceID, "status": "active", "installed_at": "2026-01-01T00:00:00Z",
		"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
	}
}

func writeTeamBridgeTestList(w http.ResponseWriter, installations []map[string]any, enabled bool) {
	_ = json.NewEncoder(w).Encode(map[string]any{"installations": installations, "configured": enabled, "install_supported": enabled})
}

func TestTeamBridgeListPreservesPublicWireFormsAndRemovesCredentials(t *testing.T) {
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces/"+tbWorkspaceID+"/mattermost/installations" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		installation := teamBridgeTestInstallation()
		installation["access_token"] = tbSecret
		installation["config"] = map[string]string{"encrypted_access_token": tbSecret}
		installation["context"].(map[string]any)["unexpected_credential"] = tbSecret
		installation["denied_reply"].(map[string]any)["unexpected_credential"] = tbSecret
		writeTeamBridgeTestList(w, []map[string]any{installation}, true)
	})
	for _, alias := range []string{"teambridge", "mattermost"} {
		out, err := executeTeamBridge(t, "", alias, "list", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, tbSecret) || strings.Contains(out, "access_token") || strings.Contains(out, "unexpected_credential") {
			t.Fatalf("public list contains a credential: %s", out)
		}
		var got struct {
			Installations []struct {
				Context     map[string]any `json:"context"`
				DeniedReply map[string]any `json:"denied_reply"`
			} `json:"installations"`
			Configured       bool `json:"configured"`
			InstallSupported bool `json:"install_supported"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Configured || !got.InstallSupported || len(got.Installations) != 1 {
			t.Fatalf("bad capability envelope: %s (%v)", out, err)
		}
		if got.Installations[0].Context["mode"] != "channel_extended" || got.Installations[0].Context["extended_lookback_hours"] != float64(8760) || got.Installations[0].DeniedReply["mode"] != "image" || got.Installations[0].DeniedReply["image_attachment_id"] != tbImageID {
			t.Fatalf("legacy public shapes were changed: %s", out)
		}
	}
}

func TestTeamBridgeInstallFileAndStdinKeepTokensOutOfOutput(t *testing.T) {
	for _, source := range []string{"file", "stdin"} {
		t.Run(source, func(t *testing.T) {
			var writes atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					writeTeamBridgeTestList(w, []map[string]any{}, true)
					return
				}
				writes.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces/"+tbWorkspaceID+"/mattermost/install" || r.URL.Query().Get("agent_id") != tbAgentID || strings.Contains(r.URL.String(), tbSecret) {
					t.Error("incorrect install endpoint")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["access_token"] != tbSecret || body["access_mode"] != "members" || body["context"].(map[string]any)["mode"] != "message" || body["denied_reply"].(map[string]any)["mode"] != "text" {
					t.Error("install did not preserve credentials or apply default settings")
				}
				if _, ok := body["allowed_usernames"]; ok {
					t.Error("members mode should omit the allowlist")
				}
				installation := teamBridgeTestInstallation()
				installation["access_token"] = tbSecret
				_ = json.NewEncoder(w).Encode(installation)
			})
			input := `{"server_url":"https://teambridge.example.test","access_token":"` + tbSecret + `"}`
			args := []string{"teambridge", "install", "--agent", tbAgentID, "--output", "json"}
			if source == "file" {
				args = append(args, "--config-file", teamBridgeTestFile(t, []byte(input)))
				input = ""
			} else {
				args = append(args, "--config-stdin")
			}
			out, err := executeTeamBridge(t, input, args...)
			if err != nil || writes.Load() != 1 || !strings.Contains(out, tbInstallationID) {
				t.Fatalf("install failed: %s, %v, writes=%d", out, err, writes.Load())
			}
			if strings.Contains(out, tbSecret) || strings.Contains(out, "access_token") {
				t.Fatal("install output exposed a credential")
			}
		})
	}
}

func TestTeamBridgeDisabledCapabilitiesBlockInstall(t *testing.T) {
	var writes atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		writeTeamBridgeTestList(w, []map[string]any{}, false)
	})
	out, err := executeTeamBridge(t, "", "teambridge", "list")
	if err != nil || !strings.Contains(out, `"configured": false`) || !strings.Contains(out, `"install_supported": false`) {
		t.Fatalf("list must preserve disabled capability flags: %s %v", out, err)
	}
	config := `{"server_url":"https://teambridge.example.test","access_token":"` + tbSecret + `"}`
	out, err = executeTeamBridge(t, config, "teambridge", "install", "--agent", tbAgentID, "--config-stdin")
	if err == nil || !strings.Contains(err.Error(), "disabled") || out != "" || writes.Load() != 0 {
		t.Fatalf("disabled install was not refused before mutation: %s %v", out, err)
	}
}

func TestTeamBridgeListRejectsIncompleteOrCrossWorkspaceResponses(t *testing.T) {
	for _, scenario := range []string{"missing-capability", "null-installations", "other-workspace"} {
		t.Run(scenario, func(t *testing.T) {
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				response := map[string]any{"installations": []map[string]any{}, "configured": true, "install_supported": true}
				switch scenario {
				case "missing-capability":
					delete(response, "install_supported")
				case "null-installations":
					response["installations"] = nil
				case "other-workspace":
					installation := teamBridgeTestInstallation()
					installation["workspace_id"] = tbAgentID
					response["installations"] = []map[string]any{installation}
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			out, err := executeTeamBridge(t, "", "teambridge", "list")
			if err == nil || out != "" {
				t.Fatalf("unsafe capability/installation response was accepted: %s %v", out, err)
			}
		})
	}
}

func TestTeamBridgeUpdateAndRevoke(t *testing.T) {
	var patches, deletes atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/"+tbWorkspaceID+"/mattermost/installations/"+tbInstallationID {
			t.Error("wrong installation endpoint")
		}
		switch r.Method {
		case http.MethodPatch:
			patches.Add(1)
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 4 || string(body["allowed_usernames"]) != "[]" || bytes.Contains(body["context"], []byte("extended")) {
				t.Errorf("unexpected patch fields: %#v", body)
			}
			if _, ok := body["access_token"]; ok {
				t.Error("update must preserve the stored token")
			}
			_ = json.NewEncoder(w).Encode(teamBridgeTestInstallation())
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request method %s", r.Method)
		}
	})
	config := `{"context":{"mode":"channel","lookback_hours":0},"access_mode":"allowlist","allowed_usernames":[],"denied_reply":{"mode":"text","text":""}}`
	if _, err := executeTeamBridge(t, config, "teambridge", "update", tbInstallationID, "--config-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := executeTeamBridge(t, "", "teambridge", "revoke", tbInstallationID)
	if err != nil || !strings.Contains(out, `"revoked": true`) || !strings.Contains(out, tbInstallationID) || patches.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("unexpected update/revoke result %s %v", out, err)
	}
}

func TestTeamBridgeRejectsInvalidConfigBeforeRequests(t *testing.T) {
	var requests atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for name, input := range map[string]string{
		"token-in-update":         `{"access_token":"` + tbSecret + `","access_mode":"members"}`,
		"unknown-field":           `{"` + tbSecret + `":"bad"}`,
		"invalid-json":            `{"context":`,
		"null":                    `null`,
		"two-objects":             `{"access_mode":"members"} {}`,
		"empty-allowlist-omitted": `{"access_mode":"allowlist"}`,
		"members-with-allowlist":  `{"access_mode":"members","allowed_usernames":[]}`,
		"unsupported-hours":       `{"context":{"mode":"channel","lookback_hours":5}}`,
		"unsupported-attachments": `{"attachment_mode":"inline"}`,
		"empty-attachments":       `{"attachment_mode":""}`,
		"ambiguous-reply":         `{"denied_reply":{"mode":"images","text":"bad","image_attachment_ids":["` + tbImageID + `"]}}`,
		"short-image-id":          `{"denied_reply":{"mode":"images","image_attachment_ids":["44444444"]}}`,
		"oversized-file":          strings.Repeat(" ", teamBridgeConfigLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := executeTeamBridge(t, input, "teambridge", "update", tbInstallationID, "--config-stdin")
			if err == nil || out != "" || strings.Contains(cli.FormatError(err, true), tbSecret) {
				t.Fatalf("unsafe config handling: output=%s, error=%v", out, err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid config made %d requests", requests.Load())
	}
	if err := validateTeamBridgeWireSize(map[string]string{"escaped": strings.Repeat("<", teamBridgeConfigLimit/5)}); err == nil {
		t.Fatal("JSON escaping must not evade the server's request limit")
	}
}

func TestTeamBridgeUpdatesOnlyAttachmentMode(t *testing.T) {
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPatch || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["attachment_mode"] != "links" {
			t.Error("attachment mode update changed other settings")
		}
		response := teamBridgeTestInstallation()
		response["attachment_mode"] = "links"
		_ = json.NewEncoder(w).Encode(response)
	})
	out, err := executeTeamBridge(t, `{"attachment_mode":"links"}`, "teambridge", "update", tbInstallationID, "--config-stdin")
	if err != nil || !strings.Contains(out, `"attachment_mode": "links"`) {
		t.Fatalf("attachment mode was lost: %s, %v", out, err)
	}
}

func TestTeamBridgeRequiresExactlyOneConfigSource(t *testing.T) {
	teamBridgeTestEnvironment(t)
	file := teamBridgeTestFile(t, []byte(`{"access_mode":"members"}`))
	for _, args := range [][]string{
		{"teambridge", "update", tbInstallationID},
		{"teambridge", "update", tbInstallationID, "--config-file", file, "--config-stdin"},
	} {
		if _, err := executeTeamBridge(t, `{}`, args...); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("expected an exclusive config-source error, got %v", err)
		}
	}
}

func TestTeamBridgeAdministrationRefusesTaskContexts(t *testing.T) {
	for name, value := range map[string]string{
		"MULTICA_AGENT_ID": tbAgentID, "MULTICA_TASK_ID": tbInstallationID,
		"MULTICA_DAEMON_PORT": "4321", cli.TaskConfigRootEnv: "/tmp/task-profile",
		"MULTICA_TOKEN": "mat_task-token",
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			t.Setenv(name, value)
			for _, command := range [][]string{{"teambridge", "list"}, {"teambridge", "verify", "--config-stdin"}, {"teambridge", "image", "download", tbImageID, "--output-file", "image.png"}} {
				out, err := executeTeamBridge(t, `{}`, command...)
				if err == nil || out != "" || !strings.Contains(err.Error(), "task") {
					t.Fatalf("admin operation was not blocked: %s %v", out, err)
				}
			}
			if requests.Load() != 0 {
				t.Fatal("task context reached the administrative API")
			}
		})
	}
	t.Run("ancestor-marker", func(t *testing.T) {
		teamBridgeTestEnvironment(t)
		marker := filepath.Join(".", execenv.TaskContextMarkerRelPath)
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"managed_by":"`+execenv.TaskContextMarkerManagedBy+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := executeTeamBridge(t, "", "teambridge", "list"); err == nil || !strings.Contains(err.Error(), "daemon-managed task") {
			t.Fatalf("ancestor marker was ignored: %v", err)
		}
	})
}

func TestTeamBridgeRequiresFullScopeAndIdentity(t *testing.T) {
	var requests atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	for _, args := range [][]string{
		{"teambridge", "list", "--workspace-id", "11111111"},
		{"teambridge", "list", "--output", "table"},
		{"teambridge", "revoke", "33333333"},
		{"teambridge", "install", "--agent", "22222222", "--config-stdin"},
	} {
		if _, err := executeTeamBridge(t, `{}`, args...); err == nil {
			t.Fatalf("unsafe abbreviated scope or unsupported output accepted: %v", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid scope reached the API")
	}
}

func TestTeamBridgeErrorsRemainSanitizedAndKeepExitCodes(t *testing.T) {
	for status, exitCode := range map[int]int{400: cli.ExitValidation, 401: cli.ExitAuth, 403: cli.ExitAuth, 404: cli.ExitNotFound, 409: cli.ExitGeneric, 500: cli.ExitGeneric} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"` + tbSecret + `"}`))
			})
			out, err := executeTeamBridge(t, "", "teambridge", "list")
			if err == nil || out != "" || cli.ExitCodeFor(err) != exitCode {
				t.Fatalf("wrong sanitized error classification: out=%s err=%v exit=%d", out, err, cli.ExitCodeFor(err))
			}
			for _, debug := range []bool{false, true} {
				formatted := cli.FormatError(err, debug)
				if strings.Contains(formatted, tbSecret) || !strings.Contains(formatted, fmt.Sprintf("HTTP %d", status)) {
					t.Fatalf("unsafe or unclassifiable formatted error: %s", formatted)
				}
			}
		})
	}
	err := safeTeamBridgeError(&cli.NetworkError{Kind: cli.KindNetworkTLS, Op: "GET https://host/?secret=" + tbSecret, Err: errors.New(tbSecret)})
	if cli.ExitCodeFor(err) != cli.ExitNetwork || strings.Contains(cli.FormatError(err, true), tbSecret) {
		t.Fatal("network error retained confidential details or lost its exit code")
	}
}

func TestTeamBridgeAPIRedirectsAndUnexpectedDeleteStatusesFailClosed(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL+"/?token="+tbSecret)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"revoked":true}`))
			})
			out, err := executeTeamBridge(t, "", "teambridge", "revoke", tbInstallationID)
			if err == nil || out != "" || strings.Contains(cli.FormatError(err, true), tbSecret) {
				t.Fatalf("unexpected API status was accepted or leaked: %s %v", out, err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("administrative API request followed a redirect")
	}
}

func TestTeamBridgeVerifyNativeBotAndAllowlistIdentities(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-bot", "not-a-bot", "reused-username", "deleted-account", "missing-account"} {
		t.Run(scenario, func(t *testing.T) {
			var lookups atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+tbSecret || r.Header.Get("X-Workspace-ID") != "" {
					t.Error("native TeamBridge verification reused Multica credentials or scope")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v4/users/me":
					bot := map[string]any{"id": tbBotID, "username": "helpbot", "is_bot": true, "delete_at": 0}
					if scenario == "wrong-bot" {
						bot["id"] = tbAccountID
					}
					if scenario == "not-a-bot" {
						bot["is_bot"] = false
					}
					_ = json.NewEncoder(w).Encode(bot)
				case "POST /api/v4/users/usernames":
					lookups.Add(1)
					var names []string
					if err := json.NewDecoder(r.Body).Decode(&names); err != nil || len(names) != 1 || names[0] != "alice" {
						t.Error("verification did not normalize the lookup usernames")
					}
					user := map[string]any{"id": tbAccountID, "username": "alice", "delete_at": 0}
					if scenario == "reused-username" {
						user["id"] = "cccccccccccccccccccccccccc"
					}
					if scenario == "deleted-account" {
						user["delete_at"] = 100
					}
					users := []map[string]any{user}
					if scenario == "missing-account" {
						users = []map[string]any{}
					}
					_ = json.NewEncoder(w).Encode(users)
				default:
					t.Errorf("verification performed an unexpected operation: %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			client := cli.NewAPIClient(server.URL, "", tbSecret)
			protectTeamBridgeClient(client)
			err := verifyTeamBridgeAccounts(context.Background(), client, teamBridgeVerifyConfig{ExpectedBotID: tbBotID, AllowedUsers: []teamBridgeAllowedAccount{{ID: tbAccountID, Username: " @Alice "}}})
			if scenario == "success" {
				if err != nil || lookups.Load() != 1 {
					t.Fatalf("valid bot/account failed verification: %v", err)
				}
			} else if err == nil || strings.Contains(cli.FormatError(err, true), tbSecret) {
				t.Fatalf("identity mismatch was not safely rejected: %v", err)
			}
			if (scenario == "wrong-bot" || scenario == "not-a-bot") && lookups.Load() != 0 {
				t.Fatal("account lookup continued with an unverified bot")
			}
		})
	}
}

var teamBridgeTestPNG = []byte("\x89PNG\r\n\x1a\nimage-test-data")

func teamBridgeTestMetadata(downloadURL string, size int) map[string]any {
	return map[string]any{"id": tbImageID, "workspace_id": tbWorkspaceID, "filename": "reply.png", "size_bytes": size, "download_url": downloadURL}
}

func TestTeamBridgeImageUploadIsStandaloneAndReportsSafeMetadata(t *testing.T) {
	var uploads atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/upload-file":
			uploads.Add(1)
			if err := r.ParseMultipartForm(teamBridgeImageLimit); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if len(r.MultipartForm.Value) != 0 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
				t.Error("image upload included private task/chat associations")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if !bytes.Equal(data, teamBridgeTestPNG) {
				t.Error("image upload changed the file bytes")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": tbImageID, "url": "https://storage.example.test/?signature=" + tbSecret})
		case "GET /api/attachments/" + tbImageID:
			_ = json.NewEncoder(w).Encode(teamBridgeTestMetadata("https://storage.example.test/?signature="+tbSecret, len(teamBridgeTestPNG)))
		default:
			t.Errorf("unexpected upload operation: %s %s", r.Method, r.URL.Path)
		}
	})
	file := teamBridgeTestFile(t, teamBridgeTestPNG)
	out, err := executeTeamBridge(t, "", "teambridge", "image", "upload", file)
	if err != nil || uploads.Load() != 1 || !strings.Contains(out, `"content_type": "image/png"`) || !strings.Contains(out, tbImageID) || strings.Contains(out, tbSecret) {
		t.Fatalf("bad upload result: %s %v", out, err)
	}
}

func TestTeamBridgeImageDownloadDropsCredentialsAndWritesPrivately(t *testing.T) {
	for _, source := range []string{"signed", "redirect"} {
		t.Run(source, func(t *testing.T) {
			var downloads atomic.Int64
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				for _, name := range []string{"Authorization", "X-Workspace-ID", "Cookie", "Referer"} {
					if r.Header.Get(name) != "" {
						t.Errorf("storage received private header %s", name)
					}
				}
				_, _ = w.Write(teamBridgeTestPNG)
			}))
			defer storage.Close()
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/attachments/" + tbImageID:
					url := storage.URL + "/?signature=" + tbSecret
					if source == "redirect" {
						url = "/attachment-image"
					}
					_ = json.NewEncoder(w).Encode(teamBridgeTestMetadata(url, len(teamBridgeTestPNG)))
				case "/attachment-image":
					http.Redirect(w, r, storage.URL+"/?signature="+tbSecret, http.StatusFound)
				default:
					t.Error("unexpected download operation")
				}
			})
			output := filepath.Join(t.TempDir(), "reply.png")
			if err := os.WriteFile(output, []byte("old content"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := executeTeamBridge(t, "", "teambridge", "image", "download", tbImageID, "--output-file", output)
			if err != nil || downloads.Load() != 1 || strings.Contains(out, tbSecret) {
				t.Fatalf("bad download result: %s %v", out, err)
			}
			var metadata struct {
				Path string `json:"path"`
				Size int    `json:"size"`
			}
			if json.Unmarshal([]byte(out), &metadata) != nil || metadata.Path != output || metadata.Size != len(teamBridgeTestPNG) {
				t.Fatalf("bad returned file metadata: %s", out)
			}
			data, err := os.ReadFile(output)
			info, statErr := os.Stat(output)
			if err != nil || statErr != nil || !bytes.Equal(data, teamBridgeTestPNG) {
				t.Fatal("download did not atomically replace the file with private image bytes")
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatal("download file does not have private permissions")
			}
		})
	}
}

func TestTeamBridgeImageRefusesPrivateAttachmentsAndOversizedContent(t *testing.T) {
	for _, association := range []string{"issue_id", "comment_id", "chat_session_id", "chat_message_id", "task_id", "source_context_id"} {
		t.Run(association, func(t *testing.T) {
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				metadata := teamBridgeTestMetadata("https://must-not-request.example.test", len(teamBridgeTestPNG))
				metadata[association] = tbInstallationID
				_ = json.NewEncoder(w).Encode(metadata)
			})
			output := filepath.Join(t.TempDir(), "reply.png")
			if _, err := executeTeamBridge(t, "", "teambridge", "image", "download", tbImageID, "--output-file", output); err == nil || !strings.Contains(err.Error(), "standalone") {
				t.Fatalf("private attachment was accepted: %v", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("private attachment created an output file")
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("<svg/>"), append(teamBridgeTestPNG, make([]byte, teamBridgeImageLimit)...)} {
		if _, err := teamBridgeImageContentType(data); err == nil {
			t.Fatal("invalid or oversized image bytes were accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, teamBridgeImageLimit+1))
	}))
	defer server.Close()
	client := cli.NewAPIClient(server.URL, tbWorkspaceID, tbSecret)
	if _, err := downloadTeamBridgeImage(context.Background(), client, server.URL+"/?signature="+tbSecret); err == nil || strings.Contains(cli.FormatError(err, true), tbSecret) {
		t.Fatal("oversized storage response was accepted or leaked its signed URL")
	}
	client.BaseURL = "https://multica.example.test"
	if _, err := downloadTeamBridgeImage(context.Background(), client, server.URL); err == nil {
		t.Fatal("HTTPS attachment download permitted an HTTP downgrade")
	}
}

func TestTeamBridgeImageOutputRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "protected")
	if err := os.WriteFile(protected, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "reply.png")
	if err := os.Symlink(protected, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := writeTeamBridgeImage(link, teamBridgeTestPNG); err == nil {
		t.Fatal("symlink output was accepted")
	}
	data, _ := os.ReadFile(protected)
	if string(data) != "original" {
		t.Fatal("symlink target was modified")
	}
}
