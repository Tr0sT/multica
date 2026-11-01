#!/usr/bin/env python3
"""Single-use semantic resolver for six conflicts at upstream 5a2cc891."""
from pathlib import Path
from subprocess import check_output, run

def git(*args):
    return check_output(["git",*args], text=True)
def staged(path):
    return git("show",":3:"+path)
def edit(s, before, after, label):
    n=s.count(before)
    if n!=1: raise RuntimeError(f"{label}: expected one occurrence, found {n}")
    return s.replace(before,after,1)
def write(path,s):
    Path(path).write_text(s)
want={
"packages/views/agents/components/agent-overview-pane.test.tsx",
"packages/views/agents/components/agent-overview-pane.tsx",
"packages/views/chat/chat-page.tsx",
"packages/views/chat/components/chat-session-header.tsx",
"server/pkg/db/generated/issue.sql.go",
"server/pkg/db/queries/issue.sql",
}
got=set(git("diff","--name-only","--diff-filter=U").splitlines())
if got!=want: raise RuntimeError(f"Unexpected conflicts: missing={want-got},extra={got-want}")

# Migrate Mattermost-only integration visibility from the removed overview
# component to upstream's new Advanced Settings implementation.
path="packages/views/agents/components/agent-advanced-settings.tsx"
s=Path(path).read_text()
s=edit(s,'import { telegramInstallationsOptions } from "@multica/core/telegram";','import { telegramInstallationsOptions } from "@multica/core/telegram";\nimport { mattermostInstallationsOptions } from "@multica/core/mattermost";',"integration-import")
s=edit(s,'  const integrationsConfigured =','  const { data: mattermostListing } = useQuery({\n    ...mattermostInstallationsOptions(wsId),\n    enabled: !!wsId,\n  });\n\n  const integrationsConfigured =',"integration-query")
s=edit(s,'    telegramListing?.configured === true;','    telegramListing?.configured === true ||\n    mattermostListing?.configured === true;',"integration-visibility")
write(path,s)
path="packages/views/agents/components/agent-advanced-settings.test.tsx"
s=Path(path).read_text()
s=edit(s,'const telegramListingRef = vi.hoisted(() => ({','const mattermostListingRef = vi.hoisted(() => ({\n  current: { installations: [] as unknown[], configured: false },\n}));\nconst telegramListingRef = vi.hoisted(() => ({',"test-ref")
s=edit(s,'vi.mock("@multica/core/telegram", () => ({','vi.mock("@multica/core/mattermost", () => ({\n  mattermostInstallationsOptions: () => ({\n    queryKey: ["mattermost", "installations"],\n    queryFn: () => Promise.resolve(mattermostListingRef.current),\n  }),\n}));\nvi.mock("@multica/core/telegram", () => ({',"test-mock")
s=edit(s,'  telegramListingRef.current = { installations: [], configured: false };','  telegramListingRef.current = { installations: [], configured: false };\n  mattermostListingRef.current = { installations: [], configured: false };',"test-reset")
s=edit(s,'    ["Telegram", telegramListingRef],','    ["Telegram", telegramListingRef],\n    ["Mattermost", mattermostListingRef],',"test-visibility")
write(path,s)
for path in sorted(want):
    if "agent-overview-pane" in path:
        run(["git","rm","--",path],check=True)

# Keep new upstream responsive chat layout; preserve read-only admin inspection.
path="packages/views/chat/chat-page.tsx"
s=staged(path)
s=edit(s,'import { IconButton } from "@multica/ui/components/common/icon-button";','import { IconButton } from "@multica/ui/components/common/icon-button";\nimport { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";',"select-import")
s=edit(s,'  const listBody = (\n','''  const ownerItems = [
    { value: "mine", label: t(($) => $.owner_filter.mine) },
    { value: "all", label: t(($) => $.owner_filter.all) },
    ...(c.members ?? []).filter((member) => member.user_id !== c.user?.id).map((member) => ({ value: member.user_id, label: member.name })),
  ];

  const listBody = (
''',"owner-items")
s=edit(s,'  const listBody = (\n    <div className="px-2 py-1">\n','''  const listBody = (
    <div className="px-2 py-1">
      {c.canInspectMembers && (
        <Select
          items={ownerItems}
          value={searchParams.get("creator") || "mine"}
          onValueChange={(value) => {
            if (!value) return;
            replace(value === "mine" ? wsPaths.chat() : wsPaths.chat() + "?creator=" + encodeURIComponent(value));
          }}
        >
          <SelectTrigger className="mb-2 w-full" aria-label={t(($) => $.owner_filter.label)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {ownerItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
''',"owner-selector")
s=edit(s,'          onArchive={handleArchive}\n        />\n      )}\n      {c.showSkeleton','          onArchive={handleArchive}\n          readOnly={c.isReadOnly}\n          ownerName={c.isReadOnly ? c.members.find((member) => member.user_id === c.currentSession?.creator_id)?.name || t(($) => $.owner_filter.former_member) : undefined}\n        />\n      )}\n      {c.showSkeleton',"header-props")
s=edit(s,'          quickActionsDisabled={\n            !!c.pendingTaskId ||','          quickActionsDisabled={\n            !!c.pendingTaskId ||\n            c.isReadOnly ||',"quick-actions")
s=edit(s,'          onRegenerateQuickActions={(message) =>','          onRegenerateQuickActions={c.isReadOnly ? undefined : (message) =>',"regenerate")
s=edit(s,'      ) : (\n        <EmptyState\n          agent={c.activeAgent}','      ) : c.isReadOnly ? (\n        <p className="p-4 text-caption text-muted-foreground">{t(($) => $.list.no_messages)}</p>\n      ) : (\n        <EmptyState\n          agent={c.activeAgent}',"readonly-empty")
s=edit(s,'      {c.isAgentAccessRevoked ? (\n','      {c.isReadOnly ? (\n        <p className="px-4 py-3 text-caption text-muted-foreground">{t(($) => $.owner_filter.read_only)}</p>\n      ) : <>\n      {c.isAgentAccessRevoked ? (\n',"readonly-controls")
s=edit(s,'        focusRequest={c.focusInputRequest}\n      />\n    </div>\n  );','        focusRequest={c.focusInputRequest}\n      />\n      <p className="px-4 pb-2 text-caption text-muted-foreground">{t(($) => $.owner_filter.visibility)}</p>\n      </>}\n    </div>\n  );',"readonly-controls-end")
write(path,s)

# The breadcrumb replaces a custom header but cannot grant actions in
# another member's chat. Keep channel attribution and owner label.
path="packages/views/chat/components/chat-session-header.tsx"
s=staged(path)
s=edit(s,'  onArchive,\n  leading,','  onArchive,\n  leading,\n  readOnly = false,\n  ownerName,',"header-args")
s=edit(s,'  agent: Agent | null;\n','  agent: Agent | null;\n  readOnly?: boolean;\n  ownerName?: string;\n',"header-types")
s=edit(s,'import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";','import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";\nimport { chatChannelLabel } from "../lib/channel-label";',"header-import")
s=edit(s,'  const title = session.title?.trim() || t(($) => $.window.untitled);','  const title = session.title?.trim() || t(($) => $.window.untitled);\n  const subtitle = [chatChannelLabel(session), ownerName].filter(Boolean).join(" · ");',"header-subtitle")
s=edit(s,'  const leaf = editing ? (','  const leaf = editing && !readOnly ? (',"header-edit")
s=edit(s,'    <TooltipHint label={t(($) => $.header.rename)}>','    <TooltipHint label={readOnly ? title : t(($) => $.header.rename)}>',"header-tooltip")
s=edit(s,'        onClick={startRename}\n        className="min-w-0 truncate','        onClick={startRename}\n        disabled={readOnly}\n        className="min-w-0 truncate',"header-disabled")
s=edit(s,'        leaf={leaf}\n        actions={\n          <DropdownMenu>','        leaf={<div className="min-w-0 flex flex-col">{leaf}{subtitle && <span className="truncate text-caption text-muted-foreground" title={subtitle}>{subtitle}</span>}</div>}\n        actions={readOnly ? null : (\n          <DropdownMenu>',"header-breadcrumb")
s=edit(s,'          </DropdownMenu>\n        }\n      />','          </DropdownMenu>\n        )}\n      />',"header-no-actions")
write(path,s)

# Both sides' issue-deletion CTEs must execute.
for path in ("server/pkg/db/generated/issue.sql.go","server/pkg/db/queries/issue.sql"):
    s=staged(path)
    s=edit(s,'cleared_unread AS (\n',"""cleared_agent_plugin_snapshots AS (
    DELETE FROM agent_plugin_snapshot
    WHERE workspace_id = $2 AND context_key IN (SELECT 'issue:' || target.id::text FROM target)
),
cleared_unread AS (
""","issue-cleanup-"+path)
    write(path,s)
run(["git","add","-A"],check=True)
if git("diff","--name-only","--diff-filter=U").strip():
    raise RuntimeError("Merge still has unresolved paths")
print("Resolved six expected conflicts with explicit preservation of fork features")
