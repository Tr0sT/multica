package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
)

type tokenTestTransport func(*http.Request) (*http.Response, error)

func (f tokenTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMattermostTokenBackupOfflineAndRevokedDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	row, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	svc.httpClient = &http.Client{Transport: tokenTestTransport(func(*http.Request) (*http.Response, error) {
		t.Error("backup or identical import contacted Mattermost")
		return nil, errors.New("synthetic offline server")
	})}
	// An operator removing a server from the outbound allowlist still permits
	// reading a stored backup, without gaining a new outbound request path.
	svc.policy = &ServerPolicy{servers: map[string]struct{}{}}
	for _, status := range []string{"active", "revoked"} {
		if status == "revoked" {
			if err := svc.Revoke(ctx, row.ID); err != nil {
				t.Fatal(err)
			}
		}
		before, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.ExportToken(ctx, row.ID, row.WorkspaceID)
		if err != nil || got != params.AccessToken {
			t.Fatalf("%s backup failed or changed the token: %v", status, err)
		}
		updated, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, params.AccessToken)
		if err != nil || updated {
			t.Fatalf("identical %s token was not a no-op: updated=%t err=%v", status, updated, err)
		}
		after, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("%s backup/no-op mutated its installation", status)
		}
	}
}

func TestMattermostTokenRestorePreservesInstallationDB(t *testing.T) {
	for _, status := range []string{"active", "revoked"} {
		t.Run(status, func(t *testing.T) {
			fx, q, agent := mattermostDBFixture(t)
			svc, params := mattermostInstallHarness(t, fx, q, agent)
			ctx := context.Background()
			params.Context = ContextSettings{Mode: ContextChannel, LookbackHours: 8760}
			params.AccessMode = AccessAllowlist
			params.AttachmentMode = AttachmentsNone
			params.AllowedUsernames = []string{"alice"}
			params.DeniedReply = DeniedReply{Mode: "text", Text: "Keep this refusal."}
			row, err := svc.Register(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			fx.Exec(t, `UPDATE channel_installation SET config=config || '{"future_setting":{"preserve":true}}'::jsonb, status=$2 WHERE id=$1`, row.ID, status)
			before, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			binding := fx.Insert(t, "channel_user_binding", dbfx.Cols{
				"workspace_id": fx.WorkspaceID, "multica_user_id": fx.UserID, "installation_id": row.ID,
				"channel_type": "mattermost", "channel_user_id": "external-user", "config": "{}",
			})
			chat := fx.ChatSession(t, agent)
			route := fx.Insert(t, "channel_chat_session_binding", dbfx.Cols{
				"chat_session_id": chat, "installation_id": row.ID, "channel_type": "mattermost",
				"channel_chat_id": "room", "chat_type": "p2p", "config": "{}",
			})
			message := fx.Insert(t, "chat_message", dbfx.Cols{"chat_session_id": chat, "role": "user", "content": "Preserve conversation history."})
			const restored = "synthetic-restored-backup-token"
			updated, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, restored)
			if err != nil || !updated {
				t.Fatalf("token restore failed: updated=%t err=%v", updated, err)
			}
			after, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			beforeConfig, afterConfig := map[string]any{}, map[string]any{}
			if json.Unmarshal(before.Config, &beforeConfig) != nil || json.Unmarshal(after.Config, &afterConfig) != nil {
				t.Fatal("invalid persisted config")
			}
			delete(beforeConfig, "access_token_encrypted")
			delete(afterConfig, "access_token_encrypted")
			if !reflect.DeepEqual(beforeConfig, afterConfig) {
				t.Fatal("token restore changed settings or server/account identity")
			}
			metadata := before
			metadata.Config, metadata.UpdatedAt = after.Config, after.UpdatedAt
			if !reflect.DeepEqual(metadata, after) {
				t.Fatal("token restore changed installation ID, status, lease, binding or installer metadata")
			}
			if bytes.Contains(after.Config, []byte(restored)) {
				t.Fatal("restored token is plaintext in storage")
			}
			if got, err := svc.ExportToken(ctx, row.ID, row.WorkspaceID); err != nil || got != restored {
				t.Fatalf("restored credential did not round-trip: %v", err)
			}
			for _, check := range []struct{ table, id string }{{"channel_user_binding", binding}, {"channel_chat_session_binding", route}, {"chat_session", chat}, {"chat_message", message}} {
				if fx.Count(t, `SELECT count(*) FROM `+check.table+` WHERE id=$1`, check.id) != 1 {
					t.Fatalf("token restore removed %s", check.table)
				}
			}
			if changed, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, restored); err != nil || changed {
				t.Fatalf("second restore was not a no-op: %v", err)
			}
			repeated, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
			if err != nil || !reflect.DeepEqual(after, repeated) {
				t.Fatal("identical restore rewrote ciphertext or updated metadata")
			}
		})
	}
}

func TestMattermostTokenRestoreRejectsForeignOrInvalidSecretsDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	before, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  error
	}{
		{"", ErrInvalidAccessToken}, {"invalid\r\ntoken", ErrInvalidAccessToken},
		{strings.Repeat("x", maxBackupTokenBytes+1), ErrInvalidAccessToken},
		{"other-bot", ErrTokenIdentityMismatch}, {"personal", ErrNotABotAccount},
	} {
		if _, err := svc.ImportToken(ctx, before.ID, before.WorkspaceID, tc.token); !errors.Is(err, tc.want) {
			t.Fatalf("bad credential accepted or wrong safe error: %v", err)
		}
	}
	foreign := mmUUID(t, "77777777-7777-4777-8777-777777777777")
	if _, err := svc.ExportToken(ctx, before.ID, foreign); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("foreign workspace read was not denied: %v", err)
	}
	if _, err := svc.ImportToken(ctx, before.ID, foreign, "synthetic-other-token"); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("foreign workspace write was not denied: %v", err)
	}
	const maliciousToken = "synthetic-rejected-secret-must-not-leak"
	svc.httpClient = &http.Client{Transport: tokenTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"bot-a","username":"multica","is_bot":true,"delete_at":1}`)), Request: r}, nil
	})}
	if _, err := svc.ImportToken(ctx, before.ID, before.WorkspaceID, maliciousToken); !errors.Is(err, ErrCredentialsRejected) {
		t.Fatalf("token for a deleted bot was not rejected: %v", err)
	}
	for _, status := range []int{401, 403, 500, 302} {
		svc.httpClient = &http.Client{Transport: tokenTestTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"` + maliciousToken + `"}`)), Request: r}, nil
		})}
		_, err := svc.ImportToken(ctx, before.ID, before.WorkspaceID, maliciousToken)
		want := ErrCredentialsUnverifiable
		if status == 401 || status == 403 {
			want = ErrCredentialsRejected
		}
		if !errors.Is(err, want) || strings.Contains(fmt.Sprintf("%+v %#v", err, err), maliciousToken) {
			t.Fatalf("remote failure was not safely classified: %v", err)
		}
	}
	after, err := svc.GetInWorkspace(ctx, before.ID, before.WorkspaceID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed restoration corrupted the existing installation")
	}
}

func TestMattermostTokenRestorePreservesConcurrentSettingsAndRevocationDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	row, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	transport := svc.httpClient.Transport
	var once sync.Once
	svc.httpClient.Transport = tokenTestTransport(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(started) })
		select {
		case <-resume:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return transport.RoundTrip(r)
	})
	finished := make(chan error, 1)
	go func() {
		updated, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, "synthetic-concurrent-restored-token")
		if err == nil && !updated {
			err = errors.New("token update unexpectedly skipped")
		}
		finished <- err
	}()
	<-started
	settings := ContextSettings{Mode: ContextChannel, LookbackHours: 720}
	if _, err := svc.UpdateSettings(ctx, row.ID, row.WorkspaceID, SettingsUpdate{Context: &settings}); err != nil {
		close(resume)
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, row.ID); err != nil {
		close(resume)
		t.Fatal(err)
	}
	close(resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	after, err := svc.GetInWorkspace(ctx, row.ID, row.WorkspaceID)
	if err != nil || after.Status != "revoked" || DecodePublicConfig(after.Config).Context != settings {
		t.Fatal("token update overwrote concurrent settings or reactivated the installation")
	}
}

func TestMattermostTokenRestoreFencesConcurrentReinstallDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	svc, params := mattermostInstallHarness(t, fx, q, agent)
	ctx := context.Background()
	row, err := svc.Register(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	transport := svc.httpClient.Transport
	svc.httpClient.Transport = tokenTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer synthetic-fenced-token" {
			close(started)
			select {
			case <-resume:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return transport.RoundTrip(r)
	})
	finished := make(chan error, 1)
	go func() {
		_, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, "synthetic-fenced-token")
		finished <- err
	}()
	<-started
	params.AccessToken = "other-bot"
	replacement, err := svc.Register(ctx, params)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("stale restore reached replacement identity: %v", err)
	}
	if got, err := svc.ExportToken(ctx, replacement.ID, replacement.WorkspaceID); err != nil || got != params.AccessToken {
		t.Fatalf("replacement token was changed: %v", err)
	}
}
