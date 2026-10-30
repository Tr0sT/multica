"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Package, Loader2, Search } from "@multica/ui/icons";
import { toast } from "sonner";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentMarketplaceCatalogOptions,
  agentPluginKey,
  agentPluginSelectionOptions,
  sameAgentPluginSelection,
  useSaveAgentPluginSelection,
  type AgentMarketplacePlugin,
  type AgentPluginSelection,
} from "@multica/core/agent-marketplaces";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { AgentMarketplacesDialog } from "../agent-marketplaces-dialog";

export function PluginsTab({
  agent, runtime, runtimes, members, currentUserId, canEdit, onDirtyChange,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId?: string | null;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const wsId = useWorkspaceId();
  const { t } = useT("agents");
  const catalog = useQuery(agentMarketplaceCatalogOptions(wsId));
  const settings = useQuery(agentPluginSelectionOptions(wsId, agent.id));
  const save = useSaveAgentPluginSelection(wsId, agent.id);
  const [draft, setDraft] = useState<AgentPluginSelection[] | null>(null);
  const [search, setSearch] = useState("");
  const [detail, setDetail] = useState<AgentMarketplacePlugin | null>(null);
  const selected = draft ?? settings.data?.plugins ?? [];
  const dirty = draft !== null && (!settings.data?.configured ||
    !sameAgentPluginSelection(draft, settings.data.plugins));
  const canSelect = canEdit && runtime?.provider === "codex";
  const canSave = canEdit && (canSelect || selected.length === 0);

  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);

  const toggle = (plugin: AgentPluginSelection) => {
    const key = agentPluginKey(plugin);
    setDraft(selected.some((value) => agentPluginKey(value) === key)
      ? selected.filter((value) => agentPluginKey(value) !== key)
      : [...selected, plugin]);
  };

  const handleSave = async () => {
    try {
      const result = await save.mutateAsync(selected);
      if (!result.valid) throw new Error(t(($) => $.tab_body.plugins.response_failed));
      setDraft(null);
      toast.success(t(($) => $.tab_body.plugins.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.plugins.response_failed));
    }
  };

  if (catalog.isLoading || settings.isLoading) {
    return <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-label={t(($) => $.tab_body.plugins.syncing)} />;
  }
  if (!catalog.data?.valid || !settings.data?.valid) {
    return <div className="space-y-3">
      <p role="alert" className="text-body text-destructive">{t(($) => $.tab_body.plugins.load_failed)}</p>
      <Button variant="outline" onClick={() => { void catalog.refetch(); void settings.refetch(); }}>
        {t(($) => $.tab_body.plugins.retry)}
      </Button>
    </div>;
  }
  const known = new Set(catalog.data.plugins.map((plugin) =>
    agentPluginKey({ marketplaceId: plugin.marketplaceId, pluginName: plugin.name })));
  const missing = selected.filter((plugin) => !known.has(agentPluginKey(plugin)));
  const filtered = catalog.data.plugins.filter((plugin) =>
    (plugin.name + " " + plugin.displayName + " " + plugin.description + " " + plugin.marketplace)
      .toLocaleLowerCase().includes(search.toLocaleLowerCase()));

  return <Dialog onOpenChange={(open) => { if (!open) setDetail(null); }}>
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-lg text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.scope_hint)}</p>
        <AgentMarketplacesDialog
          catalog={catalog.data}
          runtimes={runtimes}
          members={members}
          currentUserId={currentUserId ?? null}
          preferredRuntimeId={runtime?.id}
        />
      </div>
      {canEdit && !runtime?.provider && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.runtime_required)}</p>}
      {canEdit && runtime && runtime.provider !== "codex" && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.runtime_required)}</p>}
      {!canEdit && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.readonly)}</p>}
      {catalog.data.plugins.length > 0 && <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input aria-label={t(($) => $.tab_body.plugins.search)} placeholder={t(($) => $.tab_body.plugins.search)}
          value={search} onChange={(event) => setSearch(event.target.value)} className="pl-9" />
      </div>}
      {filtered.length === 0 && <p className="py-6 text-center text-body text-muted-foreground">{t(($) => $.tab_body.plugins.empty)}</p>}
      <div className="divide-y rounded-lg border border-surface-border">
        {filtered.map((plugin) => {
          const value = { marketplaceId: plugin.marketplaceId, pluginName: plugin.name };
          const key = agentPluginKey(value);
          const checked = selected.some((item) => agentPluginKey(item) === key);
          return <div key={key} className={cn("flex items-start gap-3 p-3", checked && "bg-surface-selected")}>
            <Package className="mt-1 size-5 shrink-0 text-muted-foreground" />
            <div className="min-w-0 flex-1 space-y-1">
              <DialogTrigger render={<button type="button" className="text-left text-body font-medium hover:underline" />}
                onClick={() => setDetail(plugin)}>
                {plugin.displayName}
              </DialogTrigger>
              {plugin.description && <p className="line-clamp-2 text-caption text-muted-foreground">{plugin.description}</p>}
              <div className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
                <span>{plugin.marketplace}</span>
                <span>{plugin.version || plugin.revision.slice(0, 8)}</span>
                <span>{t(($) => $.tab_body.plugins.skill_count, { count: plugin.skills.length })}</span>
                {plugin.hasMcp && <Badge variant="secondary">{t(($) => $.tab_body.plugins.mcp_component)}</Badge>}
                {plugin.hasHooks && <Badge variant="secondary">{t(($) => $.tab_body.plugins.hooks_component)}</Badge>}
              </div>
            </div>
            <Checkbox checked={checked} onCheckedChange={() => toggle(value)} disabled={!canEdit || (!canSelect && !checked) || save.isPending}
              aria-label={plugin.displayName} className="mt-1 shrink-0" />
          </div>;
        })}
        {missing.map((plugin) => <div key={agentPluginKey(plugin)} className="flex items-start gap-3 p-3">
          <div className="min-w-0 flex-1">
            <p className="break-words text-body font-medium">{plugin.pluginName}</p>
            <p className="text-caption text-destructive">{t(($) => $.tab_body.plugins.unavailable)}</p>
          </div>
          <Checkbox checked onCheckedChange={() => toggle(plugin)} disabled={!canEdit || save.isPending} aria-label={plugin.pluginName} />
        </div>)}
      </div>
      {canEdit && <div className="space-y-3">
        {!settings.data.configured && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.inheritance_hint)}</p>}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.selected_count, { count: selected.length })}</span>
          <Button onClick={handleSave}
            disabled={!canSave || save.isPending || missing.length > 0 || (settings.data.configured && !dirty)}
            aria-busy={save.isPending}>
            {save.isPending && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />}
            {save.isPending ? t(($) => $.tab_body.plugins.saving) : t(($) => $.tab_body.plugins.save)}
          </Button>
        </div>
      </div>}
    </div>
    <DialogContent className="max-w-lg">
      <DialogHeader><DialogTitle>{detail?.displayName ?? t(($) => $.tabs.plugins)}</DialogTitle></DialogHeader>
      {detail && <div className="space-y-4">
        {detail.description && <p className="text-body">{detail.description}</p>}
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-caption">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.plugins.source)}</dt><dd className="break-words">{detail.marketplace}</dd>
          <dt className="text-muted-foreground">{t(($) => $.tab_body.plugins.version)}</dt><dd>{detail.version || detail.revision.slice(0, 8)}</dd>
        </dl>
        <div>
          <h3 className="mb-2 text-body font-medium">{t(($) => $.tab_body.plugins.included_skills)}</h3>
          <div className="flex flex-wrap gap-2">{detail.skills.map((skill) => <Badge key={skill} variant="secondary">{skill}</Badge>)}</div>
        </div>
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.plugins.details_hint)}</p>
      </div>}
    </DialogContent>
  </Dialog>;
}
