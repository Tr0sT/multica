package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func teamBridgeTokenTestDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func teamBridgeTokenTestResponse() map[string]any {
	return map[string]any{"installation_id": tbInstallationID, "workspace_id": tbWorkspaceID, "access_token": tbSecret}
}

func assertTeamBridgeTokenFailure(t *testing.T, output string, err error) {
	t.Helper()
	if err == nil || output != "" {
		t.Fatal("token operation did not fail with empty output")
	}
	for _, debug := range []bool{false, true} {
		if strings.Contains(cli.FormatError(err, debug), tbSecret) {
			t.Fatal("token operation disclosed a secret in its error")
		}
	}
}

func TestTeamBridgeTokenExportImportRoundTrip(t *testing.T) {
	stored := tbSecret
	var reads, writes atomic.Int64
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/"+tbWorkspaceID+"/mattermost/installations/"+tbInstallationID+"/token" || r.URL.RawQuery != "" {
			t.Error("token operation used an unexpected path")
		}
		switch r.Method {
		case http.MethodGet:
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(teamBridgeTokenTestResponse())
		case http.MethodPut:
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["access_token"] == "" {
				t.Error("token restore did not send only the new token")
			}
			changed := stored != body["access_token"]
			if changed {
				writes.Add(1)
				stored = body["access_token"]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"installation_id": tbInstallationID, "workspace_id": tbWorkspaceID, "updated": changed, "unexpected_secret": stored})
		default:
			t.Error("token operation attempted to reinstall or update bot settings")
		}
	})
	dir := teamBridgeTokenTestDirectory(t)
	file := filepath.Join(dir, "credentials", "bot-token")
	out, err := executeTeamBridge(t, "", "--debug", "teambridge", "token", "export", tbInstallationID, "--output-file", file, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != tbSecret {
		t.Fatal("export did not preserve the exact stored token bytes")
	}
	info, _ := os.Stat(file)
	parent, _ := os.Stat(filepath.Dir(file))
	if runtime.GOOS != "windows" && (info.Mode().Perm() != 0o600 || parent.Mode().Perm() != 0o700) {
		t.Fatal("secret file or directory has non-private permissions")
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(out), &metadata) != nil || len(metadata) != 3 || metadata["exported"] != true || metadata["installation_id"] != tbInstallationID || metadata["workspace_id"] != tbWorkspaceID || strings.Contains(out, tbSecret) {
		t.Fatal("export emitted unsafe or incomplete metadata")
	}
	stored = "synthetic-rotated-bot-token"
	for _, changed := range []bool{true, false} {
		out, err = executeTeamBridge(t, "", "teambridge", "token", "import", tbInstallationID, "--token-file", file, "--debug")
		if err != nil || strings.Contains(out, tbSecret) {
			t.Fatal("private token import failed or disclosed a secret")
		}
		metadata = nil
		if json.Unmarshal([]byte(out), &metadata) != nil || len(metadata) != 3 || metadata["updated"] != changed || metadata["installation_id"] != tbInstallationID || metadata["workspace_id"] != tbWorkspaceID {
			t.Fatal("import emitted unsafe or incomplete confirmation")
		}
	}
	if reads.Load() != 1 || writes.Load() != 1 || stored != tbSecret {
		t.Fatal("round trip did not restore the token idempotently")
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	if len(entries) != 1 || entries[0].Name() != "bot-token" {
		t.Fatal("token operations left temporary files behind")
	}
}

func TestTeamBridgeTokenImportStdin(t *testing.T) {
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPut || json.NewDecoder(r.Body).Decode(&body) != nil || body["access_token"] != tbSecret {
			t.Error("stdin token did not reach the dedicated restore contract")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"installation_id": tbInstallationID, "workspace_id": tbWorkspaceID, "updated": false})
	})
	out, err := executeTeamBridge(t, tbSecret+"\n", "teambridge", "token", "import", tbInstallationID, "--token-stdin")
	if err != nil || strings.Contains(out, tbSecret) || !strings.Contains(out, `"updated": false`) {
		t.Fatal("stdin restore failed or revealed the token")
	}
}

func TestTeamBridgeTokenHonorsProfileAndExplicitWorkspace(t *testing.T) {
	teamBridgeTestEnvironment(t)
	t.Setenv("MULTICA_TOKEN", "")
	t.Setenv("MULTICA_SERVER_URL", "")
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-profile-token" || r.Header.Get("X-Workspace-ID") != tbWorkspaceID || !strings.Contains(r.URL.Path, "/"+tbWorkspaceID+"/") {
			t.Error("token command ignored the profile or explicit workspace")
		}
		_ = json.NewEncoder(w).Encode(teamBridgeTokenTestResponse())
	}))
	defer server.Close()
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{ServerURL: server.URL, WorkspaceID: tbAgentID, Token: "synthetic-profile-token"}, "backup"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token")
	out, err := executeTeamBridge(t, "", "--profile", "backup", "--workspace-id", tbWorkspaceID, "teambridge", "token", "export", tbInstallationID, "--output-file", file)
	if err != nil || requests.Load() != 1 || strings.Contains(out, tbSecret) {
		t.Fatal("profile-scoped token export failed")
	}
}

func TestTeamBridgeTokenNetworkErrorsRemainPrivate(t *testing.T) {
	server := teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("closed server received a request") })
	server.Close()
	for _, operation := range []string{"export", "import"} {
		args := []string{"teambridge", "token", operation, tbInstallationID, "--debug"}
		if operation == "export" {
			args = append(args, "--output-file", filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token"))
		} else {
			args = append(args, "--token-stdin")
		}
		out, err := executeTeamBridge(t, tbSecret, args...)
		assertTeamBridgeTokenFailure(t, out, err)
		if cli.ExitCodeFor(err) != cli.ExitNetwork {
			t.Fatal("token command lost network error classification")
		}
	}
}

func TestTeamBridgeTokenExportRejectsUnsafeDestinationsBeforeReadingSecret(t *testing.T) {
	for _, scenario := range []string{"existing-file", "directory", "symlink", "parent-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			var reads atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				_ = json.NewEncoder(w).Encode(teamBridgeTokenTestResponse())
			})
			dir := teamBridgeTokenTestDirectory(t)
			protected := filepath.Join(dir, "protected")
			if err := os.WriteFile(protected, []byte("existing user file"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := protected
			switch scenario {
			case "directory":
				destination = dir
			case "symlink", "parent-symlink":
				link := filepath.Join(dir, "link")
				target := protected
				if scenario == "parent-symlink" {
					target = dir
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks are unavailable: %v", err)
				}
				destination = link
				if scenario == "parent-symlink" {
					destination = filepath.Join(link, "new-token")
				}
			}
			out, err := executeTeamBridge(t, "", "teambridge", "token", "export", tbInstallationID, "--output-file", destination)
			assertTeamBridgeTokenFailure(t, out, err)
			data, _ := os.ReadFile(protected)
			if string(data) != "existing user file" || reads.Load() != 0 {
				t.Fatal("unsafe export changed a user file or requested the secret")
			}
		})
	}
}

func TestTeamBridgeTokenExportDoesNotOverwriteDestinationCreatedDuringRequest(t *testing.T) {
	file := filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token")
	teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(file, []byte("concurrent user file"), 0o600); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(teamBridgeTokenTestResponse())
	})
	out, err := executeTeamBridge(t, "", "teambridge", "token", "export", tbInstallationID, "--output-file", file)
	assertTeamBridgeTokenFailure(t, out, err)
	data, _ := os.ReadFile(file)
	if string(data) != "concurrent user file" {
		t.Fatal("export overwrote a file created after preflight")
	}
}

func TestTeamBridgeTokenRejectsUnsafeInputsBeforeRequests(t *testing.T) {
	for _, scenario := range []string{"missing-source", "two-sources", "missing-file", "directory", "symlink", "parent-symlink", "public-file", "oversized-file", "empty", "oversized", "control", "invalid-utf8"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			dir := teamBridgeTokenTestDirectory(t)
			file := filepath.Join(dir, "bot-token")
			if err := os.WriteFile(file, []byte(tbSecret), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"teambridge", "token", "import", tbInstallationID}
			input := tbSecret
			switch scenario {
			case "missing-source":
			case "two-sources":
				args = append(args, "--token-file", file, "--token-stdin")
			case "missing-file":
				args = append(args, "--token-file", file+"-missing")
			case "directory":
				args = append(args, "--token-file", dir)
			case "symlink", "parent-symlink":
				link, target := filepath.Join(dir, "link"), file
				if scenario == "parent-symlink" {
					target = dir
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks are unavailable: %v", err)
				}
				if scenario == "parent-symlink" {
					link = filepath.Join(link, "bot-token")
				}
				args = append(args, "--token-file", link)
			case "public-file":
				if runtime.GOOS == "windows" {
					t.Skip("Windows permissions are controlled by ACLs")
				}
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--token-file", file)
			case "oversized-file":
				if err := os.WriteFile(file, []byte(strings.Repeat("x", teamBridgeTokenLimit+1)), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--token-file", file)
			default:
				args = append(args, "--token-stdin")
				input = map[string]string{"empty": " \n", "oversized": strings.Repeat("x", teamBridgeTokenLimit+1), "control": tbSecret + "\ninternal", "invalid-utf8": "token\xff"}[scenario]
			}
			out, err := executeTeamBridge(t, input, args...)
			assertTeamBridgeTokenFailure(t, out, err)
			if requests.Load() != 0 {
				t.Fatal("invalid token source reached the API")
			}
		})
	}
}

func TestTeamBridgeTokenErrorsAndMalformedResponsesNeverLeak(t *testing.T) {
	for _, operation := range []string{"export", "import"} {
		for _, scenario := range []string{"bad-request", "forbidden", "missing-contract", "server-error", "malformed", "wrong-identity", "missing-value", "invalid-value", "oversized", "two-objects"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
					if status := map[string]int{"bad-request": 400, "forbidden": 403, "missing-contract": 404, "server-error": 500}[scenario]; status != 0 {
						w.WriteHeader(status)
						_, _ = fmt.Fprintf(w, `{"error":%q}`, tbSecret)
						return
					}
					response := teamBridgeTokenTestResponse()
					response["updated"] = true
					switch scenario {
					case "malformed":
						_, _ = fmt.Fprintf(w, `{"%s":`, tbSecret)
						return
					case "wrong-identity":
						response["workspace_id"] = tbSecret
					case "missing-value":
						delete(response, "access_token")
						delete(response, "updated")
					case "invalid-value":
						response["access_token"] = map[string]string{"secret": tbSecret}
						response["updated"] = tbSecret
					case "oversized":
						response["extra"] = strings.Repeat(tbSecret, 10000)
					}
					_ = json.NewEncoder(w).Encode(response)
					if scenario == "two-objects" {
						_ = json.NewEncoder(w).Encode(response)
					}
				})
				file := filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token")
				args := []string{"--debug", "teambridge", "token", operation, tbInstallationID}
				if operation == "export" {
					args = append(args, "--output-file", file)
				} else {
					args = append(args, "--token-stdin")
				}
				out, err := executeTeamBridge(t, tbSecret, args...)
				assertTeamBridgeTokenFailure(t, out, err)
				if operation == "export" {
					entries, readErr := os.ReadDir(filepath.Dir(file))
					if readErr != nil || len(entries) != 0 {
						t.Fatal("failed token export left an output or temporary file")
					}
				}
			})
		}
	}
}

func TestTeamBridgeTokenRefusesRedirectsAndMachineContexts(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, operation := range []string{"export", "import"} {
		t.Run(operation+"/redirect", func(t *testing.T) {
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/?secret="+tbSecret, http.StatusTemporaryRedirect)
			})
			args := []string{"teambridge", "token", operation, tbInstallationID}
			if operation == "export" {
				args = append(args, "--output-file", filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token"))
			} else {
				args = append(args, "--token-stdin")
			}
			out, err := executeTeamBridge(t, tbSecret, args...)
			assertTeamBridgeTokenFailure(t, out, err)
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("token request followed an unexpected redirect")
	}
	for name, value := range map[string]string{"MULTICA_TOKEN": "mat_synthetic-token", "MULTICA_TASK_ID": tbInstallationID, "MULTICA_AGENT_ID": tbAgentID, "MULTICA_DAEMON_PORT": "4321", cli.TaskConfigRootEnv: "/tmp/synthetic-task-profile"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			teamBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			t.Setenv(name, value)
			for _, operation := range []string{"export", "import"} {
				args := []string{"teambridge", "token", operation, tbInstallationID}
				if operation == "export" {
					args = append(args, "--output-file", filepath.Join(teamBridgeTokenTestDirectory(t), "bot-token"))
				} else {
					args = append(args, "--token-stdin")
				}
				out, err := executeTeamBridge(t, tbSecret, args...)
				assertTeamBridgeTokenFailure(t, out, err)
			}
			if requests.Load() != 0 {
				t.Fatal("machine context reached the secret API")
			}
		})
	}
}
