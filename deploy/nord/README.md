# Nord fork deployment

`upgrade_multica.sh` replaces `/Users/admin/upgrade_multica.sh`. The existing
`local.multica.upgrade` LaunchAgent runs it through `multicaUpgrade.app` at 04:20
in the host's local time. Keep the existing launch schedule and server checkout.

The updater checks out `Tr0sT/multica` `main` in `~/.multica/fork-source`, builds
backend, web, and the native macOS CLI from the same commit, and overlays
`docker-compose.fork.yml` onto the existing server compose file. Its `.env`,
ports, `multica_pgdata`, and `multica_backend_uploads` volumes remain in place.
The native CLI uses a source-build version and starts with `--no-auto-update`;
an official release must not overwrite the fork. The separate Codex update remains.

Build failures leave the running installation alone. Busy daemons postpone the
switch. Before switching, the updater saves a PostgreSQL custom dump, uploads,
configuration, the old CLI, and rollback image references under
`~/.multica/backups/fork-<UTC timestamp>/`, readable only by the owner. It checks
the dump, HTTP health, CLI revision, and daemon status. An activation failure
restores the previous images and CLI; it never restores or downgrades the database
automatically. Review migration compatibility before publishing future `main`
updates. Backups and rollback images are retained; inspect them before removing any.

Mattermost requires `MULTICA_MATTERMOST_SECRET_KEY` and exact HTTPS URLs in
`MULTICA_MATTERMOST_ALLOWED_SERVERS` in the existing server `.env`. The overlay
forwards these variables even when the original compose file predates Mattermost.
Do not commit the `.env`, encryption keys, bot tokens, or backups.
