package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUpdateMattermostInstallationRejectsInvalidInput(t *testing.T) {
	h := &Handler{MattermostInstall: &mattermost.InstallService{}}
	validID := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct{ workspace, installation, body string }{
		{"bad-id", validID, `{"context":{"mode":"message"}}`},
		{validID, "bad-id", `{"context":{"mode":"message"}}`},
		{validID, validID, `{`},
		{validID, validID, `{}`},
		{validID, validID, `{"context":null}`},
		{validID, validID, `{"context":{}}`},
		{validID, validID, `{"context":{"mode":"unknown"}}`},
		{validID, validID, `{"context":{"mode":"channel","lookback_hours":4}}`},
		{validID, validID, `{"access_mode":"unknown"}`},
		{validID, validID, `{"access_mode":""}`},
		{validID, validID, `{"access_mode":true}`},
		{validID, validID, `{"access_mode":null}`},
	} {
		r := testutil.JSONRequest(http.MethodPatch, "/", tc.body)
		testutil.WithHeaders(r, "X-User-ID", validID)
		r = testutil.WithURLParams(r, "id", tc.workspace, "installationId", tc.installation)
		testutil.Call(t, h.UpdateMattermostInstallation, r).Want(http.StatusBadRequest)
	}
}

func TestMattermostAccessResponse(t *testing.T) {
	for _, tc := range []struct{ config, want string }{
		{`{}`, "members"},
		{`{"access_mode":"anyone"}`, "anyone"},
		{`{"access_mode":"future-mode"}`, "unknown"},
	} {
		response := mattermostInstallationToResponse(db.ChannelInstallation{Config: []byte(tc.config)})
		if string(response.AccessMode) != tc.want {
			t.Fatalf("access for %s = %s, want %s", tc.config, response.AccessMode, tc.want)
		}
	}
}

func TestMattermostBindingRequiresExplicitConfirmation(t *testing.T) {
	// No database is wired: these requests must be rejected before redemption.
	h := &Handler{MattermostBindingTokens: &mattermost.BindingTokenService{}}
	for _, body := range []string{`{"token":"token"}`, `{"token":"token","confirmed":false}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/mattermost/binding/redeem", strings.NewReader(body))
		r.Header.Set("X-User-ID", "11111111-1111-4111-8111-111111111111")
		w := httptest.NewRecorder()
		h.RedeemMattermostBindingToken(w, r)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "confirmation") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestMattermostInstallationResponseExcludesCredentials(t *testing.T) {
	response := mattermostInstallationToResponse(db.ChannelInstallation{
		Config: []byte(`{"server_url":"https://chat.example.com","bot_user_id":"bot","access_token_encrypted":"ciphertext-sentinel","context":{"mode":"channel","lookback_hours":24}}`),
	})
	if response.Context.Mode != mattermost.ContextChannel || response.Context.LookbackHours != 24 {
		t.Fatal("management response lost context settings")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "ciphertext-sentinel") {
		t.Fatal("management response exposed credentials")
	}
}

func TestMattermostExtendedContextResponse(t *testing.T) {
	for _, hours := range []int{0, 1440, 2160, 8760} {
		config, err := json.Marshal(map[string]any{
			"context": mattermost.ContextSettings{Mode: mattermost.ContextChannel, LookbackHours: hours},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := mattermostInstallationToResponse(db.ChannelInstallation{Config: config})
		if response.Context.Mode != "channel_extended" || response.Context.LookbackHours != 0 ||
			response.Context.ExtendedLookbackHours == nil || *response.Context.ExtendedLookbackHours != hours {
			t.Fatalf("period %d lost or incompatible with older clients: %+v", hours, response.Context)
		}
	}
}
