import type { ChatSession } from "@multica/core/types";

export function chatChannelLabel(session: ChatSession): string | null {
  const source = session.channel_source;
  if (source?.channel_type !== "mattermost") return null;
  if (source.sender_username) return `@${source.sender_username}`;
  if (source.channel_name) return source.channel_name;
  return "Mattermost";
}
