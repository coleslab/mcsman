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

Open `http://<host>:8420` (or your public URL) and log in.

### Manual install

```sh
make build                          # needs Go 1.23+ and Node 20+
sudo install -m755 bin/mcsman bin/mcsctl /usr/local/bin/
make systemd                        # renders bin/mcsman.service for the current user
```

## Updating mcsman

This updates mcsman itself; upgrading a server's Paper/Java version is
separate (Version tab or `mcsctl upgrade run`).

`packaging/arch/update.sh` does all of the steps below: it pulls, builds while
servers keep running, stops them with a countdown, saves the database, config
and previous package to `~/mcsman-pre-update/<time>/`, installs, restarts the
daemon and starts the servers that were running (autostart ones start on
their own). `-d SECS` sets the countdown, `-b` backs up each running server,
`-h` lists the rest. If something goes wrong it prints the rollback command:

```sh
packaging/arch/update.sh
packaging/arch/update.sh --rollback ~/mcsman-pre-update/20261018-120000
```

To do it by hand:

**1. Warn players.** Restarting the daemon gracefully stops every running
server, but without an in-game countdown. Stop them yourself first if people
are online, and take a fresh backup if you like:

```sh
mcsctl server stop survival --delay 60
mcsctl backup create survival
```

**2. Save mcsman's state.** Database migrations run automatically on startup
and only go forward, so an older mcsman can't use an upgraded `mcsman.db`.
Keep a copy so you can roll back, plus the previous package (rebuilding
overwrites it):

```sh
sudo systemctl stop mcsman@YOURUSER
mkdir -p ~/mcsman-pre-update
sudo cp -a /var/lib/mcsman/mcsman.db* /etc/mcsman ~/mcsman-pre-update/   # db* = WAL files too
cp packaging/arch/mcsman-*.pkg.tar.zst ~/mcsman-pre-update/
```

**3. Build and install.**

```sh
git pull
cd packaging/arch && makepkg -sif   # -f: rebuild even if a package file exists
```

pacman keeps your edited `/etc/mcsman/mcsman.yaml` and `mcsman.env`; new
defaults land next to them as `.pacnew` files (`sudo pacdiff` to merge). For a
manual install, rerun the `make build` and `sudo install` steps above instead.

**4. Start and check.**

```sh
sudo systemctl start mcsman@YOURUSER
journalctl -u mcsman@YOURUSER -f    # migrations, then "autostarting server"
```

Only servers with autostart enabled come back on their own; start the others
from the web UI or with `mcsctl server start`.

**Rolling back.** Stop the service, reinstall the saved package and put the
saved database back:

```sh
sudo systemctl stop mcsman@YOURUSER
sudo pacman -U ~/mcsman-pre-update/mcsman-*.pkg.tar.zst
sudo rm -f /var/lib/mcsman/mcsman.db-wal /var/lib/mcsman/mcsman.db-shm
sudo cp -a ~/mcsman-pre-update/mcsman.db* /var/lib/mcsman/
sudo systemctl start mcsman@YOURUSER
```

Never change `MCSMAN_JWT_SECRET` as part of an update; stored RCON passwords
and webhook URLs are encrypted with it.

## Networking

| Port | Purpose | Expose? |
|---|---|---|
| 8420 | Web UI / API | LAN directly; internet via a tunnel or reverse proxy |
| 25565… (one per server) | Minecraft | Via playit.gg, a router port-forward, or similar |
| game port + 10 | RCON | **Never.** Paper binds RCON on all interfaces; keep it firewalled and untunnelled |

### Cloudflare Tunnel (web UI) + playit.gg (game)

Web UI through a `cloudflared` container on the host network:

```yaml
# docker-compose.yml
services:
  cloudflared:
    image: cloudflare/cloudflared:latest
    command: tunnel --no-autoupdate run
    environment:
      - TUNNEL_TOKEN=${CF_TUNNEL_TOKEN}
    network_mode: host        # reaches mcsman at localhost:8420
    restart: unless-stopped
```

In Zero Trust → Networks → Tunnels → Public Hostname, route `mc.example.com`
to `HTTP` → `localhost:8420`. Don't set an HTTP Host header override (the live
console's WebSocket checks it). Then:

```yaml
http:
  listen: 0.0.0.0:8420            # LAN clients connect directly
  base_url: https://mc.example.com
  trusted_proxies: [127.0.0.1]    # cloudflared; its CF-Connecting-IP is trusted
```

`trusted_proxies` makes per-client rate limits work: without it, every
tunnelled request looks like it comes from cloudflared and all users share one
login limit. Don't port-forward 8420; if the host has a firewall, allow it from
your LAN only (e.g. `ufw allow from 192.168.1.0/24 to any port 8420`).

Cloudflare caps uploads at 100 MB and responses must start within 100 s, so
import large worlds and restore big backups from the LAN
(`http://<host-LAN-IP>:8420`) or with `mcsctl` on the host.

For the game, create one playit.gg tunnel per server pointing at its local
game port (`127.0.0.1:25565`, `127.0.0.1:25566`, …; run the playit agent with
host networking if it's in Docker). Put each tunnel's address in
**Server → Settings → Public address** — it appears on the status page,
invite confirmations and Discord messages.

### Reverse proxy instead of a tunnel

```
mc.example.com {
    reverse_proxy 127.0.0.1:8420
}
```

With Caddy/nginx on the same host, set `http.listen: 127.0.0.1:8420` (or keep
`0.0.0.0` for LAN access) and `http.trusted_proxies: [127.0.0.1]`.

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
