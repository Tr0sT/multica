package mattermost

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrTokenUnavailable         = errors.New("mattermost: saved bot access token is unavailable")
	ErrTokenIdentityMismatch    = errors.New("mattermost: access token belongs to a different bot account")
	ErrTokenInstallationChanged = errors.New("mattermost: installation identity changed during token verification; retry the operation")
	ErrTokenUpdateFailed        = errors.New("mattermost: could not update bot access token")
)

const maxBackupTokenBytes = 16 << 10

// ExportToken only reads the encrypted local record. A remote outage, revoked
// installation or changed operator allowlist must not prevent a backup.
func (s *InstallService) ExportToken(ctx context.Context, id, wsID pgtype.UUID) (string, error) {
	row, err := s.GetInWorkspace(ctx, id, wsID)
	if err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return "", err
		}
		return "", ErrTokenUnavailable
	}
	creds, err := decodeCredentials(row.Config, s.box.Open)
	if err != nil || len(creds.AccessToken) > maxBackupTokenBytes {
		return "", ErrTokenUnavailable
	}
	if _, err := validateAccessToken(creds.AccessToken); err != nil {
		return "", ErrTokenUnavailable
	}
	return creds.AccessToken, nil
}

// ImportToken restores credentials without reinstalling, changing status, or
// replacing any settings. Register shares the agent-slot lock, so its identity
// cannot change between the second scoped read and this token-only write.
func (s *InstallService) ImportToken(ctx context.Context, id, wsID pgtype.UUID, raw string) (bool, error) {
	if len(raw) > maxBackupTokenBytes {
		return false, ErrInvalidAccessToken
	}
	token, err := validateAccessToken(raw)
	if err != nil {
		return false, ErrInvalidAccessToken
	}
	row, err := s.GetInWorkspace(ctx, id, wsID)
	if err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return false, err
		}
		return false, ErrTokenUpdateFailed
	}
	creds, err := decodeCredentials(row.Config, s.box.Open)
	if err != nil {
		return false, ErrTokenUnavailable
	}
	if sameBotToken(creds.AccessToken, token) {
		return false, nil
	}
	serverURL, err := s.policy.validate(creds.ServerURL)
	if err != nil {
		return false, ErrServerNotAllowed
	}
	me, err := newRESTClient(serverURL, token, s.httpClient).GetMe(ctx)
	if err != nil {
		// A proxy or Mattermost error may echo Authorization. Never retain the
		// remote error, even in an unwrap chain or a diagnostic formatter.
		if status := statusOf(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
			return false, ErrCredentialsRejected
		}
		return false, ErrCredentialsUnverifiable
	}
	if !me.IsBot {
		return false, ErrNotABotAccount
	}
	if me.DeleteAt != 0 {
		return false, ErrCredentialsRejected
	}
	if creds.BotUserID == "" || me.ID != creds.BotUserID {
		return false, ErrTokenIdentityMismatch
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return false, ErrTokenUpdateFailed
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := qtx.LockChannelInstallationAppIDSlot(ctx, db.LockChannelInstallationAppIDSlotParams{
		ChannelType: "mattermost-agent", AppID: wsID.String() + ":" + row.AgentID.String(),
	}); err != nil {
		return false, ErrTokenUpdateFailed
	}
	current, err := qtx.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID: id, WorkspaceID: wsID, ChannelType: string(TypeMattermost),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrInstallationNotFound
	}
	if err != nil {
		return false, ErrTokenUpdateFailed
	}
	currentCreds, err := decodeCredentials(current.Config, s.box.Open)
	if err != nil {
		return false, ErrTokenUnavailable
	}
	if current.AgentID != row.AgentID || currentCreds.ServerURL != creds.ServerURL || currentCreds.BotUserID != creds.BotUserID {
		return false, ErrTokenInstallationChanged
	}
	if sameBotToken(currentCreds.AccessToken, token) {
		return false, nil
	}
	sealed, err := s.box.Seal([]byte(token))
	if err != nil {
		return false, ErrTokenUpdateFailed
	}
	_, err = qtx.UpdateMattermostInstallationToken(ctx, db.UpdateMattermostInstallationTokenParams{
		ID: id, WorkspaceID: wsID, Ciphertext: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return false, ErrTokenUpdateFailed
	}
	if err := tx.Commit(ctx); err != nil {
		return false, ErrTokenUpdateFailed
	}
	// The channel supervisor fingerprints persisted config and reloads active
	// installations on its next poll. Revoked records stay outside that set.
	return true, nil
}

func sameBotToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
