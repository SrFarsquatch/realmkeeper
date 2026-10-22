# Realmkeeper

A self-hosted web panel for running game servers (Hytale, Minecraft Java, RuneScape: Dragonwilds and Terraria) from one Linux host. It is designed for Docker and CasaOS and is not affiliated with or endorsed by Hypixel Studios, Mojang, Jagex or Re-Logic.

Realmkeeper used to be called Hytale Server Manager (HSM); see [Upgrading from Hytale Server Manager](#upgrading-from-hytale-server-manager).

## What it manages

- Multiple Hytale server processes at the same time
- Separate world data, ports, configs, mods, logs and backups per server
- Start, stop and restart controls
- Live console and server commands
- Hytale device authentication
- Official Hytale downloader/import workflow
- Hytale updater commands, and a release channel per server (release or pre-release, for builds such as world generation v2). New servers download the chosen channel; switching an existing server takes a snapshot first when it is stopped, then the Game updates buttons move it to that channel's build. Players need the same channel in their Hytale launcher.
- Native backups and full offline snapshots
- `config.json`, `permissions.json`, `whitelist.json` and `bans.json`
- Per-server JVM memory and autostart settings
- Linux amd64 and arm64 builds

### Other games

- **Minecraft Java (vanilla, Paper, Fabric, Quilt, Forge or NeoForge):**
  - choose the server type when creating the server:
    - **Vanilla:** Mojang's official server.
    - **Paper:** the newest stable PaperMC build, checked against its published SHA-256. Plugins (`.jar`) go in `plugins/`, managed from the Plugins tab.
    - **Fabric:** the Fabric server launcher with the newest stable loader. Mods (`.jar`) go in `mods/`, managed from the Mods tab. Most mods also need Fabric API.
    - **Quilt:** installed with Quilt's own installer for the chosen Minecraft release. Mods go in `mods/`; Quilt runs most Fabric mods (add Quilted Fabric API).
    - **Forge:** the recommended Forge build for the Minecraft version, or the latest build when none is recommended. Minecraft 1.17 or newer: older Forge needs Java 8, which the image does not include.
    - **NeoForge:** the newest stable NeoForge build for the Minecraft version (1.20.2 or newer; NeoForge for 1.20.1 is published as Forge).
    - Forge, NeoForge and Quilt are installed by running the loader's installer, which downloads Minecraft and the loader's libraries. Installers come only from the loader's own HTTPS site, checked against the published SHA-256 when there is one. Forge and NeoForge start from the `unix_args.txt` their installer writes.
  - a Modrinth browser in the Mods (or Plugins) tab for Paper, Fabric, Quilt, Forge and NeoForge servers:
    - search shows only projects for the server's loader and Minecraft version that run on servers
    - **Install** picks the newest compatible release (or choose a version) and installs its required dependencies too; files are checked against Modrinth's SHA-512
    - **Check for updates** looks up every jar in the folder by its hash, including ones uploaded by hand, and offers newer compatible versions
    - what came from Modrinth is recorded in `game/.realmkeeper-mods.json`, so it travels with exports
  - EULA acceptance required
  - console and player list (`list`)
  - a Players tab for the whitelist, operators and bans, with kick, ban and make-operator buttons for online players. A running server is changed through its console; a stopped server's `whitelist.json`, `ops.json`, `banned-players.json` and `banned-ips.json` are edited directly, with names checked against Mojang's account lookup.
  - full snapshots, including while the server is running (autosave is paused for the copy); snapshots also include `plugins/`, `mods/` and `config/`
  - a game rules form for common `server.properties` settings
  - a Java version check before start
- **Terraria (vanilla or tModLoader):**
  - choose the server type when creating the server:
    - **Vanilla:** the official server from terraria.org (`latest` or a chosen release such as `1.4.4.9`). The Linux server runs only on x86-64 (amd64) hosts.
    - **tModLoader:** the latest (or a chosen) release from GitHub. The first start downloads the .NET runtime into the server folder, so it also runs on ARM hosts. Upload `.tmod` files from the Mods tab; Realmkeeper keeps `Mods/enabled.json` in sync. Workshop mods can be copied from a game client's `steamapps/workshop/content/1281930` folder.
  - world name, size and difficulty chosen at creation; the world is generated on first start
  - `serverconfig.txt` written before each start, keeping any extra lines you add
  - console, player list (`playing`), graceful stop (`exit` saves the world)
  - snapshots of `Worlds`, `serverconfig.txt` and `banlist.txt` (plus `Mods` and `ModConfigs` for tModLoader) while the server is stopped
  - uses TCP in the published `5520`–`5525` range (shared with Hytale)
- **RuneScape: Dragonwilds:**
  - SteamCMD install (retried when SteamCMD reports "Missing configuration"), dedicated-server settings and console output
  - a Worlds tab: upload a world's `.sav` (and `.backup`) from `%LOCALAPPDATA%\RSDragonwilds\Saved\SaveGames`, choose which world loads next (it becomes the newest save and the default world name), start a new world, and restore worlds Realmkeeper moved aside. Nothing is deleted: replaced or removed saves go to `backups/worlds/` in the server folder.
  - World rules (Standard, Custom or Creative) live in the save: create the world in the game with the rules you want and upload it, or change settings in game from Settings → Server Management with the admin password.
  - snapshots of the saves and the game's config while the server is stopped

### Data folder layout

Everything is kept under the data folder (`/data` in the container):

```
controller.json          app settings: servers, schedules, admin login
tools/                   shared downloads (SteamCMD, Hytale downloader)
servers/<id>/            one folder per server
  game/                  the game's own files: install, worlds, configs, mods
  backups/snapshots/     snapshots made by the app
  backups/worlds/        Dragonwilds saves moved aside by the Worlds tab
  imports/               files waiting to be installed (Hytale game.zip)
  home/                  home folder for the game process and SteamCMD
  tmp/                   downloads and unpacking; emptied at startup
```

The in-app file manager shows one server's folder. Older installs are moved to this layout on startup: a first server kept directly in the data folder moves to `servers/<id>/`, and `snapshots/` and `world-backups/` move under `backups/`.

### Moving a server (export and import)

- **Export** (a server's Settings tab, server stopped) downloads one `.tar.gz` with the server's `game` folder (installed server, worlds, settings, mods) and its Realmkeeper settings and schedules. Tick **with backups** to include snapshots and backups, which can be large. The export contains the server's passwords; keep it private.
- **Import** (My Servers, or the welcome screen on a fresh install) uploads an export and creates a new server from it. It keeps the name, settings, world and schedules; it gets a new ID if that one is taken, and a new port if its port is in use or outside the game's range.
- To reinstall Realmkeeper from scratch: stop each server, export it, reinstall, then import the files. Hytale may ask you to authenticate the server again on its first start.

### Schedules and crash recovery

- **Schedules (per server):** restart, start, stop, back up or run a console command, either daily at a set time (optionally on chosen weekdays) or repeating every N minutes or hours.
  - Restarts and stops can warn players in game first, through the game's broadcast command (Minecraft and Terraria `say`, Hytale `/say`).
  - Times use the container's time zone (set `TZ` in Compose).
  - Runs missed while the manager is offline are skipped, not replayed.
- **Auto-restart on crash** (opt-in, in Settings): a server that exits with an error without being asked to stop is started again after 5, 15 and 45 seconds. It is left stopped after 3 crashes in 10 minutes.

Restoring a snapshot first saves a safety copy, then replaces the world and settings with the snapshot's contents. Files created after the snapshot are removed rather than mixed in.

## Run with Docker Compose

```bash
git clone https://github.com/SrFarsquatch/realmkeeper.git
cd realmkeeper
./install.sh
```

Open `http://YOUR-SERVER-IP:8008`.

The default Hytale UDP range is `5520-5525`. Each running instance gets its own port from that range.

## CasaOS

Use `docker-compose.casaos.yml`. It pulls the published multi-architecture image from GitHub Container Registry. New installs store persistent data in:

```text
/DATA/AppData/realmkeeper
```

Both supplied Compose files explicitly configure container DNS (Cloudflare `1.1.1.1` and Google `8.8.8.8`) because Hytale's OAuth/device login and token refresh require the manager container to resolve Hytale authentication hosts. Hytale server processes run inside the manager container, so this setting covers their authentication requests too. If your network blocks public DNS or requires a local DNS resolver, replace these entries with DNS servers reachable from your host network.

## Upgrading from Hytale Server Manager

Realmkeeper is the same app under a new name. Existing installs keep working:

- **In-app updates** carry on as before: the updater keeps the container's name, data folder, ports and settings. Images are published under both `ghcr.io/srfarsquatch/realmkeeper` and the old `ghcr.io/srfarsquatch/hytale-server-manager`.
- **Settings:** the environment variables are now `REALMKEEPER_*` (for example `REALMKEEPER_DATA_DIR`). The old `HSM_*` names, and the `ORBIS_*` names from Orbis v0.4, are still read when the new one is not set.
- **Sign-in** sessions and the remembered server carry over.
- **Exports** made by Hytale Server Manager import into Realmkeeper.

A new CasaOS install of Realmkeeper uses `/DATA/AppData/realmkeeper`. To keep an existing install's servers, either export each server and import it into the new install, or point the new app's `/data` at the old `/DATA/AppData/hytale-server-manager` folder. Stop the old app first: both use the same ports.

## First-run setup

A new installation starts with an empty server list. Sign in with the admin account you create on first launch, then use **Create your first server** to configure a Hytale instance. The guided setup covers server name, UDP port, public address, game mode, player limit, optional join password, whitelist permission, JVM memory, backups and autostart. After creation, follow the Install page to download or import authorized Hytale server files.

Existing installations keep their configured servers and legacy single-server settings are migrated automatically. The new server settings are saved separately from the Hytale world data.

Game mode, server name, message, player limit, password and join-permission requirements are applied to the Hytale config.json when the server is stopped and settings are saved or before it starts. Hytale's configuration schema can change between game updates, so keep a backup before making advanced manual edits.

## Updating the application

Open **Application settings → Application updates** and choose **Check for updates**. When a newer stable GitHub release is available, choose **Update and restart**. The updater pulls the published stable image from GitHub Container Registry, stages the existing container for rollback, and recreates the manager with its existing ports, mounts, environment, networks, and restart policy. The persistent `/data` volume is not removed. If the replacement fails to start, the updater attempts to restore the previous container. The dashboard shows the updater's progress (downloading, stopping, starting the new version). If the updater stops with an error, the dashboard shows its last output and leaves the current version running, so you can try again. If Docker itself cannot download the image (for example because the host's DNS cannot resolve `ghcr.io`), the updater downloads it directly through the container's own DNS servers and loads it into Docker. If in-app updates keep failing, update the app from CasaOS instead (it pulls the same `latest` image).

In-container updates require the Docker socket mount shown in both Compose files. This is a powerful permission: anyone who compromises the manager can potentially gain host-level control through Docker. Use a strong admin password and keep the web dashboard on a trusted network. If you do not want to grant Docker socket access, update the image through CasaOS instead.

Stable releases are published automatically on each push to `main`. The workflow builds and publishes multi-architecture `linux/amd64` and `linux/arm64` images, updates the `latest` image, and creates a stable GitHub Release with generated notes. Version tags follow `v0.6.N` using the workflow run number. The in-app updater checks the latest stable GitHub Release and applies the `latest` container image. Ensure the GitHub Container Registry package is public so a self-hosted instance can pull release images without registry credentials.

## Troubleshooting Hytale authentication

If `/auth login device` fails with `java.nio.channels.UnresolvedAddressException`, or logs show `OAuth token refresh IO error`, the server process may be unable to resolve or reach Hytale's authentication hosts from inside the manager container. First update/recreate the manager using one of the supplied Compose files so the `dns:` settings take effect. Then retry `/auth login device` and complete the newly generated code promptly. Verify with `/auth status`.

If the error persists, the DNS servers may be unreachable or outbound HTTPS may be blocked by your network. Check the router/firewall DNS and HTTPS rules, and substitute your network's reachable DNS resolver in the Compose file if public DNS is restricted. Do not delete the persistent `/data` directory or authentication files as a first troubleshooting step.

## Networking

The web panel uses TCP `8008` on the host and container. The Compose and CasaOS definitions publish:

- `5520`–`5525` over UDP and TCP, for Hytale (UDP/QUIC) and Terraria (TCP) servers. `REALMKEEPER_GAME_PORT_START` and `REALMKEEPER_GAME_PORT_END` must match this range.
- `25565`–`25570` over TCP, for Minecraft Java servers. It starts at Minecraft's default port, so players can join the first server with just the address. `REALMKEEPER_MINECRAFT_PORT_START` and `REALMKEEPER_MINECRAFT_PORT_END` must match this range.
- `7777`–`7782` over UDP, for RuneScape: Dragonwilds servers.

Minecraft servers created before the 25565 range keep their port; change it in the server's Settings to move one onto the new range.

The manager only assigns ports from these ranges; it never changes the container's published ports. If you widen a range, add the matching `ports:` entries in Compose or the CasaOS app settings too. When the manager can read its own container through the Docker socket, a server's Overview warns if its port is not published.

## Hytale files

This project does not bundle proprietary Hytale server files. Install them with the official Hytale downloader or import your own authorized `game.zip`.

## Game artwork

Official icons and logos for Hytale, Minecraft Java, RuneScape: Dragonwilds and Terraria are bundled. A game without artwork falls back to a coloured monogram. Artwork is optional, bundled into the build, and only used when the file exists:

```text
cmd/realmkeeper/web/assets/games/art/<game-id>-icon.webp    square, 256×256, transparent background
cmd/realmkeeper/web/assets/games/art/<game-id>-logo.webp    the game's wordmark, shown on the Overview card
cmd/realmkeeper/web/assets/games/art/<game-id>-banner.jpg   optional wide background, about 1200×400
```

Game IDs: `hytale`, `minecraft-java`, `runescape-dragonwilds`, `terraria`. Accepted formats: PNG, WebP, JPG/JPEG or SVG. Rebuild the image after adding files. Game logos and artwork belong to their publishers; only add artwork you are permitted to use.

## Development checks

```bash
go vet ./... && go test ./...
node --check cmd/realmkeeper/web/app.js
# Browser regression test for server switching (needs the `playwright` npm package and Chromium):
go build -o /tmp/realmkeeper ./cmd/realmkeeper && node tests/browser/workspace-selection.mjs /tmp/realmkeeper
```

CI runs the same browser test as a separate `browser` job. The browser test starts its own Realmkeeper instance against a temporary data directory and uses a fake Hytale `start.sh`, so it never downloads games or touches real server data. See `docs/audit-2026-10-workspace-selection.md` for the current audit and backlog.
