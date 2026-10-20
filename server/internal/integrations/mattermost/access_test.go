package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAllowedUsernamesValidation(t *testing.T) {
	for _, tc := range []struct {
		mode    AccessMode
		names   []string
		want    []string
		invalid bool
	}{
		{mode: AccessMembers},
		{mode: AccessAnyone, names: []string{"alice"}, invalid: true},
		{mode: AccessAllowlist, invalid: true},
		{mode: AccessAllowlist, names: []string{}, want: []string{}},
		{mode: AccessAllowlist, names: []string{" @Alice ", "bob.smith", "alice"}, want: []string{"alice", "bob.smith"}},
		{mode: AccessAllowlist, names: []string{""}, invalid: true},
		{mode: AccessAllowlist, names: []string{"alice/bob"}, invalid: true},
		{mode: AccessAllowlist, names: []string{"alice bob"}, invalid: true},
		{mode: AccessAllowlist, names: make([]string, 101), invalid: true},
	} {
		got, err := normalizeAllowedUsernames(tc.mode, tc.names)
		if tc.invalid {
			if !errors.Is(err, ErrInvalidAllowedUsers) {
				t.Fatalf("%+v accepted: %v", tc, err)
			}
		} else if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v: got=%v err=%v", tc, got, err)
		}
	}
}

func TestResolveAllowedUsersPreservesAccountsAndChecksNewUsernames(t *testing.T) {
	previous := AllowedUser{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Username: "alice"}
	bob := AllowedUser{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Username: "bob"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var names []string
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/users/usernames" ||
			r.Header.Get("Authorization") != "Bearer bot-token" ||
			json.NewDecoder(r.Body).Decode(&names) != nil || !reflect.DeepEqual(names, []string{"bob"}) {
			t.Errorf("unexpected user lookup: %s %s %v", r.Method, r.URL.Path, names)
		}
		_ = json.NewEncoder(w).Encode([]AllowedUser{bob})
	}))
	defer srv.Close()
	got, err := newRESTClient(srv.URL, "bot-token", srv.Client()).resolveAllowedUsers(
		context.Background(), []string{"alice", "bob"}, []AllowedUser{previous},
	)
	if err != nil || !reflect.DeepEqual(got, []AllowedUser{previous, bob}) {
		t.Fatalf("resolved accounts=%v err=%v", got, err)
	}
}

func TestResolveAllowedUsersFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   error
	}{
		{`[]`, 200, ErrInvalidAllowedUsers},
		{`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","username":"someone-else"}]`, 200, ErrInvalidAllowedUsers},
		{`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","username":"alice","is_bot":true}]`, 200, ErrInvalidAllowedUsers},
		{`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","username":"alice","delete_at":123}]`, 200, ErrInvalidAllowedUsers},
		{`[{"id":"","username":"alice"}]`, 200, ErrInvalidAllowedUsers},
		{`{}`, 200, ErrAllowedUsersUnverifiable},
		{`{}`, 403, ErrAllowedUsersUnverifiable},
		{`{}`, 503, ErrAllowedUsersUnverifiable},
	} {
		t.Run(tc.body+http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := newRESTClient(srv.URL, "token", srv.Client()).resolveAllowedUsers(context.Background(), []string{"alice"}, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestAllowlistRejectsUnlistedSendersBeforeAccountBinding(t *testing.T) {
	for _, config := range []string{
		`{"access_mode":"allowlist"}`,
		`{"access_mode":"allowlist","allowed_users":[]}`,
		`{"access_mode":"allowlist","allowed_users":[{"id":"alice-id","username":"alice"}]}`,
	} {
		inst := engine.ResolvedInstallation{Platform: db.ChannelInstallation{Config: []byte(config)}}
		_, err := (&identityResolver{}).ResolveSender(context.Background(), inst, channel.InboundMessage{
			Source: channel.Source{SenderID: "bob-id"},
		})
		if !errors.Is(err, engine.ErrSenderNotMember) {
			t.Fatalf("unlisted sender with config %s: error = %v", config, err)
		}
	}
}

func TestAccessModesKeepSeparateConversationHistory(t *testing.T) {
	installer := mmUUID(t, "11111111-1111-4111-8111-111111111111")
	keys := make(map[string]bool)
	for _, mode := range []AccessMode{"members", "anyone", "allowlist"} {
		inst := engine.ResolvedInstallation{
			InstallerUserID: installer,
			Platform:        db.ChannelInstallation{Config: []byte(`{"access_mode":"` + mode + `"}`)},
		}
		key, err := sessionKeyForAccess(inst, "dm-alice")
		if err != nil || keys[key] {
			t.Fatalf("%s did not get separate history: key=%q err=%v", mode, key, err)
		}
		keys[key] = true
	}
}
