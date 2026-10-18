# mcsman — Implementation Specification

## Overview

`mcsman` is a self-hosted Minecraft server management service for a hobby friend group.
It runs on a single Arch Linux machine and exposes a mobile-first web UI, an admin CLI,
a public read-only status page, and Discord webhook notifications.

**Server type supported:** Paper only (Fabric/Forge in backlog).  
**Deployment:** Single static binary (`mcsman`) with the SvelteKit frontend embedded via `go:embed`. No Docker. Managed by systemd.  
**Companion CLI:** `mcsctl` — admin only, communicates with the daemon via Unix socket.  
**Build:** `CGO_ENABLED=0 go build ./cmd/mcsman` — fully static, no external C dependencies.

---

## Tech Stack

| Layer           | Choice                        | Notes                                              |
|-----------------|-------------------------------|----------------------------------------------------|
| Daemon          | Go 1.23+                      | Single binary output                               |
| HTTP router     | `chi` v5                      | Lightweight, middleware-friendly                   |
| WebSockets      | `gorilla/websocket`           | Console streaming, live metrics, events            |
| Database        | SQLite via `modernc.org/sqlite` | Pure Go, no CGO, WAL mode enabled               |
| Migrations      | `golang-migrate`              | Embedded migration files                           |
| PTY             | `creack/pty`                  | Attach to Java server process                      |
| RCON            | `gorcon/rcon`                 | Send commands, pre-backup flush                    |
| Config          | YAML via `gopkg.in/yaml.v3`   | Single file, `0600` permissions enforced on start  |
| Web UI          | SvelteKit                     | Embedded in binary via `go:embed`                  |
| Terminal widget | xterm.js                      | In-browser console                                 |
| Systemd         | Native unit file              | `mcsman.service`                                   |
| Java downloads  | Adoptium Temurin API          | Per-server Java version pinning                    |
| Plugin sources  | Modrinth API, Hangar API      | Checksum-verified downloads                        |

---

## Directory Structure

```
mcsman/
├── cmd/
│   ├── mcsman/
│   │   └── main.go              # Daemon entrypoint
│   └── mcsctl/
│       └── main.go              # CLI entrypoint
│
├── internal/
│   ├── api/
│   │   ├── router.go            # chi router, middleware chain
│   │   ├── middleware.go        # auth, rate-limit, request-id
│   │   ├── handler_auth.go      # login, logout, refresh
│   │   ├── handler_servers.go   # CRUD + actions on server instances
│   │   ├── handler_backups.go   # list, trigger, restore
│   │   ├── handler_plugins.go   # search, install, update, remove
│   │   ├── handler_versions.go  # list available, upgrade
│   │   ├── handler_worlds.go    # import, export
│   │   ├── handler_users.go     # list, remove, change role
│   │   ├── handler_invites.go   # create, list pending, revoke
│   │   ├── handler_status.go    # public status endpoint (no auth)
│   │   └── ws/
│   │       ├── hub.go           # WebSocket hub, broadcast
│   │       ├── console.go       # PTY stream → ws
│   │       └── events.go        # server events → ws
│   │
│   ├── auth/
│   │   ├── jwt.go               # issue/validate access + refresh tokens
│   │   ├── password.go          # bcrypt helpers
│   │   ├── invite.go            # generate, hash, validate invite tokens
│   │   └── session.go           # session table management
│   │
│   ├── backup/
│   │   ├── manager.go           # orchestrate backup lifecycle
│   │   ├── archive.go           # tar.gz creation + SHA-256 manifest
│   │   └── retention.go         # prune old backups by count
│   │
│   ├── config/
│   │   └── config.go            # load/validate YAML config, enforce 0600
│   │
│   ├── console/
│   │   ├── pty.go               # spawn server process with PTY
│   │   └── rcon.go              # RCON client wrapper
│   │
│   ├── db/
│   │   ├── db.go                # open SQLite, WAL mode, foreign keys
│   │   ├── migrations/          # numbered .sql migration files
│   │   └── queries/             # typed query functions
│   │
│   ├── discord/
│   │   └── webhook.go           # POST embeds to Discord webhook URLs
│   │
│   ├── java/
│   │   └── manager.go           # Adoptium API, download + verify JVM
│   │
│   ├── metrics/
│   │   └── collector.go         # poll TPS/RAM/players via RCON, broadcast
│   │
│   ├── plugin/
│   │   ├── modrinth.go          # Modrinth API client
│   │   ├── hangar.go            # Hangar API client
│   │   ├── installer.go         # download → verify → move to plugins/
│   │   └── compat.go            # check plugin versions against target MC
│   │
│   ├── scheduler/
│   │   └── scheduler.go         # cron-style task runner (robfig/cron)
│   │
│   ├── server/
│   │   ├── manager.go           # instance registry, lifecycle coordinator
│   │   ├── instance.go          # single server: start/stop/restart/crash
│   │   └── ports.go             # port conflict detection on start
│   │
│   ├── version/
│   │   ├── manifest.go          # fetch + cache Paper version manifest
│   │   ├── downloader.go        # download JAR, verify SHA-256
│   │   └── upgrade.go           # pre-flight check + upgrade orchestration
│   │
│   └── world/
│       ├── manager.go           # import/export world zips
│       └── leveldat.go          # parse level.dat for world version (NBT)
│
├── web/                         # SvelteKit frontend (go:embed target)
│   ├── src/
│   │   ├── lib/
│   │   │   ├── api.ts           # typed fetch wrapper
│   │   │   ├── ws.ts            # WebSocket store
│   │   │   └── xterm.svelte     # xterm.js console component
│   │   └── routes/
│   │       ├── +layout.svelte
│   │       ├── /                # redirect → /servers or /status
│   │       ├── status/          # public status page (no auth)
│   │       ├── login/
│   │       ├── join/[token]/    # invite redemption
│   │       ├── servers/
│   │       │   ├── +page        # server list
│   │       │   └── [id]/
│   │       │       ├── console/ # xterm.js console
│   │       │       ├── plugins/
│   │       │       ├── backups/
│   │       │       └── worlds/
│   │       ├── users/
│   │       └── settings/        # webhooks, config
│   └── static/
│
├── systemd/
│   └── mcsman.service
│
├── mcsman.yaml                  # default config location
├── Makefile
└── go.mod
```

---

## Configuration File

Location: `/etc/mcsman/mcsman.yaml` (or `./mcsman.yaml` for dev).  
Permissions: daemon refuses to start if file is not `0600`.

```yaml
daemon:
  socket: /run/mcsman/mcsman.sock   # Unix socket for CLI
  data_dir: /var/lib/mcsman         # servers, backups, JARs, worlds
  log_level: info                   # debug | info | warn | error

http:
  listen: 0.0.0.0:8080
  base_url: https://mc.example.com  # used in invite links
  tls:
    cert: /etc/mcsman/cert.pem      # leave empty to run plain HTTP
    key:  /etc/mcsman/key.pem       # (use Caddy/nginx TLS termination instead)

auth:
  jwt_secret: ""                    # REQUIRED — min 32 chars, set via env: MCSMAN_JWT_SECRET
  access_token_ttl:  15m
  refresh_token_ttl: 7d
  invite_default_ttl: 48h

backup:
  default_keep: 24                  # keep last N backups per server
  default_interval: 1h              # cron schedule for auto-backup

discord:
  webhooks:
    - name: server-status
      url: ""                       # set via env: MCSMAN_DISCORD_WEBHOOK_0
      events:
        server_online:    true
        server_offline:   true
        server_crash:     true
        restart_warning:  true
        backup_failed:    true
        upgrade_complete: true
        player_join:      false
        player_leave:     false
        backup_complete:  false

java:
  cache_dir: /var/lib/mcsman/java   # downloaded JVM installs

servers: []                         # managed at runtime via DB, not config
```

**Secret handling:** `jwt_secret` and Discord webhook URLs must be provided via environment
variables (`MCSMAN_JWT_SECRET`, `MCSMAN_DISCORD_WEBHOOK_0`, etc.). The config file must
never contain secrets in plaintext in production.

---

## SQLite Schema

Driver: `modernc.org/sqlite` (pure Go, no CGO required).  
Import: `import _ "modernc.org/sqlite"`  
Enable WAL mode and foreign keys on every connection open:
```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

### users
```sql
CREATE TABLE users (
  id            TEXT PRIMARY KEY,          -- UUID v4
  username      TEXT NOT NULL UNIQUE,      -- display name
  mc_username   TEXT,                      -- Minecraft username (nullable)
  password_hash TEXT NOT NULL,             -- bcrypt cost 12
  role          TEXT NOT NULL DEFAULT 'player', -- 'admin' | 'player'
  created_at    INTEGER NOT NULL,          -- Unix timestamp
  created_by    TEXT REFERENCES users(id), -- null for first admin
  last_login_at INTEGER
);
```

### sessions
```sql
CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,          -- UUID v4
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  refresh_token_hash TEXT NOT NULL,        -- SHA-256 of refresh token
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  revoked_at    INTEGER                    -- null = active
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
```

### invites
```sql
CREATE TABLE invites (
  id                   TEXT PRIMARY KEY,   -- UUID v4
  token_hash           TEXT NOT NULL UNIQUE, -- SHA-256 of raw token
  created_by           TEXT NOT NULL REFERENCES users(id),
  created_at           INTEGER NOT NULL,
  expires_at           INTEGER NOT NULL,
  used_at              INTEGER,            -- null = pending
  used_by              TEXT REFERENCES users(id),
  whitelist_server_id  TEXT REFERENCES servers(id) -- null = no whitelist action
);
```

### servers
```sql
CREATE TABLE servers (
  id                 TEXT PRIMARY KEY,     -- UUID v4
  name               TEXT NOT NULL UNIQUE, -- human name e.g. "survival"
  directory          TEXT NOT NULL UNIQUE, -- absolute path under data_dir
  mc_version         TEXT NOT NULL,        -- e.g. "1.21.1"
  java_version       TEXT NOT NULL,        -- e.g. "21"
  port               INTEGER NOT NULL UNIQUE,
  rcon_port          INTEGER NOT NULL UNIQUE,
  rcon_password      TEXT NOT NULL,        -- stored encrypted at rest (AES-GCM, key from jwt_secret)
  max_memory_mb      INTEGER NOT NULL DEFAULT 2048,
  auto_restart       INTEGER NOT NULL DEFAULT 1, -- bool
  created_at         INTEGER NOT NULL,
  status             TEXT NOT NULL DEFAULT 'stopped', -- 'running'|'stopped'|'starting'|'stopping'|'crashed'
  connection_address TEXT                  -- public address shown to players e.g. "survival.playit.gg"
                                           -- nullable: not set until admin configures tunnel/domain
);
```

### server_events
```sql
CREATE TABLE server_events (
  id         TEXT PRIMARY KEY,
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,               -- 'start'|'stop'|'crash'|'restart'|'backup'|'upgrade'
  detail     TEXT,                        -- JSON blob for extra context
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_events_server ON server_events(server_id, created_at DESC);
```

### backups
```sql
CREATE TABLE backups (
  id           TEXT PRIMARY KEY,
  server_id    TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  filename     TEXT NOT NULL,             -- relative to server backup dir
  size_bytes   INTEGER NOT NULL,
  sha256       TEXT NOT NULL,
  tag          TEXT,                      -- e.g. "pre-upgrade-1.20.4", "auto", "manual"
  created_at   INTEGER NOT NULL,
  created_by   TEXT REFERENCES users(id) -- null = scheduled
);
CREATE INDEX idx_backups_server ON backups(server_id, created_at DESC);
```

### plugins
```sql
CREATE TABLE plugins (
  id            TEXT PRIMARY KEY,
  server_id     TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  version       TEXT NOT NULL,
  source        TEXT NOT NULL,            -- 'modrinth'|'hangar'|'manual'
  source_id     TEXT,                     -- registry project ID if known
  filename      TEXT NOT NULL,
  sha256        TEXT NOT NULL,
  installed_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE(server_id, filename)
);
```

### world_imports
```sql
CREATE TABLE world_imports (
  id                       TEXT PRIMARY KEY,
  server_id                TEXT NOT NULL REFERENCES servers(id),
  imported_at              INTEGER NOT NULL,
  imported_by              TEXT NOT NULL REFERENCES users(id),
  source_name              TEXT NOT NULL,   -- human label
  source_mc_version        TEXT NOT NULL,   -- from level.dat
  pre_import_backup_id     TEXT REFERENCES backups(id)
);
```

### whitelist_queue
```sql
CREATE TABLE whitelist_queue (
  id          TEXT PRIMARY KEY,
  server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  mc_username TEXT NOT NULL,
  queued_at   INTEGER NOT NULL,
  applied_at  INTEGER                       -- null = pending
);
```

---

## REST API

All endpoints except those marked `[public]` require a valid JWT `Authorization: Bearer <token>` header.  
All endpoints except those marked `[admin]` are accessible to both roles.  
Request/response bodies are JSON. Timestamps are Unix integers.

### Auth
```
POST   /api/auth/login              [public]   { username, password } → { access_token, refresh_token }
POST   /api/auth/refresh            [public]   { refresh_token } → { access_token }
POST   /api/auth/logout                        revokes current session
GET    /api/auth/me                            → current user object
```

### Invite tokens
```
POST   /api/invites                 [admin]    { expires_in, whitelist_server_id? } → { token, url, expires_at }
GET    /api/invites                 [admin]    → list of pending invites
DELETE /api/invites/:id             [admin]    revoke a pending invite
POST   /api/invites/:token/redeem   [public]   { username, password, mc_username? }
                                               → { access_token, refresh_token, whitelisted_server? }
                                               whitelisted_server: { name, connection_address } | null
```

### Public status
```
GET    /api/status                  [public]   → { servers: [{ name, status, player_count, version, connection_address }] }
```

### Servers
```
GET    /api/servers                 [admin]    → list of server objects
POST   /api/servers                 [admin]    create new server instance
GET    /api/servers/:id                        → server object + live metrics (includes connection_address)
PATCH  /api/servers/:id             [admin]    update config (name, memory, connection_address, etc.)
DELETE /api/servers/:id             [admin]    delete server + data (requires confirmation token)

POST   /api/servers/:id/start       [admin]
POST   /api/servers/:id/stop        [admin]    { graceful: true, delay_seconds: 60 }
POST   /api/servers/:id/restart     [admin]    { delay_seconds: 60 }
POST   /api/servers/:id/command     [admin]    { command } → sends via RCON
GET    /api/servers/:id/players                → list of online players
```

### Console (WebSocket)
```
WS     /api/servers/:id/console     [admin]    bidirectional PTY stream
```

### Versions
```
GET    /api/versions/paper                     → cached Paper manifest (versions list)
POST   /api/servers/:id/upgrade     [admin]    { target_version } → preflight report
POST   /api/servers/:id/upgrade/confirm [admin] { target_version } → starts upgrade job
GET    /api/servers/:id/upgrade/status [admin] → current upgrade job progress
```

### Plugins
```
GET    /api/servers/:id/plugins                → installed plugins list
DELETE /api/servers/:id/plugins/:plugin_id [admin]
GET    /api/plugins/search                     ?q=&mc_version= → Modrinth+Hangar results
POST   /api/servers/:id/plugins     [admin]    { source, source_id, version? } → install job
POST   /api/servers/:id/plugins/:plugin_id/update [admin]
```

### Backups
```
GET    /api/servers/:id/backups                → list with size, tag, date
POST   /api/servers/:id/backups     [admin]    { tag? } → trigger manual backup
POST   /api/servers/:id/backups/:backup_id/restore [admin] → restore job
DELETE /api/servers/:id/backups/:backup_id [admin]
GET    /api/servers/:id/backups/:backup_id/download [admin] → file download
```

### Worlds
```
POST   /api/servers/:id/worlds/import [admin]  multipart: world zip + { source_name }
GET    /api/servers/:id/worlds/export [admin]  → triggers zip + file download
GET    /api/servers/:id/worlds/imports         → import history
```

### Users
```
GET    /api/users                   [admin]    → all users
PATCH  /api/users/:id               [admin]    { role } — promote/demote
DELETE /api/users/:id               [admin]    remove user
GET    /api/users/me/password                  change own password: { current, new }
```

### Webhooks
```
GET    /api/webhooks                [admin]
POST   /api/webhooks                [admin]    { name, url, events: {} }
PATCH  /api/webhooks/:id            [admin]
DELETE /api/webhooks/:id            [admin]
POST   /api/webhooks/:id/test       [admin]    sends a test ping
```

---

## WebSocket Events (server → client)

Connection: `WS /api/servers/:id/console` (admin only, JWT in query param or initial message).

### Event envelope
```json
{
  "type": "event_type",
  "server_id": "uuid",
  "ts": 1729180000,
  "data": {}
}
```

### Event types
```
console_output      data: { line: string }
metrics             data: { tps: float, mspt: float, ram_used_mb: int, ram_max_mb: int, player_count: int }
server_status       data: { status: "running"|"stopped"|"starting"|"stopping"|"crashed" }
player_join         data: { mc_username: string }
player_leave        data: { mc_username: string }
backup_progress     data: { phase: "flush"|"archive"|"verify"|"done", bytes_written: int }
upgrade_progress    data: { phase: string, detail: string }
```

Metrics are pushed every 10 seconds while the server is running.  
Console output is streamed as it arrives from the PTY.

---

## Core Module Behaviour

### server/instance.go

- Spawns the Java process via `creack/pty` so stdin/stdout are captured
- Wraps process in a goroutine that watches for exit
- On unexpected exit: fires `server_crash` event, waits backoff (5s, 10s, 30s), restarts up to 3 times before marking `crashed` and alerting
- Graceful stop: RCON `say Server stopping in Xs`, countdown, then RCON `stop`, wait for process exit with 30s timeout, then SIGTERM, then SIGKILL
- Maintains an in-memory ring buffer of the last 1000 console lines for new WebSocket connections

### backup/manager.go

Pre-backup flush sequence (must follow this order):
1. RCON `save-all` — flush dirty chunks to disk
2. RCON `save-off` — disable auto-save
3. Create tar.gz of the world directory
4. Compute SHA-256 of the archive
5. RCON `save-on` — re-enable auto-save
6. Record backup in DB
7. Run retention: delete oldest backups beyond `keep` limit (DB record + file)

Never skip steps 2 and 5 — skipping them risks a corrupt backup.

### version/upgrade.go

Pre-flight sequence:
1. Fetch Paper manifest, confirm target version exists
2. For each installed plugin where `source != 'manual'`: query Modrinth/Hangar for a release tagged with the target MC version. Classify as green (found), amber (not found), gray (manual/unknown).
3. Check if target MC version requires a different Java major version. If yes, note it.
4. Return preflight report — do not touch any files yet.

On confirmed upgrade:
1. Stop the server (graceful, countdown broadcast)
2. Take a backup tagged `pre-upgrade-{current_version}`
3. Download + verify new Paper JAR
4. Download new Java version if needed
5. Update green-status plugins (download + verify each)
6. Swap JAR, update server record in DB
7. Start server
8. Tail logs for 60 seconds, collect plugin load errors
9. Report result via WebSocket and Discord webhook

### auth/invite.go

#### Redemption response behaviour

After successful redemption the API returns `whitelisted_server` if the invite had a
`whitelist_server_id`. The SvelteKit `join/[token]` route uses this to render a
confirmation screen:

**If `whitelisted_server.connection_address` is set:**
```
Welcome to [server name]!
You're all set. Connect in Minecraft using:

  survival.playit.gg        [ Copy ]

Your account: Steve
```

**If `connection_address` is null:**
```
Welcome to [server name]!
You're all set — ask the admin for the server address.

Your account: Steve
```

**If invite had no whitelist target (site-only):**
```
Welcome! Your account is ready.
Check the status page to see available servers.
```

The connection address is also always visible on the server detail page and the
public status page for any logged-in player or unauthenticated visitor — redemption
is just the first time they see it.

#### Token generation
```go
raw := make([]byte, 32)
_, err := io.ReadFull(rand.Reader, raw)
token := base64.RawURLEncoding.EncodeToString(raw)
hash := sha256.Sum256([]byte(token))
// store hash, return raw token once (never stored)
```

Token validation:
```go
hash := sha256.Sum256([]byte(submitted))
// retrieve stored hash by doing a table scan with constant-time compare
// never do: WHERE token_hash = ? (leaks timing on hash comparison)
// instead: fetch candidate rows by expiry window, then constant-time compare
subtle.ConstantTimeCompare(hash[:], storedHash[:])
```

Rate limit `/api/invites/:token/redeem`: 5 requests per IP per minute.

### world/manager.go

Import sequence:
1. Validate zip contains `level.dat` at root or one level deep
2. Parse `level.dat` (NBT) to extract `DataVersion` field
3. Compare against server's current MC version DataVersion — warn if world is newer
4. If server is running, refuse import (must be stopped)
5. Auto-backup current world tagged `pre-import`
6. Extract zip to server world directory (atomic: extract to temp dir, then rename)
7. Record import in `world_imports`

### discord/webhook.go

Embed colour codes by event type:
- Server online: `0x57F287` (green)
- Server offline clean: `0x99AAB5` (gray)
- Server crash: `0xED4245` (red)
- Restart warning: `0xFEE75C` (yellow)
- Backup failed: `0xED4245` (red)
- Upgrade complete: `0x5865F2` (blurple)

Always set a `username` field in the payload to the server name so messages are
clearly attributed in a multi-server setup.

Webhook URLs are read-only at send time — the handler must not log the URL in
plaintext at any log level.

---

## Security Requirements (non-negotiable)

### Path traversal prevention
Every file operation that accepts a user-supplied name (plugin filename, world zip,
backup restore path) must resolve the full path and assert it falls under the
expected base directory:

```go
func SafePath(base, userInput string) (string, error) {
    resolved := filepath.Clean(filepath.Join(base, userInput))
    if !strings.HasPrefix(resolved, filepath.Clean(base)+string(os.PathSeparator)) {
        return "", errors.New("path traversal attempt")
    }
    return resolved, nil
}
```

This function must be used everywhere. No exceptions.

### Process spawning
Never use `exec.Command("sh", "-c", userInput)`. Always use:
```go
exec.Command(binaryPath, arg1, arg2, ...)
```
RCON passwords and sensitive values must never appear in process arguments
(visible in `/proc/<pid>/cmdline`). Pass via environment variable or config file.

### RCON passwords
Stored in the `servers` table encrypted with AES-GCM. Key derived from
`jwt_secret` using HKDF-SHA256 with label `"rcon"`. Never logged at any level.

### JWT
- Algorithm: HS256
- Access token TTL: 15 minutes
- Refresh token: stored as SHA-256 hash in `sessions` table, never plaintext
- On refresh: rotate refresh token (issue new, invalidate old)
- On logout: mark session `revoked_at`

### Password storage
bcrypt with cost 12. Minimum password length: 8 characters (enforced server-side).

### Plugin downloads
Every download (JAR from Modrinth, Hangar, Paper JAR, JVM) must:
1. Download to a temp file
2. Compute SHA-256
3. Compare against the registry-provided checksum using `subtle.ConstantTimeCompare`
4. Only on match: move to final destination
5. On mismatch: delete temp file, return error, do not proceed

Never write an unverified file to the plugins directory.

### Rate limiting
Apply to these endpoints using an in-memory token bucket (golang.org/x/time/rate):
- `POST /api/auth/login` — 10 req/min per IP
- `POST /api/auth/refresh` — 20 req/min per IP
- `POST /api/invites/:token/redeem` — 5 req/min per IP

### File permissions
- `mcsman.yaml`: must be `0600`, daemon exits if not
- Server directories: `0750`, owned by `minecraft` user
- Backup archives: `0640`
- Downloaded JARs: `0640`
- Unix socket: `0660`, group `mcsman`

---

## Filesystem Layout (runtime)

```
/var/lib/mcsman/
├── servers/
│   └── survival/                  # server.directory from DB
│       ├── server.jar
│       ├── server.properties
│       ├── plugins/
│       ├── world/
│       ├── world_nether/
│       └── world_the_end/
├── backups/
│   └── survival/
│       ├── 2026-10-17T03-00-00_auto.tar.gz
│       ├── 2026-10-17T03-00-00_auto.tar.gz.sha256
│       └── 2026-10-16T18-30-00_pre-upgrade-1.20.4.tar.gz
├── jars/
│   └── paper/
│       ├── 1.21.1/paper-1.21.1-196.jar
│       └── 1.21.4/paper-1.21.4-214.jar
└── java/
    ├── 17/                        # Temurin 17 install
    └── 21/                        # Temurin 21 install
```

---

## External APIs

### Paper version manifest
```
GET https://api.papermc.io/v2/projects/paper
GET https://api.papermc.io/v2/projects/paper/versions/{version}/builds
GET https://api.papermc.io/v2/projects/paper/versions/{version}/builds/{build}/downloads/{filename}
```
Cache the version list for 1 hour. Always re-fetch on explicit user action.

### Adoptium Temurin (Java)
```
GET https://api.adoptium.net/v3/assets/latest/{major}/hotspot?os=linux&arch=x64&image_type=jre
```
Download the JRE (not JDK). Verify SHA-256 from API response before extracting.

### Modrinth
```
GET https://api.modrinth.com/v2/project/{id}
GET https://api.modrinth.com/v2/project/{id}/version?game_versions=["1.21.1"]&loaders=["paper","bukkit","spigot"]
GET https://api.modrinth.com/v2/search?query={q}&facets=[["categories:paper"]]
```
Set `User-Agent: mcsman/1.0 (contact@yourdomain.com)` on all requests — required by Modrinth ToS.

### Hangar (PaperMC)
```
GET https://hangar.papermc.io/api/v1/projects?q={q}&platform=PAPER
GET https://hangar.papermc.io/api/v1/projects/{author}/{slug}/versions?platform=PAPER&platformVersion={mc}
```

### Discord webhook
```
POST {webhook_url}
Content-Type: application/json
{
  "username": "{server_name}",
  "embeds": [{ "color": int, "title": str, "description": str, "timestamp": ISO8601 }]
}
```

---

## CLI (`mcsctl`)

Communicates with daemon via Unix socket (`/run/mcsman/mcsman.sock`).
Uses the same REST API as the web UI but over the socket.
Requires the caller to be in the `mcsman` group (socket is `0660 mcsman:mcsman`).

```
mcsctl server list
mcsctl server start <name>
mcsctl server stop <name> [--delay 60]
mcsctl server restart <name> [--delay 60]
mcsctl server console <name>          # attach PTY locally
mcsctl server status <name>

mcsctl backup create <server> [--tag manual]
mcsctl backup list <server>
mcsctl backup restore <server> <backup-id>

mcsctl invite create [--expires 48h] [--whitelist <server>]
mcsctl invite list
mcsctl invite revoke <invite-id>

mcsctl upgrade preflight <server> <target-version>
mcsctl upgrade run <server> <target-version>

mcsctl plugin search <query> [--mc-version 1.21.1]
mcsctl plugin install <server> <modrinth-id|hangar-slug>
mcsctl plugin list <server>
mcsctl plugin update <server> [--all]

mcsctl world import <server> <path-to-zip>
mcsctl world export <server> [--output ./world.zip]

mcsctl user list
mcsctl user remove <username>
```

---

## systemd Unit

`/etc/systemd/system/mcsman.service`:
```ini
[Unit]
Description=mcsman Minecraft server manager
After=network.target

[Service]
Type=simple
User=mcsman
Group=mcsman
EnvironmentFile=/etc/mcsman/mcsman.env
ExecStart=/usr/local/bin/mcsman --config /etc/mcsman/mcsman.yaml
Restart=on-failure
RestartSec=5s
RuntimeDirectory=mcsman
RuntimeDirectoryMode=0750

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/mcsman /run/mcsman
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

`/etc/mcsman/mcsman.env` (`0600`):
```
MCSMAN_JWT_SECRET=<min-32-char-random-string>
MCSMAN_DISCORD_WEBHOOK_0=https://discord.com/api/webhooks/...
```

---

## Build Phases

### Phase 1 — Daemon foundation (no UI)
- [ ] Go module init, directory scaffold
- [ ] Config loader with 0600 enforcement and env var overlay
- [ ] SQLite setup: open, WAL mode, foreign keys, migration runner
- [ ] DB schema migrations (all tables)
- [ ] First admin user seeding (via `mcsctl init` or first-run prompt)
- [ ] JWT auth: login, refresh, logout, middleware
- [ ] `server` table CRUD
- [ ] Server process manager: spawn with PTY, start/stop/restart
- [ ] Crash detection + auto-restart with backoff
- [ ] RCON client wrapper
- [ ] Unix socket listener for CLI
- [ ] `mcsctl` skeleton: server start/stop/status/console
- [ ] `SafePath` utility — used everywhere from day one

### Phase 2 — Core features
- [ ] WebSocket hub
- [ ] Console streaming (PTY → WebSocket)
- [ ] Metrics collector (RCON polling → WebSocket broadcast)
- [ ] Paper version manifest fetch + cache
- [ ] JAR downloader with SHA-256 verification
- [ ] Java version manager (Adoptium)
- [ ] Backup manager: full sequence (flush, tar, verify, retention)
- [ ] Backup restore
- [ ] SvelteKit project scaffold embedded via `go:embed`
- [ ] Web UI: login page, server list, server detail with console

### Phase 3 — Users & access
- [ ] Invite token generation, hashing, rate-limited redemption
- [ ] Whitelist queue: apply on redeem if server running, queue if not
- [ ] Player role restrictions in API middleware
- [ ] Public status endpoint (`/api/status`)
- [ ] Public status page (no-auth SvelteKit route)
- [ ] User management (list, remove, promote) — admin UI + CLI

### Phase 4 — Plugins & versions
- [ ] Modrinth API client
- [ ] Hangar API client
- [ ] Plugin installer: download → verify → install
- [ ] Plugin update flow
- [ ] Version upgrade pre-flight (compat check against both registries)
- [ ] Version upgrade execution (full sequence)
- [ ] Plugin + version UI pages

### Phase 5 — Discord & worlds
- [ ] Discord webhook client
- [ ] Event bus wiring (server events → webhook sender)
- [ ] Webhook config UI
- [ ] World import: zip validation, NBT level.dat parse, version check, extract
- [ ] World export: zip + download
- [ ] World import/export UI

### Phase 6 — Polish
- [ ] Scheduled auto-backup (robfig/cron)
- [ ] Scheduled restart with in-game countdown
- [ ] Backup failure alerting
- [ ] Upgrade pre-flight UI (green/amber/gray report)
- [ ] Post-upgrade plugin load error surfacing
- [ ] mcsctl completion (bash/zsh)
- [ ] Arch Linux PKGBUILD
- [ ] `connection_address` field in server settings UI with placeholder hint "e.g. survival.playit.gg"

---

## Backlog (not in scope for v1)

- Discord bot / slash commands
- Fabric / Forge / NeoForge server types
- Remote backup destinations (S3, SFTP, rclone)
- Time-series metrics storage and charts
- Map rendering integration (BlueMap, Dynmap)
- Multi-host support
- RBAC beyond two roles (admin/player)
