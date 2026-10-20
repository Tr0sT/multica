// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { EMPTY_MATTERMOST_INSTALLATION } from "./schemas";

afterEach(() => vi.unstubAllGlobals());

describe("Mattermost settings update", () => {
  it.each([
    { mode: "text" as const, text: "Team accounts only" },
    { mode: "image" as const, image_attachment_id: "11111111-1111-4111-8111-111111111111" },
  ])("saves and parses a $mode automatic reply", async (denied_reply) => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "installation-1", status: "active", denied_reply,
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateMattermostInstallation("workspace-1", "installation-1", { denied_reply });
    expect(fetchMock).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({ body: JSON.stringify({ denied_reply }) }));
    expect(result.denied_reply).toMatchObject(denied_reply);
  });
  it("sends selected usernames and parses the resolved accounts", async () => {
    const allowed_users = [{ id: "alice-id", username: "alice" }];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "installation-1", status: "active", access_mode: "allowlist", allowed_users,
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const settings = { access_mode: "allowlist" as const, allowed_usernames: ["alice"] };
    const result = await new ApiClient("https://api.example.test")
      .updateMattermostInstallation("workspace-1", "installation-1", settings);
    expect(fetchMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ method: "PATCH", body: JSON.stringify(settings) }),
    );
    expect(result.allowed_users).toEqual(allowed_users);
  });

  it("patches access without resetting history", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "installation-1", status: "active", access_mode: "anyone",
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test")
      .updateMattermostInstallation("workspace-1", "installation-1", { access_mode: "anyone" });
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/workspaces/workspace-1/mattermost/installations/installation-1",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ access_mode: "anyone" }) }),
    );
    expect(result.access_mode).toBe("anyone");
  });

  it("patches only context on the existing workspace installation", async () => {
    const context = { mode: "channel", lookback_hours: 0 } as const;
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "installation-1", status: "active",
      context: { mode: "channel_extended", extended_lookback_hours: 0 },
    }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test")
      .updateMattermostInstallation("workspace-1", "installation-1", { context });
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/workspaces/workspace-1/mattermost/installations/installation-1",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ context }) }),
    );
    expect(result.context).toEqual(context);
  });

  it.each([
    null, {}, { id: "installation-1", context: { mode: "channel", lookback_hours: "720" } },
    ...[null, "alice", [{}], [{ id: "", username: "alice" }], [{ id: "alice-id", username: 3 }]]
      .map((allowed_users) => ({ id: "installation-1", access_mode: "allowlist", allowed_users })),
    ...[null, { mode: "text", text: 123 }, { mode: "image" }, { mode: "image", image_attachment_id: "invalid" },
      { mode: "image", image_attachment_id: "11111111-1111-4111-8111-111111111111", text: "also text" }]
      .map((denied_reply) => ({ id: "installation-1", denied_reply })),
  ])(
    "returns an inert fallback for malformed response %j",
    async (body) => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      })));
      expect(await new ApiClient("https://api.example.test").updateMattermostInstallation(
        "workspace-1", "installation-1", { context: { mode: "thread" } },
      )).toEqual(EMPTY_MATTERMOST_INSTALLATION);
    },
  );
});
