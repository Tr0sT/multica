import { z } from "zod";

export const AgentMarketplaceSchema = z.object({
  id: z.string().min(1),
  name: z.string().default(""),
  display_name: z.string().default(""),
  source_url: z.string().default(""),
  source_ref: z.string().default(""),
  fetch_runtime_id: z.string().default(""),
  revision: z.string().default(""),
  status: z.string().default("failed"),
  error: z.string().default(""),
  created_by: z.string().default(""),
  updated_at: z.string().default(""),
}).loose().transform((value) => ({
  id: value.id,
  name: value.name,
  displayName: value.display_name || value.name,
  sourceUrl: value.source_url,
  sourceRef: value.source_ref,
  fetchRuntimeId: value.fetch_runtime_id,
  revision: value.revision,
  status: value.status,
  error: value.error,
  createdBy: value.created_by,
  updatedAt: value.updated_at,
}));

export const AgentMarketplacePluginSchema = z.object({
  version_id: z.string().min(1),
  marketplace_id: z.string().min(1),
  marketplace: z.string().default(""),
  name: z.string().min(1),
  display_name: z.string().default(""),
  description: z.string().default(""),
  version: z.string().default(""),
  revision: z.string().default(""),
  skills: z.array(z.string()).default([]),
  has_mcp: z.boolean().default(false),
  has_hooks: z.boolean().default(false),
}).loose().transform((value) => ({
  versionId: value.version_id,
  marketplaceId: value.marketplace_id,
  marketplace: value.marketplace,
  name: value.name,
  displayName: value.display_name || value.name,
  description: value.description,
  version: value.version,
  revision: value.revision,
  skills: value.skills,
  hasMcp: value.has_mcp,
  hasHooks: value.has_hooks,
}));

export const AgentMarketplaceCatalogSchema = z.object({
  marketplaces: z.array(AgentMarketplaceSchema),
  plugins: z.array(AgentMarketplacePluginSchema),
}).loose().transform((value) => ({ ...value, valid: true }));

export const AgentPluginSettingsSchema = z.object({
  configured: z.boolean(),
  plugins: z.array(z.object({
    marketplace_id: z.string().min(1),
    plugin_name: z.string().min(1),
  }).loose().transform((value) => ({
    marketplaceId: value.marketplace_id,
    pluginName: value.plugin_name,
  }))),
}).loose().transform((value) => ({ ...value, valid: true }));
