# mcsman

Self-hosted Minecraft (Paper) server manager for a group of friends: one static
binary with a mobile-friendly web UI, an admin CLI (`mcsctl`), a public status
page, Discord notifications, backups, plugins and one-click version upgrades.

- **Servers** — create Paper servers by version; mcsman downloads Paper and the
  right Java runtime (Temurin) automatically, verifies every download, and
  supervises the process (crash auto-restart with backoff, graceful stop with
  in-game countdown).
- **Console & metrics** — live console in the browser or terminal; TPS, MSPT,
  memory and players every 10 seconds.
- **Backups** — manual or scheduled, consistent (`save-all`/`save-off`),
  checksummed, pruned to a keep count; one-click restore.
- **Plugins** — search and install from Modrinth and Hangar; updates; manual
  JARs are detected.
- **Upgrades** — compatibility pre-flight (per plugin), then stop → backup →
  new Paper + Java + plugin updates → start, with plugin errors surfaced.
- **Friends** — single-use invite links that create an account and whitelist
  their Minecraft name; public status page with copyable server address.
- **Worlds** — import a singleplayer world zip, export any world.
- **Discord** — webhook notifications per event and per server.

## Install (Arch Linux)

```sh
git clone <this repo> mcsman && cd mcsman/packaging/arch
makepkg -si
```

Then follow the printed steps (also below). mcsman runs as a normal user of
your choice — the daemon and all Minecraft servers run as that user.

```sh
# 1. Secret for sessions and encrypting stored secrets (keep it; changing it
#    makes stored RCON passwords and webhook URLs unreadable)
sudo sh -c 'echo MCSMAN_JWT_SECRET=$(head -c 48 /dev/urandom | base64 -w0) >> /etc/mcsman/mcsman.env'

# 2. The config must be mode 0600 and readable by the service user
sudo chown YOURUSER: /etc/mcsman/mcsman.yaml
sudo $EDITOR /etc/mcsman/mcsman.yaml      # base_url, listen, trusted_proxies

# 3. Start
sudo systemctl enable --now mcsman@YOURUSER

# 4. First admin account (over the local socket)
mcsctl init
```

Open `http://<host>:8080` (or your reverse proxy URL) and log in.

### Manual install

```sh
make build                          # needs Go 1.23+ and Node 20+
sudo install -m755 bin/mcsman bin/mcsctl /usr/local/bin/
make systemd                        # renders bin/mcsman.service for the current user
```

## Networking

| Port | Purpose | Expose? |
|---|---|---|
| 25565 (per server) | Minecraft | Yes — forward it, or use a tunnel (e.g. playit.gg) |
| game port + 10 | RCON | **No.** Paper binds RCON on all interfaces; firewall it |
| 8080 | Web UI / API | Behind a reverse proxy with HTTPS |

Example Caddy config:

```
mc.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

With a proxy, set `http.listen: 127.0.0.1:8080` and
`http.trusted_proxies: [127.0.0.1]` so login rate limits see real client IPs.

Set each server's **public address** (Server → Settings) to what players type
into Minecraft; it appears on the status page and invite confirmations.

## Everyday use

| Task | Web UI | CLI |
|---|---|---|
| Create a server | Servers → New server | `mcsctl server create survival --version 1.21.11 --accept-eula` |
| Start / stop | Server page | `mcsctl server start survival` · `stop survival --delay 60` |
| Console | Console tab | `mcsctl server console survival` |
| Invite a friend | Users → Invite | `mcsctl invite create --whitelist survival` |
| Backups | Backups tab | `mcsctl backup create survival` · `backup restore …` |
| Schedules | Settings tab | — |
| Plugins | Plugins tab | `mcsctl plugin search luckperms` · `plugin install survival luckperms` |
| Upgrade | Version tab | `mcsctl upgrade run survival 26.3` |
| Worlds | World tab | `mcsctl world import survival world.zip` |

`mcsctl` talks to the daemon over `/run/mcsman/mcsman.sock`; your login user
must be in the service user's primary group. Shell completion is installed by
the package (or: `mcsctl completion bash|zsh`).

## Schedules

Backups default to `backup.default_interval` (e.g. `1h`); each server can
override it, disable it, or change how many backups are kept. A stopped server
is only backed up if it ran since its last backup. Scheduled restarts give
players a 5-minute in-game countdown. Schedules accept an interval (`6h`) or a
cron expression (`0 4 * * *`, server local time).

## Data

Everything lives under `/var/lib/mcsman`: `servers/<name>`, `backups/<name>`,
`jars/` (Paper cache), `java/` (Temurin runtimes) and `mcsman.db` (SQLite).
Back up `mcsman.db` and `/etc/mcsman` along with your backups folder.

## Development

```sh
make test                           # go test -race ./...
cd web && npm install && npm run check
# run against a dev config (0600) with MCSMAN_JWT_SECRET set:
./bin/mcsman --config ./mcsman.yaml
cd web && npm run dev               # UI dev server proxying /api to :8091
```

Tests use a fake Paper server (`internal/testutil/fakemc`) and fake registries,
so they need neither Java nor network.
