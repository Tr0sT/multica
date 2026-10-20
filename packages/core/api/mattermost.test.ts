// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { EMPTY_MATTERMOST_INSTALLATION } from "./schemas";

afterEach(() => vi.unstubAllGlobals());

describe("Mattermost settings update", () => {
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

  it.each([null, {}, { id: "installation-1", context: { mode: "channel", lookback_hours: "720" } }])(
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
