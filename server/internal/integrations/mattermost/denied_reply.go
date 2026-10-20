package mattermost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxDeniedReplyImageBytes = 5 << 20
const defaultDeniedReplyText = "This Mattermost account cannot use this bot. Ask a workspace admin to check the bot's access settings."

var ErrInvalidDeniedReply = errors.New("mattermost: choose text up to 4000 characters or a standalone PNG, JPEG, GIF or WebP image up to 5 MiB uploaded to this workspace")
var ErrDeniedReplyImageUnavailable = errors.New("mattermost: could not read the automatic reply image; upload it again or choose text")

type DeniedReply struct {
	Mode              string `json:"mode"`
	Text              string `json:"text,omitempty"`
	ImageAttachmentID string `json:"image_attachment_id,omitempty"`
}

func (reply DeniedReply) normalized() (DeniedReply, error) {
	if reply.Mode == "" {
		reply.Mode = "text"
	}
	switch reply.Mode {
	case "text":
		if reply.ImageAttachmentID != "" || !utf8.ValidString(reply.Text) || utf8.RuneCountInString(reply.Text) > 4000 {
			return DeniedReply{}, ErrInvalidDeniedReply
		}
	case "image":
		if _, err := util.ParseUUID(reply.ImageAttachmentID); err != nil || reply.Text != "" {
			return DeniedReply{}, ErrInvalidDeniedReply
		}
	default:
		return DeniedReply{}, ErrInvalidDeniedReply
	}
	return reply, nil
}

type replyImageQueries interface {
	GetAttachment(context.Context, db.GetAttachmentParams) (db.Attachment, error)
}

func loadDeniedReplyImage(ctx context.Context, q replyImageQueries, objects objectStore, workspaceID pgtype.UUID, rawID string) ([]byte, string, error) {
	id, err := util.ParseUUID(rawID)
	if err != nil {
		return nil, "", ErrInvalidDeniedReply
	}
	if q == nil || objects == nil {
		return nil, "", ErrDeniedReplyImageUnavailable
	}
	row, err := q.GetAttachment(ctx, db.GetAttachmentParams{ID: id, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrInvalidDeniedReply
	}
	if err != nil {
		return nil, "", fmt.Errorf("load mattermost reply image: %w", err)
	}
	// A private chat/task image must never become an automatic reply to outsiders.
	if row.WorkspaceID != workspaceID || row.IssueID.Valid || row.CommentID.Valid || row.ChatSessionID.Valid ||
		row.ChatMessageID.Valid || row.TaskID.Valid || row.SourceContextID.Valid || row.SizeBytes > maxDeniedReplyImageBytes {
		return nil, "", ErrInvalidDeniedReply
	}
	key := objects.KeyFromURL(row.Url)
	if key == "" {
		return nil, "", ErrInvalidDeniedReply
	}
	reader, err := objects.GetReader(ctx, key)
	if err != nil {
		return nil, "", ErrDeniedReplyImageUnavailable
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxDeniedReplyImageBytes+1))
	if err != nil {
		return nil, "", ErrDeniedReplyImageUnavailable
	}
	if len(data) > maxDeniedReplyImageBytes {
		return nil, "", ErrInvalidDeniedReply
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, "", ErrInvalidDeniedReply
	}
	return data, path.Base(strings.ReplaceAll(row.Filename, "\\", "/")), nil
}

func (s *InstallService) EnableReplyImages(objects objectStore) { s.objects = objects }

func (s *InstallService) prepareDeniedReply(ctx context.Context, workspaceID pgtype.UUID, reply DeniedReply) (DeniedReply, error) {
	normalized, err := reply.normalized()
	if err != nil {
		return DeniedReply{}, err
	}
	if normalized.Mode == "image" {
		if _, _, err := loadDeniedReplyImage(ctx, s.q, s.objects, workspaceID, normalized.ImageAttachmentID); err != nil {
			return DeniedReply{}, err
		}
	}
	return normalized, nil
}
