package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type contextUpdateQueries struct {
	installQueries
	params db.UpdateMattermostInstallationSettingsParams
	row    db.ChannelInstallation
	err    error
}

func (q *contextUpdateQueries) UpdateMattermostInstallationSettings(_ context.Context, params db.UpdateMattermostInstallationSettingsParams) (db.ChannelInstallation, error) {
	q.params = params
	return q.row, q.err
}

func TestUpdateContextScopesWrite(t *testing.T) {
	id := mmUUID(t, "11111111-1111-4111-8111-111111111111")
	wsID := mmUUID(t, "22222222-2222-4222-8222-222222222222")
	q := &contextUpdateQueries{row: db.ChannelInstallation{ID: id, WorkspaceID: wsID}}
	service := &InstallService{q: q}
	settings := ContextSettings{Mode: ContextChannel, LookbackHours: 720}
	row, err := service.UpdateSettings(context.Background(), id, wsID, SettingsUpdate{Context: &settings})
	if err != nil || row.ID != id || row.WorkspaceID != wsID {
		t.Fatalf("update: row=%+v err=%v", row, err)
	}
	if q.params.ID != id || q.params.WorkspaceID != wsID {
		t.Fatal("update lost installation or workspace scope")
	}
	var got SettingsUpdate
	if err := json.Unmarshal(q.params.Settings, &got); err != nil || got.Context == nil || *got.Context != settings || got.AccessMode != nil {
		t.Fatalf("persisted context = %+v, err=%v", got, err)
	}
}

func TestAttachmentModeUpdatesOnlyItsOwnSetting(t *testing.T) {
	for _, mode := range []AttachmentMode{AttachmentsLinks, AttachmentsNone} {
		q := &contextUpdateQueries{}
		if _, err := (&InstallService{q: q}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{AttachmentMode: &mode}); err != nil {
			t.Fatal(err)
		}
		if string(q.params.Settings) != `{"attachment_mode":"`+string(mode)+`"}` {
			t.Fatalf("attachment update changed other settings: %s", q.params.Settings)
		}
	}
	for _, mode := range []AttachmentMode{"", "inline", "unknown"} {
		if _, err := (&InstallService{}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{AttachmentMode: &mode}); !errors.Is(err, ErrInvalidAttachmentMode) {
			t.Fatalf("invalid mode accepted: %v", err)
		}
	}
}

func TestUpdateContextRejectsInvalidSettingsBeforeWriting(t *testing.T) {
	service := &InstallService{}
	for _, settings := range []ContextSettings{
		{Mode: "unknown"}, {Mode: ContextChannel, LookbackHours: 4},
		{Mode: ContextThread, LookbackHours: 72},
	} {
		if _, err := service.UpdateSettings(context.Background(), mmUUID(t, "11111111-1111-4111-8111-111111111111"),
			mmUUID(t, "22222222-2222-4222-8222-222222222222"), SettingsUpdate{Context: &settings}); !errors.Is(err, ErrInvalidContextSettings) {
			t.Fatalf("invalid settings %+v accepted: %v", settings, err)
		}
	}
}

func TestUpdateContextReportsMissingInstallationsAndWriteFailures(t *testing.T) {
	for _, cause := range []error{pgx.ErrNoRows, errors.New("database unavailable")} {
		q := &contextUpdateQueries{err: cause}
		_, err := (&InstallService{q: q}).UpdateSettings(context.Background(), q.row.ID, q.row.WorkspaceID, SettingsUpdate{Context: &ContextSettings{Mode: ContextMessage}})
		want := cause
		if errors.Is(cause, pgx.ErrNoRows) {
			want = ErrInstallationNotFound
		}
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
}

func TestUpdateAccessValidatesBeforeWriting(t *testing.T) {
	for _, mode := range []AccessMode{"", "unknown", "everyone"} {
		if _, err := (&InstallService{}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{AccessMode: &mode}); !errors.Is(err, ErrInvalidAccessMode) {
			t.Fatalf("invalid access %q accepted: %v", mode, err)
		}
	}
	mode := AccessAnyone
	q := &contextUpdateQueries{}
	if _, err := (&InstallService{q: q}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{AccessMode: &mode}); err != nil {
		t.Fatal(err)
	}
	if string(q.params.Settings) != `{"access_mode":"anyone"}` {
		t.Fatalf("access-only update overwrote other settings: %s", q.params.Settings)
	}
}

func TestUpdateAllowlistRequiresAnExplicitListAndClearsItAtomically(t *testing.T) {
	mode := AccessAllowlist
	for _, settings := range []SettingsUpdate{
		{AccessMode: &mode},
		{AllowedUsernames: []string{"alice"}},
		{AccessMode: &mode, AllowedUsernames: []string{"alice/bob"}},
	} {
		if _, err := (&InstallService{}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, settings); !errors.Is(err, ErrInvalidAllowedUsers) {
			t.Fatalf("invalid allowlist accepted: %v", err)
		}
	}
	q := &contextUpdateQueries{}
	if _, err := (&InstallService{q: q}).UpdateSettings(context.Background(), pgtype.UUID{}, pgtype.UUID{}, SettingsUpdate{
		AccessMode: &mode, AllowedUsernames: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if string(q.params.Settings) != `{"access_mode":"allowlist","allowed_users":[]}` {
		t.Fatalf("empty list did not revoke access in the same write: %s", q.params.Settings)
	}
}
