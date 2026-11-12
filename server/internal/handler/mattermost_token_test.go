package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/integrations/mattermost"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

const tokenHandlerSecret = "synthetic-handler-backup-secret"

func tokenHandlerFixture(t *testing.T) (*Handler, string, string) {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{12}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(tokenHandlerSecret))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := mattermost.NewServerPolicy("https://mattermost.example.test")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := mattermost.NewInstallService(testHandler.Queries, testPool, box, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	agent := dbfx.Agent(t, "Token handler bot", "")
	config, err := json.Marshal(map[string]any{
		"app_id": "token-handler-" + uuid.NewString(), "server_url": "https://mattermost.example.test",
		"bot_user_id": "syntheticbotaccountabcdefgh", "bot_username": "test-bot",
		"access_token_encrypted": base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Insert(t, "channel_installation", testutil.Cols{
		"workspace_id": testWorkspaceID, "agent_id": agent, "channel_type": "mattermost",
		"config": string(config), "installer_user_id": testUserID, "status": "revoked",
	})
	h := &Handler{Queries: testHandler.Queries, MattermostInstall: svc}
	return h, id, agent
}

func tokenHandlerPAT(t *testing.T, user string) string {
	t.Helper()
	token := "mul_synthetic_" + uuid.NewString()
	dbfx.Insert(t, "personal_access_token", testutil.Cols{
		"user_id": user, "name": "synthetic token test", "token_hash": auth.HashToken(token), "token_prefix": "mul_synthetic",
	})
	return token
}

func tokenHandlerRouter(h *Handler, cloud *auth.CloudPATVerifier) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.Auth(h.Queries, nil, cloud, nil))
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceRoleFromURL(h.Queries, "id", "owner", "admin"))
		r.With(RequireHumanActor).Get("/mattermost/installations/{installationId}/token", h.ExportMattermostToken)
		r.With(RequireHumanActor).Put("/mattermost/installations/{installationId}/token", h.ImportMattermostToken)
	})
	return router
}

func TestMattermostTokenRoutesRequireHumanWorkspaceAdmin(t *testing.T) {
	h, id, agent := tokenHandlerFixture(t)
	admin := createTestUserAndMember(t, "admin")
	member := createTestUserAndMember(t, "member")
	outsider := createTestUserAndMember(t, "")
	ownerPAT, adminPAT, memberPAT := tokenHandlerPAT(t, testUserID), tokenHandlerPAT(t, admin), tokenHandlerPAT(t, member)
	outsiderPAT := tokenHandlerPAT(t, outsider)
	taskToken := "mat_synthetic_" + uuid.NewString()
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID})
	dbfx.Insert(t, "task_token", testutil.Cols{
		"token_hash": auth.HashToken(taskToken), "task_id": task, "agent_id": agent,
		"workspace_id": testWorkspaceID, "user_id": testUserID, "expires_at": time.Now().Add(time.Hour),
	})
	daemonToken := "mdt_synthetic_" + uuid.NewString()
	dbfx.Insert(t, "daemon_token", testutil.Cols{
		"token_hash": auth.HashToken(daemonToken), "workspace_id": testWorkspaceID,
		"daemon_id": "synthetic-token-test-daemon", "expires_at": time.Now().Add(time.Hour),
	})
	// Prove this is a valid machine credential, not merely a malformed token
	// rejected by every authentication surface.
	daemonProbe := middleware.DaemonAuth(h.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware.DaemonIDFromContext(r.Context()) != "synthetic-token-test-daemon" {
			t.Error("daemon identity was not authenticated")
		}
		w.WriteHeader(http.StatusOK)
	}))
	probe := testutil.JSONRequest(http.MethodGet, "/unused", nil)
	probe.Header.Set("Authorization", "Bearer "+daemonToken)
	testutil.Call(t, daemonProbe.ServeHTTP, probe).Want(http.StatusOK)
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "owner_id": testUserID})
	}))
	t.Cleanup(fleet.Close)
	router := tokenHandlerRouter(h, auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{FleetBaseURL: fleet.URL}))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, tc := range []struct {
			name, token string
			status      int
		}{
			{"owner", ownerPAT, http.StatusOK}, {"admin", adminPAT, http.StatusOK},
			{"member", memberPAT, http.StatusForbidden}, {"task-owner", taskToken, http.StatusForbidden},
			{"outsider", outsiderPAT, http.StatusNotFound},
			{"cloud-owner", "mcn_synthetic-test-node", http.StatusForbidden},
			{"daemon", daemonToken, http.StatusUnauthorized},
			{"plugin", "mpi_synthetic-test-plugin", http.StatusUnauthorized},
			{"unauthenticated", "", http.StatusUnauthorized},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				body := map[string]string{"access_token": tokenHandlerSecret}
				req := testutil.JSONRequest(method, "/api/workspaces/"+testWorkspaceID+"/mattermost/installations/"+id+"/token", body)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				// A machine cannot erase its authoritative actor classification.
				req.Header.Set("X-Actor-Source", "member")
				req.Header.Set("X-User-ID", testUserID)
				recorder := testutil.Call(t, router.ServeHTTP, req).Want(tc.status)
				if tc.status == http.StatusOK && recorder.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("secret endpoint allowed caching")
				}
				if tc.status == http.StatusOK && method == http.MethodGet {
					var body struct {
						AccessToken string `json:"access_token"`
					}
					recorder.JSON(&body)
					if body.AccessToken != tokenHandlerSecret {
						t.Fatal("authorized backup changed stored token")
					}
				} else if strings.Contains(recorder.Body.String(), tokenHandlerSecret) {
					t.Fatal("non-export response exposed token")
				}
			})
		}
	}
	foreign := dbfx.Workspace(t, "Other token workspace", "token-other-"+uuid.NewString())
	dbfx.Member(t, foreign, testUserID, "owner")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req := testutil.JSONRequest(method, "/api/workspaces/"+foreign+"/mattermost/installations/"+id+"/token", map[string]string{"access_token": tokenHandlerSecret})
		req.Header.Set("Authorization", "Bearer "+ownerPAT)
		recorder := testutil.Call(t, router.ServeHTTP, req).Want(http.StatusNotFound)
		if strings.Contains(recorder.Body.String(), tokenHandlerSecret) {
			t.Fatal("cross-workspace error leaked token")
		}
	}
}

func TestMattermostTokenHandlersRetainAuthorizationBackstop(t *testing.T) {
	h, id, _ := tokenHandlerFixture(t)
	member := createTestUserAndMember(t, "member")
	for _, handler := range []http.HandlerFunc{h.ExportMattermostToken, h.ImportMattermostToken} {
		for _, tc := range []struct{ user, source string }{{member, ""}, {testUserID, "task_token"}, {testUserID, "cloud_pat"}} {
			req := testutil.WithURLParams(newRequest(http.MethodPut, "/unused", nil), "id", testWorkspaceID, "installationId", id)
			req.Header.Set("X-User-ID", tc.user)
			req.Header.Set("X-Actor-Source", tc.source)
			testutil.Call(t, handler, req).Want(http.StatusForbidden)
		}
	}
}

func TestMattermostTokenMalformedRequestsAndPublicResponsesStaySecretFree(t *testing.T) {
	h, id, _ := tokenHandlerFixture(t)
	for _, body := range []string{
		`{"` + tokenHandlerSecret + `":"unknown"}`, `{"access_token":123}`, `null`,
		`{"access_token":"` + tokenHandlerSecret + `"} {}`, `{"access_token":"invalid\r\ntoken"}`,
		`{"access_token":"` + strings.Repeat("x", 33<<10) + `"}`,
	} {
		req := testutil.WithURLParams(testutil.JSONRequest(http.MethodPut, "/unused", body), "id", testWorkspaceID, "installationId", id)
		req.Header.Set("X-User-ID", testUserID)
		recorder := testutil.Call(t, h.ImportMattermostToken, req).Want(http.StatusBadRequest)
		if strings.Contains(recorder.Body.String(), tokenHandlerSecret) {
			t.Fatal("malformed request echoed secret")
		}
	}
	req := testutil.WithURLParams(newRequest(http.MethodGet, "/unused", nil), "id", testWorkspaceID)
	recorder := testutil.Call(t, h.ListMattermostInstallations, req).Want(http.StatusOK)
	for _, forbidden := range []string{tokenHandlerSecret, "access_token", "encrypted"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatal("ordinary list response exposed credential")
		}
	}
}
