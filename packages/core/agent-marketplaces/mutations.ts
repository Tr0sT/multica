import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { AgentPluginSelection, ConnectAgentMarketplaceRequest } from "../types/agent-marketplace";
import { agentMarketplaceKeys } from "./queries";

function useInvalidateMarketplaces(wsId: string) {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: agentMarketplaceKeys.all(wsId) });
}

export function useConnectAgentMarketplace(wsId: string) {
  const invalidate = useInvalidateMarketplaces(wsId);
  return useMutation({
    mutationFn: (request: ConnectAgentMarketplaceRequest) => api.connectAgentMarketplace(wsId, request),
    onSettled: invalidate,
  });
}

export function useRefreshAgentMarketplace(wsId: string) {
  const invalidate = useInvalidateMarketplaces(wsId);
  return useMutation({
    mutationFn: ({ id, runtimeId }: { id: string; runtimeId: string }) =>
      api.refreshAgentMarketplace(wsId, id, runtimeId),
    onSettled: invalidate,
  });
}

export function useDisconnectAgentMarketplace(wsId: string) {
  const invalidate = useInvalidateMarketplaces(wsId);
  return useMutation({
    mutationFn: (id: string) => api.disconnectAgentMarketplace(wsId, id),
    onSettled: invalidate,
  });
}

export function useSaveAgentPluginSelection(wsId: string, agentId: string) {
  const qc = useQueryClient();
  const invalidate = useInvalidateMarketplaces(wsId);
  return useMutation({
    mutationFn: (plugins: AgentPluginSelection[]) =>
      api.setAgentPluginSelection(wsId, agentId, plugins),
    onSuccess: (result) => {
      if (result.valid) qc.setQueryData(agentMarketplaceKeys.selection(wsId, agentId), result);
    },
    onSettled: invalidate,
  });
}
