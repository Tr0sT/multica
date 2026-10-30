"use client";

import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, LoaderCircle, Pencil, Trash2 } from "@multica/ui/icons";
import { MattermostMark } from "./mattermost-mark";
import { MattermostDeniedReplyFields, prepareMattermostDeniedReply, deniedReplyMatches, type MattermostReplyDraft } from "./mattermost-denied-reply-fields";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { mattermostInstallationsOptions, mattermostKeys } from "@multica/core/mattermost";
import { api } from "@multica/core/api";
import type {
  MattermostAccessMode,
  MattermostAttachmentMode,
  MattermostDeniedReply,
  MattermostContextMode,
  MattermostContextSettings,
  MattermostInstallation,
} from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { openExternal } from "../../platform";
import { useT } from "../../i18n";

// MattermostTab is the workspace settings panel for Mattermost bot
// installations, mirroring TelegramTab: listing is member-visible; the
// management actions are admin-only (backend-enforced; the UI hides the buttons
// to match). Adding a new installation flows through the Agent detail page —
// the install path is per-agent (one bot per agent, the
// (workspace_id, agent_id, channel_type) UNIQUE in channel_installation).
export function MattermostTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage = currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading, isError } = useQuery({
    ...mattermostInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      // Await the server before touching cache/UI (repo rule: no optimistic
      // removal on flows that confirm/destroy).
      await api.deleteMattermostInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: mattermostKeys.installations(wsId) });
      toast.success(t(($) => $.mattermost.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.mattermost.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-8">
      {isError ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">
              {t(($) => $.mattermost.load_failed)}
            </p>
          </CardContent>
        </Card>
      ) : isLoading ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">{t(($) => $.mattermost.loading)}</p>
          </CardContent>
        </Card>
      ) : !configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.mattermost.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.mattermost.not_enabled_description)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.mattermost.connected_bots)}</h2>
          {installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.mattermost.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.mattermost.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.mattermost.empty_description_cta)}</strong>{" "}
                  {t(($) => $.mattermost.empty_description_suffix)}
                </p>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="divide-y">
                {installations.map((inst) => (
                  <InstallationRow
                    key={inst.id}
                    installation={inst}
                    canManage={canManage}
                    onDisconnect={() => setDisconnectTarget(inst.id)}
                  />
                ))}
              </CardContent>
            </Card>
          )}
        </section>
      )}

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.mattermost.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.mattermost.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.mattermost.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.mattermost.disconnecting)
                : t(($) => $.mattermost.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: MattermostInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const { getAgentName } = useActorName();
  const isActive = installation.status === "active";
  const agentName = getAgentName(installation.agent_id);
  return (
    <div className="flex flex-wrap items-start justify-between gap-4 py-3 first:pt-0 last:pb-0">
      <div className="flex min-w-0 items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={installation.agent_id}
          size="lg"
          enableHoverCard
          profileLink
        />
        <div className="min-w-0 space-y-1">
          <p className="text-body font-medium">
            {agentName}
            {installation.bot_username ? (
              <span className="ml-2 text-caption text-muted-foreground">
                @{installation.bot_username}
              </span>
            ) : null}
            {!isActive && (
              <span className="ml-2 rounded-sm bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.mattermost.revoked_badge)}
              </span>
            )}
          </p>
          {/* Mattermost is self-hosted, so the server is part of the row's
              identity: two agents can be connected to bots on two different
              deployments, and the username alone would not distinguish them. */}
          {installation.server_url ? (
            <p className="truncate text-micro text-muted-foreground">{installation.server_url}</p>
          ) : null}
          <MattermostContextSummary context={installation.context} />
          <MattermostAccessSummary accessMode={installation.access_mode} />
          <p className="text-micro text-muted-foreground">
            {t(($) => $.mattermost.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <MattermostEditButton installation={installation} />
          <Button variant="outline" size="sm" onClick={onDisconnect}>
            <Trash2 className="h-3 w-3" />
            {t(($) => $.mattermost.disconnect)}
          </Button>
        </div>
      )}
    </div>
  );
}

// mattermostDocsUrl points at the Mattermost integration guide on the docs
// site, localized like the Slack and Telegram docs links.
function mattermostDocsUrl(lang: string | undefined): string {
  const prefix = lang?.startsWith("zh")
    ? "/zh"
    : lang?.startsWith("ja")
      ? "/ja"
      : lang?.startsWith("ko")
        ? "/ko"
        : "";
  return `https://multica.ai/docs${prefix}/mattermost-bot-integration`;
}

// MattermostAgentBindButton is the per-agent CTA on the agent detail page.
// Mattermost uses the paste-two-fields model: the admin creates a bot account
// in the System Console and pastes its server URL and access token; the
// backend validates them live before persisting. Visibility mirrors
// TelegramAgentBindButton (owner/admin only).
export function MattermostAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  /** Compact read-only connected row that invokes this instead of the full
   * badge — the agent inspector passes a "jump to the Integrations tab"
   * handler so management actions live in one place. */
  onShowConnectedDetails?: () => void;
}) {
  const { t, i18n } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [serverUrl, setServerUrl] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [accessMode, setAccessMode] = useState<MattermostAccessMode>("members");
  const [allowedUsers, setAllowedUsers] = useState("");
  const [deniedReply, setDeniedReply] = useState<MattermostReplyDraft>({ mode: "text" });
  const [contextMode, setContextMode] = useState<MattermostContextMode>("message");
  const [attachmentMode, setAttachmentMode] = useState<MattermostAttachmentMode>("links");
  const [lookbackHours, setLookbackHours] = useState(24);
  const [submitting, setSubmitting] = useState(false);

  const { data: listing } = useQuery({
    ...mattermostInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage = currentMember?.role === "owner" || currentMember?.role === "admin";

  if (!canManage) return null;

  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <MattermostAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <MattermostAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  function closeDialog() {
    if (submitting) return;
    setDialogOpen(false);
    setServerUrl("");
    setAccessToken("");
    setAccessMode("members");
    setAllowedUsers("");
    setDeniedReply({ mode: "text" });
    setContextMode("message");
    setLookbackHours(24);
  }

  async function handleSubmit() {
    const server_url = serverUrl.trim();
    const access_token = accessToken.trim();
    if (submitting || !agentId || !server_url || !access_token) return;
    setSubmitting(true);
    try {
      const allowedUsernames = parseAllowedUsernames(allowedUsers);
      const reply = accessMode === "allowlist" ? await prepareMattermostDeniedReply(deniedReply, setDeniedReply, wsId) : undefined;
      if (reply) setDeniedReply(reply);
      const installation = await api.registerMattermostBot(wsId, agentId, {
        server_url,
        access_token,
        access_mode: accessMode,
        attachment_mode: attachmentMode,
        ...(accessMode === "allowlist" ? { allowed_usernames: allowedUsernames } : {}),
        ...(reply ? { denied_reply: reply } : {}),
        context: {
          mode: contextMode,
          ...(contextMode === "channel" ? { lookback_hours: lookbackHours } : {}),
        },
      });
      if (
        !installation.id || installation.status !== "active" || (installation.access_mode ?? "members") !== accessMode ||
        installation.attachment_mode !== attachmentMode ||
        (accessMode === "allowlist" && installation.allowed_users?.map((user) => user.username).join("\n") !== allowedUsernames.join("\n")) ||
        (reply && !deniedReplyMatches(installation.denied_reply, reply))
      ) {
        throw new Error("Mattermost connection returned an invalid installation");
      }
      // The mattermost_installation realtime event also refreshes this list,
      // but invalidate explicitly so the connected badge appears immediately.
      await qc.invalidateQueries({ queryKey: mattermostKeys.installations(wsId) });
      toast.success(t(($) => $.mattermost.connect_success_toast));
      setDialogOpen(false);
      setServerUrl("");
      setAccessToken("");
      setAccessMode("members");
      setAllowedUsers("");
      setDeniedReply({ mode: "text" });
      setContextMode("message");
      setAttachmentMode("links");
      setLookbackHours(24);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.mattermost.connect_failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const canSubmit = serverUrl.trim() !== "" && accessToken.trim() !== "" && !submitting &&
    (accessMode !== "allowlist" || (deniedReply.mode !== "unknown" &&
      (deniedReply.mode !== "images" || !!deniedReply.files?.length || !!deniedReply.image_attachment_ids?.length)));

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="mattermost-agent-bind-buttons"
    >
      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogTrigger
          render={
            <Button
              variant="outline"
              size="sm"
              disabled={!agentId}
              title={
                agentName
                  ? t(($) => $.mattermost.bind_button_title, { agent: agentName })
                  : undefined
              }
              data-testid="mattermost-agent-connect"
            />
          }
        >
          <MattermostMark className="h-3 w-3" />
          {t(($) => $.mattermost.bind_button)}
        </DialogTrigger>

        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg" data-testid="mattermost-connect-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.mattermost.connect_dialog_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.mattermost.connect_dialog_description)}
            </DialogDescription>
          </DialogHeader>

          <button
            type="button"
            onClick={() => openExternal(mattermostDocsUrl(i18n.language))}
            className="inline-flex w-fit items-center gap-2 text-body font-medium text-primary underline-offset-2 hover:underline"
            data-testid="mattermost-docs-link"
          >
            <ExternalLink className="h-4 w-4" />
            {t(($) => $.mattermost.connect_docs_link)}
          </button>

          <div className="space-y-1.5">
            <Label htmlFor="mattermost-server-url">
              {t(($) => $.mattermost.server_url_label)}
            </Label>
            <Input
              id="mattermost-server-url"
              data-testid="mattermost-server-url"
              type="url"
              value={serverUrl}
              onChange={(e) => setServerUrl(e.target.value)}
              // Server URL shape: a format hint, not copy.
              placeholder="https://mattermost.example.com"
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="mattermost-access-token">
              {t(($) => $.mattermost.access_token_label)}
            </Label>
            <Input
              id="mattermost-access-token"
              data-testid="mattermost-access-token"
              type="password"
              value={accessToken}
              onChange={(e) => setAccessToken(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>

          <MattermostAccessFields
            accessMode={accessMode}
            setAccessMode={setAccessMode}
            allowedUsers={allowedUsers}
            setAllowedUsers={setAllowedUsers}
            disabled={submitting}
          />

          {accessMode === "allowlist" && <MattermostDeniedReplyFields value={deniedReply} onChange={setDeniedReply} disabled={submitting} />}

          <MattermostContextFields
            contextMode={contextMode}
            setContextMode={setContextMode}
            lookbackHours={lookbackHours}
            setLookbackHours={setLookbackHours}
            disabled={submitting}
          />
          <MattermostAttachmentFields value={attachmentMode} onChange={setAttachmentMode} disabled={submitting} />

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.mattermost.connect_cancel)}
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!canSubmit}
              data-testid="mattermost-connect-submit"
            >
              {submitting
                ? t(($) => $.mattermost.connect_submitting)
                : t(($) => $.mattermost.connect_submit)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// MattermostAgentBotStatusRow is the compact, read-only connected affordance
// the agent inspector renders; it deep-links into the Integrations tab.
function MattermostAgentBotStatusRow({
  onClick,
  className,
}: {
  onClick: () => void;
  className?: string;
}) {
  const { t } = useT("settings");
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-caption text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
      data-testid="mattermost-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.mattermost.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// MattermostAgentBotConnectedBadge is the full "already connected" affordance:
// status and management actions, then an "Open in Mattermost" link.
function MattermostAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: MattermostInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteMattermostInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: mattermostKeys.installations(wsId) });
      toast.success(t(($) => $.mattermost.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.mattermost.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="mattermost-agent-bot-connected"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">
            {t(($) => $.mattermost.agent_bot_connected_label)}
            {installation.bot_username ? ` · @${installation.bot_username}` : ""}
          </span>
        </span>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <MattermostEditButton installation={installation} disabled={disconnecting} />
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setConfirmOpen(true)}
            disabled={disconnecting}
            title={t(($) => $.mattermost.agent_bot_disconnect_tooltip)}
            aria-label={t(($) => $.mattermost.disconnect)}
            data-testid="mattermost-agent-bot-disconnect"
          >
            <Trash2 className="h-3 w-3" />
            {disconnecting
              ? t(($) => $.mattermost.disconnecting)
              : t(($) => $.mattermost.disconnect)}
          </Button>
        </div>
      </div>

      <MattermostContextSummary context={installation.context} />
      <MattermostAccessSummary accessMode={installation.access_mode} />

      {installation.server_url && (
        <button
          type="button"
          onClick={() => openExternal(installation.server_url)}
          className="inline-flex items-center gap-1 text-caption text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
          title={t(($) => $.mattermost.agent_bot_manage_tooltip)}
        >
          <ExternalLink className="h-3 w-3" />
          {t(($) => $.mattermost.agent_bot_manage_link)}
        </button>
      )}

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.mattermost.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.mattermost.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.mattermost.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.mattermost.disconnecting)
                : t(($) => $.mattermost.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function MattermostEditButton({
  installation,
  disabled = false,
}: {
  installation: MattermostInstallation;
  disabled?: boolean;
}) {
  const { t } = useT("settings");
  const { t: common } = useT("common");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [accessMode, setAccessMode] = useState<MattermostAccessMode | "unknown">("members");
  const [allowedUsers, setAllowedUsers] = useState("");
  const [deniedReply, setDeniedReply] = useState<MattermostReplyDraft>({ mode: "text" });
  const [contextMode, setContextMode] = useState<MattermostContextMode | "unknown">("message");
  const [attachmentMode, setAttachmentMode] = useState<MattermostAttachmentMode | "unknown">("links");
  const [lookbackHours, setLookbackHours] = useState(24);
  const update = useMutation({
    mutationFn: async (settings: { context: MattermostContextSettings; attachment_mode: MattermostAttachmentMode; access_mode: MattermostAccessMode; allowed_usernames?: string[] }) => {
      const { context } = settings;
      let reply: MattermostDeniedReply | undefined;
      if (settings.access_mode === "allowlist") {
        reply = await prepareMattermostDeniedReply(deniedReply, setDeniedReply, wsId);
        setDeniedReply(reply);
      }
      const saved = await api.updateMattermostInstallation(wsId, installation.id, { ...settings, ...(reply ? { denied_reply: reply } : {}) });
      if (
        saved.id !== installation.id || saved.status !== "active" ||
        saved.context?.mode !== context.mode ||
        saved.attachment_mode !== settings.attachment_mode ||
        (saved.access_mode ?? "members") !== settings.access_mode ||
        (reply && !deniedReplyMatches(saved.denied_reply, reply)) ||
        (settings.access_mode === "allowlist" &&
          saved.allowed_users?.map((user) => user.username).join("\n") !== settings.allowed_usernames?.join("\n")) ||
        (context.mode === "channel" && saved.context.lookback_hours !== context.lookback_hours)
      ) {
        throw new Error(t(($) => $.mattermost.edit_failed_toast));
      }
      return saved;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: mattermostKeys.installations(wsId) });
      toast.success(t(($) => $.mattermost.edit_success_toast));
      setOpen(false);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t(($) => $.mattermost.edit_failed_toast));
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (update.isPending) return;
        if (value) {
          setAccessMode(installation.access_mode ?? "members");
          setAllowedUsers(installation.allowed_users?.map((user) => `@${user.username}`).join("\n") ?? "");
          setDeniedReply(installation.denied_reply ?? { mode: "text" });
          setContextMode(installation.context?.mode ?? "message");
          setAttachmentMode(installation.attachment_mode ?? "none");
          setLookbackHours(installation.context?.mode === "channel" ? installation.context.lookback_hours ?? 24 : 24);
        }
        setOpen(value);
      }}
    >
      <DialogTrigger render={<Button variant="outline" size="sm" disabled={disabled} />}>
        <Pencil className="h-3 w-3" />
        {t(($) => $.mattermost.edit_button)}
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.mattermost.edit_dialog_title)}</DialogTitle>
          <DialogDescription className="break-words">
            {installation.bot_username ? `@${installation.bot_username} · ` : ""}
            {installation.server_url}
          </DialogDescription>
        </DialogHeader>
        <MattermostAccessFields
          accessMode={accessMode}
          setAccessMode={setAccessMode}
          allowedUsers={allowedUsers}
          setAllowedUsers={setAllowedUsers}
          disabled={update.isPending}
        />
        {accessMode === "allowlist" && <MattermostDeniedReplyFields value={deniedReply} onChange={setDeniedReply} disabled={update.isPending} />}
        <MattermostContextFields
          contextMode={contextMode}
          setContextMode={setContextMode}
          lookbackHours={lookbackHours}
          setLookbackHours={setLookbackHours}
          disabled={update.isPending}
        />
        <MattermostAttachmentFields value={attachmentMode} onChange={setAttachmentMode} disabled={update.isPending} />
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => setOpen(false)} disabled={update.isPending}>
            {common(($) => $.cancel)}
          </Button>
          <Button
            size="sm"
            disabled={update.isPending || contextMode === "unknown" || attachmentMode === "unknown" || accessMode === "unknown" ||
              (accessMode === "allowlist" && (deniedReply.mode === "unknown" ||
                (deniedReply.mode === "images" && !deniedReply.files?.length && !deniedReply.image_attachment_ids?.length)))}
            aria-busy={update.isPending}
            onClick={() => {
              if (contextMode === "unknown" || attachmentMode === "unknown" || accessMode === "unknown") return;
              update.mutate({
                access_mode: accessMode,
                attachment_mode: attachmentMode,
                ...(accessMode === "allowlist" ? { allowed_usernames: parseAllowedUsernames(allowedUsers) } : {}),
                context: {
                  mode: contextMode,
                  ...(contextMode === "channel" ? { lookback_hours: lookbackHours } : {}),
                },
              });
            }}
          >
            {update.isPending && <LoaderCircle className="h-3 w-3 animate-spin" aria-hidden="true" />}
            {common(($) => $.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function MattermostAccessFields({
  accessMode,
  setAccessMode,
  allowedUsers,
  setAllowedUsers,
  disabled,
}: {
  accessMode: MattermostAccessMode | "unknown";
  setAccessMode: (mode: MattermostAccessMode) => void;
  allowedUsers: string;
  setAllowedUsers: (users: string) => void;
  disabled: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const options = [
    { value: "members", label: t(($) => $.mattermost.access.members) },
    { value: "anyone", label: t(($) => $.mattermost.access.anyone) },
    { value: "allowlist", label: t(($) => $.mattermost.access.allowlist) },
  ];
  return (
    <div className="space-y-1.5">
      <Label htmlFor={`${id}-access`}>{t(($) => $.mattermost.access.label)}</Label>
      <Select
        items={options}
        value={accessMode === "unknown" ? null : accessMode}
        onValueChange={(value) => {
          if (value === "members" || value === "anyone" || value === "allowlist") setAccessMode(value);
        }}
        disabled={disabled}
      >
        <SelectTrigger
          id={`${id}-access`}
          className="w-full"
          aria-describedby={accessMode === "anyone" || accessMode === "allowlist" ? `${id}-notice` : undefined}
        >
          <SelectValue placeholder={t(($) => $.mattermost.access.unknown)} />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
          ))}
        </SelectContent>
      </Select>
      {accessMode === "allowlist" && (
        <div className="space-y-1.5 pt-2">
          <Label htmlFor={`${id}-users`}>{t(($) => $.mattermost.access.users_label)}</Label>
          <Textarea
            id={`${id}-users`}
            value={allowedUsers}
            onChange={(event) => setAllowedUsers(event.target.value)}
            aria-describedby={`${id}-users-help`}
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            rows={4}
          />
          <p id={`${id}-users-help`} className="text-caption text-muted-foreground">
            {t(($) => $.mattermost.access.users_help)}
          </p>
        </div>
      )}
      {(accessMode === "anyone" || accessMode === "allowlist") && (
        <p id={`${id}-notice`} className="text-caption text-muted-foreground">
          {t(($) => $.mattermost.access.notice)}
        </p>
      )}
    </div>
  );
}

function parseAllowedUsernames(value: string): string[] {
  return [...new Set(value.split(/[\s,]+/).filter(Boolean).map((name) => name.replace(/^@/, "").toLowerCase()))];
}

function MattermostAccessSummary({ accessMode }: { accessMode?: MattermostInstallation["access_mode"] }) {
  const { t } = useT("settings");
  const label = accessMode === "anyone"
    ? t(($) => $.mattermost.access.anyone)
    : accessMode === "allowlist"
      ? t(($) => $.mattermost.access.allowlist)
      : accessMode === "unknown"
        ? t(($) => $.mattermost.access.unknown)
        : t(($) => $.mattermost.access.members);
  return <p className="text-caption text-muted-foreground">{t(($) => $.mattermost.access.summary, { selection: label })}</p>;
}

function MattermostAttachmentFields({ value, onChange, disabled }: {
  value: MattermostAttachmentMode | "unknown";
  onChange: (mode: MattermostAttachmentMode) => void;
  disabled: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const options = [
    { value: "links", label: t(($) => $.mattermost.attachments.links) },
    { value: "none", label: t(($) => $.mattermost.attachments.none) },
  ];
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{t(($) => $.mattermost.attachments.label)}</Label>
      <Select items={options} value={value === "unknown" ? null : value}
        onValueChange={(mode) => { if (mode === "none" || mode === "links") onChange(mode); }} disabled={disabled}>
        <SelectTrigger id={id} className="w-full" aria-describedby={`${id}-help`}>
          <SelectValue placeholder={t(($) => $.mattermost.attachments.unknown)} />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
      <p id={`${id}-help`} className="text-caption text-muted-foreground">{t(($) => $.mattermost.attachments.help)}</p>
    </div>
  );
}

function MattermostContextFields({
  contextMode,
  setContextMode,
  lookbackHours,
  setLookbackHours,
  disabled,
}: {
  contextMode: MattermostContextMode | "unknown";
  setContextMode: (mode: MattermostContextMode) => void;
  lookbackHours: number;
  setLookbackHours: (hours: number) => void;
  disabled: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const contextOptions = [
    { value: "message", label: t(($) => $.mattermost.context.message) },
    { value: "thread", label: t(($) => $.mattermost.context.thread) },
    { value: "channel", label: t(($) => $.mattermost.context.channel) },
  ];
  const periodOptions = [
    { value: 1, label: t(($) => $.mattermost.context.hour) },
    { value: 6, label: t(($) => $.mattermost.context.six_hours) },
    { value: 24, label: t(($) => $.mattermost.context.day) },
    { value: 72, label: t(($) => $.mattermost.context.three_days) },
    { value: 168, label: t(($) => $.mattermost.context.week) },
    { value: 720, label: t(($) => $.mattermost.context.month) },
    { value: 1440, label: t(($) => $.mattermost.context.sixty_days) },
    { value: 2160, label: t(($) => $.mattermost.context.ninety_days) },
    { value: 8760, label: t(($) => $.mattermost.context.year) },
    { value: 0, label: t(($) => $.mattermost.context.unlimited) },
  ];

  return (
    <>
      <div className="space-y-1.5">
        <Label htmlFor={`${id}-mode`}>
          {t(($) => $.mattermost.context.label)}
        </Label>
        <Select
          items={contextOptions}
          value={contextMode === "unknown" ? null : contextMode}
          onValueChange={(value) => {
            if (value === "message" || value === "thread" || value === "channel") {
              setContextMode(value);
            }
          }}
          disabled={disabled}
        >
          <SelectTrigger
            id={`${id}-mode`}
            className="w-full"
            aria-describedby={contextMode === "message" || contextMode === "unknown"
              ? `${id}-help`
              : `${id}-help ${id}-notice`}
          >
            <SelectValue placeholder={t(($) => $.mattermost.context.unknown)} />
          </SelectTrigger>
          <SelectContent>
            {contextOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p id={`${id}-help`} className="text-caption text-muted-foreground">
          {t(($) => $.mattermost.context.help)}
        </p>
      </div>

      {contextMode === "channel" && (
        <div className="space-y-1.5">
          <Label htmlFor={`${id}-period`}>
            {t(($) => $.mattermost.context.period)}
          </Label>
          <Select
            items={periodOptions}
            value={lookbackHours}
            onValueChange={(value) => {
              if (value !== null) setLookbackHours(value);
            }}
            disabled={disabled}
          >
            <SelectTrigger id={`${id}-period`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {periodOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      {(contextMode === "thread" || contextMode === "channel") && (
        <p id={`${id}-notice`} className="text-caption text-muted-foreground">
          {t(($) => $.mattermost.context.notice)}
        </p>
      )}
    </>
  );
}

function MattermostContextSummary({ context }: { context?: MattermostInstallation["context"] }) {
  const { t } = useT("settings");
  const label =
    context?.mode === "unknown"
      ? t(($) => $.mattermost.context.unknown)
      : context?.mode === "channel"
        ? context.lookback_hours === 0
          ? t(($) => $.mattermost.context.channel_unlimited_summary)
          : t(($) => $.mattermost.context.channel_summary, { hours: context.lookback_hours })
        : context?.mode === "thread"
          ? t(($) => $.mattermost.context.thread)
          : t(($) => $.mattermost.context.message);
  return (
    <p className="text-caption text-muted-foreground">
      {t(($) => $.mattermost.context.summary, { selection: label })}
    </p>
  );
}
