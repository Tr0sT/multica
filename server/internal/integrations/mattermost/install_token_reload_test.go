package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

type tokenReloadStore struct {
	lark.ChannelInstallationStore
	id    pgtype.UUID
	polls atomic.Int64
}

func (s *tokenReloadStore) ListActiveInstallations(ctx context.Context) ([]engine.Installation, error) {
	rows, err := s.ChannelInstallationStore.ListActiveInstallations(ctx)
	if err != nil {
		return nil, err
	}
	selected := []engine.Installation{}
	for _, row := range rows {
		if row.ID == s.id {
			selected = append(selected, row)
		}
	}
	s.polls.Add(1)
	return selected, nil
}

func TestMattermostTokenRestoreReloadsOnlyActiveChangedConnectionDB(t *testing.T) {
	fx, q, agent := mattermostDBFixture(t)
	connections := make(chan string, 16)
	closed := make(chan struct{}, 16)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPath+"/users/me" {
			_ = json.NewEncoder(w).Encode(User{ID: "abcdefghijklmnopqrstuvwxab", Username: "reload-bot", IsBot: true})
			return
		}
		if r.URL.Path != websocketPath {
			http.NotFound(w, r)
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer func() { closed <- struct{}{} }()
		var action wsAction
		if conn.ReadJSON(&action) != nil {
			return
		}
		token, _ := action.Data["token"].(string)
		connections <- token
		if conn.WriteJSON(map[string]any{"status": statusOK, "seq_reply": action.Seq}) != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	policy, err := NewServerPolicy(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{19}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewInstallService(q, fx.Pool, box, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.httpClient = server.Client()
	const original = "synthetic-reload-original"
	const restored = "synthetic-reload-restored"
	const revokedToken = "synthetic-reload-revoked"
	row, err := svc.Register(context.Background(), RegisterParams{
		WorkspaceID: mmUUID(t, fx.WorkspaceID), AgentID: mmUUID(t, agent), InitiatorID: mmUUID(t, fx.UserID), ServerURL: server.URL, AccessToken: original,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	registry := channel.NewRegistry()
	RegisterMattermost(registry, ChannelDeps{
		Decrypt: box.Open, Policy: policy, Logger: logger, HTTPClient: server.Client(),
		Dialer: &websocket.Dialer{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig},
	})
	store := &tokenReloadStore{ChannelInstallationStore: lark.NewChannelInstallationStore(q), id: row.ID}
	supervisor := engine.NewSupervisor(store, store, registry, func(context.Context, channel.InboundMessage) error { return nil }, engine.Config{
		PollInterval: 20 * time.Millisecond, LeaseTTL: time.Second, LeaseRenewInterval: 200 * time.Millisecond,
		MinBackoff: 5 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, Logger: logger,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !supervisor.WaitWithTimeout(5 * time.Second) {
			t.Error("supervisor failed to stop")
		}
	})
	waitConnection := func(want string) {
		t.Helper()
		select {
		case got := <-connections:
			if got != want {
				t.Fatal("connection authenticated with stale or unexpected token")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("expected connection did not authenticate")
		}
	}
	waitClosed := func() {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("old connection did not close")
		}
	}
	waitPolls := func() {
		t.Helper()
		target := store.polls.Load() + 5
		deadline := time.After(5 * time.Second)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for store.polls.Load() < target {
			select {
			case <-tick.C:
			case <-deadline:
				t.Fatal("supervisor stopped polling")
			}
		}
	}
	waitConnection(original)
	if changed, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, restored); err != nil || !changed {
		t.Fatalf("restore failed: %v", err)
	}
	waitConnection(restored)
	waitClosed()
	if changed, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, restored); err != nil || changed {
		t.Fatalf("identical restore was not a no-op: %v", err)
	}
	waitPolls()
	select {
	case <-connections:
		t.Fatal("identical token restarted the connection")
	default:
	}
	if err := svc.Revoke(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	waitClosed()
	if changed, err := svc.ImportToken(ctx, row.ID, row.WorkspaceID, revokedToken); err != nil || !changed {
		t.Fatalf("revoked credential restore failed: %v", err)
	}
	waitPolls()
	select {
	case <-connections:
		t.Fatal("revoked token restore reactivated the connection")
	default:
	}
	cancel()
	if !supervisor.WaitWithTimeout(5 * time.Second) {
		t.Fatal("supervisor did not finish")
	}
	for _, token := range []string{original, restored, revokedToken} {
		if strings.Contains(logs.String(), token) {
			t.Fatal("supervisor log exposed a token")
		}
	}
}
