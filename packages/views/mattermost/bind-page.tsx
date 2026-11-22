"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { Loader2 } from "@multica/ui/icons";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { mattermostBindingPreviewOptions } from "@multica/core/mattermost";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

export function MattermostBindPage({ token }: { token: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const navigation = useNavigation();

  return (
    <main className="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center p-6">
      <Card className="w-full">
        <CardContent className="space-y-4">
          <h1 className="text-title font-semibold">{t(($) => $.mattermost_bind.page_title)}</h1>
          {!token ? (
            <BindingError reason="missing_token" />
          ) : isAuthLoading ? (
            <p role="status">{t(($) => $.mattermost_bind.loading)}</p>
          ) : !user ? (
            <>
              <p className="text-body text-muted-foreground">
                {t(($) => $.mattermost_bind.needs_auth_description)}
              </p>
              <Button
                type="button"
                onClick={() => navigation.push(`/login?next=${encodeURIComponent(`/mattermost/bind?token=${encodeURIComponent(token)}`)}`)}
              >
                {t(($) => $.mattermost_bind.sign_in)}
              </Button>
            </>
          ) : (
            <BindingConfirmation
              key={`${user.id}:${token}`}
              token={token}
              userId={user.id}
              account={user.email || user.name || user.id}
            />
          )}
        </CardContent>
      </Card>
    </main>
  );
}

function BindingConfirmation({ token, userId, account }: { token: string; userId: string; account: string }) {
  const { t } = useT("common");
  const navigation = useNavigation();
  const preview = useQuery(mattermostBindingPreviewOptions(token, userId));
  const redeem = useMutation({
    mutationFn: async () => {
      const response = await api.redeemMattermostBindingToken(token);
      if (
        response.workspace_id !== preview.data?.workspaceId ||
        response.installation_id !== preview.data?.installationId ||
        response.mattermost_user_id !== preview.data?.mattermostUserId
      ) {
        throw new Error("Mattermost binding returned a malformed response");
      }
      return response;
    },
    retry: false,
  });

  if (redeem.isSuccess) {
    return (
      <div role="status" className="space-y-2">
        <p className="text-body font-medium">{t(($) => $.mattermost_bind.done_title)}</p>
        <p className="text-caption text-muted-foreground">{t(($) => $.mattermost_bind.done_description)}</p>
      </div>
    );
  }
  if (preview.isPending) return <p role="status">{t(($) => $.mattermost_bind.loading)}</p>;
  if (preview.isError) return <BindingError reason={redemptionFailureReason(preview.error)} />;
  const identity = preview.data;
  if (!identity?.username || !identity.serverUrl || !identity.mattermostUserId || !identity.workspaceId || !identity.workspaceName || !identity.installationId) {
    return <BindingError reason="unknown" />;
  }

  return (
    <>
      <p className="break-words text-body">
        {t(($) => $.mattermost_bind.confirm_description, {
          username: identity.username,
          server: identity.serverUrl,
          account,
          workspace: identity.workspaceName,
        })}
      </p>
      <p className="text-caption text-muted-foreground">{t(($) => $.mattermost_bind.confirm_warning)}</p>
      {redeem.isError && <BindingError reason={redemptionFailureReason(redeem.error)} />}
      <div className="flex flex-wrap gap-2">
        <Button type="button" onClick={() => redeem.mutate()} disabled={redeem.isPending || preview.isFetching} aria-busy={redeem.isPending}>
          {redeem.isPending && <Loader2 aria-hidden="true" className="size-4 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.mattermost_bind.confirm)}
        </Button>
        <Button type="button" variant="outline" onClick={() => navigation.push("/")} disabled={redeem.isPending}>
          {t(($) => $.mattermost_bind.cancel)}
        </Button>
      </div>
    </>
  );
}

function BindingError({ reason }: { reason: string }) {
  const { t } = useT("common");
  let description: string;
  switch (reason) {
    case "missing_token": description = t(($) => $.mattermost_bind.error_missing_token); break;
    case "expired": description = t(($) => $.mattermost_bind.error_expired); break;
    case "already_bound": description = t(($) => $.mattermost_bind.error_already_bound); break;
    case "not_member": description = t(($) => $.mattermost_bind.error_not_member); break;
    default: description = t(($) => $.mattermost_bind.error_unknown); break;
  }
  return <p role="alert" className="text-body">{description}</p>;
}

function redemptionFailureReason(err: unknown): string {
  const lower = err instanceof Error ? err.message.toLowerCase() : "";
  if (lower.includes("invalid") || lower.includes("expired") || lower.includes("410")) return "expired";
  if (lower.includes("already bound") || lower.includes("409")) return "already_bound";
  if (lower.includes("workspace member") || lower.includes("403")) return "not_member";
  return "unknown";
}
