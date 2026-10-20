// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { MattermostContextMode } from "@multica/core/types";
import enSettings from "../../locales/en/settings.json";
import enCommon from "../../locales/en/common.json";

const mockRegister = vi.hoisted(() => vi.fn());
const mockList = vi.hoisted(() => vi.fn());
const mockUpdate = vi.hoisted(() => vi.fn());
const mockUpload = vi.hoisted(() => vi.fn());
const mockGetAttachment = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());
const role = vi.hoisted(() => ({ current: "owner" }));

vi.mock("@multica/core/api", () => ({ api: {
  registerMattermostBot: mockRegister, listMattermostInstallations: mockList,
  updateMattermostInstallation: mockUpdate,
  uploadFile: mockUpload, getAttachment: mockGetAttachment, getBaseUrl: () => "https://api.test",
} }));
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

import { MattermostAgentBindButton, MattermostTab } from "./mattermost-tab";

function renderConnect(workspaceSettings = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={{ en: { settings: enSettings, common: enCommon } }}>
        {workspaceSettings ? <MattermostTab /> : <MattermostAgentBindButton agentId="agent-2" />}
      </I18nProvider>
    </QueryClientProvider>,
  );
}

const BrowserURL = URL;
afterEach(() => vi.unstubAllGlobals());
beforeEach(() => {
  vi.stubGlobal("URL", class extends BrowserURL {
    static createObjectURL = vi.fn(() => "blob:reply-preview");
    static revokeObjectURL = vi.fn();
  });
  vi.clearAllMocks();
  role.current = "owner";
  // Another bot on the same server must not disable this agent's connect flow.
  mockList.mockResolvedValue({ configured: true, install_supported: true, installations: [
    { id: "existing", agent_id: "agent-1", status: "active", server_url: "https://chat.test" },
  ] });
  mockRegister.mockResolvedValue({ id: "new-installation", status: "active" });
  mockUpload.mockResolvedValue({ id: "11111111-1111-4111-8111-111111111111" });
  mockGetAttachment.mockResolvedValue({ filename: "reply.png", download_url: "https://files.test/reply.png" });
});

const connectedInstallation = {
  id: "existing", agent_id: "agent-2", status: "active",
  server_url: "https://chat.test", bot_username: "summary",
  installed_at: "2026-10-20T00:00:00Z",
  context: { mode: "channel", lookback_hours: 72 },
};

describe("Mattermost settings editing", () => {
  beforeEach(() => {
    mockList.mockResolvedValue({ configured: true, installations: [connectedInstallation] });
    mockUpdate.mockImplementation(async (_workspaceId, _installationId, settings) => {
      const updated = { ...connectedInstallation, ...settings,
        ...(settings.allowed_usernames ? {
          allowed_users: settings.allowed_usernames.map((username: string) => ({ id: `${username}-id`, username })),
        } : {}),
      };
      mockList.mockResolvedValue({ configured: true, installations: [updated] });
      return updated;
    });
  });

  it.each([false, true])("changes 3 days to 30 without reconnecting (workspace settings: %s)", async (workspaceSettings) => {
    const user = userEvent.setup();
    renderConnect(workspaceSettings);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("combobox", { name: "History sent to agent" })).toHaveTextContent("Chat history");
    expect(screen.getByRole("combobox", { name: "History period" })).toHaveTextContent("Last 3 days");
    expect(screen.queryByLabelText(enSettings.mattermost.access_token_label)).toBeNull();
    await user.click(screen.getByRole("combobox", { name: "History period" }));
    await user.click(await screen.findByRole("option", { name: "Last 30 days" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", {
      access_mode: "members", context: { mode: "channel", lookback_hours: 720 },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText("Context: Chat history, last 720 hours")).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it("changes the history mode without sending a stale channel period", async () => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: "History sent to agent" }));
    await user.click(await screen.findByRole("option", { name: "Whole thread" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", {
      access_mode: "members", context: { mode: "thread" },
    }));
  });

  it.each([
    { hours: 1440, label: "Last 60 days" },
    { hours: 2160, label: "Last 90 days" },
    { hours: 8760, label: "Last year" },
    { hours: 0, label: "No limit" },
  ])("saves and restores the $label period", async ({ hours, label }) => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: "History period" }));
    await user.click(await screen.findByRole("option", { name: label }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", {
      access_mode: "members", context: { mode: "channel", lookback_hours: hours },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    if (hours === 0) {
      expect(screen.getByText("Context: Chat history, no date limit")).toBeInTheDocument();
    }
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByRole("combobox", { name: "History period" })).toHaveTextContent(label);
  });

  it("defaults to 24 hours when enabling channel history from message-only context", async () => {
    mockList.mockResolvedValue({ configured: true, installations: [
      { ...connectedInstallation, context: { mode: "message", lookback_hours: 0 } },
    ] });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: "History sent to agent" }));
    await user.click(await screen.findByRole("option", { name: "Chat history" }));
    expect(screen.getByRole("combobox", { name: "History period" })).toHaveTextContent("Last 24 hours");
  });

  it("preserves the form after a failed save", async () => {
    mockUpdate.mockRejectedValue(new Error("Save failed"));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: "History period" }));
    await user.click(await screen.findByRole("option", { name: "Last 30 days" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith("Save failed"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "History period" })).toHaveTextContent("Last 30 days");
  });

  it("keeps the dialog open and prevents duplicate saves while the request is pending", async () => {
    let finish!: (value: typeof connectedInstallation) => void;
    mockUpdate.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const save = screen.getByRole("button", { name: "Save" });
    await user.click(save);
    await waitFor(() => expect(save).toBeDisabled());
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(mockUpdate).toHaveBeenCalledTimes(1);
    await act(async () => finish(connectedInstallation));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("does not treat a malformed update response as a successful save", async () => {
    mockUpdate.mockResolvedValue({ id: "", status: "revoked" });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith(enSettings.mattermost.edit_failed_toast));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("resets cancelled edits and restores focus to the edit button", async () => {
    const user = userEvent.setup();
    renderConnect();
    const edit = await screen.findByRole("button", { name: "Edit" });
    await user.click(edit);
    await user.click(screen.getByRole("combobox", { name: "History period" }));
    await user.click(await screen.findByRole("option", { name: "Last 30 days" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(edit).toHaveFocus());
    await user.click(edit);
    expect(screen.getByRole("combobox", { name: "History period" })).toHaveTextContent("Last 3 days");
    expect(mockUpdate).not.toHaveBeenCalled();
  });

  it("requires an explicit selection for an unknown server history mode", async () => {
    mockList.mockResolvedValue({ configured: true, installations: [
      { ...connectedInstallation, context: { mode: "unknown" } },
    ] });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "History sent to agent" })).toHaveTextContent("Unknown history setting");
  });

  it.each([false, true])("saves public access and restores it on reopen (workspace settings: %s)", async (workspaceSettings) => {
    const user = userEvent.setup();
    renderConnect(workspaceSettings);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }))
      .toHaveTextContent(enSettings.mattermost.access.members);
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.anyone }));
    expect(screen.getByText(enSettings.mattermost.access.notice)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", {
      access_mode: "anyone", context: connectedInstallation.context,
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }))
      .toHaveTextContent(enSettings.mattermost.access.anyone);
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it("does not report public access enabled when the server ignored it", async () => {
    mockUpdate.mockResolvedValue(connectedInstallation);
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.anyone }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith(enSettings.mattermost.edit_failed_toast));
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it.each([false, true])("saves, restores and clears selected accounts (workspace settings: %s)", async (workspaceSettings) => {
    const user = userEvent.setup();
    renderConnect(workspaceSettings);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    expect(screen.getByText(enSettings.mattermost.access.notice)).toBeVisible();
    await user.type(screen.getByLabelText(enSettings.mattermost.access.users_label), "@Alice,\nbob @alice");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", {
      access_mode: "allowlist", allowed_usernames: ["alice", "bob"], context: connectedInstallation.context,
      denied_reply: { mode: "text", text: "" },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const users = screen.getByLabelText(enSettings.mattermost.access.users_label);
    expect(users).toHaveValue("@alice\n@bob");
    await user.clear(users);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenLastCalledWith("workspace-1", "existing", {
      access_mode: "allowlist", allowed_usernames: [], context: connectedInstallation.context,
      denied_reply: { mode: "text", text: "" },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it.each(["failure", "ignored list"])("retains the account list after %s", async (outcome) => {
    if (outcome === "failure") mockUpdate.mockRejectedValue(new Error("Account not found"));
    else mockUpdate.mockResolvedValue({ ...connectedInstallation, access_mode: "allowlist", allowed_users: [] });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.type(screen.getByLabelText(enSettings.mattermost.access.users_label), "@alice");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalled());
    expect(screen.getByLabelText(enSettings.mattermost.access.users_label)).toHaveValue("@alice");
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("requires a choice for an unknown server access mode", async () => {
    mockList.mockResolvedValue({ configured: true, installations: [
      { ...connectedInstallation, access_mode: "unknown" },
    ] });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }))
      .toHaveTextContent(enSettings.mattermost.access.unknown);
  });

  it.each([false, true])("saves automatic text without uploading an image (workspace settings: %s)", async (workspaceSettings) => {
    const user = userEvent.setup();
    renderConnect(workspaceSettings);
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.type(screen.getByLabelText(enSettings.mattermost.denied_reply.text_label), "Team accounts only");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", expect.objectContaining({
      denied_reply: { mode: "text", text: "Team accounts only" },
    })));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText(enSettings.mattermost.denied_reply.text_label)).toHaveValue("Team accounts only");
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("uploads on save, restores the image and replaces it with text", async () => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.denied_reply.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.denied_reply.image }));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    const file = new File(["PNG"], "reply.png", { type: "image/png" });
    await user.upload(screen.getByLabelText(enSettings.mattermost.denied_reply.image_label), file);
    expect(mockUpload).not.toHaveBeenCalled();
    expect(screen.getByRole("img", { name: enSettings.mattermost.denied_reply.preview })).toHaveAttribute("src", "blob:reply-preview");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpload).toHaveBeenCalledWith(file, { workspaceId: "workspace-1" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", expect.objectContaining({
      denied_reply: { mode: "images", image_attachment_ids: ["11111111-1111-4111-8111-111111111111"] },
    })));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(await screen.findByText("reply.png")).toBeVisible();
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.denied_reply.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.denied_reply.text }));
    await user.type(screen.getByLabelText(enSettings.mattermost.denied_reply.text_label), "No access");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenLastCalledWith("workspace-1", "existing", expect.objectContaining({ denied_reply: { mode: "text", text: "No access" } })));
    expect(mockUpload).toHaveBeenCalledTimes(1);
  });

  it.each(["upload", "save"])("keeps the image available to retry a failed %s", async (failure) => {
    if (failure === "upload") mockUpload.mockRejectedValueOnce(new Error("Upload failed"));
    else mockUpdate.mockRejectedValueOnce(new Error("Save failed"));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.denied_reply.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.denied_reply.image }));
    await user.upload(screen.getByLabelText(enSettings.mattermost.denied_reply.image_label), new File(["PNG"], "reply.png", { type: "image/png" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalled());
    expect(await screen.findByText("reply.png")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mockUpload).toHaveBeenCalledTimes(failure === "upload" ? 2 : 1);
  });

  it("adds and removes images without fixing the set at ten or re-uploading saved files", async () => {
    const files = Array.from({ length: 13 }, (_, index) => new File(["PNG"], `pic-${index}.png`, { type: "image/png" }));
    const ids = files.map((_, index) => `00000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}`);
    mockUpload.mockImplementation(async (file) => ({ id: ids[files.indexOf(file)] }));
    mockGetAttachment.mockImplementation(async (id) => ({ filename: files[ids.indexOf(id)]!.name, download_url: `https://files.test/${id}.png` }));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.denied_reply.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.denied_reply.image }));
    await user.upload(screen.getByLabelText(enSettings.mattermost.denied_reply.image_label), files.slice(0, 12));
    await user.click(screen.getByRole("button", { name: "Remove pic-0.png" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", expect.objectContaining({
      denied_reply: { mode: "images", image_attachment_ids: ids.slice(1, 12) },
    })));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mockUpload).toHaveBeenCalledTimes(11);
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Remove pic-1.png" }));
    await user.upload(screen.getByLabelText(enSettings.mattermost.denied_reply.image_label), files[12]!);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockUpdate).toHaveBeenLastCalledWith("workspace-1", "existing", expect.objectContaining({
      denied_reply: { mode: "images", image_attachment_ids: ids.slice(2) },
    })));
    expect(mockUpload).toHaveBeenCalledTimes(12);
  });

  it("keeps successful uploads when a later image in the batch fails", async () => {
    const files = [new File(["PNG"], "first.png", { type: "image/png" }), new File(["PNG"], "second.png", { type: "image/png" })];
    const ids = ["11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"];
    mockUpload.mockResolvedValueOnce({ id: ids[0] }).mockRejectedValueOnce(new Error("Second upload failed")).mockResolvedValueOnce({ id: ids[1] });
    mockGetAttachment.mockImplementation(async (id) => ({ filename: id === ids[0] ? "first.png" : "second.png", download_url: `https://files.test/${id}.png` }));
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.denied_reply.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.denied_reply.image }));
    await user.upload(screen.getByLabelText(enSettings.mattermost.denied_reply.image_label), files);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockToastError).toHaveBeenCalledWith("Second upload failed"));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(await screen.findByText("first.png")).toBeVisible();
    expect(screen.getByText("second.png")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mockUpload).toHaveBeenCalledTimes(3);
    expect(mockUpload).toHaveBeenNthCalledWith(3, files[1], { workspaceId: "workspace-1" });
    expect(mockUpdate).toHaveBeenCalledWith("workspace-1", "existing", expect.objectContaining({ denied_reply: { mode: "images", image_attachment_ids: ids } }));
  });

  it("does not silently replace an unsupported reply type", async () => {
    mockList.mockResolvedValue({ configured: true, installations: [{ ...connectedInstallation, access_mode: "allowlist", denied_reply: { mode: "unknown" } }] });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it.each([false, true])("hides editing from members (workspace settings: %s)", async (workspaceSettings) => {
    role.current = "member";
    renderConnect(workspaceSettings);
    await waitFor(() => expect(mockList).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });
});

describe("Mattermost context selection", () => {
  it("connects a bot with selected Mattermost accounts", async () => {
    mockRegister.mockResolvedValue({ id: "new-installation", status: "active", access_mode: "allowlist",
      allowed_users: [{ id: "alice-id", username: "alice" }],
    });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByTestId("mattermost-agent-connect"));
    await user.type(screen.getByLabelText(enSettings.mattermost.server_url_label), "https://chat.test");
    await user.type(screen.getByLabelText(enSettings.mattermost.access_token_label), "token");
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.allowlist }));
    await user.type(screen.getByLabelText(enSettings.mattermost.access.users_label), "@alice");
    await user.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(mockRegister).toHaveBeenCalledWith("workspace-1", "agent-2", {
      server_url: "https://chat.test", access_token: "token", access_mode: "allowlist",
      allowed_usernames: ["alice"], context: { mode: "message" },
      denied_reply: { mode: "text", text: "" },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("can connect a new bot with public access", async () => {
    mockRegister.mockResolvedValue({ id: "new-installation", status: "active", access_mode: "anyone" });
    const user = userEvent.setup();
    renderConnect();
    await user.click(await screen.findByTestId("mattermost-agent-connect"));
    await user.type(screen.getByLabelText(enSettings.mattermost.server_url_label), "https://chat.test");
    await user.type(screen.getByLabelText(enSettings.mattermost.access_token_label), "token");
    await user.click(screen.getByRole("combobox", { name: enSettings.mattermost.access.label }));
    await user.click(await screen.findByRole("option", { name: enSettings.mattermost.access.anyone }));
    expect(screen.getByText(enSettings.mattermost.access.notice)).toBeVisible();
    await user.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(mockRegister).toHaveBeenCalledWith("workspace-1", "agent-2", {
      server_url: "https://chat.test", access_token: "token",
      access_mode: "anyone", context: { mode: "message" },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

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
      await user.click(await screen.findByRole("option", { name: "No limit" }));
    } else {
      expect(screen.queryByRole("combobox", { name: "History period" })).toBeNull();
    }
    await user.click(screen.getByTestId("mattermost-connect-submit"));
    await waitFor(() => expect(mockRegister).toHaveBeenCalledWith("workspace-1", "agent-2", {
      server_url: "https://chat.test", access_token: "bot-two-token",
      access_mode: "members",
      context: { mode, ...(mode === "channel" ? { lookback_hours: 0 } : {}) },
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
