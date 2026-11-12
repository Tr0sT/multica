// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

function response(value: unknown) {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => value,
    headers: new Headers(),
  }));
}

afterEach(() => vi.unstubAllGlobals());

describe("agent marketplace API boundary", () => {
  it("parses wire names, supplies optional defaults, and preserves unknown source states", async () => {
    response({
      marketplaces: [{ id: "source", name: "studio", status: "future_status" }],
      plugins: [{ version_id: "version", marketplace_id: "source", name: "sample" }],
    });
    const result = await new ApiClient("https://example.test").getAgentMarketplaceCatalog("workspace");
    expect(result.valid).toBe(true);
    expect(result.marketplaces[0]).toMatchObject({ displayName: "studio", status: "future_status", sourceUrl: "" });
    expect(result.plugins[0]).toMatchObject({ marketplaceId: "source", displayName: "sample", skills: [] });
    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.headers).toMatchObject({ "X-Workspace-ID": "workspace" });
  });

  it.each([null, {}, { marketplaces: {}, plugins: [] }, { marketplaces: [], plugins: [{ name: "missing-id" }] }])(
    "marks malformed catalogs unusable: %j", async (body) => {
      response(body);
      expect(await new ApiClient("https://example.test").getAgentMarketplaceCatalog("workspace")).toEqual({
        marketplaces: [], plugins: [], valid: false,
      });
    },
  );

  it("does not acknowledge an invalid selection response as a successful empty selection", async () => {
    response({ configured: true, plugins: [{ wrong: "shape" }] });
    const result = await new ApiClient("https://example.test").setAgentPluginSelection("workspace", "agent", [
      { marketplaceId: "source", pluginName: "sample" },
    ]);
    expect(result.valid).toBe(false);
    const init = vi.mocked(fetch).mock.calls[0]?.[1];
    expect(JSON.parse(String(init?.body))).toEqual({
      plugins: [{ marketplace_id: "source", plugin_name: "sample" }],
    });
  });

  it("preserves explicit empty selection and sends refresh credentials only as a runtime reference", async () => {
    response({ configured: true, plugins: [] });
    expect(await new ApiClient("https://example.test").getAgentPluginSelection("workspace", "agent"))
      .toEqual({ configured: true, plugins: [], valid: true });
    response({ id: "source", status: "pending" });
    await new ApiClient("https://example.test").refreshAgentMarketplace("workspace", "source", "runtime");
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0]?.[1]?.body))).toEqual({ runtime_id: "runtime" });
  });
});
