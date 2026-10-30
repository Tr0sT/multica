# Agent marketplace plugins

The agent's **Capabilities → Plugins** tab selects native Codex packages for its
issues and chats. This is separate from **Settings → Plugins**, which manages
Multica Plugin V1 integrations.

## Connect and select

1. Update the backend and the Multica daemon used to read the repository and run
   the agent. The daemon advertises `agent-plugin-bundles-v1`.
2. Open **Capabilities → Plugins → Marketplaces**. Choose an online runtime you
   own, enter an HTTPS or `ssh://` Git URL, and optionally a branch/tag.
3. The daemon reads committed Git objects using its existing Git access. It does
   not check out the repository, run its hooks, or install anything into Codex.
   The backend stores a validated catalog and immutable package versions.
4. Select plugins and save. The agent must use Codex CLI 0.165.0 or newer.
   Older or unknown versions fail before launching work with a managed selection.

Saving opts this agent into an explicit plugin set instead of its inherited
runtime plugin choices. Saving an empty set is supported. Existing workspace and
standalone runtime skills remain governed by the Skills tab.

The reader runtime is only the machine that can access the Git source. It does
not bind plugin execution to that machine. Agent execution uses the bound Codex
runtime and that runtime's own authorization; reader credentials are never sent
to another machine.

## Versions and task isolation

The first claim pins the selected immutable version IDs for the issue/chat and
agent. Continuations retain that snapshot even if the marketplace refreshes or
the agent's checkboxes change. Conversations already started before the first
opt-in retain their previous behavior.

The daemon places the full packages, including scripts, binary files, MCP
configuration and hooks, in the isolated task Codex home. It builds local
marketplace catalogs and calls the native plugin manager against that home.
Explicit launch overrides make the task selection authoritative over inherited
runtime plugin configuration, including account installations through
`plugins._default.enabled = false`. The CLI must support this native activation
setting ([native configuration types](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/config/src/types.rs)).
The user's plugin configuration and cache are not written.

These files follow the existing execution-environment retention and cleanup
policy, so a continuation can reuse them. Their presence on disk does not enable
them in the user's Codex or other agents. Provider authentication, native hook
trust, and administrator restrictions still apply; this feature does not bypass
them.

Dependencies are not selected automatically. For example, if a Studio Nord UI
plugin calls skills from `unity@unity-agent-plugin`, connect the Unity marketplace
and select that package too.

## Supported catalog and package shapes

- A Git source must contain `.agents/plugins/marketplace.json`.
- Entries point to package directories in the same repository with a `./` path.
  A package at the repository root (`./`), as used by the Unity marketplace, is
  supported. External URL/npm/git-subdir entry sources are rejected explicitly.
- Packages use `.codex-plugin/plugin.json` or a portable root `plugin.json`.
  Ambiguous dual manifests are rejected. Unknown manifest fields are retained in
  the package files.
- `NOT_AVAILABLE` entries are excluded. Refreshing to an empty catalog is valid;
  it does not remove immutable versions used by existing conversations.
- Symlinks, hard links, traversal, duplicate case-insensitive paths and
  non-regular archive entries are rejected. Executable flags and binary bytes
  are preserved.
- Limits: 32 selectable plugins, 2,048 files per package, 8 MiB per file, 16 MiB
  per package, 32 MiB of package content per catalog/selection, and a 48 MiB
  synchronization report.

Only the source creator or a workspace owner/admin can refresh or disconnect a
source. Reading a source using Git credentials additionally requires owning the
chosen runtime. Agent selections use the existing agent-management permissions.
Disconnect is refused while active agents still select packages from that source.
Pinned versions remain available until workspace cleanup.

## API

- `GET/POST /api/agent-marketplaces`
- `POST /api/agent-marketplaces/{id}/refresh`
- `DELETE /api/agent-marketplaces/{id}`
- `GET/PUT /api/agents/{id}/marketplace-plugins`

Synchronization travels through the existing daemon heartbeat/result channel.
It is asynchronous, workspace-scoped, idempotent per synchronization ID, and
expires after five minutes without a result. Reports from a different runtime,
workspace or synchronization generation are rejected.

## Verification

Default tests use temporary Git repositories and a test-created plugin-manager
executable. They cover path validation, binary/executable preservation, isolated
Codex homes, configuration precedence, snapshot stability, permissions, malformed
API responses and checkbox/source workflows. The browser test uses the real
backend and simulates only the daemon's Git synchronization report. Real Codex
model/account smoke tests remain separately authorized under the repository's
testing rules.
