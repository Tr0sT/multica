import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { AgentPluginSelection } from "../types/agent-marketplace";

export const agentMarketplaceKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-marketplaces"] as const,
  catalog: (wsId: string) => [...agentMarketplaceKeys.all(wsId), "catalog"] as const,
  selection: (wsId: string, agentId: string) =>
    [...agentMarketplaceKeys.all(wsId), "agents", agentId] as const,
};

export function agentMarketplaceCatalogOptions(wsId: string) {
  return queryOptions({
    queryKey: agentMarketplaceKeys.catalog(wsId),
    queryFn: () => api.getAgentMarketplaceCatalog(wsId),
    enabled: !!wsId,
    refetchInterval: (query) =>
      query.state.data?.marketplaces.some((source) =>
        source.status === "pending" || source.status === "running") ? 1500 : false,
  });
}

export function agentPluginSelectionOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentMarketplaceKeys.selection(wsId, agentId),
    queryFn: () => api.getAgentPluginSelection(wsId, agentId),
    enabled: !!wsId && !!agentId,
  });
}

export function agentPluginKey(plugin: AgentPluginSelection): string {
  return plugin.marketplaceId + "/" + plugin.pluginName;
}

export function sameAgentPluginSelection(a: AgentPluginSelection[], b: AgentPluginSelection[]): boolean {
  const keys = new Set(a.map(agentPluginKey));
  return a.length === b.length && keys.size === new Set(b.map(agentPluginKey)).size &&
    b.every((plugin) => keys.has(agentPluginKey(plugin)));
}
