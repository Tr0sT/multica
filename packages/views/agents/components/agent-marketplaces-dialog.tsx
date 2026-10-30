"use client";

import { useId, useState } from "react";
import { Loader2, Plus, RefreshCw, Globe, Trash2 } from "@multica/ui/icons";
import { toast } from "sonner";
import type { AgentRuntime, MemberWithUser } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import {
  useConnectAgentMarketplace,
  useDisconnectAgentMarketplace,
  useRefreshAgentMarketplace,
  type AgentMarketplace,
  type AgentMarketplaceCatalog,
} from "@multica/core/agent-marketplaces";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import {
  AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle,
  AlertDialogDescription, AlertDialogFooter, AlertDialogCancel,
} from "@multica/ui/components/ui/alert-dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { RuntimePicker } from "./runtime-picker";
import { useT } from "../../i18n";

export function AgentMarketplacesDialog({
  catalog, runtimes, members, currentUserId, preferredRuntimeId,
}: {
  catalog: AgentMarketplaceCatalog;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  preferredRuntimeId?: string;
}) {
  const wsId = useWorkspaceId();
  const urlInputId = useId();
  const refInputId = useId();
  const { t } = useT("agents");
  const { role } = useCurrentMember(wsId);
  const connect = useConnectAgentMarketplace(wsId);
  const refresh = useRefreshAgentMarketplace(wsId);
  const disconnect = useDisconnectAgentMarketplace(wsId);
  const available = runtimes.filter((runtime) => runtime.owner_id === currentUserId &&
    runtime.runtime_mode === "local" && runtime.status === "online");
  const [runtimeId, setRuntimeId] = useState(() =>
    available.find((runtime) => runtime.id === preferredRuntimeId)?.id ?? available[0]?.id ?? "");
  const [open, setOpen] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [url, setUrl] = useState("");
  const [ref, setRef] = useState("");
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState<AgentMarketplace | null>(null);
  const canFetch = available.some((runtime) => runtime.id === runtimeId);
  const busy = connect.isPending || refresh.isPending || disconnect.isPending;

  const showError = (error: unknown) => {
    setError(error instanceof Error ? error.message : t(($) => $.tab_body.plugins.response_failed));
  };
  const handleConnect = async (event: React.FormEvent) => {
    event.preventDefault();
    setError("");
    try {
      const result = await connect.mutateAsync({ url: url.trim(), ref: ref.trim(), runtimeId });
      if (!result) throw new Error(t(($) => $.tab_body.plugins.response_failed));
      setShowForm(false); setUrl(""); setRef("");
    } catch (error) { showError(error); }
  };
  const handleRefresh = async (source: AgentMarketplace) => {
    setError("");
    try {
      if (!await refresh.mutateAsync({ id: source.id, runtimeId })) throw new Error(t(($) => $.tab_body.plugins.response_failed));
    } catch (error) { showError(error); }
  };
  const handleDisconnect = async () => {
    if (!removing) return;
    try {
      await disconnect.mutateAsync(removing.id);
      setRemoving(null);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.plugins.response_failed));
    }
  };
  const statusLabel = (status: string) => {
    switch (status) {
      case "pending":
      case "running": return t(($) => $.tab_body.plugins.syncing);
      case "ready": return t(($) => $.tab_body.plugins.ready);
      case "failed": return t(($) => $.tab_body.plugins.failed);
      default: return t(($) => $.tab_body.plugins.unknown);
    }
  };

  return <>
    <Dialog open={open} onOpenChange={(value) => { if (!busy) setOpen(value); }}>
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <Globe className="size-4" />{t(($) => $.tab_body.plugins.marketplaces)}
      </DialogTrigger>
      <DialogContent className="flex max-h-[85vh] max-w-2xl flex-col">
        <DialogHeader><DialogTitle>{t(($) => $.tab_body.plugins.marketplaces)}</DialogTitle></DialogHeader>
        <div className="space-y-5 overflow-y-auto pr-1">
          <RuntimePicker runtimes={available} members={members} currentUserId={currentUserId}
            selectedRuntimeId={runtimeId} onSelect={setRuntimeId} disabled={busy} />
          <p className="text-caption text-muted-foreground">{canFetch
            ? t(($) => $.tab_body.plugins.fetch_hint) : t(($) => $.tab_body.plugins.no_fetch_runtime)}</p>
          {error && <p role="alert" className="text-caption text-destructive">{error}</p>}
          {catalog.marketplaces.length === 0 && <p className="text-body text-muted-foreground">{t(($) => $.tab_body.plugins.source_empty)}</p>}
          <div className="space-y-3">
            {catalog.marketplaces.map((source) => {
              const pending = source.status === "pending" || source.status === "running";
              const manage = source.createdBy === currentUserId || role === "owner" || role === "admin";
              return <div key={source.id} className="space-y-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <p className="break-words text-body font-medium">{source.displayName || source.name || source.sourceUrl}</p>
                    {(source.displayName || source.name) && <p className="break-all text-caption text-muted-foreground">{source.sourceUrl}</p>}
                  </div>
                  <Badge variant="secondary">
                    {pending && <Loader2 className="mr-1 size-3 animate-spin motion-reduce:animate-none" />}
                    {statusLabel(source.status)}
                  </Badge>
                </div>
                <p className="break-words text-caption text-muted-foreground">
                  {source.sourceRef || t(($) => $.tab_body.plugins.default_branch)}
                  {source.revision && " · " + source.revision.slice(0, 8)}
                </p>
                {source.error && <p role="alert" className="text-caption text-destructive">{source.error}</p>}
                {manage && <div className="flex gap-2">
                  <Button variant="outline" size="sm" disabled={!canFetch || pending || busy} onClick={() => handleRefresh(source)}>
                    <RefreshCw className="size-3.5" />{t(($) => $.tab_body.plugins.refresh)}
                  </Button>
                  <Button variant="ghost" size="sm" disabled={busy} onClick={() => setRemoving(source)}>
                    <Trash2 className="size-3.5" />{t(($) => $.tab_body.plugins.disconnect)}
                  </Button>
                </div>}
              </div>;
            })}
          </div>
          {!showForm ? <Button variant="outline" onClick={() => { setShowForm(true); setError(""); }} disabled={!canFetch}>
            <Plus className="size-4" />{t(($) => $.tab_body.plugins.connect)}
          </Button> : <form onSubmit={handleConnect} className="space-y-4 rounded-lg border p-4">
            <div className="space-y-2">
              <Label htmlFor={urlInputId}>{t(($) => $.tab_body.plugins.source_url)}</Label>
              {/* eslint-disable-next-line no-restricted-syntax -- Git URL examples use protocol syntax in every locale. */}
              <Input placeholder="ssh://git@example.com/team/plugins.git" id={urlInputId} value={url}
                onChange={(event) => setUrl(event.target.value)} disabled={connect.isPending} required />
            </div>
            <div className="space-y-2">
              <Label htmlFor={refInputId}>{t(($) => $.tab_body.plugins.source_ref)}</Label>
              <Input id={refInputId} value={ref} onChange={(event) => setRef(event.target.value)}
                placeholder={t(($) => $.tab_body.plugins.default_branch)} disabled={connect.isPending} />
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => setShowForm(false)} disabled={connect.isPending}>{t(($) => $.tab_body.plugins.cancel)}</Button>
              <Button type="submit" disabled={!canFetch || !url.trim() || connect.isPending} aria-busy={connect.isPending}>
                {connect.isPending && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />}
                {t(($) => $.tab_body.plugins.connect)}
              </Button>
            </div>
          </form>}
        </div>
      </DialogContent>
    </Dialog>
    <AlertDialog open={removing !== null} onOpenChange={(value) => { if (!value && !disconnect.isPending) setRemoving(null); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.tab_body.plugins.disconnect_title)}</AlertDialogTitle>
          <AlertDialogDescription>{t(($) => $.tab_body.plugins.disconnect_description)}</AlertDialogDescription>
        </AlertDialogHeader>
        <p className="break-words text-body">{removing?.displayName || removing?.sourceUrl}</p>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={disconnect.isPending}>{t(($) => $.tab_body.plugins.cancel)}</AlertDialogCancel>
          <Button variant="destructive" onClick={handleDisconnect} disabled={disconnect.isPending} aria-busy={disconnect.isPending}>
            {t(($) => $.tab_body.plugins.disconnect)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </>;
}
