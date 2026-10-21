import { expect, test } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

test("admins inspect another admin's Mattermost chat without changing it", async ({ page }, testInfo) => {
  const owner = new TestApiClient();
  const peer = new TestApiClient();
  const database = new pg.Client(process.env.DATABASE_URL);
  await database.connect();
  const suffix = `${Date.now()}-${process.pid}`;
  let agentId: string | undefined;
  let sessionId: string | undefined;
  let installationId: string | undefined;
  let peerMembership: string | undefined;
  try {
    const ownerLogin = await owner.login(`chat-owner-${suffix}@multica.ai`, "Chat Owner");
    const peerLogin = await peer.login(`chat-peer-${suffix}@multica.ai`, "Alice Admin");
    const workspace = await owner.ensureWorkspace("Chat inspection", `chat-inspection-${suffix}`);
    await owner.markUserOnboarded();
    await peer.markUserOnboarded();
    const membership = await database.query(
      "INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin') RETURNING id",
      [workspace.id, peerLogin.user.id],
    );
    peerMembership = membership.rows[0].id;
    const agent = await database.query(
      `INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, visibility, permission_mode, max_concurrent_tasks, owner_id)
       VALUES ($1, 'Mattermost Bot', '', 'cloud', '{}', 'private', 'private', 1, $2) RETURNING id`,
      [workspace.id, peerLogin.user.id],
    );
    agentId = agent.rows[0].id;
    const session = await database.query(
      `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, explicitly_created_at)
       VALUES ($1, $2, $3, 'Investigate game behavior', now()) RETURNING id`,
      [workspace.id, agentId, peerLogin.user.id],
    );
    sessionId = session.rows[0].id;
    await database.query(
      "INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1, 'assistant', 'Verified response')",
      [sessionId],
    );
    const installation = await database.query(
      `INSERT INTO channel_installation (workspace_id, agent_id, installer_user_id, channel_type, status)
       VALUES ($1, $2, $3, 'mattermost', 'revoked') RETURNING id`,
      [workspace.id, agentId, peerLogin.user.id],
    );
    installationId = installation.rows[0].id;
    await database.query(
      `INSERT INTO channel_chat_session_binding (chat_session_id, installation_id, channel_type, channel_chat_id, chat_type, config)
       VALUES ($1, $2, 'mattermost', 'fake-direct-channel', 'p2p', '{"channel_id":"fake-direct-channel","sender_username":"alice.example"}')`,
      [sessionId, installationId],
    );
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, ownerLogin.token);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/${workspace.slug}/chat`);
    const filter = page.getByRole("combobox", { name: "Chat owner" });
    await expect(filter).toBeVisible();
    await expect(page.getByText(/@alice\.example/)).toHaveCount(0);
    await filter.click();
    await page.getByRole("option", { name: "All members", exact: true }).click();
    await page.getByText(/@alice\.example/).first().click();
    await expect(page.getByText("Verified response", { exact: true })).toBeVisible();
    await expect(page.getByText("You are viewing another member’s chat. Only its owner can send messages or change it.")).toBeVisible();
    await expect(page.locator('[contenteditable="true"]')).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Rename chat" })).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath("admin-chat.png"), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByText("Verified response", { exact: true })).toBeVisible();
    await expect(page.locator('[contenteditable="true"]')).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath("admin-chat-compact.png"), fullPage: true });
    await page.getByRole("button", { name: "Chat", exact: true }).click();
    await expect(filter).toBeVisible();
    await filter.click();
    await page.getByRole("option", { name: "Alice Admin", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`creator=${peerLogin.user.id}`));
    await expect(page.getByText(/@alice\.example/)).toBeVisible();
    await filter.click();
    await page.getByRole("option", { name: "My chats", exact: true }).click();
    await expect(page.getByText(/@alice\.example/)).toHaveCount(0);
  } finally {
    if (sessionId) {
      await database.query("DELETE FROM channel_chat_session_binding WHERE chat_session_id=$1", [sessionId]);
      await database.query("DELETE FROM chat_message WHERE chat_session_id=$1", [sessionId]);
      await database.query("DELETE FROM chat_session WHERE id=$1", [sessionId]);
    }
    if (installationId) await database.query("DELETE FROM channel_installation WHERE id=$1", [installationId]);
    if (agentId) await database.query("DELETE FROM agent WHERE id=$1", [agentId]);
    if (peerMembership) await database.query("DELETE FROM member WHERE id=$1", [peerMembership]);
    await database.end();
    await owner.cleanup();
    await peer.cleanup();
  }
});
