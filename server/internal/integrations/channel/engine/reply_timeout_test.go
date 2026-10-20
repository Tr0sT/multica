package engine

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

type deadlineReplier struct{ remaining chan time.Duration }

func (r deadlineReplier) Reply(ctx context.Context, _ ResolvedInstallation, _ channel.InboundMessage, _ Result) {
	deadline, _ := ctx.Deadline()
	r.remaining <- time.Until(deadline)
}

func TestAdapterReplyTimeoutPreservesTheDefaultForOtherChannels(t *testing.T) {
	for _, override := range []time.Duration{0, 45 * time.Second} {
		router := &Router{replyTimeout: time.Second}
		replier := deadlineReplier{remaining: make(chan time.Duration, 1)}
		router.scheduleReply(ResolverSet{Replier: replier, ReplyTimeout: override}, ResolvedInstallation{}, channel.InboundMessage{}, Result{})
		router.replyWg.Wait()
		remaining := <-replier.remaining
		want := override
		if want == 0 {
			want = time.Second
		}
		if remaining <= want/2 || remaining > want {
			t.Fatalf("reply budget = %s, want approximately %s", remaining, want)
		}
	}
}
