// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ChatSession } from "@multica/core/types";
import { chatChannelLabel } from "./channel-label";

describe("chatChannelLabel", () => {
  it("identifies direct and group chats without treating a group as a person", () => {
    const session = { channel_source: { channel_type: "mattermost", installation_id: "installation", route_revision: 1 } } as ChatSession;
    expect(chatChannelLabel(session)).toBe("Mattermost");
    expect(chatChannelLabel({ ...session, channel_source: { ...session.channel_source!, sender_username: "alice.example" } })).toBe("@alice.example");
    expect(chatChannelLabel({ ...session, channel_source: { ...session.channel_source!, channel_name: "Hustle Castle" } })).toBe("Hustle Castle");
    expect(chatChannelLabel({ ...session, channel_source: undefined })).toBeNull();
  });
});
