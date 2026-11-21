//go:build teambridgetokenintegration

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// This opt-in test requires a CLI explicitly built from the same checkout. It
// connects that subprocess to real Auth, workspace guards, handlers, service,
// encryption and PostgreSQL, with only Mattermost itself replaced by a TLS stub.
func TestMattermostTokenCLIEncryptedRoundTrip(t *testing.T) {
	binary := os.Getenv("MULTICA_TOKEN_TEST_CLI")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("MULTICA_TOKEN_TEST_CLI must name the absolute path of the CLI built from this checkout")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("the explicitly selected CLI binary is unavailable")
	}
	const savedToken = "synthetic-cli-backup-token"
	const rotatedToken = "synthetic-cli-rotated-token"
	const botID = "abcdefghijklmnopqrstuvwxyz"
	var remoteCalls atomic.Int64
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/users/me" {
			http.Error(w, "unexpected Mattermost operation", http.StatusBadRequest)
			return
		}
		if value := r.Header.Get("Authorization"); value != "Bearer "+savedToken && value != "Bearer "+rotatedToken {
			http.Error(w, "invalid synthetic token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": botID, "username": "fixture-bot", "is_bot": true, "delete_at": 0})
	}))
	defer remote.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = remote.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()
	box, err := secretbox.New(bytes.Repeat([]byte{42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := mattermost.NewServerPolicy(remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := mattermost.NewInstallService(testHandler.Queries, testPool, box, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.MattermostInstall = svc
	api := httptest.NewServer(tokenHandlerRouter(&h, nil))
	defer api.Close()
	pat := tokenHandlerPAT(t, testUserID)
	for _, status := range []string{"active", "revoked"} {
		t.Run(status, func(t *testing.T) {
			sealed, err := box.Seal([]byte(savedToken))
			if err != nil {
				t.Fatal(err)
			}
			agent := dbfx.Agent(t, "CLI token roundtrip", "")
			config, err := json.Marshal(map[string]any{
				"app_id": "cli-token-" + uuid.NewString(), "server_url": remote.URL,
				"bot_user_id": botID, "bot_username": "fixture-bot",
				"access_token_encrypted": base64.StdEncoding.EncodeToString(sealed),
				"context":                map[string]any{"mode": "channel", "lookback_hours": 8760},
				"attachment_mode":        "links", "access_mode": "allowlist",
				"allowed_users":  []any{map[string]any{"id": "zzzzzzzzzzzzzzzzzzzzzzzzzz", "username": "sergey"}},
				"denied_reply":   map[string]any{"mode": "text", "text": "Synthetic refusal"},
				"future_setting": map[string]any{"preserved": true},
			})
			if err != nil {
				t.Fatal(err)
			}
			id := dbfx.Insert(t, "channel_installation", testutil.Cols{
				"workspace_id": testWorkspaceID, "agent_id": agent, "channel_type": "mattermost",
				"config": string(config), "installer_user_id": testUserID, "status": status,
			})
			before, err := svc.GetInWorkspace(t.Context(), parseUUID(id), parseUUID(testWorkspaceID))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			backup := filepath.Join(dir, "backup-token")
			rotated := filepath.Join(dir, "rotated-token")
			if err := os.WriteFile(rotated, []byte(rotatedToken), 0600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) map[string]any {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				args = append([]string{"--workspace-id", testWorkspaceID, "--debug", "teambridge", "token"}, args...)
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Dir = dir
				cmd.Env = []string{
					"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XDG_CONFIG_HOME=" + dir,
					"XDG_CACHE_HOME=" + dir, "MULTICA_TOKEN=" + pat,
					"MULTICA_SERVER_URL=" + api.URL, "NO_PROXY=127.0.0.1,localhost",
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				for _, secret := range []string{savedToken, rotatedToken, pat} {
					if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
						t.Fatal("CLI exposed a credential in stdout/stderr with --debug")
					}
				}
				if err != nil {
					t.Fatalf("synthetic CLI operation failed: %v; %s", err, stderr.String())
				}
				var result map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
					t.Fatal("CLI did not return safe JSON metadata")
				}
				if result["installation_id"] != id || result["workspace_id"] != testWorkspaceID {
					t.Fatal("CLI response lost the selected installation/workspace identity")
				}
				return result
			}
			callsBefore := remoteCalls.Load()
			if run("export", id, "--output-file", backup)["exported"] != true {
				t.Fatal("CLI did not confirm the export")
			}
			data, err := os.ReadFile(backup)
			if err != nil || string(data) != savedToken {
				t.Fatal("CLI export did not save the exact decrypted token")
			}
			if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("CLI exported token file is not private")
			}
			if remoteCalls.Load() != callsBefore {
				t.Fatal("backup contacted Mattermost")
			}
			if run("import", id, "--token-file", rotated)["updated"] != true {
				t.Fatal("CLI did not rotate the existing token")
			}
			if actual, err := svc.ExportToken(t.Context(), parseUUID(id), parseUUID(testWorkspaceID)); err != nil || actual != rotatedToken {
				t.Fatal("rotation did not persist the encrypted synthetic token")
			}
			if run("import", id, "--token-file", backup)["updated"] != true {
				t.Fatal("CLI did not restore the backup")
			}
			if actual, err := svc.ExportToken(t.Context(), parseUUID(id), parseUUID(testWorkspaceID)); err != nil || actual != savedToken {
				t.Fatal("CLI import did not restore the exact saved token")
			}
			after, err := svc.GetInWorkspace(t.Context(), parseUUID(id), parseUUID(testWorkspaceID))
			if err != nil {
				t.Fatal(err)
			}
			var beforeConfig, afterConfig map[string]any
			if json.Unmarshal(before.Config, &beforeConfig) != nil || json.Unmarshal(after.Config, &afterConfig) != nil {
				t.Fatal("persisted configuration is invalid")
			}
			delete(beforeConfig, "access_token_encrypted")
			delete(afterConfig, "access_token_encrypted")
			if !reflect.DeepEqual(beforeConfig, afterConfig) {
				t.Fatal("restoring a token changed public or unknown settings")
			}
			persisted := after
			before.Config, after.Config = nil, nil
			after.UpdatedAt = before.UpdatedAt
			if !reflect.DeepEqual(before, after) {
				t.Fatal("restoring a token changed installation identity, status, audit or lease fields")
			}
			callsBefore = remoteCalls.Load()
			if run("import", id, "--token-file", backup)["updated"] != false {
				t.Fatal("identical token import was not a no-op")
			}
			unchanged, err := svc.GetInWorkspace(t.Context(), parseUUID(id), parseUUID(testWorkspaceID))
			if err != nil || !reflect.DeepEqual(persisted, unchanged) || remoteCalls.Load() != callsBefore {
				t.Fatal("identical import changed storage or contacted Mattermost")
			}
		})
	}
}
