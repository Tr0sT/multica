package daemon

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/agentplugins"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (d *Daemon) handleMarketplaceSync(ctx context.Context, runtime Runtime, request protocol.DaemonHeartbeatPendingMarketplace) {
	catalog, err := agentplugins.Fetch(ctx, request.URL, request.Ref)
	result := agentplugins.SyncResult{Catalog: catalog}
	if err != nil {
		result.Error = err.Error()
	}
	d.reportRuntimeResultWithRetry(ctx, "marketplace_sync", runtime.ID, request.ID, func(reportCtx context.Context) error {
		return d.client.postJSON(reportCtx,
			fmt.Sprintf("/api/daemon/runtimes/%s/agent-marketplaces/%s/sync/%s", runtime.ID, request.MarketplaceID, request.ID),
			result, nil)
	})
}
