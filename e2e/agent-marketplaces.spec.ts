import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

test("connect a marketplace and save plugins for an agent without installing on its runtime", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  const suffix = randomUUID().slice(0, 8);
  let workspaceCreated = false;
  try {
    await api.login("e2e-plugins-" + suffix + "@multica.ai", "E2E Plugin Owner");
    const workspace = await api.ensureWorkspace("Plugin E2E " + suffix, "plugins-e2e-" + suffix);
    workspaceCreated = true;
    await api.markUserOnboarded();
    const registered = await api.authedFetch("/api/daemon/register", {
      method: "POST",
      headers: { "X-Client-Capabilities": "agent-plugin-bundles-v1" },
      body: JSON.stringify({
        workspace_id: workspace.id, daemon_id: randomUUID(), device_name: "E2E marketplace reader",
        cli_version: "0.6.3", runtimes: [{ name: "Codex (E2E)", type: "codex", version: "0.165.0", status: "online" }],
      }),
    });
    expect(registered.ok, await registered.clone().text()).toBe(true);
    const runtime = (await registered.json()).runtimes[0];
    const created = await api.authedFetch("/api/agents", {
      method: "POST", body: JSON.stringify({ name: "Plugin Worker", runtime_id: runtime.id }),
    });
    expect(created.ok, await created.clone().text()).toBe(true);
    const agent = await created.json();
    const token = api.getToken();
    if (!token) throw new Error("test login did not return a token");
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, token);
    await page.goto("/" + workspace.slug + "/agents/" + agent.id);
    await page.getByRole("tab", { name: "Capabilities", exact: true }).click();
    await page.getByRole("tab", { name: "Plugins", exact: true }).click();
    await page.getByRole("button", { name: "Marketplaces", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Marketplaces", exact: true });
    await dialog.getByRole("button", { name: "Connect marketplace", exact: true }).click();
    await dialog.getByLabel("Git repository URL").fill("ssh://git@example.test/team/plugins.git");
    await dialog.getByLabel("Branch or tag").fill("main");
    const connected = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith("/api/agent-marketplaces"));
    await dialog.getByRole("button", { name: "Connect marketplace", exact: true }).click();
    const sourceResponse = await connected;
    expect(sourceResponse.ok(), await sourceResponse.text()).toBe(true);
    const source = await sourceResponse.json();
    await expect(dialog.getByText("Syncing", { exact: true })).toBeVisible();

    // Only the daemon side is simulated; browser reads and writes use the real API.
    const heartbeat = await api.authedFetch("/api/daemon/heartbeat", {
      headers: { "X-Client-Capabilities": "agent-plugin-bundles-v1" },
      method: "POST", body: JSON.stringify({ runtime_id: runtime.id, supports_batch_import: true }),
    });
    expect(heartbeat.ok, await heartbeat.clone().text()).toBe(true);
    const pending = (await heartbeat.json()).pending_marketplace;
    expect(pending.marketplace_id).toBe(source.id);
    const file = (path: string, content: string) => ({ path, content: Buffer.from(content).toString("base64") });
    const report = await api.authedFetch("/api/daemon/runtimes/" + runtime.id + "/agent-marketplaces/" + source.id + "/sync/" + pending.id, {
      method: "POST",
      body: JSON.stringify({ catalog: {
        name: "e2e-plugins", display_name: "E2E plugins", revision: "a".repeat(40),
        plugins: [{ name: "example", files: [
          file(".codex-plugin/plugin.json", JSON.stringify({ name: "example", version: "1.0", interface: { displayName: "Example plugin" } })),
          file("skills/check/SKILL.md", "---\nname: check\ndescription: E2E fixture\n---\nCheck the fixture."),
        ] }],
      } }),
    });
    expect(report.ok, await report.clone().text()).toBe(true);
    await expect(dialog.getByText("Connected", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    const checkbox = page.getByRole("checkbox", { name: "Example plugin", exact: true });
    await checkbox.check();
    const saved = page.waitForResponse((response) =>
      response.request().method() === "PUT" && response.url().endsWith("/marketplace-plugins"));
    await page.getByRole("button", { name: "Save selection", exact: true }).click();
    expect((await saved).ok()).toBe(true);
    const settingsResponse = await api.authedFetch("/api/agents/" + agent.id + "/marketplace-plugins");
    expect(await settingsResponse.json()).toMatchObject({
      configured: true, plugins: [{ marketplace_id: source.id, plugin_name: "example" }],
    });
    await page.reload();
    await page.getByRole("tab", { name: "Capabilities", exact: true }).click();
    await page.getByRole("tab", { name: "Plugins", exact: true }).click();
    await expect(page.getByRole("checkbox", { name: "Example plugin", exact: true })).toBeChecked();
    await expect(page.getByRole("button", { name: /install/i })).toHaveCount(0);
    const screenshot = testInfo.outputPath("agent-plugins.png");
    await page.screenshot({ path: screenshot, fullPage: true });
    await testInfo.attach("Agent Plugins tab", { path: screenshot, contentType: "image/png" });
  } finally {
    await api.cleanup();
    if (workspaceCreated) await api.deleteWorkspace();
  }
});
