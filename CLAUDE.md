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

## Web UI (`web/`, SvelteKit 3 + Svelte 5 runes)
- Kit 3: config lives in `sveltekit({...})` in `vite.config.ts` (no svelte.config.js);
  `$lib` is gone — use `#lib/...` (package.json "imports") with explicit `.ts` extensions.
- `npm run check` must be clean; `npm run build` writes `web/build` (embedded by `web/embed.go`).
- Dev: `npm run dev` proxies `/api` to `MCSMAN_DEV_API` (default http://127.0.0.1:8091).

## Operational notes
- Paper binds RCON on all interfaces (no separate bind option); firewall the RCON ports.
- External APIs: Paper Fill v3 (`fill.papermc.io/v3`; v2 is 410 Gone), Adoptium v3 for JREs.

## Workflow
Phase-by-phase; after each phase demo + wait for user confirmation, then one commit.
