// @vitest-environment node
import { describe, expect, it } from "vitest";
import { agentMarketplaceKeys, sameAgentPluginSelection } from "./queries";

describe("agent plugin selection", () => {
  const first = { marketplaceId: "one", pluginName: "sample" };
  const second = { marketplaceId: "two", pluginName: "sample" };
  it("uses marketplace identity and ignores order", () => {
    expect(sameAgentPluginSelection([first, second], [second, first])).toBe(true);
    expect(sameAgentPluginSelection([first], [second])).toBe(false);
    expect(sameAgentPluginSelection([first, second], [first, first])).toBe(false);
    expect(sameAgentPluginSelection([first], [])).toBe(false);
    expect(sameAgentPluginSelection([], [])).toBe(true);
  });
  it("scopes catalogs and selections to the workspace", () => {
    expect(agentMarketplaceKeys.catalog("one")).not.toEqual(agentMarketplaceKeys.catalog("two"));
    expect(agentMarketplaceKeys.selection("one", "agent")).not.toEqual(agentMarketplaceKeys.selection("two", "agent"));
  });
});
