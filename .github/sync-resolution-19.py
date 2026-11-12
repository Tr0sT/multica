"""One-shot reconstruction of the reviewed PR 19 tree; removed before publication."""
from pathlib import Path
import re
import subprocess
import sys

root = Path(sys.argv[1]).resolve()
conflict = re.compile(r'^<<<<<<< HEAD\n(.*?)^=======\n(.*?)^>>>>>>> refs/remotes/review/upstream\n', re.M | re.S)

def edit(path, old, new, count=1):
    p = root / path
    text = p.read_text()
    assert text.count(old) == count, (path, old[:80], text.count(old), count)
    p.write_text(text.replace(old, new))

def resolve(path, fn, expected):
    p = root / path
    text = p.read_text()
    assert len(list(conflict.finditer(text))) == expected, path
    p.write_text(conflict.sub(lambda m: fn(m.group(1), m.group(2)), text))

for lang in ('', 'ja.', 'ko.', 'zh.'):
    resolve('apps/docs/content/docs/environment-variables.' + lang + 'mdx', lambda fork, up: up + fork, 1)
resolve('docker-compose.selfhost.yml', lambda fork, up: up + fork, 1)
resolve('.github/RELEASING.md', lambda fork, up: fork.replace('~1.27.3', '~1.27.4').replace('at least 1.27.3', 'at least 1.27.4'), 1)
resolve('server/internal/handler/daemon.go', lambda fork, up: up + fork, 1)
resolve('server/internal/daemon/daemon.go', lambda fork, up: up.replace('d.executeAndDrain(ctx, backend, freshPrompt,', 'd.executeWithModelCapacityRetry(ctx, backend, freshPrompt,'), 1)
resolve('packages/views/package.json', lambda fork, up: fork.replace('    "./invite": "./invite/index.ts",\n', ''), 1)
resolve('packages/views/agents/components/agent-advanced-settings.tsx', lambda fork, up: up, 2)
resolve('packages/views/agents/components/agent-advanced-settings.test.tsx', lambda fork, up: '    ["Mattermost", mattermostListingRef],\n' + up, 1)
resolve('packages/views/chat/components/use-chat-controller.ts', lambda fork, up: up + fork, 1)
p = root / 'packages/views/chat/components/chat-thread-list.tsx'
s = p.read_text()
ms = list(conflict.finditer(s))
assert len(ms) == 5
repls = [
    '    const isRenaming = !readOnly && renamingId === session.id;\n' + ms[0].group(1),
    ms[1].group(2),
    ms[2].group(2).replace('<span className={cn("min-w-0 flex-1 truncate text-body",', '<span title={titleText} className={cn("min-w-0 flex-1 truncate text-body",'),
    '        {!isConfirmingAction && !isRenaming && (rowActions.length > 0 || menuActions.length > 0) && (\n',
    '        {!isConfirmingAction && !isRenaming && rowActions.length > 0 && (\n',
]
i = iter(repls)
s = conflict.sub(lambda _: next(i), s)
s = s.replace('  const rowActionsFor = (session: ChatSession): RowActionItem[] =>\n    view === "archived"', '  const isReadOnlySession = (session: ChatSession) =>\n    !!viewerId && session.creator_id !== viewerId;\n  const rowActionsFor = (session: ChatSession): RowActionItem[] =>\n    isReadOnlySession(session) ? [] : view === "archived"')
s = s.replace('    if (view === "archived") return [];', '    if (isReadOnlySession(session) || view === "archived") return [];')
s = s.replace('    if (!title || title === (session.title ?? "")) return;', '    if (isReadOnlySession(session) || !title || title === (session.title ?? "")) return;')
p.write_text(s)

p = 'packages/views/settings/components/channels-tab.tsx'
(root / p).write_bytes(subprocess.check_output(['git', 'show', 'refs/remotes/review/upstream:' + p], cwd=root))
for p in ['packages/views/settings/components/channels-tab.test.tsx', 'packages/views/agents/components/tabs/integrations-tab.tsx', 'packages/views/agents/components/tabs/integrations-tab.test.tsx']:
    (root / p).unlink()
p = 'packages/views/settings/components/use-messaging-channels.ts'
edit(p, 'import { telegramInstallationsOptions } from "@multica/core/telegram";', 'import { telegramInstallationsOptions } from "@multica/core/telegram";\nimport { mattermostInstallationsOptions } from "@multica/core/mattermost";')
edit(p, '  TelegramInstallation,', '  TelegramInstallation,\n  MattermostInstallation,')
edit(p, '  | { channel: "telegram"; installation: TelegramInstallation };', '  | { channel: "telegram"; installation: TelegramInstallation }\n  | { channel: "mattermost"; installation: MattermostInstallation };')
edit(p, '  const queries = { lark, slack, dingtalk, wecom, telegram };', '  const mattermost = useQuery({ ...mattermostInstallationsOptions(wsId), enabled });\n  const queries = { lark, slack, dingtalk, wecom, telegram, mattermost };')
edit(p, '  ].filter(({ installation }) => installation.status === "active");', '    ...(mattermost.data?.installations ?? []).map((installation) => ({\n      channel: "mattermost" as const,\n      installation,\n    })),\n  ].filter(({ installation }) => installation.status === "active");')
p = 'packages/views/settings/components/channel-disconnect-dialog.tsx'
edit(p, '        telegram: api.deleteTelegramInstallation.bind(api),', '        telegram: api.deleteTelegramInstallation.bind(api),\n        mattermost: api.deleteMattermostInstallation.bind(api),')
p = 'packages/views/settings/components/mattermost-tab.tsx'
edit(p, '  onShowConnectedDetails,\n}: {', '  onShowConnectedDetails,\n  inline = false,\n  onClose,\n  onBusyChange,\n}: {')
edit(p, '  onShowConnectedDetails?: () => void;\n}) {', '  onShowConnectedDetails?: () => void;\n  /** Reuse the full connection form inside the shared channel dialog. */\n  inline?: boolean;\n  onClose?: () => void;\n  onBusyChange?: (busy: boolean) => void;\n}) {')
edit(p, '    setLookbackHours(24);\n  }\n\n  async function handleSubmit()', '    setLookbackHours(24);\n    onClose?.();\n  }\n\n  async function handleSubmit()')
edit(p, '    if (submitting || !agentId || !server_url || !access_token) return;\n    setSubmitting(true);', '    if (submitting || !agentId || !server_url || !access_token) return;\n    setSubmitting(true);\n    onBusyChange?.(true);')
edit(p, '      setLookbackHours(24);\n    } catch (e)', '      setLookbackHours(24);\n      onClose?.();\n    } catch (e)')
edit(p, '    } finally {\n      setSubmitting(false);\n    }\n  }\n\n  const canSubmit', '    } finally {\n      setSubmitting(false);\n      onBusyChange?.(false);\n    }\n  }\n\n  const canSubmit')
path = root / p
s = path.read_text()
start = s.index('        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg" data-testid="mattermost-connect-dialog">')
end = s.index('        </DialogContent>', start)
content = s[start:s.index('\n', start) + 1]
body = s[s.index('\n', start) + 1:end]
header_start = body.index('          <DialogHeader>')
header_end = body.index('          </DialogHeader>') + len('          </DialogHeader>')
header = body[header_start:header_end]
body = body[:header_start] + '          {!inline && (\n' + header + '\n          )}' + body[header_end:]
body = '\n'.join(line[6:] if line.startswith('      ') else line for line in body.splitlines())
form = '  const connectionFields = (\n    <>\n' + body + '\n    </>\n  );\n\n  if (inline) return <div className="grid gap-4" data-testid="mattermost-connect-dialog">{connectionFields}</div>;\n\n'
s = s[:start] + content + '          {connectionFields}\n' + s[end:]
insert = s.index('  return (\n    <div\n      className={cn("flex flex-wrap items-center gap-2", className)}', s.index('export function MattermostAgentBindButton'))
s = s[:insert] + form + s[insert:]
s = s.replace('function MattermostEditButton({', 'export function MattermostEditButton({')
path.write_text(s)
p = 'packages/views/settings/components/channel-connect-dialog.tsx'
edit(p, 'import { WecomChannelAuthorization } from "./wecom-channel-authorization";', 'import { WecomChannelAuthorization } from "./wecom-channel-authorization";\nimport { MattermostAgentBindButton } from "./mattermost-tab";')
edit(p, '<DialogContent className="sm:max-w-lg" showCloseButton={!busy}>', '<DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg" showCloseButton={!busy}>')
edit(p, '          {channel === "lark" ? (', '          {channel === "mattermost" ? (\n            <MattermostAgentBindButton\n              agentId={agent.id}\n              agentName={agent.name}\n              inline\n              onClose={onClose}\n              onBusyChange={onBusyChange}\n            />\n          ) : channel === "lark" ? (')
edit(p, '  channel: Exclude<ChannelIntegration, "lark">;', '  channel: Exclude<ChannelIntegration, "lark" | "mattermost">;')
p = 'packages/views/agents/components/agent-channels-section.tsx'
edit(p, 'import { ChannelConnectDialog } from "../../settings/components/channel-connect-dialog";', 'import { ChannelConnectDialog } from "../../settings/components/channel-connect-dialog";\nimport { MattermostEditButton } from "../../settings/components/mattermost-tab";')
edit(p, '    connection.channel === "wecom"\n      ? connection.installation.bot_name', '    connection.channel === "mattermost"\n      ? connection.installation.bot_username\n      : connection.channel === "wecom"\n      ? connection.installation.bot_name')
edit(p, '        : connection.channel === "telegram" &&\n            connection.installation.bot_username', '        : (connection.channel === "telegram" || connection.channel === "mattermost") &&\n            connection.installation.bot_username')
edit(p, '          <span\n            className={\n              identityIssue', '          {connection.channel === "mattermost" && connection.installation.server_url ? (\n            <p className="truncate text-caption text-muted-foreground">\n              {connection.installation.server_url}\n            </p>\n          ) : null}\n          <span\n            className={\n              identityIssue')
edit(p, '        {canManage ? (\n          <DropdownMenu>', '        {canManage && connection.channel === "mattermost" ? (\n          <MattermostEditButton installation={connection.installation} />\n        ) : null}\n        {canManage ? (\n          <DropdownMenu>')
p = 'packages/views/chat/components/chat-thread-list.tsx'
edit(p, '          if (isConfirmingAction || isRenaming || !shouldOpenRowMenu(e)) return;', '          if (readOnly || isConfirmingAction || isRenaming || !shouldOpenRowMenu(e)) return;')
p = '.github/workflows/declarative-compatibility.yml'
edit(p, "go-version: '~1.27.3'", "go-version: '~1.27.4'")
edit(p, 'ref: d7e3ffe4a52d86ade56c4bcc39d622c4c3417549', 'ref: bf320699c1bb3d89b7dad10a82138bb1375f08f8')

p = 'packages/views/settings/components/messaging-channels.test.tsx'
edit(p, 'vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));', '''vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => {
  const auth = { user: { id: "user-1" } };
  return {
    useAuthStore: Object.assign(
      (selector: (value: typeof auth) => unknown) => selector(auth),
      { getState: () => auth },
    ),
  };
});''')
edit(p, '      "listTelegramInstallations",', '      "listTelegramInstallations",\n      "listMattermostInstallations",\n      "listMembers",\n      "registerMattermostBot",\n      "updateMattermostInstallation",\n      "deleteMattermostInstallation",')
edit(p, '    "listTelegramInstallations",\n  ] as const)', '    "listTelegramInstallations",\n    "listMattermostInstallations",\n  ] as const)')
edit(p, '  vi.mocked(api.listAgents).mockResolvedValue([agent, otherAgent]);', '  vi.mocked(api.listAgents).mockResolvedValue([agent, otherAgent]);\n  vi.mocked(api.listMembers).mockImplementation(async () =>\n    (state.member ? [{ user_id: "user-1", role: state.role }] : []) as never,\n  );')
edit(p, 'it.each(["lark", "slack", "dingtalk", "wecom", "telegram"] as const)', 'it.each(["lark", "slack", "dingtalk", "wecom", "telegram", "mattermost"] as const)')
edit(p, '        telegram: ["listTelegramInstallations", "deleteTelegramInstallation"],', '        telegram: ["listTelegramInstallations", "deleteTelegramInstallation"],\n        mattermost: ["listMattermostInstallations", "deleteMattermostInstallation"],')
edit(p, '      within(dialog).getByRole("button", { name: /WeCom Ask an admin/ }),\n    ).toBeDisabled();', '      within(dialog).getByRole("button", { name: /WeCom Ask an admin/ }),\n    ).toBeDisabled();\n    expect(\n      within(dialog).getByRole("button", { name: /Mattermost Ask an admin/ }),\n    ).toBeDisabled();')
with (root / p).open('a') as f:
    f.write('''

describe("fork Mattermost channel integration", () => {
  const installed = {
    ...active,
    bot_username: "support_bot",
    server_url: "https://chat.example.test",
    access_mode: "allowlist",
    allowed_users: [{ id: "stable-user-id", username: "alice" }],
    context: { mode: "channel", lookback_hours: 72 },
    attachment_mode: "none",
    installed_at: "2026-11-01T00:00:00Z",
  };

  it("keeps all six platform marks in the workspace overview", async () => {
    show(<ChannelsTab />);
    await screen.findByText("Mattermost");
    const marks = ["lark", "slack", "dingtalk", "wecom", "telegram", "mattermost"]
      .map((id) => screen.getByTestId(`integration-channel-icon-${id}`).innerHTML);
    expect(new Set(marks).size).toBe(6);
  });

  it("shows the bot and server to members without exposing edit or disconnect", async () => {
    state.role = "member";
    vi.mocked(api.listMattermostInstallations).mockResolvedValue(listing([installed]) as never);
    show(<AgentChannelsSection agent={agent} />);
    expect(await screen.findByText("@support_bot")).toBeInTheDocument();
    expect(screen.getByText(installed.server_url)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Manage Mattermost" })).toBeNull();
    expect(api.updateMattermostInstallation).not.toHaveBeenCalled();
    expect(api.deleteMattermostInstallation).not.toHaveBeenCalled();
  });

  it("keeps existing bot settings editable when new installations are disabled", async () => {
    vi.mocked(api.listMattermostInstallations).mockResolvedValue({
      ...listing([installed]), install_supported: false,
    } as never);
    show(<AgentChannelsSection agent={agent} />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "History sent to agent" })).toHaveTextContent("Chat history");
    expect(screen.getByRole("combobox", { name: "Attachments sent to agent" })).toHaveTextContent("Do not send");
    expect(screen.queryByTestId("mattermost-access-token")).toBeNull();
    expect(api.registerMattermostBot).not.toHaveBeenCalled();
  });

  it("reuses the full connection form inside one shared modal and preserves failed input", async () => {
    const onOpenChange = vi.fn();
    vi.mocked(api.registerMattermostBot)
      .mockRejectedValueOnce(new Error("Synthetic registration failure"))
      .mockResolvedValueOnce({ ...active, attachment_mode: "links", access_mode: "members" } as never);
    show(<ChannelConnectDialog agents={[agent]} initialAgent={agent} open onOpenChange={onOpenChange} />);
    fireEvent.click(await screen.findByRole("button", { name: /^Mattermost$/ }));
    const server = await screen.findByTestId("mattermost-server-url");
    const token = screen.getByTestId("mattermost-access-token");
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(screen.getByRole("combobox", { name: "History sent to agent" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Attachments sent to agent" })).toBeInTheDocument();
    fireEvent.change(server, { target: { value: "https://chat.example.test" } });
    fireEvent.change(token, { target: { value: "synthetic-not-a-real-token" } });
    fireEvent.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(api.registerMattermostBot).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByTestId("mattermost-connect-submit")).toBeEnabled());
    expect(server).toHaveValue("https://chat.example.test");
    expect(token).toHaveValue("synthetic-not-a-real-token");
    expect(onOpenChange).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(api.registerMattermostBot).toHaveBeenLastCalledWith("ws-1", "agent-1", {
      server_url: "https://chat.example.test",
      access_token: "synthetic-not-a-real-token",
      access_mode: "members",
      attachment_mode: "links",
      context: { mode: "message" },
    });
  });
});
''')
p = 'packages/views/chat/components/chat-thread-list.test.tsx'
edit(p, '  expect(screen.queryByRole("button", { name: enChat.list.row_actions_aria })).not.toBeInTheDocument();\n});', '''  expect(screen.queryByRole("button", { name: enChat.list.row_actions_aria })).not.toBeInTheDocument();
  fireEvent.contextMenu(screen.getByText(/@alice\\.example/), { clientX: 30, clientY: 90 });
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: enChat.header.rename })).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
});''')
p = root / 'packages/core/issues/queries.ts'
p.write_text(p.read_text().rstrip() + '\n')
p = root / 'packages/views/settings/components/mattermost-tab.tsx'
s = p.read_text()
start = s.index('  const connectionFields = (')
end = s.index('  if (inline)', start)
chunk = s[start:end].splitlines()
out = []
for n, line in enumerate(chunk):
    if n >= 2 and line not in ['    </>', '  );', '']:
        line = '  ' + line
    out.append(line)
s = s[:start] + '\n'.join(out) + '\n' + s[end:]
s = s.replace('''      {!inline && (
      <DialogHeader>
        <DialogTitle>{t(($) => $.mattermost.connect_dialog_title)}</DialogTitle>
        <DialogDescription>
          {t(($) => $.mattermost.connect_dialog_description)}
        </DialogDescription>
      </DialogHeader>
      )}''', '''      {!inline && (
        <DialogHeader>
          <DialogTitle>{t(($) => $.mattermost.connect_dialog_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.mattermost.connect_dialog_description)}
          </DialogDescription>
        </DialogHeader>
      )}''')
s = s.replace('''  if (inline) return <div className="grid gap-4" data-testid="mattermost-connect-dialog">{connectionFields}</div>;''', '''  if (inline) {
    return (
      <div className="grid gap-4" data-testid="mattermost-connect-dialog">
        {connectionFields}
      </div>
    );
  }''')
p.write_text(s)
p = root / 'packages/views/settings/components/channel-connect-dialog.tsx'
s = p.read_text().replace('''      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg" showCloseButton={!busy}>''', '''      <DialogContent
        className="max-h-[85dvh] overflow-y-auto sm:max-w-lg"
        showCloseButton={!busy}
      >''')
a = s.index('          <Button\n            variant="link"')
b = s.index('          </Button>', a) + len('          </Button>')
block = s[a:b]
s = s[:a] + '          {channel !== "mattermost" ? (\n' + ''.join('  ' + line + '\n' for line in block.splitlines()) + '          ) : null}' + s[b:]
p.write_text(s)
p = root / 'packages/views/settings/components/channels-tab.tsx'
s = p.read_text().replace('              {t(($) => $.telegram.section_title)}\n', '              {t(($) => $.telegram.section_title)},{" "}\n              {t(($) => $.mattermost.section_title)}\n')
p.write_text(s)
p = root / 'packages/views/settings/components/messaging-channels.test.tsx'
s = p.read_text().replace('expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(5);', 'expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(6);')
s = s.replace('    await screen.findByText("Mattermost");', '    await screen.findByTestId("integration-channel-icon-mattermost");')
p.write_text(s)
p = root / 'packages/views/settings/components/use-messaging-channels.test.ts'
s = p.read_text().replace('"telegram", "wecom", "dingtalk"]', '"telegram", "wecom", "dingtalk", "mattermost"]')
p.write_text(s)
subprocess.run(['gofmt', '-w', 'server/internal/daemon/daemon.go', 'server/internal/handler/daemon.go'], cwd=root, check=True)
subprocess.run(['git', 'add', '-A'], cwd=root, check=True)
subprocess.run(['git', 'diff', '--cached', '--check'], cwd=root, check=True)
assert not subprocess.check_output(['git', 'ls-files', '-u'], cwd=root)
actual = subprocess.check_output(['git', 'write-tree'], cwd=root, text=True).strip()
assert actual == '1ef5655a01e34b64ff933dbb3fc7e301bbc6428b', ('Unexpected combined tree', actual)
print('Verified reviewed combined tree:', actual)
