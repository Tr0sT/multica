package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
