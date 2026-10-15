package mattermost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Committed task replies are the durable queue. Events only wake this worker;
// a restart or a dropped event cannot discard an unsent reply.
type Outbound struct {
	q       *db.Queries
	tx      engine.TxStarter
	decrypt Decrypter
	policy  *ServerPolicy
	client  *http.Client
	logger  *slog.Logger
	wake    chan struct{}
	done    chan struct{}
	start   sync.Once
}

const (
	replyPollInterval  = 30 * time.Second
	replyLeaseSeconds  = 60.0
	replyCallTimeout   = 15 * time.Second
	replyRecordTimeout = 5 * time.Second
)

func NewOutbound(q *db.Queries, tx engine.TxStarter, decrypt Decrypter, policy *ServerPolicy, client *http.Client, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbound{q: q, tx: tx, decrypt: decrypt, policy: policy, client: client,
		logger: logger, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, func(events.Event) {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	})
}

func (o *Outbound) Start(ctx context.Context) {
	o.start.Do(func() {
		go func() {
			defer close(o.done)
			ticker := time.NewTicker(replyPollInterval)
			defer ticker.Stop()
			for {
				o.sweep(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-o.wake:
				}
			}
		}()
	})
}

func (o *Outbound) WaitWithTimeout(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-o.done:
		return true
	case <-timer.C:
		return false
	}
}

func (o *Outbound) sweep(ctx context.Context) {
	readCtx, cancel := context.WithTimeout(ctx, replyRecordTimeout)
	tasks, err := o.q.ListPendingMattermostReplies(readCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			o.logger.WarnContext(ctx, "mattermost: reading reply queue failed", "error", err)
		}
		return
	}
	for _, taskID := range tasks {
		if ctx.Err() != nil {
			return
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := o.processTask(deliveryCtx, taskID)
		cancel()
		if err != nil && ctx.Err() == nil {
			o.logger.WarnContext(ctx, "mattermost: reply delivery attempt failed", "task_id", taskID.String(), "error", err)
		}
	}
}

func (o *Outbound) processTask(ctx context.Context, taskID pgtype.UUID) error {
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if delivery.ChannelType != string(TypeMattermost) {
		return nil
	}
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != "completed" {
		return nil
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, o.q, task)
	if err != nil || !deliver {
		return err
	}
	message, err := o.q.GetChatMessageByTaskAssistant(ctx, taskID)
	if err != nil {
		return err
	}
	if message.Content == "" {
		return nil
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID: delivery.InstallationID, ChannelType: string(TypeMattermost),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if inst.Status != "active" {
		return nil
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return err
	}
	base, err := o.policy.validate(creds.ServerURL)
	if err != nil {
		return err
	}
	turn, err := o.q.GetChannelReplyTurn(ctx, taskID)
	if err != nil {
		return err
	}
	if !turn.TurnID.Valid {
		return errors.New("mattermost: reply has no task lineage")
	}
	channelID, root := outboundTarget(bindingFromTaskDelivery(delivery))
	token := dbid.NewV7()
	lease, err := o.q.AcquireChannelReplyDelivery(ctx, db.AcquireChannelReplyDeliveryParams{
		TurnID: turn.TurnID, TaskID: taskID, AttemptDepth: int32(turn.AttemptDepth),
		BindingID: delivery.BindingID, InstallationID: inst.ID, ChannelType: string(TypeMattermost),
		ChatID: channelID, Phase: "terminal", OwnerToken: token, LeaseSeconds: replyLeaseSeconds,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replyRecordTimeout)
		defer cancel()
		if _, err := o.q.ReleaseChannelReplyDelivery(recordCtx, db.ReleaseChannelReplyDeliveryParams{TurnID: lease.TurnID, OwnerToken: token}); err != nil {
			o.logger.ErrorContext(recordCtx, "mattermost: releasing reply lease failed", "error", err)
		}
	}()
	// Reload progress after acquiring ownership, not before waiting for it.
	message, err = o.q.GetChatMessageByTaskAssistant(ctx, taskID)
	if err != nil {
		return err
	}
	chunks := chunkMessage(message.Content, maxPostRunes)
	ids := append([]string(nil), message.ChannelOutboundMessageIds...)
	if len(ids) != int(lease.ChunksSent) || len(ids) > len(chunks) {
		return errors.New("mattermost: reply progress is inconsistent; refusing to duplicate it")
	}
	if root == "" && len(ids) > 0 {
		root = ids[0]
	}
	rest := newRESTClient(base, creds.AccessToken, o.client)
	for index := len(ids); index < len(chunks); index++ {
		owned, err := o.q.RenewChannelReplyDelivery(ctx, db.RenewChannelReplyDeliveryParams{
			TurnID: lease.TurnID, OwnerToken: token, LeaseSeconds: replyLeaseSeconds,
		})
		if err != nil {
			return err
		}
		if owned != 1 {
			return errors.New("mattermost: reply lease lost")
		}
		marker := replyMarker(lease.TurnID, index)
		var post Post
		if lease.SendState == "in_flight" || lease.SendState == "unknown" {
			// Mattermost's pending_post_id cache expires. A lost POST is only
			// reconciled by reading its durable marker, never blindly repeated.
			lookupCtx, cancel := context.WithTimeout(ctx, replyCallTimeout)
			found, ok, lookupErr := rest.FindReply(lookupCtx, channelID, creds.BotUserID, marker, lease.CreatedAt.Time.Add(-time.Minute).UnixMilli())
			cancel()
			if lookupErr != nil {
				return lookupErr
			}
			if !ok {
				if lease.SendState == "in_flight" {
					recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replyRecordTimeout)
					_, markErr := o.q.MarkChannelReplyDeliverySendUnknown(recordCtx, lease.TurnID)
					cancel()
					if markErr != nil {
						return markErr
					}
				}
				return errors.New("mattermost: send outcome unknown; awaiting reconciliation, inspect the reply before retrying manually")
			}
			post = found
		} else {
			claimed, err := o.q.MarkChannelReplyDeliverySending(ctx, db.MarkChannelReplyDeliverySendingParams{TurnID: lease.TurnID, OwnerToken: token})
			if err != nil {
				return err
			}
			if claimed != 1 {
				return errors.New("mattermost: reply send not owned")
			}
			callCtx, cancel := context.WithTimeout(ctx, replyCallTimeout)
			post, err = rest.CreatePost(callCtx, Post{
				ChannelID: channelID, RootID: root, Message: chunks[index], PendingPostID: marker,
				Props: map[string]any{"multica_reply_id": marker},
			})
			cancel()
			if err != nil {
				recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replyRecordTimeout)
				defer cancel()
				status := statusOf(err)
				if definiteSendRejection(err) {
					if _, resetErr := o.q.ResetChannelReplyDeliverySend(recordCtx, db.ResetChannelReplyDeliverySendParams{TurnID: lease.TurnID, OwnerToken: token}); resetErr != nil {
						return resetErr
					}
					if status != 0 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout && status != http.StatusUnauthorized && status != http.StatusForbidden {
						_, settleErr := o.q.SettleChannelReplyDelivery(recordCtx, db.SettleChannelReplyDeliveryParams{TurnID: lease.TurnID, OwnerToken: token, SettledReason: "provider_rejected"})
						if settleErr != nil {
							return settleErr
						}
					}
				} else {
					if _, markErr := o.q.MarkChannelReplyDeliverySendUnknown(recordCtx, lease.TurnID); markErr != nil {
						return markErr
					}
				}
				return fmt.Errorf("mattermost reply send: %w", err)
			}
		}
		if post.ID == "" || post.ChannelID != channelID || post.RootID != root {
			return errors.New("mattermost: inconsistent post acknowledgement")
		}
		ids = append(ids, post.ID)
		if err := o.recordChunk(ctx, lease.TurnID, token, delivery, channelID, taskID, ids); err != nil {
			return err
		}
		lease.SendState = "known"
		if root == "" {
			root = post.ID
		}
	}
	settled, err := o.q.SettleChannelReplyDelivery(ctx, db.SettleChannelReplyDeliveryParams{
		TurnID: lease.TurnID, OwnerToken: token, SettledReason: "delivered",
	})
	if err != nil {
		return err
	}
	if settled != 1 {
		return errors.New("mattermost: reply completion lease lost")
	}
	return nil
}

func definiteSendRejection(err error) bool {
	status := statusOf(err)
	if status >= 400 && status < 500 {
		return true
	}
	var network *net.OpError
	return errors.As(err, &network) && network.Op == "dial"
}

func replyMarker(turnID pgtype.UUID, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("multica:mattermost:%s:%d", turnID.String(), index)))
	return hex.EncodeToString(sum[:])
}

// Progress, message IDs, and provenance commit together. A failed DB write
// leaves the send in flight so recovery reads the provider marker first.
func (o *Outbound) recordChunk(ctx context.Context, turnID, token pgtype.UUID, delivery db.ChannelTaskDelivery, channelID string, taskID pgtype.UUID, ids []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replyRecordTimeout)
	defer cancel()
	tx, err := o.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := o.q.WithTx(tx)
	rows, err := q.RecordChannelReplyDeliveryChunk(ctx, db.RecordChannelReplyDeliveryChunkParams{
		TurnID: turnID, OwnerToken: token, MessageID: ids[len(ids)-1], ChunksSent: int32(len(ids)),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("mattermost: reply progress lease lost")
	}
	rows, err = q.SetChatMessageChannelOutboundProvenanceByTask(ctx, db.SetChatMessageChannelOutboundProvenanceByTaskParams{
		ChannelType: pgtype.Text{String: string(TypeMattermost), Valid: true}, InstallationID: delivery.InstallationID,
		ChannelChatID: pgtype.Text{String: channelID, Valid: true}, MessageIds: ids, TaskID: taskID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("mattermost: assistant reply disappeared")
	}
	if err := q.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
		OutboundInstallationID: delivery.InstallationID, OutboundChannelType: string(TypeMattermost),
		OutboundMessageID: ids[len(ids)-1], OutboundBindingID: delivery.BindingID,
		OutboundRouteRevision: delivery.RouteRevision, OutboundTaskID: taskID, OutboundKind: "task_reply",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func bindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// outboundTarget recovers the real send target from the chat binding. The
// channel_chat_id may be a composite "channel:threadRoot" isolation key, so the
// real channel id is read from the binding config; the reply thread is the
// recorded last_thread_id.
func outboundTarget(b db.ChannelChatSessionBinding) (channelID, threadRoot string) {
	channelID = b.ChannelChatID
	if len(b.Config) > 0 {
		var cfg mattermostBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChannelID != "" {
			channelID = cfg.ChannelID
		}
	}
	if b.LastThreadID.Valid {
		threadRoot = b.LastThreadID.String
	}
	return channelID, threadRoot
}
