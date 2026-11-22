import { expect, test } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

// The app, authentication, membership and agent are real isolated fixtures.
// Only the external bot connection is simulated: no live Mattermost server,
// credential or agent execution is involved.
test("Mattermost keeps its full connection form in the shared Channels dialog", async ({ page }, testInfo) => {
  const owner = new TestApiClient();
  const database = new pg.Client(process.env.DATABASE_URL);
  await database.connect();
  const suffix = `${Date.now()}-${process.pid}`;
  let agentId: string | undefined;
  try {
    const login = await owner.login(`channel-owner-${suffix}@multica.ai`, "Channel Owner");
    const workspace = await owner.ensureWorkspace("Channel regression", `channel-regression-${suffix}`);
    await owner.markUserOnboarded();
    const agent = await database.query(
      `INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, visibility, permission_mode, max_concurrent_tasks, owner_id)
       VALUES ($1, 'Channel Test Agent', '', 'cloud', '{}', 'private', 'private', 1, $2) RETURNING id`,
      [workspace.id, login.user.id],
    );
    agentId = agent.rows[0].id;
    let installation: Record<string, unknown> | undefined;
    let attempts = 0;
    await page.route("**/api/workspaces/*/mattermost/installations", async (route) => {
      await route.fulfill({ json: { configured: true, install_supported: true, installations: installation ? [installation] : [] } });
    });
    await page.route("**/api/workspaces/*/mattermost/install?*", async (route) => {
      expect(route.request().method()).toBe("POST");
      expect(route.request().postDataJSON()).toMatchObject({
        server_url: "https://chat.example.test",
        access_token: "synthetic-not-a-real-token",
        access_mode: "members",
        attachment_mode: "links",
        context: { mode: "message" },
      });
      attempts++;
      if (attempts === 1) {
        await route.fulfill({ status: 502, json: { error: "Synthetic bot registration failure" } });
        return;
      }
      installation = {
        id: "00000000-0000-4000-8000-000000000019",
        workspace_id: workspace.id,
        agent_id: agentId,
        installer_user_id: login.user.id,
        status: "active",
        server_url: "https://chat.example.test",
        bot_username: "synthetic_bot",
        bot_user_id: "synthetic-bot-id",
        access_mode: "members",
        attachment_mode: "links",
        context: { mode: "message", lookback_hours: 0 },
        installed_at: "2026-11-12T00:00:00Z",
      };
      await route.fulfill({ json: installation });
    });
    await page.addInitScript((token) => localStorage.setItem("multica_token", token), login.token);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/${workspace.slug}/agents/${agentId}?view=channels`);
    await page.getByRole("button", { name: "Add messaging channel", exact: true }).first().click();
    await page.getByRole("button", { name: "Mattermost", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await expect(page.getByRole("combobox", { name: "History sent to agent" })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Attachments sent to agent" })).toBeVisible();
    await page.getByTestId("mattermost-server-url").fill("https://chat.example.test");
    await page.getByTestId("mattermost-access-token").fill("synthetic-not-a-real-token");
    await page.getByTestId("mattermost-connect-submit").click();
    await expect.poll(() => attempts).toBe(1);
    await expect(page.getByTestId("mattermost-connect-submit")).toBeEnabled();
    await expect(page.getByTestId("mattermost-server-url")).toHaveValue("https://chat.example.test");
    await expect(page.getByTestId("mattermost-access-token")).toHaveValue("synthetic-not-a-real-token");
    await page.screenshot({ path: testInfo.outputPath("mattermost-connect-desktop.png"), animations: "disabled" });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("mattermost-connect-compact.png"), animations: "disabled" });
    await page.getByTestId("mattermost-connect-submit").click();
    await expect.poll(() => attempts).toBe(2);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByText("@synthetic_bot", { exact: true })).toBeVisible();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.screenshot({ path: testInfo.outputPath("mattermost-connected.png"), animations: "disabled" });
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    if (agentId) await database.query("DELETE FROM agent WHERE id=$1", [agentId]);
    await database.end();
    await owner.cleanup();
  }
});
