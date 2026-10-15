// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { MattermostContextMode } from "@multica/core/types";
import enSettings from "../../locales/en/settings.json";
import enCommon from "../../locales/en/common.json";

const mockRegister = vi.hoisted(() => vi.fn());
const mockList = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());
const role = vi.hoisted(() => ({ current: "owner" }));

vi.mock("@multica/core/api", () => ({ api: { registerMattermostBot: mockRegister, listMattermostInstallations: mockList } }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "workspace-1"], queryFn: async () => [{ user_id: "user-1", role: role.current }] }),
}));
vi.mock("@multica/core/auth", () => {
  const state = { user: { id: "user-1" } };
  return { useAuthStore: Object.assign((selector: (value: typeof state) => unknown) => selector(state), { getState: () => state }) };
});
vi.mock("@multica/core/workspace/hooks", () => ({ useActorName: () => ({ getAgentName: () => "Agent" }) }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../platform", () => ({ openExternal: vi.fn() }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: mockToastError } }));

import { MattermostAgentBindButton } from "./mattermost-tab";

function renderConnect() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={{ en: { settings: enSettings, common: enCommon } }}>
        <MattermostAgentBindButton agentId="agent-2" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  role.current = "owner";
  // Another bot on the same server must not disable this agent's connect flow.
  mockList.mockResolvedValue({ configured: true, install_supported: true, installations: [
    { id: "existing", agent_id: "agent-1", status: "active", server_url: "https://chat.test" },
  ] });
  mockRegister.mockResolvedValue({ id: "new-installation", status: "active" });
});

describe("Mattermost context selection", () => {
  it.each<{ mode: MattermostContextMode; label: string }>([
    { mode: "message", label: "Current message only" },
    { mode: "thread", label: "Whole thread" },
    { mode: "channel", label: "Chat history" },
  ])("submits $mode context for a second agent without changing the first bot", async ({ mode, label }) => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByTestId("mattermost-agent-connect"));
    await user.type(screen.getByLabelText(enSettings.mattermost.server_url_label), "https://chat.test");
    await user.type(screen.getByLabelText(enSettings.mattermost.access_token_label), "bot-two-token");
    const context = screen.getByRole("combobox", { name: "History sent to agent" });
    if (mode !== "message") {
      await user.click(context);
      await user.click(await screen.findByRole("option", { name: label }));
    }
    if (mode === "channel") {
      await user.click(screen.getByRole("combobox", { name: "History period" }));
      await user.click(await screen.findByRole("option", { name: "Last 7 days" }));
    } else {
      expect(screen.queryByRole("combobox", { name: "History period" })).toBeNull();
    }
    await user.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(mockRegister).toHaveBeenCalledWith("workspace-1", "agent-2", {
      server_url: "https://chat.test", access_token: "bot-two-token",
      context: { mode, ...(mode === "channel" ? { lookback_hours: 168 } : {}) },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("retains selected context and credentials after a failed connection", async () => {
    mockRegister.mockRejectedValue(new Error("Connection failed"));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByTestId("mattermost-agent-connect"));
    await user.type(screen.getByLabelText(enSettings.mattermost.server_url_label), "https://chat.test");
    await user.type(screen.getByLabelText(enSettings.mattermost.access_token_label), "token");
    await user.click(screen.getByRole("combobox", { name: "History sent to agent" }));
    await user.click(await screen.findByRole("option", { name: "Whole thread" }));
    await user.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith("Connection failed"));
    expect(screen.getByRole("combobox", { name: "History sent to agent" })).toHaveTextContent("Whole thread");
    expect(screen.getByLabelText(enSettings.mattermost.access_token_label)).toHaveValue("token");
  });

  it("hides connection settings from a workspace member", async () => {
    role.current = "member";
    renderConnect();
    await waitFor(() => expect(mockList).toHaveBeenCalled());
    expect(screen.queryByTestId("mattermost-agent-connect")).toBeNull();
  });
});
