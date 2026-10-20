package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type contextUpdateQueries struct {
	installQueries
	params db.UpdateMattermostInstallationContextParams
	row    db.ChannelInstallation
	err    error
}

func (q *contextUpdateQueries) UpdateMattermostInstallationContext(_ context.Context, params db.UpdateMattermostInstallationContextParams) (db.ChannelInstallation, error) {
	q.params = params
	return q.row, q.err
}

func TestUpdateContextScopesWrite(t *testing.T) {
	id := mmUUID(t, "11111111-1111-4111-8111-111111111111")
	wsID := mmUUID(t, "22222222-2222-4222-8222-222222222222")
	q := &contextUpdateQueries{row: db.ChannelInstallation{ID: id, WorkspaceID: wsID}}
	service := &InstallService{q: q}
	settings := ContextSettings{Mode: ContextChannel, LookbackHours: 720}
	row, err := service.UpdateContext(context.Background(), id, wsID, settings)
	if err != nil || row.ID != id || row.WorkspaceID != wsID {
		t.Fatalf("update: row=%+v err=%v", row, err)
	}
	if q.params.ID != id || q.params.WorkspaceID != wsID {
		t.Fatal("update lost installation or workspace scope")
	}
	var got ContextSettings
	if err := json.Unmarshal(q.params.Context, &got); err != nil || got != settings {
		t.Fatalf("persisted context = %+v, err=%v", got, err)
	}
}

func TestUpdateContextRejectsInvalidSettingsBeforeWriting(t *testing.T) {
	service := &InstallService{}
	for _, settings := range []ContextSettings{
		{Mode: "unknown"}, {Mode: ContextChannel, LookbackHours: 4},
		{Mode: ContextThread, LookbackHours: 72},
	} {
		if _, err := service.UpdateContext(context.Background(), mmUUID(t, "11111111-1111-4111-8111-111111111111"),
			mmUUID(t, "22222222-2222-4222-8222-222222222222"), settings); !errors.Is(err, ErrInvalidContextSettings) {
			t.Fatalf("invalid settings %+v accepted: %v", settings, err)
		}
	}
}

func TestUpdateContextReportsMissingInstallationsAndWriteFailures(t *testing.T) {
	for _, cause := range []error{pgx.ErrNoRows, errors.New("database unavailable")} {
		q := &contextUpdateQueries{err: cause}
		_, err := (&InstallService{q: q}).UpdateContext(context.Background(), q.row.ID, q.row.WorkspaceID, ContextSettings{Mode: ContextMessage})
		want := cause
		if errors.Is(cause, pgx.ErrNoRows) {
			want = ErrInstallationNotFound
		}
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
}
