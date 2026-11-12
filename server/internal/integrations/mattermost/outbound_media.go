package mattermost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Match Multica's upload ceiling; the Mattermost server can impose a lower one.
const maxOutboundFileBytes = 100 << 20
const replyUploadTimeout = 30 * time.Second

const attachmentUnavailableText = ":warning: I couldn't send an attachment from this reply. Check the reply in Multica; the file may have been removed or rejected by Mattermost."

type objectStore interface {
	KeyFromURL(string) string
	GetReader(context.Context, string) (io.ReadCloser, error)
}

// EnableFileDelivery is called at boot when this deployment has object storage.
func (o *Outbound) EnableFileDelivery(objects objectStore) {
	o.objects = objects
}

func (o *Outbound) uploadAttachment(ctx context.Context, rest *restClient, channelID string, row db.Attachment) (string, error) {
	if o.objects == nil {
		return "", errors.New("mattermost: attachment storage is not configured")
	}
	if row.SizeBytes > maxOutboundFileBytes {
		return "", errors.New("mattermost: attachment exceeds the 100 MiB upload limit")
	}
	key := o.objects.KeyFromURL(row.Url)
	if key == "" {
		return "", errors.New("mattermost: attachment is not in this deployment's storage")
	}
	reader, err := o.objects.GetReader(ctx, key)
	if err != nil {
		return "", fmt.Errorf("mattermost: read attachment: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxOutboundFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("mattermost: read attachment: %w", err)
	}
	if len(data) > maxOutboundFileBytes {
		return "", errors.New("mattermost: attachment exceeds the 100 MiB upload limit")
	}
	filename := path.Base(strings.ReplaceAll(row.Filename, "\\", "/"))
	if filename == "" || filename == "." || filename == ".." || filename == "/" {
		filename = "attachment"
	}
	// shortcut: retries can leave unposted uploads; add upload reconciliation if provider storage requires stricter retention.
	return rest.UploadFile(ctx, channelID, filename, data)
}

func retryableUploadError(err error) bool {
	status := statusOf(err)
	var request *requestError
	var network net.Error
	return errors.As(err, &request) || errors.As(err, &network) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}
