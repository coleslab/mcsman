# mcsman

Self-hosted Paper server manager. Spec: `docs/SPEC.md`. Build plan and spec deviations:
see "Decisions" below.

## Commands
- `make build` — static binaries (`CGO_ENABLED=0`) into `bin/`, builds `web/` first if present
- `make test` — `go test -race ./...`
- `make vet`
- `make systemd` — render unit file with the current user/group

## Conventions
- All user-influenced file paths go through `fsutil.SafePath`. No exceptions.
- All downloads go through `fsutil.DownloadVerified` (temp file → SHA-256 → constant-time compare → rename).
- Never `sh -c`; never put secrets (RCON password, webhook URLs, JWT secret) in argv or logs.
- Secrets at rest: AES-GCM via `internal/crypto`, key = HKDF-SHA256(jwt_secret, label).
- External API base URLs are injectable so tests use `httptest` fakes; process tests use `internal/testutil/fakemc`.

## Decisions (deviations from spec)
- Daemon and Java run as the invoking user (no `minecraft` user).
- Discord webhooks stored in DB (`webhooks` table, URL encrypted, label "webhook"); config/env only seed on first start.
- Invite tokens looked up by `WHERE token_hash = ?` (SHA-256 of 256-bit token).
- `POST /api/users/me/password`; WebSocket auth via single-use ticket (`POST /api/ws-ticket`).
- `trusted_proxies` config for client IP behind reverse proxy.
- Server creation requires `accept_eula: true`.
- Socket clients are admin (socket is 0660); `mcsctl init` seeds first admin only when no users exist.
- Server membership (`server_members`) = whitelisted. Players see only member servers
  (`memberInstance`; non-members get 404, not 403): overview, players, plugin names/versions,
  event history without `detail`. Backups, world imports, plugin search are admin-only.
- Whitelist queue rows carry `action` add|remove; queueing supersedes any pending row for the
  same player/server. MC names are validated before reaching RCON. Removing a member skips
  the un-whitelist if another member shares the MC name.
- Existing users accept server invites via `POST /api/invites/{token}/accept`; site-only
  invites → 409. Changing `mc_username` moves whitelist entries on the user's servers.

## Web UI (`web/`, SvelteKit 3 + Svelte 5 runes)
- Kit 3: config lives in `sveltekit({...})` in `vite.config.ts` (no svelte.config.js);
  `$lib` is gone — use `#lib/...` (package.json "imports") with explicit `.ts` extensions.
- `npm run check` must be clean; `npm run build` writes `web/build` (embedded by `web/embed.go`).
- Dev: `npm run dev` proxies `/api` to `MCSMAN_DEV_API` (default http://127.0.0.1:8091).

## Operational notes
- Paper binds RCON on all interfaces (no separate bind option); firewall the RCON ports.
- External APIs: Paper Fill v3 (`fill.papermc.io/v3`; v2 is 410 Gone), Adoptium v3 for JREs.
- Modrinth publishes sha512/sha1 only (no sha256): plugin downloads verify against sha512;
  the `plugins.sha256` column is our own digest of the installed file. Hangar gives sha256;
  Hangar releases with only an `externalUrl` are refused (unverifiable).
- Upgrades are stage-then-commit: backup, download JAR/Java/plugins, then swap all at once.
  Verify waits up to 15 min for startup (Paper pauses 30s before world-folder migrations).
- Server DataVersion comes from `version.json` (`world_version`) at the root of server.jar.
- Paper 26.x keeps dimensions in `world/dimensions/`; older versions use `world_nether`/
  `world_the_end` siblings. Import maps sibling folders; Paper migrates them on start.
- Webhooks can be scoped to servers (`webhook_servers` + explicit `all_servers` flag, so
  deleting a scoped webhook's last server makes it inactive rather than global).
- Large downloads (backups, world export) accept `?ticket=` so browsers stream to disk.

## Scheduling & packaging
- Schedules (`internal/cronspec`): Go duration ("6h", min 5m), 5-field cron, or "off".
  Per-server `backup_schedule`/`backup_keep`/`restart_schedule` override config defaults
  (NULL = default). The scheduler reloads a server on create/update/delete.
- Stopped servers are auto-backed-up only if they stopped/crashed since the last backup.
- `autostart` servers start sequentially after the daemon boots.
- Packaging: `packaging/arch/PKGBUILD` (`MCSMAN_SRC=$(realpath ../..) makepkg` for
  uncommitted trees) installs `mcsman@.service` (run as `mcsman@<user>`); `KillMode=mixed`
  is required so systemd doesn't SIGTERM Java before the graceful RCON stop.
- Version: `internal/buildinfo.Version`, stamped by `make build VERSION=...`; the PKGBUILD
  derives `0.1.0.r<commits>.g<sha>` from git. `GET /api/system` (socket only) reports it
  plus effective config for `mcsctl version`/`doctor`.
- `packaging/mcsman-setup` (installed to /usr/bin) does first-time setup; the package's
  post_install runs `mcsman-setup secret` to generate the JWT secret if unset.
  `packaging/arch/update.sh` updates an installed package with snapshot + `--rollback`.

## Workflow
Phase-by-phase; after each phase demo + wait for user confirmation, then one commit.
