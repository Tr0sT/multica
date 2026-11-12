export interface AgentMarketplace {
  id: string;
  name: string;
  displayName: string;
  sourceUrl: string;
  sourceRef: string;
  fetchRuntimeId: string;
  revision: string;
  status: string;
  error: string;
  createdBy: string;
  updatedAt: string;
}

export interface AgentMarketplacePlugin {
  versionId: string;
  marketplaceId: string;
  marketplace: string;
  name: string;
  displayName: string;
  description: string;
  version: string;
  revision: string;
  skills: string[];
  hasMcp: boolean;
  hasHooks: boolean;
}

export interface AgentMarketplaceCatalog {
  marketplaces: AgentMarketplace[];
  plugins: AgentMarketplacePlugin[];
  valid: boolean;
}

export interface AgentPluginSelection {
  marketplaceId: string;
  pluginName: string;
}

export interface AgentPluginSettings {
  configured: boolean;
  plugins: AgentPluginSelection[];
  valid: boolean;
}

export interface ConnectAgentMarketplaceRequest {
  url: string;
  ref: string;
  runtimeId: string;
}
