// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentRuntime } from "@multica/core/types";
import type { AgentMarketplaceCatalog, AgentPluginSelection, AgentPluginSettings } from "@multica/core/agent-marketplaces";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const mocks = vi.hoisted(() => ({
  catalog: vi.fn(), selection: vi.fn(), save: vi.fn(), connect: vi.fn(), refresh: vi.fn(), disconnect: vi.fn(),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ userId: "user-1", role: "owner", member: null, isLoading: false }),
}));
vi.mock("@multica/core/api", () => ({ api: {
  getAgentMarketplaceCatalog: (...args: unknown[]) => mocks.catalog(...args),
  getAgentPluginSelection: (...args: unknown[]) => mocks.selection(...args),
  setAgentPluginSelection: (...args: unknown[]) => mocks.save(...args),
  connectAgentMarketplace: (...args: unknown[]) => mocks.connect(...args),
  refreshAgentMarketplace: (...args: unknown[]) => mocks.refresh(...args),
  disconnectAgentMarketplace: (...args: unknown[]) => mocks.disconnect(...args),
} }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { PluginsTab } from "./plugins-tab";

const agent: Agent = {
  id: "agent-1", workspace_id: "ws-1", runtime_id: "runtime-1", name: "Worker",
  description: "", instructions: "", avatar_url: null, runtime_mode: "local", runtime_config: {},
  custom_args: [], visibility: "workspace", permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }], status: "idle",
  max_concurrent_tasks: 1, model: "", owner_id: "user-1", skills: [],
  created_at: "", updated_at: "", archived_at: null, archived_by: null,
};
const runtime: AgentRuntime = {
  id: "runtime-1", workspace_id: "ws-1", daemon_id: "daemon-1", name: "Codex (Machine)",
  runtime_mode: "local", provider: "codex", launch_header: "", status: "online",
  device_info: "Machine", metadata: {}, owner_id: "user-1", visibility: "private",
  last_seen_at: null, created_at: "", updated_at: "",
};
let catalog: AgentMarketplaceCatalog;
let settings: AgentPluginSettings;
const clients: QueryClient[] = [];

function renderTab(canEdit = true, selectedRuntime = runtime) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  clients.push(qc);
  return render(<QueryClientProvider client={qc}>
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <PluginsTab agent={agent} runtime={selectedRuntime} runtimes={[selectedRuntime]} members={[]}
        currentUserId="user-1" canEdit={canEdit} />
    </I18nProvider>
  </QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  catalog = {
    valid: true,
    marketplaces: [{
      id: "market-1", name: "studio", displayName: "Studio", sourceUrl: "https://example.test/plugins.git",
      sourceRef: "main", fetchRuntimeId: "runtime-1", revision: "abc", status: "ready",
      error: "", createdBy: "user-1", updatedAt: "",
    }],
    plugins: [
      { versionId: "v1", marketplaceId: "market-1", marketplace: "studio", name: "ui", displayName: "UI plugin",
        description: "", version: "1.0", revision: "abc", skills: ["layout"], hasMcp: false, hasHooks: false },
      { versionId: "v2", marketplaceId: "market-1", marketplace: "studio", name: "qa", displayName: "QA plugin",
        description: "", version: "1.0", revision: "abc", skills: ["qa"], hasMcp: false, hasHooks: false },
    ],
  };
  settings = { configured: true, plugins: [], valid: true };
  mocks.catalog.mockImplementation(async () => catalog);
  mocks.selection.mockImplementation(async () => settings);
  mocks.save.mockImplementation(async (_ws: string, _agent: string, plugins: AgentPluginSelection[]) => {
    settings = { configured: true, plugins: [...plugins], valid: true };
    return settings;
  });
});
afterEach(() => {
  cleanup();
  clients.forEach((qc) => qc.clear());
  clients.length = 0;
});

describe("agent Plugins tab", () => {
  it("saves checkbox selection without an installation confirmation or OS gates", async () => {
    const user = userEvent.setup();
    renderTab();
    const ui = await screen.findByRole("checkbox", { name: "UI plugin" });
    const qa = screen.getByRole("checkbox", { name: "QA plugin" });
    expect(ui).not.toBeDisabled(); expect(qa).not.toBeDisabled();
    await user.click(ui); await user.click(qa);
    await user.click(screen.getByRole("button", { name: "Save selection" }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledWith("ws-1", "agent-1", [
      { marketplaceId: "market-1", pluginName: "ui" }, { marketplaceId: "market-1", pluginName: "qa" },
    ]));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save selection" })).toBeDisabled());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByText(/install on|linux only/i)).not.toBeInTheDocument();
  });

  it("retains the draft on failed save and permits an explicit empty selection", async () => {
    const user = userEvent.setup();
    settings.plugins = [{ marketplaceId: "market-1", pluginName: "ui" }];
    mocks.save.mockRejectedValueOnce(new Error("offline"));
    renderTab();
    await user.click(await screen.findByRole("checkbox", { name: "UI plugin" }));
    await user.click(screen.getByRole("button", { name: "Save selection" }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save selection" })).toBeEnabled());
    expect(screen.getByRole("checkbox", { name: "UI plugin" })).not.toBeChecked();
    await user.click(screen.getByRole("button", { name: "Save selection" }));
    await waitFor(() => expect(mocks.save).toHaveBeenLastCalledWith("ws-1", "agent-1", []));
  });

  it("keeps an unavailable binding visible until the user removes it", async () => {
    const user = userEvent.setup();
    settings.plugins = [{ marketplaceId: "market-1", pluginName: "removed" }];
    renderTab();
    const missing = await screen.findByRole("checkbox", { name: "removed" });
    expect(missing).toBeChecked();
    expect(screen.getByRole("button", { name: "Save selection" })).toBeDisabled();
    await user.click(missing);
    expect(screen.getByRole("button", { name: "Save selection" })).toBeEnabled();
  });

  it("does not expose an editable selection to a read-only viewer", async () => {
    const user = userEvent.setup();
    renderTab(false);
    const checkbox = await screen.findByRole("checkbox", { name: "UI plugin" });
    expect(checkbox).toHaveAttribute("aria-disabled", "true");
    await user.click(checkbox);
    expect(checkbox).not.toBeChecked();
    expect(mocks.save).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Save selection" })).not.toBeInTheDocument();
  });

  it("lets an owner clear old plugins after moving to another provider", async () => {
    const user = userEvent.setup();
    settings.plugins = [{ marketplaceId: "market-1", pluginName: "ui" }];
    renderTab(true, { ...runtime, provider: "claude-code" });
    const selected = await screen.findByRole("checkbox", { name: "UI plugin" });
    expect(screen.getByRole("checkbox", { name: "QA plugin" })).toHaveAttribute("aria-disabled", "true");
    await user.click(selected);
    await user.click(screen.getByRole("button", { name: "Save selection" }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledWith("ws-1", "agent-1", []));
  });

  it("connects a workspace catalog using the selected reader runtime", async () => {
    const user = userEvent.setup();
    mocks.connect.mockImplementation(async () => {
      const source = { ...catalog.marketplaces[0]!, id: "market-2", status: "pending" };
      catalog = { ...catalog, marketplaces: [...catalog.marketplaces, source] };
      return source;
    });
    renderTab();
    await user.click(await screen.findByRole("button", { name: "Marketplaces" }));
    const dialog = await screen.findByRole("dialog", { name: "Marketplaces" });
    await user.click(within(dialog).getByRole("button", { name: "Connect marketplace" }));
    await user.type(within(dialog).getByLabelText("Git repository URL"), "ssh://git@example.test/team/second.git");
    await user.type(within(dialog).getByLabelText("Branch or tag"), "main");
    await user.click(within(dialog).getByRole("button", { name: "Connect marketplace" }));
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledWith("ws-1", {
      url: "ssh://git@example.test/team/second.git", ref: "main", runtimeId: "runtime-1",
    }));
    expect(await within(dialog).findByText("Syncing")).toBeInTheDocument();
  });
});
